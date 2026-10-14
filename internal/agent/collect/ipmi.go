package collect

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"svrmon/internal/model"
)

var validPowerActions = map[string]bool{
	"on": true, "off": true, "soft": true, "cycle": true, "reset": true, "status": true,
}

// IPMICollector reads local BMC sensors through ipmitool. Sensor reads are
// slow (1-3s) so results are cached and refreshed on their own schedule.
type IPMICollector struct {
	path string
	mu   sync.Mutex

	sensorEvery time.Duration
	selEvery    time.Duration

	sensors   []model.Sensor
	powerW    float64
	sensorAt  time.Time
	sel       []model.SELEntry
	selAt     time.Time
	failUntil time.Time
	lastErr   string
	everOK    bool
}

// NewIPMICollector creates a collector with the given refresh intervals.
func NewIPMICollector(path string, sensorSec, selSec int) *IPMICollector {
	if sensorSec <= 0 {
		sensorSec = 30
	}
	if selSec <= 0 {
		selSec = 60
	}
	return &IPMICollector{
		path:        path,
		sensorEvery: time.Duration(sensorSec) * time.Second,
		selEvery:    time.Duration(selSec) * time.Second,
	}
}

func (c *IPMICollector) run(ctx context.Context, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, c.path, args...).CombinedOutput()
	return string(bytes.TrimSpace(out)), err
}

// Probe checks whether local IPMI is usable (ipmitool present + device opens).
func (c *IPMICollector) Probe(ctx context.Context) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.probeLocked(ctx)
}

func (c *IPMICollector) probeLocked(ctx context.Context) bool {
	if time.Now().Before(c.failUntil) {
		return false
	}
	if _, err := c.run(ctx, "mc", "info"); err != nil {
		c.lastErr = classifyIPMIError(err)
		c.failUntil = time.Now().Add(backoffFor(c.lastErr))
		return false
	}
	c.everOK = true
	c.lastErr = ""
	return true
}

func classifyIPMIError(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return strings.ToLower(string(ee.Stderr))
	}
	if errors.Is(err, exec.ErrNotFound) {
		return "ipmitool not found"
	}
	return err.Error()
}

func backoffFor(msg string) time.Duration {
	m := strings.ToLower(msg)
	if strings.Contains(m, "not found") {
		return 365 * 24 * time.Hour // effectively permanent
	}
	if strings.Contains(m, "unable to open") || strings.Contains(m, "could not open") ||
		strings.Contains(m, "device") {
		return 10 * time.Minute
	}
	return 30 * time.Second
}

// Fill attaches the cached IPMI state, refreshing sensors / SEL as needed.
func (c *IPMICollector) Fill(ctx context.Context, snap *model.Snapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	if now.After(c.failUntil) {
		if now.Sub(c.sensorAt) >= c.sensorEvery {
			if err := c.refreshSensorsLocked(ctx); err != nil {
				c.lastErr = err.Error()
				c.failUntil = time.Now().Add(backoffFor(c.lastErr))
			} else {
				c.sensorAt = now
				c.lastErr = ""
				c.everOK = true
			}
		}
		if now.Sub(c.selAt) >= c.selEvery && now.Sub(c.sensorAt) < c.sensorEvery {
			// only bother with SEL when sensors are working
			_ = c.refreshSELLocked(ctx)
			c.selAt = now
		}
	}
	avail := c.everOK && c.lastErr == "" && len(c.sensors) > 0
	ipmi := &model.IPMI{
		TS:         now.Unix(),
		Available:  avail,
		PowerWatts: c.powerW,
	}
	if !avail {
		if c.lastErr != "" {
			ipmi.Error = c.lastErr
		} else if len(c.sensors) == 0 {
			ipmi.Error = "no sensors"
		}
	}
	if len(c.sensors) > 0 {
		ipmi.Sensors = c.sensors
	}
	if len(c.sel) > 0 {
		ipmi.SEL = c.sel
	}
	snap.IPMI = ipmi
}

func (c *IPMICollector) refreshSensorsLocked(ctx context.Context) error {
	out, err := c.run(ctx, "sensor", "list")
	if err != nil {
		return fmt.Errorf("ipmitool sensor list: %s", firstLine(out))
	}
	var sensors []model.Sensor
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "|")
		if len(f) < 4 {
			continue
		}
		name := strings.TrimSpace(f[0])
		if name == "" {
			continue
		}
		value := strings.TrimSpace(f[1])
		s := model.Sensor{
			Name:   name,
			Value:  value,
			Unit:   strings.TrimSpace(f[2]),
			Status: strings.ToLower(strings.TrimSpace(f[3])),
		}
		if v, e := strconv.ParseFloat(value, 64); e == nil {
			s.ValueF = v
			s.HasValue = true
		}
		sensors = append(sensors, s)
	}
	if len(sensors) == 0 {
		return fmt.Errorf("empty sensor list")
	}
	c.sensors = sensors
	c.powerW = wattsFromSensors(sensors)
	return nil
}

// wattsFromSensors picks the total system power draw among Watt sensors.
func wattsFromSensors(sensors []model.Sensor) float64 {
	var totalCands, allCands []float64
	for _, s := range sensors {
		if !s.HasValue || !strings.EqualFold(s.Unit, "watts") {
			continue
		}
		n := strings.ToLower(s.Name)
		isTotal := (strings.Contains(n, "sys") || strings.Contains(n, "total") || strings.Contains(n, "system") || strings.Contains(n, "board")) &&
			(strings.Contains(n, "pwr") || strings.Contains(n, "power"))
		if isTotal {
			totalCands = append(totalCands, s.ValueF)
		}
		allCands = append(allCands, s.ValueF)
	}
	pick := func(cands []float64) float64 {
		best := 0.0
		for _, v := range cands {
			if v > best {
				best = v
			}
		}
		return best
	}
	if len(totalCands) > 0 {
		return pick(totalCands)
	}
	return pick(allCands)
}

func (c *IPMICollector) refreshSELLocked(ctx context.Context) error {
	out, err := c.run(ctx, "sel", "list")
	if err != nil {
		return fmt.Errorf("ipmitool sel list: %s", firstLine(out))
	}
	if strings.Contains(strings.ToLower(out), "no entries") {
		c.sel = nil
		return nil
	}
	var entries []model.SELEntry
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "|")
		if len(f) < 4 {
			continue
		}
		id, _ := strconv.Atoi(strings.TrimSpace(f[0]))
		rest := make([]string, 0, len(f)-3)
		for _, x := range f[3:] {
			rest = append(rest, strings.TrimSpace(x))
		}
		entries = append(entries, model.SELEntry{
			ID:    id,
			Date:  strings.TrimSpace(f[1]),
			Time:  strings.TrimSpace(f[2]),
			Event: strings.Join(rest, " | "),
		})
	}
	if len(entries) > 100 {
		entries = entries[len(entries)-100:]
	}
	c.sel = entries
	return nil
}

// Power executes a chassis power action and returns the ipmitool output.
func (c *IPMICollector) Power(ctx context.Context, action string) (string, error) {
	if !validPowerActions[action] {
		return "", fmt.Errorf("invalid power action %q", action)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out, err := c.run(ctx, "chassis", "power", action)
	if err != nil {
		return out, fmt.Errorf("ipmitool chassis power %s: %s", action, firstLine(out))
	}
	return out, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
