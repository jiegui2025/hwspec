package collect

import (
	"errors"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/jiegui2025/hwspec/internal/report"
)

// Firmware that failed to load, from the kernel log (#213). The firmware
// loader warns "Direct firmware load for %s failed with error %d" through
// dev_warn (drivers/base/firmware_loader/main.c), which prefixes "<driver>
// <device>: " (drivers/base/core.c, __dev_printk) and attaches the
// device as a DEVICE= key. /dev/kmsg gives each record whole
// (Documentation/ABI/testing/dev-kmsg): "<prio>,<seq>,<usec>,<flags>[,…];
// <text>", continuation lines " KEY=value", non-printable bytes and "\"
// escaped as "\x00" (left so here). It needs root (dmesg_restrict,
// CAP_SYSLOG), so it is read under --full only (#7, owner decision).

const (
	kmsgMaxRecords = 1 << 17 // the ring buffer holds far fewer
	kmsgRecordSize = 1 << 14 // a record is at most 8 KiB (CONSOLE_EXT_LOG_MAX)
)

// fwFailed is the loader's warning, with dev_printk's prefix. Text is
// matched as kmsg escaped it, and kept so: a real file or device name is
// printable, and unescaping would let a control character into the
// capture.
var fwFailed = regexp.MustCompile(`^(?:(\S+) (\S+): )?Direct firmware load for (\S+) failed with error (-[1-9][0-9]*)$`)

// readKmsg returns every record /dev/kmsg holds, and how many were
// overwritten while it read; a seam, as the device needs root.
var readKmsg = func() (records []string, lost int, err error) {
	fd, err := unix.Open("/dev/kmsg", unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, 0, err
	}
	defer unix.Close(fd)
	buf := make([]byte, kmsgRecordSize)
	for len(records) < kmsgMaxRecords {
		n, err := unix.Read(fd, buf)
		switch {
		case errors.Is(err, unix.EAGAIN):
			return records, lost, nil // the end
		case errors.Is(err, unix.EPIPE):
			lost++ // overwritten while reading: the next read goes on
		case errors.Is(err, unix.EINTR):
		case err != nil:
			return records, lost, err
		default:
			records = append(records, string(buf[:n]))
		}
	}
	return records, lost, nil
}

// firmwareFailures records the firmware the kernel log says failed to
// load, under --full; without root, a warning, and the field stays absent
// (not read).
func (c *collector) firmwareFailures() []report.FirmwareFailure {
	if !c.privileged {
		c.warn("kernel log (missing firmware): needs root (run with --full)")
		return nil
	}
	records, lost, err := readKmsg()
	if err != nil {
		c.warn("kernel log (missing firmware): %v", err)
		return nil
	}
	if lost > 0 {
		c.warn("kernel log: records overwritten while hwspec read it: %d; missing-firmware messages among them are lost", lost)
	}
	return parseFirmwareFailures(records)
}

// parseFirmwareFailures finds the firmware load failures in kmsg records:
// the file, the error, and the device and its driver, from the DEVICE key
// ("+pci:0000:02:00.0") or else the message's "<driver> <device>: "
// prefix. Each device and file is listed once, however often it was
// retried.
func parseFirmwareFailures(records []string) []report.FirmwareFailure {
	out := []report.FirmwareFailure{}
	seen := map[[2]string]bool{}
	for _, rec := range records {
		lines := strings.Split(strings.TrimSuffix(rec, "\n"), "\n")
		_, text, ok := strings.Cut(lines[0], ";")
		if !ok {
			continue
		}
		m := fwFailed.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		f := report.FirmwareFailure{Driver: m[1], Device: m[2], File: m[3]}
		f.Error, _ = strconv.Atoi(m[4]) // the pattern makes it a number
		for _, l := range lines[1:] {
			if dev, ok := strings.CutPrefix(l, " DEVICE=+"); ok {
				if _, name, ok := strings.Cut(dev, ":"); ok {
					f.Device = name
				}
			}
		}
		key := [2]string{f.Device, f.File}
		if !seen[key] {
			seen[key] = true
			out = append(out, f)
		}
	}
	return out
}
