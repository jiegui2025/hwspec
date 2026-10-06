package collect

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/jiegui2025/hwspec/internal/report"
)

// driverAt describes the kernel driver bound to the device directory dev
// (e.g. /sys/bus/pci/devices/0000:00:1f.6), or nil if none is bound.
func (c *collector) driverAt(dev string) *report.Driver {
	name := linkBase(dev + "/driver")
	if name == "" {
		return nil
	}
	d := &report.Driver{Name: name}
	// Built-in PCI and USB drivers link to a module directory too; only a
	// loaded module has an initstate.
	module := linkBase(dev + "/driver/module")
	mod := "/sys/module/" + module + "/"
	if module == "" || !exists(mod+"initstate") {
		d.Builtin = true
		if module != "" {
			d.Version = readStr(mod + "version")
		}
		return d
	}
	d.Module = module
	d.Version = readStr(mod + "version")
	d.SrcVersion = readStr(mod + "srcversion")
	// Taint flags: O out-of-tree, P proprietary, E unsigned (on kernels
	// that check signatures). An empty file means a clean in-tree module.
	if taint, err := readStrErr(mod + "taint"); err == nil {
		inTree := !strings.Contains(taint, "O")
		proprietary := strings.Contains(taint, "P")
		d.InTree, d.Proprietary = &inTree, &proprietary
		if c.moduleSigning {
			unsigned := strings.Contains(taint, "E")
			d.Unsigned = &unsigned
		}
	}
	return d
}

// controllerDriver finds the driver of the controller a class device (a
// block device, a network interface, ...) hangs off: the nearest device
// above it in sysfs with a driver on a hardware bus. For a SATA disk that
// is ahci (not the generic sd), for an NVMe namespace nvme, for a USB disk
// usb-storage or uas.
func (c *collector) controllerDriver(classDevice string) *report.Driver {
	for dir := unroot(realPath(classDevice)); strings.HasPrefix(dir, "/sys/devices/"); dir = filepath.Dir(dir) {
		if linkBase(dir+"/driver") == "" {
			continue
		}
		switch filepath.Base(realPath(dir + "/subsystem")) {
		case "pci", "usb", "platform", "virtio", "mmc", "sdio":
			return c.driverAt(dir)
		}
	}
	return nil
}

// unroot turns a resolved path back into one relative to root, so it can
// be passed to the read helpers again (they prefix root).
func unroot(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Clean("/" + strings.TrimPrefix(path, filepath.Clean(root)))
}

// usbRelease formats a USB bcdDevice value ("0543") as a version ("5.43").
func usbRelease(bcd string) string {
	bcd = strings.TrimSpace(bcd)
	if len(bcd) != 4 {
		return bcd
	}
	major := strings.TrimLeft(bcd[:2], "0")
	if major == "" {
		major = "0"
	}
	return major + "." + bcd[2:]
}

// ethtoolDrvinfo asks a network driver for its firmware version (no root
// needed). Tests replace it.
var ethtoolDrvinfo = func(ifname string) (firmware string, err error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	defer unix.Close(fd)
	info, err := unix.IoctlGetEthtoolDrvinfo(fd, ifname)
	if err != nil {
		return "", err
	}
	return cString(info.Fw_version[:]), nil
}

func cString(b []byte) string {
	if i := strings.IndexByte(string(b), 0); i >= 0 {
		b = b[:i]
	}
	return strings.TrimSpace(string(b))
}

// firmwareVersion builds a Firmware block, or nil when the version is
// empty or a placeholder ("N/A", "0x0").
func firmwareVersion(version, source string) *report.Firmware {
	v := strings.TrimSpace(version)
	switch strings.ToLower(v) {
	case "", "n/a", "na", "none", "0x0", "0":
		return nil
	}
	return &report.Firmware{Version: v, Source: source}
}

// firmwareOrUnknown returns fw, or, when no version was read, an unknown block
// with the reason: a part that has firmware never goes without the block
// (ADR 0008).
func firmwareOrUnknown(fw *report.Firmware, reason string) *report.Firmware {
	if fw == nil {
		return report.UnknownFirmware(reason)
	}
	return fw
}

// metric adds a measurement to h when ok.
func metric(h *report.Health, name string, value float64, ok bool) {
	if !ok {
		return
	}
	if h.Metrics == nil {
		h.Metrics = map[string]float64{}
	}
	h.Metrics[name] = value
}

// isoWeek formats a manufacture year and week ("2020-W10"), falling back
// to the year alone when the week is unknown.
func isoWeek(year, week int) string {
	if year <= 0 {
		return ""
	}
	if week >= 1 && week <= 53 {
		return fmt.Sprintf("%04d-W%02d", year, week)
	}
	return strconv.Itoa(year)
}
