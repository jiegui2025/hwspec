// Package collect gathers hardware information from sysfs, procfs and the
// SMBIOS table. Every collector degrades gracefully: anything it can't read
// is left empty and, when the reason is useful, noted in Report.Warnings.
package collect

import (
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/jiegui2025/hwspec/internal/report"
	"github.com/jiegui2025/hwspec/internal/resolve"
)

type collector struct {
	r          *report.Report
	privileged bool
	cpuinfo    map[string]string // lazily read by cpuinfoField
}

func (c *collector) warn(format string, args ...any) {
	c.r.Warnings = append(c.r.Warnings, fmt.Sprintf(format, args...))
}

// warnRead records a failed read, pointing at --full for permission errors.
func (c *collector) warnRead(what string, err error) {
	if os.IsPermission(err) && !c.privileged {
		c.warn("%s: needs root (run with --full)", what)
		return
	}
	c.warn("%s: %v", what, err)
}

// Collect runs every collector and returns the finished report.
func Collect(version string) *report.Report {
	captureMu.Lock()
	defer captureMu.Unlock()
	return collectNow(version)
}

// collectNow is Collect for callers already holding captureMu.
func collectNow(version string) *report.Report {
	r := &report.Report{
		SchemaVersion: report.SchemaVersion,
		Tool:          report.Tool{Name: "hwspec", Version: version},
		CapturedAt:    time.Now().UTC().Truncate(time.Second),
		Privileged:    geteuid() == 0,
	}
	r.Hostname, _ = hostname()
	c := &collector{r: r, privileged: r.Privileged}

	c.dmi() // before osInfo, which uses DMI to recognise VMs
	c.osInfo()
	c.cpu()
	c.memory()
	c.storage()
	c.pci()
	c.gpus() // after pci
	c.displays()
	c.network()
	c.bluetooth()
	c.audio()
	c.batteries()
	c.sensors()
	c.usb()

	resolve.Names(r)
	r.Sanitize() // strings from hardware and firmware are untrusted

	// Keep empty lists as [] rather than null in JSON.
	if r.Warnings == nil {
		r.Warnings = []string{}
	}
	sort.Strings(r.Warnings)
	return r
}
