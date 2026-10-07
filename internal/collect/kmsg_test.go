package collect

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/jiegui2025/hwspec/internal/report"
)

// iwlwifiFailure is the record #7's criterion names, as /dev/kmsg gives it.
const iwlwifiFailure = "4,1234,5678901,-;iwlwifi 0000:02:00.0: Direct firmware load for iwlwifi-cc-a0-77.ucode failed with error -2\n SUBSYSTEM=pci\n DEVICE=+pci:0000:02:00.0\n"

func failures(fs []report.FirmwareFailure) string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Driver+" "+f.Device+" "+f.File+" "+strconv.Itoa(f.Error))
	}
	return strings.Join(out, "; ")
}

func TestParseFirmwareFailures(t *testing.T) {
	got := parseFirmwareFailures([]string{
		"6,1,1,-;Linux version 7.2.9\n",
		iwlwifiFailure,
		iwlwifiFailure, // retried: listed once
		// The same file for a second device: listed again.
		strings.ReplaceAll(iwlwifiFailure, "0000:02:00.0", "0000:03:00.0"),
		// No DEVICE key: the device from dev_printk's prefix.
		"4,2,2,-;btusb 1-14:1.0: Direct firmware load for intel/ibt-20-1-3.sfi failed with error -2\n",
		// A DEVICE key that isn't subsystem:name (a char device): the prefix's.
		"4,3,3,-;rtw88 phy0: Direct firmware load for rtw88/rtw8822c_fw.bin failed with error -110\n DEVICE=c254:0\n",
		// A USB DEVICE key wins over the prefix.
		"4,4,4,-;usb 3-1: Direct firmware load for x.bin failed with error -2\n DEVICE=+usb:3-1\n",
		// Escaped bytes stay escaped.
		"4,5,5,-;drv dev: Direct firmware load for a\\x1bb.bin failed with error -2\n",
		// Without dev_printk's prefix (no device).
		"4,6,6,-;Direct firmware load for orphan.bin failed with error -2\n",
		"4,7,7,-;iwlwifi 0000:02:00.0: Direct firmware load for x failed with error 2\n", // not negative: not the loader's
		"4,9,9,-;d v: Direct firmware load for y failed with error -0\n",                 // the loader logs only failures
		"4,10,10,-;some text Direct firmware load for z failed with error -2\n",          // not the loader's line
		"no prefix separator\n",
		"4,8,8,-;iwlwifi 0000:02:00.0: something else\n",
	})
	want := "iwlwifi 0000:02:00.0 iwlwifi-cc-a0-77.ucode -2; iwlwifi 0000:03:00.0 iwlwifi-cc-a0-77.ucode -2; btusb 1-14:1.0 intel/ibt-20-1-3.sfi -2; rtw88 phy0 rtw88/rtw8822c_fw.bin -110; " +
		"usb 3-1 x.bin -2; drv dev a\\x1bb.bin -2;   orphan.bin -2"
	if got := failures(got); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if got := parseFirmwareFailures(nil); got == nil || len(got) != 0 {
		t.Errorf("no records: %#v", got)
	}
}

// Under --full the log is read; without root, a warning and nothing (not
// read); overwritten records and errors are warnings.
func TestFirmwareFailures(t *testing.T) {
	t.Cleanup(saveHooks())
	readKmsg = func() ([]string, int, error) { return []string{iwlwifiFailure}, 0, nil }
	col := &collector{r: &report.Report{}}
	if got := col.firmwareFailures(); got != nil || strings.Join(col.r.Warnings, "") != "kernel log (missing firmware): needs root (run with --full)" {
		t.Errorf("user: %+v, warnings %q", got, col.r.Warnings)
	}
	col = &collector{r: &report.Report{}, privileged: true}
	got := col.firmwareFailures()
	if len(got) != 1 || got[0] != (report.FirmwareFailure{Device: "0000:02:00.0", Driver: "iwlwifi", File: "iwlwifi-cc-a0-77.ucode", Error: -2}) || len(col.r.Warnings) != 0 {
		t.Errorf("root: %+v, warnings %q", got, col.r.Warnings)
	}
	readKmsg = func() ([]string, int, error) { return nil, 1, nil }
	col = &collector{r: &report.Report{}, privileged: true}
	if got := col.firmwareFailures(); got == nil || len(got) != 0 || !strings.Contains(strings.Join(col.r.Warnings, ""), "kernel log: records overwritten while hwspec read it: 1;") {
		t.Errorf("lost: %+v, warnings %q", got, col.r.Warnings)
	}
	readKmsg = func() ([]string, int, error) { return nil, 0, unix.EPERM }
	col = &collector{r: &report.Report{}, privileged: true}
	if got := col.firmwareFailures(); got != nil || strings.Join(col.r.Warnings, "") != "kernel log (missing firmware): operation not permitted" {
		t.Errorf("error: %+v, warnings %q", got, col.r.Warnings)
	}
}

// The real device: as a user here, dmesg_restrict refuses it; as root,
// it ends with EAGAIN, never a hang.
func TestHostKmsg(t *testing.T) {
	hostTest(t)
	records, _, err := readKmsg()
	switch {
	case err == nil:
		if len(records) == 0 {
			t.Error("root read no records")
		}
	case errors.Is(err, unix.EPERM), errors.Is(err, unix.EACCES), errors.Is(err, unix.ENOENT):
		t.Logf("not readable here: %v", err)
	default:
		t.Errorf("unexpected error: %v", err)
	}
}

// Records come from the kernel, carrying hardware's text: the parser must
// handle anything, and keep only what its pattern matched.
func FuzzParseFirmwareFailures(f *testing.F) {
	f.Add(iwlwifiFailure)
	f.Add("4,2,2,-;btusb 1-14:1.0: Direct firmware load for intel/ibt-20-1-3.sfi failed with error -2\n")
	f.Add(";\n DEVICE=+")
	f.Fuzz(func(t *testing.T, rec string) {
		for _, fw := range parseFirmwareFailures([]string{rec}) {
			if fw.File == "" || fw.Error >= 0 || strings.ContainsAny(fw.File+fw.Driver, " \n") {
				t.Fatalf("%+v from %q", fw, rec)
			}
		}
	})
}
