package collect

import (
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/jiegui2025/hwspec/internal/report"
)

// GUIDs are fwupd's for the same instance ID: these pairs are from
// `fwupdmgr get-devices --json` (fwupd 2.1.8) on the reference machine,
// plus RFC 4122's own example of a version 5 UUID in the DNS namespace.
func TestInstanceGUIDIsFwupds(t *testing.T) {
	for id, want := range map[string]string{
		`NVME\VEN_144D&DEV_A808`:                 "47335265-a509-51f7-841e-1c94911af66b",
		`NVME\VEN_144D&DEV_A808&SUBSYS_144DA801`: "c9d531ea-ee7d-5562-8def-c64d0d144813",
		"SAMSUNG MZVLB256HAHQ-000L7":             "9657ce89-450f-58d2-ade0-2c6ae667541a",
		"www.example.com":                        "2ed6657d-e927-568b-95e1-2665a8aea6a2",
	} {
		if got := instanceGUID(id); got != want {
			t.Errorf("%s: %s, want %s", id, got, want)
		}
	}
}

// A PCI NVMe controller gets fwupd's three IDs; without a subsystem, a
// usable model or a PCI parent, fewer.
func TestNVMeInstanceIDs(t *testing.T) {
	file, link := fakeRoot(t)
	ctrl := "/sys/devices/pci0000:00/0000:01:00.0/nvme/nvme0"
	link(ctrl+"/device", "../../../0000:01:00.0")
	pci := "/sys/devices/pci0000:00/0000:01:00.0"
	file(pci+"/vendor", "0x144d\n")
	file(pci+"/device", "0xa808\n")
	file(pci+"/subsystem_vendor", "0x144d\n")
	file(pci+"/subsystem_device", "0xa801\n")
	file(ctrl+"/model", "SAMSUNG MZVLB256HAHQ-000L7               \n")
	ids := func() string {
		var s []string
		for _, id := range nvmeInstanceIDs(ctrl) {
			if id.GUID != instanceGUID(id.ID) {
				t.Errorf("%s: GUID %s", id.ID, id.GUID)
			}
			s = append(s, id.ID)
		}
		return strings.Join(s, " | ")
	}
	if got := ids(); got != `NVME\VEN_144D&DEV_A808 | NVME\VEN_144D&DEV_A808&SUBSYS_144DA801 | SAMSUNG MZVLB256HAHQ-000L7` {
		t.Errorf("all three: %s", got)
	}
	file(ctrl+"/model", "Bad\x01Model\n")
	file(pci+"/subsystem_device", "a801\n") // not sysfs's 0x form
	if got := ids(); got != `NVME\VEN_144D&DEV_A808` {
		t.Errorf("no subsystem, a model with a control character: %s", got)
	}
	file(pci+"/device", "0x1a808\n")
	if got := ids(); got != "" {
		t.Errorf("a device ID over 16 bits: %s", got)
	}
	if got := nvmeInstanceIDs("/sys/devices/virtual/nvme-fabrics/ctl/nvme1"); got != nil {
		t.Errorf("no PCI controller: %v", got)
	}
}

func TestUpperHex4AndPrintableASCII(t *testing.T) {
	for in, want := range map[string]string{"0x144d": "144D", "0x1": "0001", "0xffff": "FFFF", "144d": "", "0x": "", "0x10000": "", "0xg": ""} {
		if got := upperHex4(in); got != want {
			t.Errorf("upperHex4(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]bool{"WDC PC SN730": true, "": false, "a\x7f": false, "é": false, "~ !": true} {
		if got := printableASCII(in); got != want {
			t.Errorf("printableASCII(%q) = %v", in, got)
		}
	}
}

// The ESRT as root: each entry in table order (entry10 after entry2),
// with its type named; without root, "needs --full"; without an ESRT, no
// block. The first entry is the reference machine's system firmware as
// fwupd's uefi_capsule plugin reports it (GUID, version 141056, lowest 1).
func TestESRT(t *testing.T) {
	file, _ := fakeRoot(t)
	c := &collector{r: &report.Report{}, privileged: true}
	if got := c.esrt(); got != nil {
		t.Errorf("no ESRT: %+v", got)
	}
	entry := func(n, class, typ, version, lowest string) {
		d := esrtDir + "/entry" + n + "/"
		file(d+"fw_class", class+"\n")
		file(d+"fw_type", typ+"\n")
		file(d+"fw_version", version+"\n")
		file(d+"lowest_supported_fw_version", lowest+"\n")
	}
	entry("0", "B1413CA8-C3DE-4754-9E3C-2A719D79CDBC", "1", "141056", "1")
	entry("10", "00000000-0000-0000-0000-00000000000a", "3", "4294967295", "0")
	entry("2", "00000000-0000-0000-0000-000000000002", "2", "7", "7")
	entry("3", "00000000-0000-0000-0000-000000000003", "9", "1", "1")
	got := c.esrt()
	want := []report.ESRTEntry{
		{FWClass: "b1413ca8-c3de-4754-9e3c-2a719d79cdbc", FWType: "system", FWVersion: 141056, LowestSupportedFWVersion: 1},
		{FWClass: "00000000-0000-0000-0000-000000000002", FWType: "device", FWVersion: 7, LowestSupportedFWVersion: 7},
		{FWClass: "00000000-0000-0000-0000-000000000003", FWType: "unknown", FWVersion: 1, LowestSupportedFWVersion: 1},
		{FWClass: "00000000-0000-0000-0000-00000000000a", FWType: "uefi-driver", FWVersion: 4294967295},
	}
	if got == nil || got.Status != "" || !slices.Equal(got.Entries, want) {
		t.Errorf("as root: %+v", got)
	}

	// As a user the files are root's (-r--------); a root-only file that
	// fails as root is a problem worth a warning instead.
	unreadable = map[string]error{esrtDir + "/entry0/fw_class": syscall.EACCES}
	user := &collector{r: &report.Report{}}
	if got := user.esrt(); got == nil || got.Status != report.FirmwareUnknown || got.Reason != "needs --full" || got.Entries != nil || len(user.r.Warnings) > 0 {
		t.Errorf("as a user: %+v, warnings %q", got, user.r.Warnings)
	}
	if got := c.esrt(); got == nil || !strings.HasPrefix(got.Reason, "entry0: ") || !strings.Contains(got.Reason, "permission denied") || !hasWarning(c.r, "esrt entry0") {
		t.Errorf("as root, denied: %+v", got)
	}
	unreadable = nil

	for _, bad := range []struct{ file, content, want string }{
		{"fw_class", "not-a-guid", `entry2: fw_class "not-a-guid" isn't a GUID`},
		{"fw_version", "4294967296", `entry2: fw_version "4294967296" isn't a 32-bit number`},
		{"fw_type", "-1", `entry2: fw_type "-1" isn't a 32-bit number`},
	} {
		entry("2", "00000000-0000-0000-0000-000000000002", "2", "7", "7")
		file(esrtDir+"/entry2/"+bad.file, bad.content)
		c := &collector{r: &report.Report{}, privileged: true}
		got := c.esrt()
		if got == nil || got.Status != report.FirmwareUnknown || got.Reason != bad.want || got.Entries != nil || !hasWarning(c.r, "esrt entry2") {
			t.Errorf("%s %q: %+v, warnings %q", bad.file, bad.content, got, c.r.Warnings)
		}
	}
	entry("2", "00000000-0000-0000-0000-000000000002", "2", "7", "7")
	for _, f := range []string{"fw_class", "lowest_supported_fw_version"} {
		unreadable = map[string]error{esrtDir + "/entry0/" + f: syscall.EACCES}
		if got := c.esrt(); got == nil || !strings.HasPrefix(got.Reason, "entry0: ") || !strings.Contains(got.Reason, "permission denied") {
			t.Errorf("%s unreadable: %+v", f, got)
		}
	}
}
