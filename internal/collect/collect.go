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
	"github.com/jiegui2025/hwspec/schema"
)

type collector struct {
	r          *report.Report
	privileged bool
	// moduleSigning is fixed for a capture: the E taint flag is meaningful
	// only when the kernel exposes its module-signing parameter.
	moduleSigning bool
	cpuinfo       map[string]string // lazily read by cpuinfoField
	smbios        *smbiosTable      // lazily read by smbiosStructures
	aliases       *[]moduleAlias    // lazily read by moduleAliases: nil until tried
}

func moduleSigningSupported() bool {
	return exists("/sys/module/module/parameters/sig_enforce")
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
		Schema:        schema.URL,
		SchemaVersion: report.SchemaVersion,
		Tool:          report.Tool{Name: "hwspec", Version: version},
		CapturedAt:    time.Now().UTC().Truncate(time.Second),
		Privileged:    geteuid() == 0,
	}
	r.Hostname, _ = hostname()
	c := &collector{
		r:             r,
		privileged:    r.Privileged,
		moduleSigning: moduleSigningSupported(),
	}

	idsLoaded := resolve.Prefetch() // loads while the collectors wait on the kernel
	// Sensor chips can be slow (an NVMe drive answers each reading in about
	// 7.5 ms, one at a time), and only this collector touches r.Sensors and
	// it returns its warnings, so it runs while the others do.
	sensorsDone := make(chan struct{})
	var sensorWarnings []string
	go func() {
		sensorWarnings = c.sensors()
		close(sensorsDone)
	}()
	c.dmi() // before osInfo, which uses DMI to recognise VMs
	c.osInfo()
	c.cpu()
	c.memory()
	c.firmwareTables()
	c.platformFirmware()
	c.r.RTC = c.rtc()
	c.storage()
	c.pci()
	c.gpus()          // after pci
	c.kernelModules() // after osInfo and pci
	if c.r.Kernel == nil {
		c.r.Kernel = &report.Kernel{}
	}
	c.r.Kernel.ModuleBlacklist = c.moduleBlacklist()
	c.r.Kernel.FirmwareFailures = c.firmwareFailures()
	c.mountings()         // after pci, memory, storage and the firmware tables
	c.processorGraphics() // after mountings and cpu
	c.displays()
	c.network()
	c.bluetooth()
	c.audio()
	c.batteries()
	c.usb()
	c.usbCandidates() // after usb and kernelModules

	<-sensorsDone
	c.r.Warnings = append(c.r.Warnings, sensorWarnings...)
	idsLoaded()
	resolve.Names(r)
	r.Sanitize() // strings from hardware and firmware are untrusted

	// Keep empty lists as [] rather than null in JSON.
	if r.Warnings == nil {
		r.Warnings = []string{}
	}
	sort.Strings(r.Warnings)
	return r
}
