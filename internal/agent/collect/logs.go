package collect

import (
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"svrmon/internal/model"
)

// LogsCollector tails severe error events and forwards only new entries on
// each poll. Two interchangeable backends are auto-detected:
//   - journalctl (Linux): priority err and worse, cursor-based dedup
//   - Windows event log via PowerShell Get-WinEvent: System channel,
//     Level 1 (Critical) / 2 (Error), RecordId-based dedup
type LogsCollector struct {
	backend string // "journal" | "winlog" | ""
	every   time.Duration
	backfill time.Duration

	mu        sync.Mutex
	seen      map[string]struct{} // journal cursors
	lastRID   uint64              // windows record id cursor
	since     time.Time
	lastPoll  time.Time
	ok        bool
	failUntil time.Time
}

// NewLogsCollector builds a collector; intervalSec/backfillMin <= 0 use defaults (30s / 10min).
func NewLogsCollector(path string, intervalSec, backfillMin int) *LogsCollector {
	if intervalSec <= 0 {
		intervalSec = 30
	}
	if backfillMin <= 0 {
		backfillMin = 10
	}
	backend := ""
	if _, err := exec.LookPath(path); err == nil {
		backend = "journal"
	} else if _, err := exec.LookPath("powershell"); err == nil {
		backend = "winlog"
	}
	return &LogsCollector{
		backend:  backend,
		every:    time.Duration(intervalSec) * time.Second,
		backfill: time.Duration(backfillMin) * time.Minute,
		seen:     map[string]struct{}{},
		since:    time.Now().Add(-time.Duration(backfillMin) * time.Minute),
	}
}

type jEntry struct {
	Cursor    flexString `json:"__CURSOR"`
	TS        flexString `json:"__REALTIME_TIMESTAMP"`
	Prio      flexString `json:"PRIORITY"`
	Message   string     `json:"MESSAGE"`
	Transport string     `json:"_TRANSPORT"`
	Ident     string     `json:"SYSLOG_IDENTIFIER"`
	Unit      string     `json:"_SYSTEMD_UNIT"`
	Comm      string     `json:"_COMM"`
}

// flexString accepts both "123" (journalctl's usual string form) and 123.
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	*f = flexString(strings.Trim(string(b), `"`))
	return nil
}

type winEntry struct {
	RID  uint64 `json:"rid"`
	TS   int64  `json:"ts"`
	Lvl  int    `json:"lvl"`
	EID  int    `json:"eid"`
	Unit string `json:"unit"`
	Msg  string `json:"msg"`
}

// Fill appends entries discovered since the previous poll to the snapshot.
func (l *LogsCollector) Fill(ctx context.Context, snap *model.Snapshot) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.backend == "" {
		return
	}
	now := time.Now()
	if !l.ok && now.Before(l.failUntil) {
		return
	}
	if l.ok && now.Sub(l.lastPoll) < l.every {
		return
	}
	l.lastPoll = now

	var entries []model.LogEntry
	if l.backend == "journal" {
		entries = l.pollJournal(ctx, now)
	} else {
		entries = l.pollWinLog(ctx, now)
	}
	if len(l.seen) > 2000 {
		l.seen = map[string]struct{}{}
	}
	if len(entries) > 0 {
		snap.Logs = entries
	}
}

func (l *LogsCollector) pollJournal(ctx context.Context, now time.Time) []model.LogEntry {
	sinceArg := l.since.Add(-5 * time.Second) // overlap, dedup handles repeats
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "journalctl",
		"-p", "3", "-o", "json", "--no-pager",
		"--since", "@"+strconv.FormatInt(sinceArg.Unix(), 10)).Output()
	if err != nil && len(out) == 0 {
		if !l.ok {
			l.failUntil = now.Add(5 * time.Minute)
		}
		return nil
	}
	l.ok = true
	l.since = now

	var entries []model.LogEntry
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] != '{' {
			continue
		}
		var je jEntry
		if json.Unmarshal([]byte(line), &je) != nil || je.Cursor == "" {
			continue
		}
		if _, dup := l.seen[string(je.Cursor)]; dup {
			continue
		}
		l.seen[string(je.Cursor)] = struct{}{}

		tsUs, _ := strconv.ParseInt(string(je.TS), 10, 64)
		prio, _ := strconv.Atoi(string(je.Prio))
		src := "system"
		if je.Transport == "kernel" || je.Ident == "kernel" {
			src = "kernel"
		}
		unit := je.Unit
		if unit == "" {
			unit = je.Ident
		}
		if unit == "" {
			unit = je.Comm
		}
		entries = append(entries, model.LogEntry{
			TS: tsUs / 1_000_000, Prio: prio, Source: src, Unit: unit,
			Message: truncateRunes(strings.TrimSpace(je.Message), 500),
		})
		if len(entries) >= 100 {
			break
		}
	}
	return entries
}

// psUTF8 forces PowerShell 5.1 to write UTF-8 to the pipe (default is the
// OEM code page, which breaks Go's UTF-8 JSON parsing on Chinese systems).
const psUTF8 = `[Console]::OutputEncoding=[System.Text.Encoding]::UTF8;`

// winlogQuery is the PowerShell used for event polling. %d = lookback seconds.
const winlogQuery = psUTF8 +
	`$ErrorActionPreference='SilentlyContinue';` +
	`$s=(Get-Date).AddSeconds(-%d);` +
	`$e=Get-WinEvent -FilterHashtable @{LogName='System'; Level=@(1,2); StartTime=$s} | Sort-Object RecordId |` +
	` ForEach-Object { [PSCustomObject]@{ rid=$_.RecordId; ts=([DateTimeOffset]$_.TimeCreated).ToUnixTimeSeconds(); lvl=$_.Level; eid=$_.Id; unit=$_.ProviderName; msg=($_.Message -replace '\s+',' ') } };` +
	` if($e){ ConvertTo-Json -Compress -InputObject @($e) } else { '[]' }`

func (l *LogsCollector) pollWinLog(ctx context.Context, now time.Time) []model.LogEntry {
	lookback := int(now.Sub(l.since.Add(-30 * time.Second)).Seconds())
	if lookback < 60 {
		lookback = 60
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	script := strings.Replace(winlogQuery, "%d", strconv.Itoa(lookback), 1)
	out, err := exec.CommandContext(cctx, "powershell", "-NoProfile", "-Command", script).Output()
	if err != nil && len(out) == 0 {
		if !l.ok {
			l.failUntil = now.Add(5 * time.Minute)
		}
		return nil
	}
	l.ok = true
	l.since = now

	var rows []winEntry
	if err := json.Unmarshal(trimBOM(out), &rows); err != nil {
		return nil
	}
	var entries []model.LogEntry
	for _, r := range rows {
		if r.RID == 0 || r.RID <= l.lastRID {
			continue
		}
		prio := 3 // Error
		if r.Lvl == 1 {
			prio = 2 // Critical
		}
		msg := strings.TrimSpace(r.Msg)
		if msg == "" {
			msg = "事件 ID " + strconv.Itoa(r.EID)
		}
		entries = append(entries, model.LogEntry{
			TS: r.TS, Prio: prio, Source: "windows", Unit: r.Unit,
			Message: truncateRunes(msg, 500),
		})
		if r.RID > l.lastRID {
			l.lastRID = r.RID
		}
		if len(entries) >= 100 {
			break
		}
	}
	return entries
}

// trimBOM strips a UTF-8 BOM that PowerShell may emit.
func trimBOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
