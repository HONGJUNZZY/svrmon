package hub

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"log"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure-Go sqlite driver, cross-compiles without cgo

	"svrmon/internal/model"
)

// ptVal is one in-memory disk rate sample used to compute window peaks.
type ptVal struct {
	ts   int64
	r, w float64
}

// Store persists decimated snapshots (default one row per 10s) in SQLite.
type Store struct {
	db         *sql.DB
	writeEvery int64
	retention  time.Duration

	mu     sync.Mutex
	last   map[string]int64
	recent map[string][]ptVal // snapshots since the last stored row, for peaks
}

// OpenStore opens (creating if needed) the history database.
func OpenStore(path string, writeEverySec, retentionDays int) (*Store, error) {
	if writeEverySec <= 0 {
		writeEverySec = 10
	}
	if retentionDays <= 0 {
		retentionDays = 7
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{
		db:         db,
		writeEvery: int64(writeEverySec),
		retention:  time.Duration(retentionDays) * 24 * time.Hour,
		last:       map[string]int64{},
		recent:     map[string][]ptVal{},
	}
	if err := s.init(); err != nil {
		db.Close()
		return nil, err
	}
	go s.retentionLoop()
	return s, nil
}

func (s *Store) init() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS samples (
			server TEXT NOT NULL,
			ts INTEGER NOT NULL,
			cpu REAL, iowait REAL, load1 REAL,
			mem_used REAL, mem_total REAL, swap_used REAL,
			disk_r REAL, disk_w REAL, disk_r_max REAL, disk_w_max REAL,
			net_rx REAL, net_tx REAL,
			gpu_util REAL, gpu_temp REAL, gpu_power REAL, gpu_mem_used REAL,
			cpu_temp REAL, fan_max REAL, ipmi_power REAL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_samples_st ON samples(server, ts)`,
		`CREATE TABLE IF NOT EXISTS server_names (
			real_name TEXT PRIMARY KEY,
			display_name TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS disk_samples (
			server TEXT NOT NULL,
			ts INTEGER NOT NULL,
			disk TEXT NOT NULL,
			r REAL, w REAL, util REAL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_disk_samples_st ON disk_samples(server, ts)`,
		`CREATE TABLE IF NOT EXISTS logs (
			server TEXT NOT NULL,
			ts INTEGER NOT NULL,
			prio INTEGER,
			source TEXT,
			unit TEXT,
			message TEXT,
			dedup INTEGER,
			UNIQUE(server, ts, dedup)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_st ON logs(server, ts)`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("init: %w", err)
		}
	}
	// Migrate databases created before the disk peak columns existed.
	if err := s.ensureColumns("samples", map[string]string{
		"disk_r_max": "REAL",
		"disk_w_max": "REAL",
	}); err != nil {
		return err
	}
	return nil
}

// ensureColumns adds missing columns to an existing table (no-op when present).
func (s *Store) ensureColumns(table string, cols map[string]string) error {
	rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	existing := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		existing[name] = true
	}
	rows.Close()
	for c, decl := range cols {
		if existing[c] {
			continue
		}
		if _, err := s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, c, decl)); err != nil {
			return fmt.Errorf("migrate %s.%s: %w", table, c, err)
		}
		log.Printf("store: migrated %s: added column %s", table, c)
	}
	return nil
}

func (s *Store) retentionLoop() {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	s.sweep()
	for range t.C {
		s.sweep()
	}
}

func (s *Store) sweep() {
	cutoff := time.Now().Add(-s.retention).Unix()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM samples WHERE ts < ?`, cutoff); err != nil {
		log.Printf("store: retention sweep: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM disk_samples WHERE ts < ?`, cutoff); err != nil {
		log.Printf("store: disk retention sweep: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM logs WHERE ts < ?`, cutoff); err != nil {
		log.Printf("store: logs retention sweep: %v", err)
	}
}

// Insert writes a decimated sample row (nil values become NULL). Disk rate
// peaks are tracked across all snapshots since the previous stored row, so
// short bursts between store ticks are not averaged away.
func (s *Store) Insert(server string, snap *model.Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var diskR, diskW float64
	for _, d := range snap.Disks {
		diskR += d.ReadBps
		diskW += d.WriteBps
	}
	s.recent[server] = append(s.recent[server], ptVal{ts: snap.TS, r: diskR, w: diskW})
	if len(s.recent[server]) > 64 {
		s.recent[server] = s.recent[server][len(s.recent[server])-32:]
	}

	if snap.TS-s.last[server] < s.writeEvery {
		return
	}
	prevStore := s.last[server]
	s.last[server] = snap.TS

	// Peak within the just-closed storage window.
	var rm, wm float64
	for _, v := range s.recent[server] {
		if v.ts <= prevStore {
			continue
		}
		if v.r > rm {
			rm = v.r
		}
		if v.w > wm {
			wm = v.w
		}
	}
	s.recent[server] = []ptVal{{ts: snap.TS, r: diskR, w: diskW}}

	var cpu, iowait, load1 any
	if snap.CPU != nil {
		cpu, iowait = snap.CPU.Usage, snap.CPU.Iowait
	}
	if snap.Load != nil {
		load1 = snap.Load.Load1
	}
	var memUsed, memTotal, swapUsed any
	if snap.Mem != nil {
		memUsed, memTotal, swapUsed = snap.Mem.Used, snap.Mem.Total, snap.Mem.SwapUsed
	}
	var netRx, netTx float64
	for _, n := range snap.Nets {
		netRx += n.RxBps
		netTx += n.TxBps
	}
	var gpuUtil, gpuTemp, gpuPower, gpuMem any
	if len(snap.GPUs) > 0 {
		var su, st, gpuPowerF, gpuMemF float64
		for _, g := range snap.GPUs {
			su += g.Util
			if g.Temp > st {
				st = g.Temp
			}
			if g.PowerDraw != nil {
				gpuPowerF += *g.PowerDraw
			}
			gpuMemF += float64(g.MemUsed)
		}
		gpuUtil = su / float64(len(snap.GPUs))
		gpuTemp = st
		if gpuPowerF > 0 {
			gpuPower = gpuPowerF
		}
		if gpuMemF > 0 {
			gpuMem = gpuMemF
		}
	}
	var cpuTemp, fanMax, ipmiPower any
	if snap.CPU != nil && snap.CPU.Temp > 0 {
		cpuTemp = snap.CPU.Temp
	}
	if snap.IPMI != nil {
		if snap.IPMI.PowerWatts > 0 {
			ipmiPower = snap.IPMI.PowerWatts
		}
		var fm float64
		for _, x := range snap.IPMI.Sensors {
			if x.HasValue && strings.EqualFold(x.Unit, "rpm") && x.ValueF > fm {
				fm = x.ValueF
			}
		}
		if fm > 0 {
			fanMax = fm
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var rMax, wMax any
	if rm > 0 {
		rMax = rm
	}
	if wm > 0 {
		wMax = wm
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO samples
		(server, ts, cpu, iowait, load1, mem_used, mem_total, swap_used,
		 disk_r, disk_w, disk_r_max, disk_w_max, net_rx, net_tx,
		 gpu_util, gpu_temp, gpu_power, gpu_mem_used, cpu_temp, fan_max, ipmi_power)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		server, snap.TS, cpu, iowait, load1, memUsed, memTotal, swapUsed,
		diskR, diskW, rMax, wMax, netRx, netTx,
		gpuUtil, gpuTemp, gpuPower, gpuMem, cpuTemp, fanMax, ipmiPower)
	if err != nil {
		log.Printf("store: insert %s: %v", server, err)
	}

	// Per-disk detail rows (same decimation gate as the samples row).
	if len(snap.Disks) > 0 {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			log.Printf("store: disk tx %s: %v", server, err)
			return
		}
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO disk_samples (server, ts, disk, r, w, util) VALUES (?,?,?,?,?,?)`)
		if err != nil {
			_ = tx.Rollback()
			log.Printf("store: disk stmt %s: %v", server, err)
			return
		}
		for _, d := range snap.Disks {
			if _, err := stmt.ExecContext(ctx, server, snap.TS, d.Name, d.ReadBps, d.WriteBps, d.Util); err != nil {
				log.Printf("store: disk insert %s/%s: %v", server, d.Name, err)
			}
		}
		_ = stmt.Close()
		if err := tx.Commit(); err != nil {
			log.Printf("store: disk commit %s: %v", server, err)
		}
	}
}

// Point is one aggregated history sample returned to the browser.
type Point struct {
	T          int64    `json:"t"`
	CPU        *float64 `json:"cpu,omitempty"`
	Iowait     *float64 `json:"iowait,omitempty"`
	Load1      *float64 `json:"load1,omitempty"`
	MemUsed    *int64   `json:"memUsed,omitempty"`
	MemTotal   *int64   `json:"memTotal,omitempty"`
	SwapUsed   *int64   `json:"swapUsed,omitempty"`
	DiskR      *float64 `json:"diskR,omitempty"`
	DiskW      *float64 `json:"diskW,omitempty"`
	DiskRMax   *float64 `json:"diskRMax,omitempty"`
	DiskWMax   *float64 `json:"diskWMax,omitempty"`
	NetRx      *float64 `json:"netRx,omitempty"`
	NetTx      *float64 `json:"netTx,omitempty"`
	GpuUtil    *float64 `json:"gpuUtil,omitempty"`
	GpuTemp    *float64 `json:"gpuTemp,omitempty"`
	GpuPower   *float64 `json:"gpuPower,omitempty"`
	GpuMemUsed *int64   `json:"gpuMemUsed,omitempty"`
	CpuTemp    *float64 `json:"cpuTemp,omitempty"`
	FanMax     *float64 `json:"fanMax,omitempty"`
	IpmiPower  *float64 `json:"ipmiPower,omitempty"`
	LogCount   *int64   `json:"logCount,omitempty"`
}

// Query returns bucketed averages for one server over [from, to].
func (s *Store) Query(server string, from, to, bucket int64) ([]Point, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `
		SELECT CAST(ts / ? AS INTEGER) * ? AS b,
		       avg(cpu), avg(iowait), avg(load1),
		       avg(mem_used), avg(mem_total), avg(swap_used),
		       avg(disk_r), avg(disk_w), max(disk_r_max), max(disk_w_max),
		       avg(net_rx), avg(net_tx),
		       avg(gpu_util), avg(gpu_temp), avg(gpu_power), avg(gpu_mem_used),
		       avg(cpu_temp), avg(fan_max), avg(ipmi_power)
		FROM samples
		WHERE server = ? AND ts >= ? AND ts <= ?
		GROUP BY b ORDER BY b`, bucket, bucket, server, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pts []Point
	for rows.Next() {
		var n [20]sql.NullFloat64
		dest := make([]any, 20)
		for i := range n {
			dest[i] = &n[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		p := Point{T: int64(n[0].Float64)}
		p.CPU = fptr(n[1])
		p.Iowait = fptr(n[2])
		p.Load1 = fptr(n[3])
		p.MemUsed = iptr(n[4])
		p.MemTotal = iptr(n[5])
		p.SwapUsed = iptr(n[6])
		p.DiskR = fptr(n[7])
		p.DiskW = fptr(n[8])
		p.DiskRMax = fptr(n[9])
		p.DiskWMax = fptr(n[10])
		p.NetRx = fptr(n[11])
		p.NetTx = fptr(n[12])
		p.GpuUtil = fptr(n[13])
		p.GpuTemp = fptr(n[14])
		p.GpuPower = fptr(n[15])
		p.GpuMemUsed = iptr(n[16])
		p.CpuTemp = fptr(n[17])
		p.FanMax = fptr(n[18])
		p.IpmiPower = fptr(n[19])
		pts = append(pts, p)
	}
	return pts, rows.Err()
}

func fptr(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	v := n.Float64
	return &v
}

func iptr(n sql.NullFloat64) *int64 {
	if !n.Valid {
		return nil
	}
	v := int64(n.Float64)
	return &v
}

// DiskPoint is one bucketed per-disk history sample.
type DiskPoint struct {
	TS   int64    `json:"t"`
	R    *float64 `json:"r"`
	W    *float64 `json:"w"`
	RMax *float64 `json:"rMax,omitempty"`
	WMax *float64 `json:"wMax,omitempty"`
	Util *float64 `json:"util"`
}

// QueryDisks returns bucketed per-disk read/write/util series over [from, to].
func (s *Store) QueryDisks(server string, from, to, bucket int64) (map[string][]DiskPoint, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `
		SELECT CAST(ts / ? AS INTEGER) * ? AS b, disk, avg(r), avg(w), max(r), max(w), avg(util)
		FROM disk_samples
		WHERE server = ? AND ts >= ? AND ts <= ?
		GROUP BY b, disk ORDER BY b`, bucket, bucket, server, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]DiskPoint{}
	for rows.Next() {
		var b int64
		var disk string
		var r, w, rm, wm, u sql.NullFloat64
		if err := rows.Scan(&b, &disk, &r, &w, &rm, &wm, &u); err != nil {
			return nil, err
		}
		out[disk] = append(out[disk], DiskPoint{TS: b, R: fptr(r), W: fptr(w), RMax: fptr(rm), WMax: fptr(wm), Util: fptr(u)})
	}
	return out, rows.Err()
}

// InsertLogs stores severe log entries (duplicates silently ignored).
func (s *Store) InsertLogs(server string, entries []model.LogEntry) {
	if len(entries) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		log.Printf("store: logs tx %s: %v", server, err)
		return
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO logs (server, ts, prio, source, unit, message, dedup) VALUES (?,?,?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		log.Printf("store: logs stmt %s: %v", server, err)
		return
	}
	for _, e := range entries {
		h := fnv.New32a()
		_, _ = h.Write([]byte(e.Unit + "|" + e.Message))
		if _, err := stmt.ExecContext(ctx, server, e.TS, e.Prio, e.Source, e.Unit, e.Message, int64(h.Sum32())); err != nil {
			log.Printf("store: logs insert %s: %v", server, err)
		}
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		log.Printf("store: logs commit %s: %v", server, err)
	}
}

// QueryLogs returns severe log entries, newest first.
func (s *Store) QueryLogs(server string, from, to int64, limit int) ([]model.LogEntry, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx,
		`SELECT ts, prio, source, unit, message FROM logs WHERE server = ? AND ts >= ? AND ts <= ? ORDER BY ts DESC LIMIT ?`,
		server, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.LogEntry
	for rows.Next() {
		var e model.LogEntry
		if err := rows.Scan(&e.TS, &e.Prio, &e.Source, &e.Unit, &e.Message); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// QueryLogCounts returns per-bucket severe-log counts for the history chart.
func (s *Store) QueryLogCounts(server string, from, to, bucket int64) (map[int64]int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `
		SELECT CAST(ts / ? AS INTEGER) * ? AS b, count(*)
		FROM logs WHERE server = ? AND ts >= ? AND ts <= ?
		GROUP BY b`, bucket, bucket, server, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var b, c int64
		if err := rows.Scan(&b, &c); err != nil {
			return nil, err
		}
		out[b] = c
	}
	return out, rows.Err()
}

// LoadNames returns the persisted display-name map (agent hostname -> name).
func (s *Store) LoadNames() map[string]string {
	m := map[string]string{}
	rows, err := s.db.Query(`SELECT real_name, display_name FROM server_names`)
	if err != nil {
		return m
	}
	defer rows.Close()
	for rows.Next() {
		var real, disp string
		if err := rows.Scan(&real, &disp); err == nil {
			m[real] = disp
		}
	}
	return m
}

// SetName persists a display name (or resets it when display == real).
func (s *Store) SetName(real, display string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if display == real {
		_, err := s.db.ExecContext(ctx, `DELETE FROM server_names WHERE real_name = ?`, real)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO server_names (real_name, display_name) VALUES (?, ?)
		ON CONFLICT(real_name) DO UPDATE SET display_name = excluded.display_name`, real, display)
	return err
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }
