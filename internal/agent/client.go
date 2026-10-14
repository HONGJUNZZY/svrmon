// Package agent implements the monitoring agent: it collects local metrics
// and streams them to the hub over a WebSocket connection.
package agent

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"

	"svrmon/internal/agent/collect"
	"svrmon/internal/model"
)

// Version is the agent build version.
const Version = "0.2.0"

// GPUConfig controls the NVIDIA collector.
type GPUConfig struct {
	Enable      bool   `json:"enable"`
	Path        string `json:"path"`
	IntervalSec int    `json:"intervalSec"` // nvidia-smi execution interval
}

// IPMIConfig controls the local BMC collector.
type IPMIConfig struct {
	Enable    bool   `json:"enable"`
	Path      string `json:"path"`
	SensorSec int    `json:"sensorSec"`
	SELSec    int    `json:"selSec"`
}

// LogsConfig controls the severe-journal (kernel/hardware/system error) collector.
type LogsConfig struct {
	Enable      bool   `json:"enable"`
	Path        string `json:"path"`
	IntervalSec int    `json:"intervalSec"`
	BackfillMin int    `json:"backfillMin"`
}

// HWConfig controls the static hardware inventory collector.
type HWConfig struct {
	Enable     bool `json:"enable"`
	IntervalMin int `json:"intervalMin"` // refresh interval, minutes
}

// Config is the agent configuration file format.
type Config struct {
	HubURL     string     `json:"hubUrl"`
	Token      string     `json:"token"`
	Name       string     `json:"name"`
	IntervalMS int        `json:"intervalMs"`
	GPU        GPUConfig  `json:"gpu"`
	IPMI       IPMIConfig `json:"ipmi"`
	Logs       LogsConfig `json:"logs"`
	HW         HWConfig   `json:"hw"`
	LogFile    string     `json:"logFile"`          // empty = stderr/journald
	LogRetentionDays int  `json:"logRetentionDays"` // daily rotation, auto-delete older
}

// DefaultConfig returns sane defaults.
func DefaultConfig() Config {
	return Config{
		HubURL:     "ws://127.0.0.1:8080/agent",
		Token:      "change-me",
		IntervalMS: 2000,
		GPU:        GPUConfig{Enable: true, Path: "nvidia-smi", IntervalSec: 5},
		IPMI:       IPMIConfig{Enable: true, Path: "ipmitool", SensorSec: 30, SELSec: 60},
		Logs:       LogsConfig{Enable: true, Path: "journalctl", IntervalSec: 30, BackfillMin: 10},
		HW:         HWConfig{Enable: true, IntervalMin: 30},
		LogRetentionDays: 7,
	}
}

// Agent bundles the collectors and the hub connection.
type Agent struct {
	cfg  Config
	sys  *collect.SysCollector
	gpu  *collect.GPUCollector
	ipmi *collect.IPMICollector
	logs *collect.LogsCollector
	hw   *collect.HWConfigCollector
	info model.ServerInfo

	conn    *websocket.Conn
	writeMu sync.Mutex
}

// New builds an agent from configuration.
func New(cfg Config) *Agent {
	a := &Agent{cfg: cfg, sys: collect.NewSysCollector()}
	if cfg.GPU.Enable {
		a.gpu = collect.NewGPUCollector(cfg.GPU.Path, cfg.GPU.IntervalSec)
	}
	if cfg.IPMI.Enable {
		a.ipmi = collect.NewIPMICollector(cfg.IPMI.Path, cfg.IPMI.SensorSec, cfg.IPMI.SELSec)
	}
	if cfg.Logs.Enable {
		a.logs = collect.NewLogsCollector(cfg.Logs.Path, cfg.Logs.IntervalSec, cfg.Logs.BackfillMin)
	}
	if cfg.HW.Enable {
		a.hw = collect.NewHWConfigCollector(cfg.HW.IntervalMin)
	}
	return a
}

func (a *Agent) buildInfo(ctx context.Context) {
	if hi, err := host.Info(); err == nil {
		a.info.Hostname = hi.Hostname
		a.info.OS = hi.OS
		a.info.Platform = fmt.Sprintf("%s %s", hi.Platform, hi.PlatformVersion)
		a.info.Kernel = hi.KernelVersion
		a.info.KernelArch = hi.KernelArch
		a.info.BootAt = time.Now().Unix() - int64(hi.Uptime)
	} else {
		a.info.Hostname = "unknown"
	}
	if a.cfg.Name != "" {
		a.info.Hostname = a.cfg.Name
	}
	if cores, err := cpu.Counts(true); err == nil {
		a.info.CPUCores = cores
	}
	if ci, err := cpu.Info(); err == nil && len(ci) > 0 {
		a.info.CPUModel = ci[0].ModelName
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		a.info.MemTotal = vm.Total
	}
	a.info.AgentVer = Version
	if a.gpu != nil && a.gpu.Available(ctx) {
		a.info.GPUCount = len(a.gpu.Names())
		a.info.GPUNames = a.gpu.Names()
	}
	if a.ipmi != nil {
		a.info.IPMIAvail = a.ipmi.Probe(ctx)
	}
	// Boot evidence for the hub's abnormal-shutdown detection.
	a.info.BootID = collect.ReadBootID()
	a.info.LastShutdownUnclean = collect.DetectUncleanShutdown(ctx)
}

// Run connects to the hub and streams snapshots until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) {
	a.buildInfo(ctx)
	log.Printf("agent: server=%q cpu=%d cores gpu=%d ipmi=%v",
		a.info.Hostname, a.info.CPUCores, a.info.GPUCount, a.info.IPMIAvail)

	backoff := 2 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		started := time.Now()
		err := a.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Printf("agent: %v", err)
		}
		if time.Since(started) > time.Minute {
			backoff = 2 * time.Second
		} else {
			backoff = minDur(backoff*2, 30*time.Second)
		}
		wait := backoff + time.Duration(rand.Int63n(int64(time.Second)))
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (a *Agent) session(ctx context.Context) error {
	u, err := url.Parse(a.cfg.HubURL)
	if err != nil {
		return fmt.Errorf("hub url: %w", err)
	}
	q := u.Query()
	if a.cfg.Token != "" {
		q.Set("token", a.cfg.Token)
	}
	u.RawQuery = q.Encode()

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	conn, _, err := websocket.DefaultDialer.DialContext(dialCtx, u.String(), nil)
	cancel()
	if err != nil {
		return fmt.Errorf("dial %s: %w", u.Host, err)
	}
	defer conn.Close()
	a.conn = conn
	log.Printf("agent: connected to %s", u.Host)

	conn.SetReadLimit(1 << 20)
	a.writeMu.Lock()
	err = conn.WriteJSON(model.AgentHelloMsg{Type: model.TypeHello, Info: a.info})
	a.writeMu.Unlock()
	if err != nil {
		return fmt.Errorf("hello: %w", err)
	}

	cmds := make(chan model.AgentCmdMsg, 4)
	go func() {
		defer close(cmds)
		_ = conn.SetReadDeadline(time.Now().Add(120 * time.Second))
		conn.SetPongHandler(func(string) error {
			return conn.SetReadDeadline(time.Now().Add(120 * time.Second))
		})
		for {
			var msg model.AgentCmdMsg
			if err := conn.ReadJSON(&msg); err != nil {
				return
			}
			select {
			case cmds <- msg:
			default:
			}
		}
	}()

	interval := time.Duration(a.cfg.IntervalMS) * time.Millisecond
	if interval < 500*time.Millisecond {
		interval = 500 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()

	// Hardware inventory: send right after connect, then on its own schedule.
	var hwC <-chan time.Time
	if a.hw != nil {
		if cfgData := a.hw.Get(ctx); cfgData != nil {
			a.writeMu.Lock()
			_ = conn.WriteJSON(model.AgentConfigMsg{Type: model.TypeConfig, Data: *cfgData})
			a.writeMu.Unlock()
		}
		hwTicker := time.NewTicker(a.hwInterval())
		defer hwTicker.Stop()
		hwC = hwTicker.C
	}

	for {
		select {
		case <-ctx.Done():
			// Graceful stop (SIGTERM from systemd): say goodbye so the hub
			// does not raise an abnormal-shutdown alert.
			a.writeMu.Lock()
			_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
			if err := conn.WriteJSON(model.AgentGoodbyeMsg{Type: model.TypeGoodbye, Reason: "stopping"}); err != nil {
				log.Printf("agent: goodbye: %v", err)
			}
			a.writeMu.Unlock()
			_ = conn.Close()
			return nil
		case <-hwC:
			if cfgData := a.hw.Get(ctx); cfgData != nil {
				a.writeMu.Lock()
				if err := conn.WriteJSON(model.AgentConfigMsg{Type: model.TypeConfig, Data: *cfgData}); err != nil {
					log.Printf("agent: send config: %v", err)
				}
				a.writeMu.Unlock()
			}
		case <-ticker.C:
			if err := a.sendSnapshot(ctx, conn); err != nil {
				return err
			}
		case <-ping.C:
			a.writeMu.Lock()
			err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			a.writeMu.Unlock()
			if err != nil {
				return err
			}
		case msg, ok := <-cmds:
			if !ok {
				return fmt.Errorf("connection closed by hub")
			}
			a.handleCmd(ctx, conn, msg)
		}
	}
}

func (a *Agent) sendSnapshot(ctx context.Context, conn *websocket.Conn) error {
	snap := &model.Snapshot{TS: time.Now().Unix()}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	a.sys.Fill(cctx, snap)
	if a.gpu != nil {
		a.gpu.Fill(cctx, snap)
	}
	if a.ipmi != nil {
		a.ipmi.Fill(cctx, snap)
	}
	if a.logs != nil {
		a.logs.Fill(cctx, snap)
	}
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return conn.WriteJSON(model.AgentSnapshotMsg{Type: model.TypeSnapshot, Data: *snap})
}

func (a *Agent) handleCmd(ctx context.Context, conn *websocket.Conn, msg model.AgentCmdMsg) {
	log.Printf("agent: cmd %s/%s", msg.Cmd, msg.Action)
	res := model.AgentCmdResultMsg{Type: model.TypeCmdResult, ID: msg.ID}
	switch msg.Cmd {
	case "ipmi_power":
		if a.ipmi == nil {
			res.Output = "ipmi module disabled on this agent"
		} else {
			cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
			out, err := a.ipmi.Power(cctx, msg.Action)
			cancel()
			res.OK = err == nil
			res.Output = strings.TrimSpace(out)
			if err != nil {
				res.Output = err.Error() + " | " + res.Output
			}
		}
	default:
		res.Output = "unknown cmd " + msg.Cmd
	}
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	if err := conn.WriteJSON(res); err != nil {
		log.Printf("agent: send cmd result: %v", err)
	}
}

// hwInterval returns the inventory refresh interval with a sane minimum.
func (a *Agent) hwInterval() time.Duration {
	if a.hw != nil {
		return a.hw.Every()
	}
	return 30 * time.Minute
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
