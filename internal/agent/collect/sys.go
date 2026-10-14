// Package collect implements the agent-side metric collectors.
package collect

import (
	"context"
	"log"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/sensors"

	"svrmon/internal/model"
)

// diskWholeRe matches whole block devices (excludes partitions like sda1,
// nvme0n1p2, and pseudo devices like loop*/ram*/zram*). It covers Linux
// physical disks (sd/nvme/vd/xvd/hd/mmcblk), md software RAID, dm/LVM
// volumes, nbd, and Windows drive letters.
var diskWholeRe = regexp.MustCompile(`^(sd[a-z]+|nvme\d+(c\d+)?n\d+|vd[a-z]+|xvd[a-z]+|hd[a-z]+|mmcblk\d+|md\d+|dm-\d+|nbd\d+|[A-Za-z]:)$`)

var netSkipPrefix = []string{"lo", "docker", "veth", "br-", "virbr", "tun", "tap", "zt", "utun", "vmnet", "vEthernet"}

// SysCollector collects CPU / memory / disk / network / load / uptime metrics.
type SysCollector struct {
	prevCPU    []cpu.TimesStat
	prevCPUAt  time.Time
	prevDisk   map[string]disk.IOCountersStat
	prevDiskAt time.Time
	prevNet    map[string]gnet.IOCountersStat
	prevNetAt  time.Time
	mounts     []model.Mount
	tick       int
	freqMHz    float64
	temp       float64
	tempAt     time.Time
}

// NewSysCollector probes static CPU info (model, nominal frequency).
func NewSysCollector() *SysCollector {
	s := &SysCollector{}
	if infos, err := cpu.Info(); err == nil {
		for _, ci := range infos {
			if ci.Mhz > 0 {
				s.freqMHz = math.Round(ci.Mhz)
				break
			}
		}
	}
	return s
}

// Fill populates the system sections of a snapshot.
func (s *SysCollector) Fill(ctx context.Context, snap *model.Snapshot) {
	s.tick++
	s.fillCPU(ctx, snap)
	s.fillMem(snap)
	s.fillLoad(snap)
	s.fillDisks(snap)
	if s.tick%15 == 1 {
		s.fillMounts(ctx, snap)
	} else {
		snap.Mounts = s.mounts
	}
	s.fillNets(snap)
	if hi, err := host.Info(); err == nil {
		snap.Uptime = hi.Uptime
	}
}

func (s *SysCollector) fillCPU(ctx context.Context, snap *model.Snapshot) {
	times, err := cpu.Times(true)
	if err != nil {
		log.Printf("sys: cpu times: %v", err)
		return
	}
	now := time.Now()
	c := &model.CPU{Freq: s.freqMHz}
	if s.prevCPU != nil && now.After(s.prevCPUAt) {
		n := len(times)
		if pn := len(s.prevCPU); pn < n {
			n = pn
		}
		var allSum, busySum, iowaitSum float64
		for i := 0; i < n; i++ {
			cur, prev := times[i], s.prevCPU[i]
			dAll := cur.Total() - prev.Total()
			dIdle := (cur.Idle + cur.Iowait) - (prev.Idle + prev.Iowait)
			dIowait := cur.Iowait - prev.Iowait
			if dAll <= 0 {
				continue
			}
			u := (dAll - dIdle) / dAll * 100
			if u < 0 {
				u = 0
			}
			c.PerCore = append(c.PerCore, math.Round(u*10)/10)
			allSum += dAll
			busySum += dAll - dIdle
			iowaitSum += dIowait
		}
		if allSum > 0 {
			c.Usage = math.Round(busySum/allSum*1000) / 10
			c.Iowait = math.Round(iowaitSum/allSum*1000) / 10
		}
	}
	s.prevCPU, s.prevCPUAt = times, now

	// CPU temperature, refreshed at most every 10s.
	if time.Since(s.tempAt) > 10*time.Second {
		s.tempAt = time.Now()
		tctx, tcancel := context.WithTimeout(ctx, 3*time.Second)
		temps, err := sensors.TemperaturesWithContext(tctx)
		tcancel()
		if err == nil {
			best := 0.0
			for _, t := range temps {
				if t.Temperature <= 0 || t.Temperature > 120 {
					continue
				}
				k := t.SensorKey
				if strings.HasPrefix(k, "coretemp") || strings.HasPrefix(k, "k10temp") ||
					strings.HasPrefix(k, "zenpower") || strings.HasPrefix(k, "cpu_thermal") ||
					strings.Contains(k, "package") {
					if t.Temperature > best {
						best = t.Temperature
					}
				}
			}
			s.temp = math.Round(best*10) / 10
		}
	}
	c.Temp = s.temp
	snap.CPU = c
}

func (s *SysCollector) fillMem(snap *model.Snapshot) {
	vm, err := mem.VirtualMemory()
	if err != nil {
		log.Printf("sys: mem: %v", err)
		return
	}
	m := &model.Mem{
		Total:     vm.Total,
		Used:      vm.Used,
		Available: vm.Available,
		Cached:    vm.Cached,
		Buffers:   vm.Buffers,
	}
	if sm, err := mem.SwapMemory(); err == nil && sm != nil {
		m.SwapTotal = sm.Total
		m.SwapUsed = sm.Used
	}
	snap.Mem = m
}

func (s *SysCollector) fillLoad(snap *model.Snapshot) {
	if av, err := load.Avg(); err == nil {
		snap.Load = &model.Load{Load1: av.Load1, Load5: av.Load5, Load15: av.Load15}
	}
}

func (s *SysCollector) fillDisks(snap *model.Snapshot) {
	counters, err := disk.IOCounters()
	if err != nil {
		log.Printf("sys: disk io: %v", err)
		return
	}
	now := time.Now()
	var out []model.DiskIO
	for name, st := range counters {
		if !diskWholeRe.MatchString(name) {
			continue
		}
		d := model.DiskIO{Name: name}
		if prev, ok := s.prevDisk[name]; ok && now.After(s.prevDiskAt) {
			dt := now.Sub(s.prevDiskAt).Seconds()
			if dt > 0 {
				d.ReadBps = clampF(float64(st.ReadBytes-prev.ReadBytes) / dt)
				d.WriteBps = clampF(float64(st.WriteBytes-prev.WriteBytes) / dt)
				d.ReadIOPS = clampF(float64(st.ReadCount-prev.ReadCount) / dt)
				d.WriteIOPS = clampF(float64(st.WriteCount-prev.WriteCount) / dt)
				util := float64(st.IoTime-prev.IoTime) / (dt * 1000) * 100
				d.Util = math.Min(100, math.Max(0, util))
			}
		}
		out = append(out, d)
	}
	s.prevDisk, s.prevDiskAt = counters, now
	if len(out) > 0 {
		snap.Disks = out
	}
}

var mountFSTypes = map[string]bool{
	"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true,
	"zfs": true, "f2fs": true, "jfs": true, "reiserfs": true, "ntfs": true,
	"exfat": true, "vfat": true, "apfs": true, "overlay": true, "refs": true,
}

func (s *SysCollector) fillMounts(ctx context.Context, snap *model.Snapshot) {
	parts, err := disk.Partitions(false)
	if err != nil {
		log.Printf("sys: partitions: %v", err)
		snap.Mounts = s.mounts
		return
	}
	var out []model.Mount
	for _, p := range parts {
		if !mountFSTypes[strings.ToLower(p.Fstype)] {
			continue
		}
		c, cancel := context.WithTimeout(ctx, 2*time.Second)
		u, err := disk.UsageWithContext(c, p.Mountpoint)
		cancel()
		if err != nil || u.Total == 0 {
			continue
		}
		out = append(out, model.Mount{
			Device: p.Device, MountPoint: p.Mountpoint, FSType: p.Fstype,
			Total: u.Total, Used: u.Used, Free: u.Free,
		})
	}
	s.mounts = out
	snap.Mounts = out
}

func (s *SysCollector) fillNets(snap *model.Snapshot) {
	stats, err := gnet.IOCounters(true)
	if err != nil {
		log.Printf("sys: net io: %v", err)
		return
	}
	now := time.Now()
	var out []model.NetIO
	for _, st := range stats {
		if skipNet(st.Name) {
			continue
		}
		n := model.NetIO{Name: st.Name, RxErrs: st.Errin, TxDrops: st.Dropout}
		if prev, ok := s.prevNet[st.Name]; ok && now.After(s.prevNetAt) {
			dt := now.Sub(s.prevNetAt).Seconds()
			if dt > 0 {
				n.RxBps = clampF(float64(st.BytesRecv-prev.BytesRecv) / dt)
				n.TxBps = clampF(float64(st.BytesSent-prev.BytesSent) / dt)
			}
		}
		out = append(out, n)
	}
	prev := map[string]gnet.IOCountersStat{}
	for _, st := range stats {
		prev[st.Name] = st
	}
	s.prevNet, s.prevNetAt = prev, now
	if len(out) > 0 {
		snap.Nets = out
	}
}

func skipNet(name string) bool {
	for _, p := range netSkipPrefix {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func clampF(v float64) float64 {
	if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*10) / 10
}
