package collect

import (
	"strings"
	"testing"

	"github.com/jiegui2025/hwspec/internal/report"
)

// rtcProc is the reference machine's /proc/driver/rtc (2026-10-07), its
// batt_status replaced per case.
const rtcProc = "rtc_time\t: 19:06:56\nrtc_date\t: 2026-10-07\nalrm_time\t: 00:00:00\n24hr\t\t: yes\nBCD\t\t: yes\nperiodic_freq\t: 1024\nbatt_status\t: %s\n"

// The RTC driver's batt_status (#113): okay or dead as given; no file,
// no line, nothing and no warning; an unreadable file or another value,
// a warning.
func TestRTCBattery(t *testing.T) {
	for _, c := range []struct {
		name, content string // "-": no file; "/x": a directory
		want, warn    string
	}{
		{"okay", strings.Replace(rtcProc, "%s", "okay", 1), report.RTCBattOkay, ""},
		{"dead", strings.Replace(rtcProc, "%s", "dead", 1), report.RTCBattDead, ""},
		{"no line", "rtc_time\t: 19:06:56\nrtc_date\t: 2026-10-07\n", "", ""},
		{"a line without a colon", "batt_status okay\n", "", ""},
		{"no file", "-", "", ""},
		{"another value", strings.Replace(rtcProc, "%s", "low", 1), "", `rtc: unexpected batt_status "low"`},
		{"unreadable", "/x", "", "rtc: read"},
	} {
		file, _ := fakeRoot(t)
		switch c.content {
		case "-":
		case "/x":
			file("/proc/driver/rtc/x", "")
		default:
			file("/proc/driver/rtc", c.content)
		}
		col := &collector{r: &report.Report{}}
		got := col.rtc()
		w := strings.Join(col.r.Warnings, "\n")
		status := ""
		if got != nil {
			status = got.BattStatus
		}
		if status != c.want || c.want == "" && got != nil || c.warn == "" && w != "" || c.warn != "" && !strings.Contains(w, c.warn) {
			t.Errorf("%s: %+v, warnings %q", c.name, got, w)
		}
	}
}
