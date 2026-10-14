package collect

import (
	"bytes"
	"context"
	"encoding/csv"
	"log"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"svrmon/internal/model"
)

const gpuQuery = "index,uuid,name,utilization.gpu,utilization.memory,memory.used,memory.total," +
	"temperature.gpu,fan.speed,power.draw,power.limit,clocks.sm,clocks.mem,pstate," +
	"ecc.errors.corrected.volatile.total,ecc.errors.uncorrected.volatile.total"

// GPUCollector collects NVIDIA GPU metrics by parsing nvidia-smi output.
// It requires no cgo and works on Linux and Windows. Results are cached and
// nvidia-smi only runs once per refresh interval, independent of the
// (usually faster) snapshot interval.
type GPUCollector struct {
	path      string
	every     time.Duration
	ok        bool
	names     []string
	failUntil time.Time
	lastErr   string

	cached   []model.GPU
	cachedAt time.Time
}

// NewGPUCollector creates a collector using the given nvidia-smi path and
// refresh interval in seconds (<=0 falls back to 5).
func NewGPUCollector(path string, intervalSec int) *GPUCollector {
	if intervalSec <= 0 {
		intervalSec = 5
	}
	return &GPUCollector{path: path, every: time.Duration(intervalSec) * time.Second}
}

// Available reports whether nvidia-smi works; it probes lazily with backoff.
func (g *GPUCollector) Available(ctx context.Context) bool {
	if g.ok {
		return true
	}
	if time.Now().Before(g.failUntil) {
		return false
	}
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(c, g.path, "--query-gpu=index,name", "--format=csv,noheader").Output()
	if err != nil {
		g.lastErr = err.Error()
		g.failUntil = time.Now().Add(5 * time.Minute)
		return false
	}
	g.names = g.names[:0]
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, ",", 2)
		if len(f) == 2 {
			g.names = append(g.names, strings.TrimSpace(f[1]))
		}
	}
	g.ok = true
	return true
}

// Names returns GPU names discovered during the probe.
func (g *GPUCollector) Names() []string { return g.names }

// LastErr returns the last probe error, if any.
func (g *GPUCollector) LastErr() string { return g.lastErr }

// Fill appends the cached GPU metrics to the snapshot, refreshing from
// nvidia-smi only when the refresh interval has elapsed.
func (g *GPUCollector) Fill(ctx context.Context, snap *model.Snapshot) {
	if !g.ok && !g.Available(ctx) {
		return
	}
	if time.Since(g.cachedAt) >= g.every {
		g.cachedAt = time.Now()
		g.refresh(ctx)
	}
	if len(g.cached) > 0 {
		snap.GPUs = g.cached
	}
}

func (g *GPUCollector) refresh(ctx context.Context) {
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	out, err := exec.CommandContext(c, g.path, "--query-gpu="+gpuQuery, "--format=csv,noheader,nounits").Output()
	cancel()
	if err != nil {
		log.Printf("gpu: query: %v", err)
		return
	}
	rows, err := parseCSV(out)
	if err != nil {
		log.Printf("gpu: parse: %v", err)
		return
	}
	var gpus []model.GPU
	for _, f := range rows {
		if len(f) < 16 {
			continue
		}
		idx, _ := strconv.Atoi(strings.TrimSpace(f[0]))
		gpus = append(gpus, model.GPU{
			Index:          idx,
			UUID:           strings.TrimSpace(f[1]),
			Name:           strings.TrimSpace(f[2]),
			Util:           orZero(pfloat(f[3])),
			MemUtil:        orZero(pfloat(f[4])),
			MemUsed:        uint64(orZero(pfloat(f[5]))),
			MemTotal:       uint64(orZero(pfloat(f[6]))),
			Temp:           orZero(pfloat(f[7])),
			Fan:            pfloat(f[8]),
			PowerDraw:      pfloat(f[9]),
			PowerLimit:     pfloat(f[10]),
			ClockSM:        orZero(pfloat(f[11])),
			ClockMem:       orZero(pfloat(f[12])),
			PState:         strings.TrimSpace(f[13]),
			ECCCorrected:   uint64(orZero(pfloat(f[14]))),
			ECCUncorrected: uint64(orZero(pfloat(f[15]))),
		})
	}
	if len(gpus) == 0 {
		return
	}
	g.fillProcs(ctx, gpus)
	g.cached = gpus // publish a fresh slice; published slices are never mutated
}

func (g *GPUCollector) fillProcs(ctx context.Context, gpus []model.GPU) {
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	out, err := exec.CommandContext(c, g.path,
		"--query-compute-apps=gpu_uuid,pid,process_name,used_memory",
		"--format=csv,noheader,nounits").CombinedOutput()
	cancel()
	if err != nil && len(out) == 0 {
		return
	}
	rows, err := parseCSV(out)
	if err != nil {
		return
	}
	for _, f := range rows {
		if len(f) < 4 {
			continue
		}
		uuid := strings.TrimSpace(f[0])
		pid, _ := strconv.ParseInt(strings.TrimSpace(f[1]), 10, 32)
		proc := model.GPUProc{
			PID:     int32(pid),
			Name:    strings.TrimSpace(f[2]),
			MemUsed: uint64(orZero(pfloat(f[3]))),
		}
		for i := range gpus {
			if gpus[i].UUID == uuid || (uuid == "" && i == 0) {
				gpus[i].Procs = append(gpus[i].Procs, proc)
				break
			}
		}
	}
}

// pfloat parses a nvidia-smi numeric field; nil when unsupported ("[N/A]").
func pfloat(s string) *float64 {
	s = strings.TrimSpace(s)
	switch s {
	case "", "[N/A]", "N/A", "NA", "Not Supported", "[Not Supported]":
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	r := math.Round(v*10) / 10
	return &r
}

func orZero(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func parseCSV(b []byte) ([][]string, error) {
	r := csv.NewReader(bytes.NewReader(b))
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	return r.ReadAll()
}
