package collect

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/host"
)

// ReadBootID returns a stable per-boot identifier. On Linux it is the kernel
// boot id; on Windows (and elsewhere) it is derived from the boot time.
func ReadBootID() string {
	if b, err := os.ReadFile("/proc/sys/kernel/random/boot_id"); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	if hi, err := host.Info(); err == nil && hi.Uptime > 0 {
		return fmt.Sprintf("bt-%d", time.Now().Unix()-int64(hi.Uptime))
	}
	return ""
}

// DetectUncleanShutdown reports whether the previous boot ended without a
// clean shutdown.
//   - Linux: wtmp via `last -x` — a clean power-off leaves a "shutdown"
//     entry between two "reboot" entries; a crash or power loss does not.
//   - Windows: the System event log — an unexpected shutdown leaves
//     Kernel-Power event 41 and/or EventLog event 6008 around boot time.
func DetectUncleanShutdown(ctx context.Context) bool {
	if runtime.GOOS == "windows" {
		return detectUncleanWindows(ctx)
	}
	return detectUncleanLinux(ctx)
}

func detectUncleanLinux(ctx context.Context) bool {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "last", "-x", "-n", "40").CombinedOutput()
	if err != nil || len(out) == 0 {
		return false // unknown -> treat as clean
	}
	lines := strings.Split(string(bytes.TrimSpace(out)), "\n")

	// wtmp line example:
	//   reboot   system boot  6.8.0-40-generic Tue Oct 14 09:41 - 10:23 (00:42)
	//   shutdown system down  6.8.0-40-generic Mon Oct 13 22:10 - 22:10
	kind := func(line string) string {
		f := strings.Fields(line)
		if len(f) == 0 {
			return ""
		}
		switch f[0] {
		case "reboot", "shutdown", "crash", "runlevel":
			return f[0]
		}
		return ""
	}

	i := 0
	for ; i < len(lines); i++ {
		switch kind(lines[i]) {
		case "crash":
			return true // this boot followed an unclean shutdown
		case "reboot":
			for j := i + 1; j < len(lines); j++ {
				switch kind(lines[j]) {
				case "shutdown":
					return false // clean shutdown recorded
				case "reboot", "crash":
					return true // previous boot ended without a shutdown
				}
			}
			return false // not enough history to tell
		}
	}
	return false
}

// detectUncleanWindows counts Kernel-Power 41 / EventLog 6008 records since
// just before the current boot started; any hit means the previous shutdown
// was unexpected (power loss, hang, hard reset).
func detectUncleanWindows(ctx context.Context) bool {
	uptime := int64(0)
	if hi, err := host.Info(); err == nil {
		uptime = int64(hi.Uptime)
	}
	window := uptime + 300 // seconds back from now
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	script := strings.Replace(
		psUTF8+`$ErrorActionPreference='SilentlyContinue';`+
			`$s=(Get-Date).AddSeconds(-%d);`+
			`(Get-WinEvent -FilterHashtable @{LogName='System'; Id=@(41,6008); StartTime=$s} | Measure-Object).Count`,
		"%d", strconv.FormatInt(window, 10), 1)
	out, err := exec.CommandContext(cctx, "powershell", "-NoProfile", "-Command", script).Output()
	if err != nil {
		return false
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n > 0
}
