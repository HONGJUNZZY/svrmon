package collect

import (
	"context"
	"encoding/json"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"

	"svrmon/internal/model"
)

// HWConfigCollector gathers the static hardware inventory (CPU / DIMMs /
// disks / NICs / GPU / board / BIOS) via standard commands. It refreshes on
// its own slow schedule (default 30 minutes) and never blocks snapshots.
type HWConfigCollector struct {
	every time.Duration
	mu    sync.Mutex
	cached *model.MachineConfig
	lastAt time.Time
}

// NewHWConfigCollector builds a collector; intervalMin <= 0 means 30.
func NewHWConfigCollector(intervalMin int) *HWConfigCollector {
	if intervalMin <= 0 {
		intervalMin = 30
	}
	return &HWConfigCollector{every: time.Duration(intervalMin) * time.Minute}
}

// Every exposes the refresh interval.
func (h *HWConfigCollector) Every() time.Duration { return h.every }

// Get returns the cached inventory, refreshing when older than the interval.
func (h *HWConfigCollector) Get(ctx context.Context) *model.MachineConfig {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cached != nil && time.Since(h.lastAt) < h.every {
		return h.cached
	}
	cfg := gatherMachineConfig(ctx)
	if cfg == nil {
		return h.cached // keep previous on partial failure
	}
	h.cached = cfg
	h.lastAt = time.Now()
	return cfg
}

func runCmd(ctx context.Context, timeout time.Duration, name string, args ...string) string {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := exec.CommandContext(cctx, name, args...).CombinedOutput()
	if err != nil {
		return ""
	}
	return string(out)
}

func gatherMachineConfig(ctx context.Context) *model.MachineConfig {
	cfg := &model.MachineConfig{CollectedAt: time.Now().Unix()}

	// --- OS / kernel / arch / virtualization ---
	if hi, err := host.Info(); err == nil {
		cfg.OS = strings.TrimSpace(hi.Platform + " " + hi.PlatformVersion)
		cfg.Kernel = hi.KernelVersion
		cfg.Arch = hi.KernelArch
	}
	if v := strings.TrimSpace(runCmd(ctx, 5*time.Second, "systemd-detect-virt")); v != "" && v != "none" {
		cfg.Virtual = v
	}

	// --- CPU: lscpu preferred, gopsutil fallback ---
	if m := parseLscpu(runCmd(ctx, 5*time.Second, "lscpu")); m != nil {
		cfg.CPUModel = m["Model name"]
		cfg.CPUSockets = atoiSafe(m["Socket(s)"])
		cfg.CPUCoresPerSocket = atoiSafe(m["Core(s) per socket"])
		threadsPerCore := atoiSafe(m["Thread(s) per core"])
		if threadsPerCore == 0 {
			threadsPerCore = 1
		}
		cfg.CPUThreads = cfg.CPUSockets * cfg.CPUCoresPerSocket * threadsPerCore
		cfg.CPUNumaNodes = atoiSafe(m["NUMA node(s)"])
		cfg.CPUL3Cache = m["L3 cache"]
		if f := m["CPU max MHz"]; f != "" && f != "0.0000" {
			v := atofSafe(f)
			cfg.CPUMaxFreq = strconv.FormatFloat(v/1000, 'f', 2, 64) + " GHz"
		}
		cfg.CPUVirt = m["Virtualization"]
		cfg.Arch = firstNonEmpty(m["Architecture"], cfg.Arch)
	}
	if cfg.CPUModel == "" {
		if infos, err := cpu.Info(); err == nil && len(infos) > 0 {
			cfg.CPUModel = infos[0].ModelName
		}
	}
	if cfg.CPUThreads == 0 {
		if n, err := cpu.Counts(true); err == nil {
			cfg.CPUThreads = n
		}
	}

	// --- Memory total ---
	if vm, err := mem.VirtualMemory(); err == nil {
		cfg.MemTotalBytes = vm.Total
	}

	// --- Board / BIOS / system / DIMMs via dmidecode ---
	parseDmidecode(runCmd(ctx, 10*time.Second, "dmidecode", "-t", "0", "-t", "1", "-t", "2", "-t", "17"), cfg)

	// --- Disks via lsblk ---
	parseLsblk(runCmd(ctx, 5*time.Second, "lsblk", "-d", "-b", "-J", "-o", "NAME,MODEL,SERIAL,SIZE,ROTA,TRAN,TYPE"), cfg)

	// --- NICs via lspci ---
	for _, line := range strings.Split(runCmd(ctx, 5*time.Second, "lspci"), "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "Ethernet controller:") && !strings.Contains(line, "Network controller:") {
			continue
		}
		idx := strings.Index(line, ": ")
		if idx < 0 {
			continue
		}
		modelName := strings.TrimSpace(line[idx+2:])
		if j := strings.LastIndex(modelName, " (rev "); j > 0 {
			modelName = modelName[:j]
		}
		cfg.NICs = append(cfg.NICs, model.NICInfo{Address: strings.Fields(line)[0], Model: modelName})
	}

	// --- GPUs (name / VRAM / driver) ---
	out := runCmd(ctx, 10*time.Second, "nvidia-smi",
		"--query-gpu=index,name,memory.total,driver_version", "--format=csv,noheader,nounits")
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, ",")
		if len(f) < 4 {
			continue
		}
		cfg.GPUs = append(cfg.GPUs, model.GPUInfo{
			Index:     atoiSafe(strings.TrimSpace(f[0])),
			Name:      strings.TrimSpace(f[1]),
			MemoryMiB: uint64(atofSafe(strings.TrimSpace(f[2]))),
			Driver:    strings.TrimSpace(f[3]),
		})
	}

	// --- BMC firmware via ipmitool ---
	if out := runCmd(ctx, 8*time.Second, "ipmitool", "mc", "info"); out != "" {
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "BMC Firmware Revision") {
				if i := strings.Index(line, ":"); i > 0 {
					cfg.BMCVersion = strings.TrimSpace(line[i+1:])
				}
				break
			}
		}
	}

	// --- Windows: WMI fills everything the Linux commands cannot ---
	if runtime.GOOS == "windows" {
		gatherWindowsWMI(ctx, cfg)
	}

	return cfg
}

func parseLscpu(out string) map[string]string {
	if out == "" {
		return nil
	}
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		i := strings.Index(line, ":")
		if i <= 0 {
			continue
		}
		m[strings.TrimSpace(line[:i])] = strings.TrimSpace(line[i+1:])
	}
	return m
}

var dimmTailRe = regexp.MustCompile(`(?i)([-_ ]*dimm[-_ ]*[0-9a-z]+)$`)

func parseDmidecode(out string, cfg *model.MachineConfig) {
	if out == "" {
		return
	}
	type dmiSection struct {
		title  string
		fields map[string]string
	}
	var sections []dmiSection
	cur := -1
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "Handle "):
			sections = append(sections, dmiSection{fields: map[string]string{}})
			cur = len(sections) - 1
			continue
		case cur < 0:
			continue
		case line == "" || strings.HasPrefix(line, "\t") == false && strings.HasPrefix(line, " ") == false:
			// section title line (unindented)
			t := strings.TrimSpace(line)
			if t != "" && !strings.HasPrefix(t, "#") && sections[cur].title == "" {
				sections[cur].title = t
			}
			continue
		}
		i := strings.Index(line, ":")
		if i <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:i])
		v := strings.TrimSpace(line[i+1:])
		sections[cur].fields[k] = v
	}

	channels := map[string]bool{}
	for _, s := range sections {
		switch s.title {
		case "BIOS Information":
			cfg.BIOSVendor = s.fields["Vendor"]
			cfg.BIOSVersion = s.fields["Version"]
			cfg.BIOSDate = s.fields["Release Date"]
		case "System Information":
			cfg.SystemVendor = s.fields["Manufacturer"]
			cfg.SystemSerial = s.fields["Serial Number"]
		case "Base Board Information":
			cfg.BoardVendor = s.fields["Manufacturer"]
			cfg.BoardName = s.fields["Product Name"]
			cfg.BoardSerial = s.fields["Serial Number"]
		case "Memory Device":
			size := s.fields["Size"]
			populated := size != "" && !strings.Contains(size, "No Module")
			speed := s.fields["Configured Memory Speed"]
			if speed == "" || strings.Contains(speed, "Unknown") {
				speed = s.fields["Speed"]
			}
			locator := s.fields["Locator"]
			cfg.MemModules = append(cfg.MemModules, model.MemModule{
				Locator:      locator,
				Type:         s.fields["Type"],
				Size:         size,
				Speed:        speed,
				FormFactor:   s.fields["Form Factor"],
				Manufacturer: s.fields["Manufacturer"],
				PartNumber:   s.fields["Part Number"],
				Populated:    populated,
			})
			cfg.MemSlotsTotal++
			if populated {
				cfg.MemSlotsPopulated++
			}
			ch := dimmTailRe.ReplaceAllString(locator, "")
			if ch = strings.TrimSpace(ch); ch != "" {
				channels[ch] = true
			}
		}
	}
	if len(channels) > 0 {
		cfg.MemChannels = len(channels)
	}
}

func parseLsblk(out string, cfg *model.MachineConfig) {
	if out == "" {
		return
	}
	var data struct {
		Blockdevices []struct {
			Name   string `json:"name"`
			Model  string `json:"model"`
			Serial string `json:"serial"`
			Size   uint64 `json:"size"`
			Rota   bool   `json:"rota"`
			Tran   string `json:"tran"`
			Type   string `json:"type"`
		} `json:"blockdevices"`
	}
	if json.Unmarshal([]byte(out), &data) != nil {
		return
	}
	for _, d := range data.Blockdevices {
		if d.Type != "disk" {
			continue
		}
		cfg.Disks = append(cfg.Disks, model.DiskInfo{
			Name:       d.Name,
			Model:      d.Model,
			Serial:     d.Serial,
			SizeBytes:  d.Size,
			Rotational: d.Rota,
			Transport:  d.Tran,
		})
	}
}

// wmiScript gathers the full hardware inventory through one PowerShell run.
const wmiScript = psUTF8 + `
$ErrorActionPreference='SilentlyContinue'
$bb = Get-CimInstance Win32_BaseBoard | Select-Object -First 1
$sp = Get-CimInstance Win32_ComputerSystemProduct | Select-Object -First 1
$cs = Get-CimInstance Win32_ComputerSystem | Select-Object -First 1
$bios = Get-CimInstance Win32_BIOS | Select-Object -First 1
$cpus = @(Get-CimInstance Win32_Processor)
$mem = @(Get-CimInstance Win32_PhysicalMemory)
$ma = Get-CimInstance Win32_PhysicalMemoryArray | Select-Object -First 1
$pdisks = @(Get-PhysicalDisk)
if(-not $pdisks -or $pdisks.Count -eq 0){ $pdisks = @(Get-CimInstance Win32_DiskDrive | ForEach-Object { [PSCustomObject]@{ DeviceId=[string]$_.Index; FriendlyName=$_.Model; SerialNumber=$_.SerialNumber; Size=$_.Size; MediaType='HDD'; BusType=$_.InterfaceType } }) }
$nics = @(Get-CimInstance Win32_NetworkAdapter | Where-Object {$_.PhysicalAdapter})
$vgpus = @(Get-CimInstance Win32_VideoController)
$out = [PSCustomObject]@{
  board = @{ vendor=$bb.Manufacturer; name=$bb.Product; serial=$bb.SerialNumber }
  system = @{ vendor=$sp.Vendor; model=$sp.Name; serial=$sp.IdentifyingNumber; csVendor=$cs.Manufacturer }
  bios = @{ vendor=$bios.Manufacturer; version=$bios.SMBIOSBIOSVersion; date=$(if($bios.ReleaseDate){$bios.ReleaseDate.ToString('yyyy-MM-dd')}) }
  cpu = @{ model=$cpus[0].Name; sockets=$cpus.Count; cores=($cpus | Measure-Object NumberOfCores -Sum).Sum; threads=($cpus | Measure-Object NumberOfLogicalProcessors -Sum).Sum; mhz=$cpus[0].MaxClockSpeed; l3kb=$cpus[0].L3CacheSize; virt=$cpus[0].VirtualizationFirmwareEnabled }
  mem = @( $mem | ForEach-Object { [PSCustomObject]@{ loc=$_.DeviceLocator; bank=$_.BankLabel; type=$_.SMBIOSMemoryType; cap=$_.Capacity; speed=$_.ConfiguredClockSpeed; speedMax=$_.Speed; mfr=$_.Manufacturer; part=$_.PartNumber; form=$_.FormFactor } } )
  slots = $ma.MemoryDevices
  disks = @( $pdisks | ForEach-Object { [PSCustomObject]@{ num=$_.DeviceId; model=$_.FriendlyName; serial=$_.SerialNumber; size=$_.Size; media=$_.MediaType; bus=$_.BusType } } )
  nics = @( $nics | ForEach-Object { [PSCustomObject]@{ addr=$_.PNPDeviceID; name=$_.Name } } )
  gpus = @( $vgpus | ForEach-Object { [PSCustomObject]@{ name=$_.Name; ram=$_.AdapterRAM; driver=$_.DriverVersion } } )
}
ConvertTo-Json -Compress -InputObject $out -Depth 4`

type wmiData struct {
	Board struct {
		Vendor, Name, Serial string
	} `json:"board"`
	System struct {
		Vendor, Model, Serial, CSVendor string
	} `json:"system"`
	BIOS struct {
		Vendor, Version, Date string
	} `json:"bios"`
	CPU struct {
		Model   string  `json:"model"`
		Sockets float64 `json:"sockets"`
		Cores   float64 `json:"cores"`
		Threads float64 `json:"threads"`
		Mhz     float64 `json:"mhz"`
		L3kb    float64 `json:"l3kb"`
		Virt    bool    `json:"virt"`
	} `json:"cpu"`
	Mem []struct {
		Loc     string `json:"loc"`
		Bank    string `json:"bank"`
		Type    int    `json:"type"`
		Cap     uint64 `json:"cap"`
		Speed   int    `json:"speed"`
		SpeedMax int   `json:"speedMax"`
		Mfr     string `json:"mfr"`
		Part    string `json:"part"`
		Form    int    `json:"form"`
	} `json:"mem"`
	Slots int `json:"slots"`
	Disks []struct {
		Num, Model, Serial string
		Size               uint64
		Media, Bus         string
	} `json:"disks"`
	NICs []struct {
		Addr, Name string
	} `json:"nics"`
	GPUs []struct {
		Name   string `json:"name"`
		Ram    uint64 `json:"ram"`
		Driver string `json:"driver"`
	} `json:"gpus"`
}

var winMemTypes = map[int]string{
	20: "DDR", 21: "DDR2", 24: "DDR3", 26: "DDR4", 34: "DDR5",
}

var winFormFactors = map[int]string{
	8: "DIMM", 13: "SODIMM", 23: "FB-DIMM", 30: "LRDIMM",
}

// gatherWindowsWMI fills the config from a single CIM query round.
func gatherWindowsWMI(ctx context.Context, cfg *model.MachineConfig) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "powershell", "-NoProfile", "-Command", wmiScript).Output()
	if err != nil || len(out) == 0 {
		return
	}
	var d wmiData
	if json.Unmarshal(trimBOM(out), &d) != nil {
		return
	}

	if d.System.Vendor != "" {
		cfg.SystemVendor = d.System.Vendor
	}
	if d.System.Serial != "" {
		cfg.SystemSerial = d.System.Serial
	}
	cfg.BoardVendor = firstNonEmpty(d.Board.Vendor, cfg.BoardVendor)
	cfg.BoardName = firstNonEmpty(d.Board.Name, cfg.BoardName)
	cfg.BoardSerial = firstNonEmpty(d.Board.Serial, cfg.BoardSerial)
	if d.BIOS.Version != "" {
		cfg.BIOSVendor, cfg.BIOSVersion, cfg.BIOSDate = d.BIOS.Vendor, d.BIOS.Version, d.BIOS.Date
	}
	if d.CPU.Model != "" {
		cfg.CPUModel = strings.TrimSpace(d.CPU.Model)
		sockets := int(d.CPU.Sockets)
		if sockets == 0 {
			sockets = 1
		}
		cfg.CPUSockets = sockets
		cfg.CPUCoresPerSocket = int(d.CPU.Cores) / sockets
		cfg.CPUThreads = int(d.CPU.Threads)
		if d.CPU.Mhz > 0 {
			cfg.CPUMaxFreq = strconv.FormatFloat(d.CPU.Mhz/1000, 'f', 2, 64) + " GHz"
		}
		if d.CPU.L3kb > 0 {
			cfg.CPUL3Cache = strconv.Itoa(int(d.CPU.L3kb)/1024) + " MiB"
		}
		if d.CPU.Virt {
			cfg.CPUVirt = "已启用"
		}
	}

	// Memory modules.
	cfg.MemSlotsTotal = d.Slots
	channels := map[string]bool{}
	for _, m := range d.Mem {
		loc := strings.TrimSpace(m.Loc)
		size := ""
		populated := m.Cap > 0
		if populated {
			size = strconv.FormatUint(m.Cap/(1024*1024*1024), 10) + " GB"
		}
		speed := ""
		if m.Speed > 0 {
			speed = strconv.Itoa(m.Speed) + " MT/s"
		} else if m.SpeedMax > 0 {
			speed = strconv.Itoa(m.SpeedMax) + " MT/s"
		}
		cfg.MemModules = append(cfg.MemModules, model.MemModule{
			Locator:      loc,
			Type:         winMemTypes[m.Type],
			Size:         size,
			Speed:        speed,
			FormFactor:   winFormFactors[m.Form],
			Manufacturer: strings.TrimSpace(m.Mfr),
			PartNumber:   strings.TrimSpace(m.Part),
			Populated:    populated,
		})
		if populated {
			cfg.MemSlotsPopulated++
		}
		if ch := dimmTailRe.ReplaceAllString(loc, ""); strings.TrimSpace(ch) != "" {
			channels[strings.TrimSpace(ch)] = true
		}
	}
	if len(channels) > 0 {
		cfg.MemChannels = len(channels)
	}

	// Disks (skip USB gadgets and unspecified media pseudo-devices).
	for _, dd := range d.Disks {
		if dd.Size < 1024*1024*1024 {
			continue
		}
		if strings.EqualFold(dd.Media, "Unspecified") || strings.EqualFold(dd.Bus, "USB") {
			continue
		}
		cfg.Disks = append(cfg.Disks, model.DiskInfo{
			Name:       "PhysicalDrive" + dd.Num,
			Model:      strings.TrimSpace(dd.Model),
			Serial:     strings.TrimSpace(dd.Serial),
			SizeBytes:  dd.Size,
			Rotational: strings.EqualFold(dd.Media, "HDD"),
			Transport:  strings.ToLower(dd.Bus),
		})
	}

	// NICs (physical PCI/USB adapters only; skip virtual tunnels & bluetooth).
	for _, n := range d.NICs {
		if n.Name == "" {
			continue
		}
		if !strings.HasPrefix(n.Addr, `PCI\`) && !strings.HasPrefix(n.Addr, `USB\`) {
			continue
		}
		cfg.NICs = append(cfg.NICs, model.NICInfo{Address: n.Addr, Model: n.Name})
	}

	// GPU fallback (when nvidia-smi is absent, e.g. integrated graphics).
	if len(cfg.GPUs) == 0 {
		i := 0
		for _, g := range d.GPUs {
			if g.Name == "" || strings.Contains(strings.ToLower(g.Name), "virtual") {
				continue
			}
			cfg.GPUs = append(cfg.GPUs, model.GPUInfo{
				Index:     i,
				Name:      g.Name,
				MemoryMiB: g.Ram / (1024 * 1024),
				Driver:    g.Driver,
			})
			i++
		}
	}

	// Virtualization vendor detection.
	v := strings.ToLower(d.System.CSVendor)
	for _, kw := range []string{"microsoft", "vmware", "innotek", "qemu", "xen", "parallels"} {
		if strings.Contains(v, kw) {
			cfg.Virtual = d.System.CSVendor
			break
		}
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func atoiSafe(s string) int {	v, _ := strconv.Atoi(strings.TrimSpace(s))
	return v
}

func atofSafe(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
