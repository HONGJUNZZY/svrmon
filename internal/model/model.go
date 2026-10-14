// Package model defines the data contract shared between agents, the hub
// and browser clients. All messages are JSON over WebSocket.
package model

// Message types exchanged over WebSocket connections.
const (
	TypeHello      = "hello"       // agent -> hub (once, after connect)
	TypeSnapshot   = "snapshot"    // agent -> hub, periodic
	TypeInit       = "init"        // hub -> browser, full state on connect
	TypeOffline    = "offline"     // hub -> browser, agent disconnected
	TypeCmd        = "cmd"         // hub -> agent, run a command (e.g. ipmi power)
	TypeCmdResult  = "cmd_result"  // agent -> hub, command result
	TypeServerJoin = "join"        // hub -> browser, a (new) agent connected
	TypeGoodbye    = "goodbye"     // agent -> hub, sent on graceful stop (SIGTERM)
	TypeRename     = "rename"      // hub -> browser, a server was renamed
	TypeConfig     = "config"      // agent -> hub -> browser, machine hardware config
)

// ServerInfo is the static description of a monitored server,
// reported by the agent in its hello message.
type ServerInfo struct {
	Hostname   string   `json:"hostname"`
	OS         string   `json:"os"`
	Platform   string   `json:"platform"`
	Kernel     string   `json:"kernel"`
	KernelArch string   `json:"kernelArch"`
	CPUModel   string   `json:"cpuModel"`
	CPUCores   int      `json:"cpuCores"`
	MemTotal   uint64   `json:"memTotal"` // bytes
	GPUCount   int      `json:"gpuCount"`
	GPUNames   []string `json:"gpuNames,omitempty"`
	IPMIAvail  bool     `json:"ipmiAvail"` // local BMC reachable via ipmitool
	AgentVer   string   `json:"agentVer"`
	RemoteAddr string   `json:"remoteAddr,omitempty"` // filled in by the hub

	// Boot evidence used by the hub's abnormal-shutdown detection.
	BootID              string `json:"bootId,omitempty"`              // /proc/sys/kernel/random/boot_id
	BootAt              int64  `json:"bootAt,omitempty"`              // unix seconds of this boot
	LastShutdownUnclean bool   `json:"lastShutdownUnclean,omitempty"` // previous boot ended without a clean shutdown record
}

// AgentGoodbyeMsg is the agent's last frame before a graceful exit.
type AgentGoodbyeMsg struct {
	Type   string `json:"type"`
	Reason string `json:"reason"` // "stopping" (SIGTERM / systemd stop)
}

// AgentConfigMsg carries the machine hardware inventory.
type AgentConfigMsg struct {
	Type string        `json:"type"`
	Data MachineConfig `json:"data"`
}

// Snapshot is one periodic sample of all live metrics of a server.
type Snapshot struct {
	TS      int64     `json:"ts"` // unix seconds
	CPU     *CPU      `json:"cpu,omitempty"`
	Mem     *Mem      `json:"mem,omitempty"`
	Load    *Load     `json:"load,omitempty"`
	Uptime  uint64    `json:"uptime"`
	Disks   []DiskIO  `json:"disks,omitempty"`  // per-device io rates
	Mounts  []Mount   `json:"mounts,omitempty"` // filesystem capacity (refreshed lazily)
	Nets    []NetIO   `json:"nets,omitempty"`
	GPUs    []GPU     `json:"gpus,omitempty"`
	IPMI    *IPMI     `json:"ipmi,omitempty"`
	Logs    []LogEntry `json:"logs,omitempty"` // new severe log entries since last snapshot
}

// LogEntry is one severe journal entry (kernel/hardware/system error).
type LogEntry struct {
	TS      int64  `json:"ts"`   // unix seconds
	Prio    int    `json:"prio"` // 0 emerg, 1 alert, 2 crit, 3 err
	Source  string `json:"source"`
	Unit    string `json:"unit,omitempty"`
	Message string `json:"msg"`
}

// MachineConfig is the (slowly changing) hardware inventory of a server.
type MachineConfig struct {
	CollectedAt int64  `json:"collectedAt"`
	OS          string `json:"os,omitempty"`
	Kernel      string `json:"kernel,omitempty"`
	Arch        string `json:"arch,omitempty"`
	Virtual     string `json:"virtual,omitempty"` // KVM / VMware / none

	SystemVendor string `json:"systemVendor,omitempty"`
	SystemSerial string `json:"systemSerial,omitempty"`
	BoardVendor  string `json:"boardVendor,omitempty"`
	BoardName    string `json:"boardName,omitempty"`
	BoardSerial  string `json:"boardSerial,omitempty"`
	BIOSVendor   string `json:"biosVendor,omitempty"`
	BIOSVersion  string `json:"biosVersion,omitempty"`
	BIOSDate     string `json:"biosDate,omitempty"`
	BMCVersion   string `json:"bmcVersion,omitempty"`

	CPUModel          string `json:"cpuModel,omitempty"`
	CPUSockets        int    `json:"cpuSockets,omitempty"`
	CPUCoresPerSocket int    `json:"cpuCoresPerSocket,omitempty"`
	CPUThreads        int    `json:"cpuThreads,omitempty"`
	CPUNumaNodes      int    `json:"cpuNumaNodes,omitempty"`
	CPUL3Cache        string `json:"cpuL3Cache,omitempty"`
	CPUMaxFreq        string `json:"cpuMaxFreq,omitempty"`
	CPUVirt           string `json:"cpuVirt,omitempty"`

	MemTotalBytes    uint64      `json:"memTotalBytes,omitempty"`
	MemChannels      int         `json:"memChannels,omitempty"`
	MemSlotsTotal    int         `json:"memSlotsTotal,omitempty"`
	MemSlotsPopulated int        `json:"memSlotsPopulated,omitempty"`
	MemModules       []MemModule `json:"memModules,omitempty"`

	GPUs []GPUInfo `json:"gpus,omitempty"`

	Disks []DiskInfo `json:"disks,omitempty"`

	NICs []NICInfo `json:"nics,omitempty"`
}

type MemModule struct {
	Locator     string `json:"locator"`
	Type        string `json:"type,omitempty"`
	Size        string `json:"size,omitempty"`
	Speed       string `json:"speed,omitempty"`
	FormFactor  string `json:"formFactor,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
	PartNumber  string `json:"partNumber,omitempty"`
	Populated   bool   `json:"populated"`
}

type GPUInfo struct {
	Index     int    `json:"index"`
	Name      string `json:"name"`
	MemoryMiB uint64 `json:"memoryMiB,omitempty"`
	Driver    string `json:"driver,omitempty"`
}

type DiskInfo struct {
	Name       string `json:"name"`
	Model      string `json:"model,omitempty"`
	Serial     string `json:"serial,omitempty"`
	SizeBytes  uint64 `json:"sizeBytes,omitempty"`
	Rotational bool   `json:"rotational"`
	Transport  string `json:"transport,omitempty"`
}

type NICInfo struct {
	Address string `json:"address"`
	Model   string `json:"model"`
}

type CPU struct {
	Usage   float64   `json:"usage"`            // percent, all cores
	PerCore []float64 `json:"perCore,omitempty"` // percent, per core
	Iowait  float64   `json:"iowait"`           // percent
	Temp    float64   `json:"temp,omitempty"`   // max package/core temp, degC
	Freq    float64   `json:"freq,omitempty"`   // nominal MHz
}

type Mem struct {
	Total     uint64 `json:"total"`
	Used      uint64 `json:"used"`
	Available uint64 `json:"available"`
	Cached    uint64 `json:"cached"`
	Buffers   uint64 `json:"buffers"`
	SwapTotal uint64 `json:"swapTotal"`
	SwapUsed  uint64 `json:"swapUsed"`
}

type Load struct {
	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`
}

// DiskIO holds rates for one block device (bytes/s, iops, busy percent).
type DiskIO struct {
	Name      string  `json:"name"`
	ReadBps   float64 `json:"readBps"`
	WriteBps  float64 `json:"writeBps"`
	ReadIOPS  float64 `json:"readIops"`
	WriteIOPS float64 `json:"writeIops"`
	Util      float64 `json:"util"` // percent busy
}

// Mount is filesystem usage for one mount point.
type Mount struct {
	Device     string `json:"device"`
	MountPoint string `json:"mountPoint"`
	FSType     string `json:"fsType"`
	Total      uint64 `json:"total"`
	Used       uint64 `json:"used"`
	Free       uint64 `json:"free"`
}

type NetIO struct {
	Name    string  `json:"name"`
	RxBps   float64 `json:"rxBps"`
	TxBps   float64 `json:"txBps"`
	RxErrs  uint64  `json:"rxErrs"`
	TxDrops uint64  `json:"txDrops"`
}

type GPU struct {
	Index        int      `json:"index"`
	UUID         string   `json:"uuid"`
	Name         string   `json:"name"`
	Util         float64  `json:"util"`         // percent
	MemUtil      float64  `json:"memUtil"`      // percent
	MemUsed      uint64   `json:"memUsed"`      // MiB
	MemTotal     uint64   `json:"memTotal"`     // MiB
	Temp         float64  `json:"temp"`         // degC
	Fan          *float64 `json:"fan,omitempty"` // percent (nil if unsupported)
	PowerDraw    *float64 `json:"powerDraw,omitempty"` // W
	PowerLimit   *float64 `json:"powerLimit,omitempty"` // W
	ClockSM      float64  `json:"clockSM"`      // MHz
	ClockMem     float64  `json:"clockMem"`     // MHz
	PState       string   `json:"pState"`
	ECCCorrected uint64   `json:"eccCorrected"` // volatile count
	ECCUncorrected uint64 `json:"eccUncorrected"`
	Procs        []GPUProc `json:"procs,omitempty"`
}

type GPUProc struct {
	PID     int32  `json:"pid"`
	Name    string `json:"name"`
	MemUsed uint64 `json:"memUsed"` // MiB
}

type IPMI struct {
	TS         int64     `json:"ts"`
	Available  bool      `json:"available"`
	Error      string    `json:"error,omitempty"` // last error when degraded
	PowerWatts float64   `json:"powerWatts"`
	Sensors    []Sensor  `json:"sensors,omitempty"`
	SEL        []SELEntry `json:"sel,omitempty"`
}

type Sensor struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`          // raw value as printed by ipmitool
	ValueF   float64 `json:"valueF,omitempty"` // numeric value if parseable
	HasValue bool    `json:"hasValue"`
	Unit     string  `json:"unit"`
	Status   string  `json:"status"` // ok / cr / nr / nc / ...
}

type SELEntry struct {
	ID   int    `json:"id"`
	Date string `json:"date"`
	Time string `json:"time"`
	Event string `json:"event"`
}

// ---- agent <-> hub envelopes ----

type AgentHelloMsg struct {
	Type string     `json:"type"`
	Info ServerInfo `json:"info"`
}

type AgentSnapshotMsg struct {
	Type string   `json:"type"`
	Data Snapshot `json:"data"`
}

type AgentCmdMsg struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	Cmd    string `json:"cmd"`
	Action string `json:"action"`
}

type AgentCmdResultMsg struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Output string `json:"output"`
}

// ---- hub <-> browser envelopes ----

type ServerState struct {
	Name     string         `json:"name"`
	Info     ServerInfo     `json:"info"`
	Online   bool           `json:"online"`
	LastSeen int64          `json:"lastSeen"` // unix seconds
	Latest   *Snapshot      `json:"latest,omitempty"`
	Config   *MachineConfig `json:"config,omitempty"`
}

type BrowserInitMsg struct {
	Type    string                  `json:"type"`
	HubVer  string                  `json:"hubVer,omitempty"`
	Servers map[string]*ServerState `json:"servers"`
}

type BrowserServerMsg struct {
	Type   string   `json:"type"` // "snapshot" | "join"
	Server string   `json:"server"`
	Data   *Snapshot `json:"data,omitempty"`
	Info   *ServerInfo `json:"info,omitempty"`
}

type BrowserOfflineMsg struct {
	Type   string `json:"type"`
	Server string `json:"server"`
}

// BrowserRenameMsg tells browsers a server changed its display name.
type BrowserRenameMsg struct {
	Type string `json:"type"`
	From string `json:"from"`
	To   string `json:"to"`
}

// BrowserConfigMsg carries machine config updates to browsers.
type BrowserConfigMsg struct {
	Type   string         `json:"type"`
	Server string         `json:"server"`
	Data   *MachineConfig `json:"data,omitempty"`
}
