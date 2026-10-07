package collect

import (
	"errors"
	"io/fs"
	"strings"

	"github.com/jiegui2025/hwspec/internal/report"
)

// rtc reads the RTC driver's batt_status from /proc/driver/rtc, which is
// world-readable: rtc-cmos's reading of the clock chip's "valid RAM and
// time" bit, which only shows a dead cell where the chip implements it
// (report.RTC). No file or no line: nothing, since many RTC drivers don't
// say. A value other than okay or dead is a warning.
func (c *collector) rtc() *report.RTC {
	data, err := readFile("/proc/driver/rtc")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		c.warn("rtc: %v", err)
		return nil
	}
	for line := range strings.Lines(string(data)) {
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "batt_status" {
			continue
		}
		switch value = strings.TrimSpace(value); value {
		case report.RTCBattOkay, report.RTCBattDead:
			return &report.RTC{BattStatus: value}
		default:
			c.warn("rtc: unexpected batt_status %q", value)
			return nil
		}
	}
	return nil
}
