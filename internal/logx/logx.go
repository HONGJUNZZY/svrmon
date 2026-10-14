// Package logx implements a minimal daily-rotating log writer with
// time-based retention, driven through the standard library log package.
//
// Output goes to <dir>/<prefix>-YYYYMMDD.log; the file rolls over at
// midnight and files older than the retention window are deleted
// automatically (at startup, at each rollover and once per hour).
package logx

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const dayLayout = "20060102"

// Writer is an io.WriteCloser with daily rotation and retention pruning.
type Writer struct {
	dir       string
	prefix    string
	retention time.Duration

	mu   sync.Mutex
	day  string
	file *os.File
	once sync.Once
	stop chan struct{}
}

// Setup redirects the standard logger to a daily-rotating file at path.
// retentionDays <= 0 means 7. Callers should defer Close() on the result.
func Setup(path string, retentionDays int) (*Writer, error) {
	if retentionDays <= 0 {
		retentionDays = 7
	}
	w, err := New(path, time.Duration(retentionDays)*24*time.Hour)
	if err != nil {
		return nil, err
	}
	log.SetOutput(w)
	return w, nil
}

// New creates a writer for the given file path. The path's directory and
// base name (minus an optional .log suffix) define the output location
// and the rotation prefix.
func New(path string, retention time.Duration) (*Writer, error) {
	if path == "" {
		return nil, fmt.Errorf("logx: empty path")
	}
	dir := filepath.Dir(path)
	prefix := strings.TrimSuffix(filepath.Base(path), ".log")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("logx: create %s: %w", dir, err)
	}
	w := &Writer{
		dir:       dir,
		prefix:    prefix,
		retention: retention,
		stop:      make(chan struct{}),
	}
	if err := w.open(time.Now()); err != nil {
		return nil, err
	}
	go w.loop()
	return w, nil
}

func (w *Writer) path(now time.Time) string {
	return filepath.Join(w.dir, fmt.Sprintf("%s-%s.log", w.prefix, now.Format(dayLayout)))
}

func (w *Writer) open(now time.Time) error {
	f, err := os.OpenFile(w.path(now), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return fmt.Errorf("logx: open %s: %w", w.path(now), err)
	}
	w.file = f
	w.day = now.Format(dayLayout)
	w.prune(now)
	return nil
}

// Write appends to today's file, rolling over when the date changes.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	if w.file == nil || now.Format(dayLayout) != w.day {
		if w.file != nil {
			_ = w.file.Close()
		}
		if err := w.open(now); err != nil {
			return 0, err
		}
	}
	return w.file.Write(p)
}

// Close stops the pruning loop and closes the current file.
func (w *Writer) Close() error {
	w.once.Do(func() { close(w.stop) })
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		err := w.file.Close()
		w.file = nil
		return err
	}
	return nil
}

// loop rolls the file at midnight and prunes hourly.
func (w *Writer) loop() {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-w.stop:
			return
		case now := <-t.C:
			w.mu.Lock()
			if w.file != nil && now.Format(dayLayout) != w.day {
				_ = w.file.Close()
				if err := w.open(now); err != nil {
					w.file = nil // next Write retries
				}
			}
			w.prune(now)
			w.mu.Unlock()
		}
	}
}

// prune deletes this writer's files whose date is older than the retention
// window. Callers must hold w.mu (or be inside open, which runs locked).
func (w *Writer) prune(now time.Time) {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return
	}
	cutoff := now.Add(-w.retention).Format(dayLayout)
	prefix := w.prefix + "-"
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".log") {
			continue
		}
		day := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".log")
		if len(day) != 8 {
			continue
		}
		if _, err := time.Parse(dayLayout, day); err != nil {
			continue // not one of ours
		}
		if day < cutoff {
			_ = os.Remove(filepath.Join(w.dir, name))
		}
	}
}
