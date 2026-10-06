package collect

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/jiegui2025/hwspec/internal/report"
	"github.com/jiegui2025/hwspec/internal/smbios/smbiostest"
)

// rootCollector is a collector as root with the given raw SMBIOS table
// and PCI devices.
func rootCollector(t *testing.T, table []byte, pci []report.PCIDevice) *collector {
	t.Helper()
	file, _ := fakeRoot(t)
	file("/sys/firmware/dmi/tables/DMI", string(table))
	unreadable = nil
	c := &collector{r: &report.Report{PCI: pci}, privileged: true}
	c.firmwareTables()
	return c
}

func port(addr string, width int) report.PCIDevice {
	return report.PCIDevice{Address: addr, ClassCode: "060400", Link: &report.PCIeLink{Width: width, MaxWidth: width}}
}

func dev(addr, class, parent string, width int) report.PCIDevice {
	d := report.PCIDevice{Address: addr, ClassCode: class, Parent: parent}
	if width > 0 {
		d.Link = &report.PCIeLink{Width: width, MaxWidth: width}
	}
	return d
}

func mountingOf(c *collector, addr string) *report.Mounting {
	for _, d := range c.r.PCI {
		if d.Address == addr {
			return d.Mounting
		}
	}
	return nil
}

type want struct{ kind, slot, slotType, confidence, reason string }

func check(t *testing.T, c *collector, cases map[string]want) {
	t.Helper()
	for addr, w := range cases {
		m := mountingOf(c, addr)
		if m == nil || m.Kind != w.kind || m.Slot != w.slot || m.SlotType != w.slotType || m.Confidence != w.confidence || !strings.Contains(m.Reason, w.reason) {
			t.Errorf("%s: %+v\n want %+v", addr, m, w)
		}
	}
}

// recordedAsRoot captures the recorded reference machine (still as an
// unprivileged user) with its raw SMBIOS types 4, 9 and 41 (#117's
// `dmidecode -u` bytes) made readable, as they are to root: the PCI
// topology comes from the recording itself.
func recordedAsRoot(t *testing.T) *report.Report {
	t.Helper()
	src := "testdata/machines/hp-elitedesk-800-g5-mini"
	dir := t.TempDir()
	err := filepath.WalkDir(src, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		to := filepath.Join(dir, strings.TrimPrefix(path, src))
		switch {
		case e.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(target, to)
		case e.IsDir():
			return os.MkdirAll(to, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(to, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	table := filepath.Join(dir, "root/sys/firmware/dmi/tables/DMI")
	if err := os.MkdirAll(filepath.Dir(table), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(table, smbiostest.EliteDesk800G5Mini(), 0o644); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	b, _ := os.ReadFile(filepath.Join(dir, "machine.json"))
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	delete(m["unreadable"].(map[string]any), "/sys/firmware/dmi/tables/DMI")
	b, _ = json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "machine.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := CollectRecorded(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// The reference machine with its tables readable: the IGD and LAN are
// onboard. The NVMe is in an M.2 SSD slot of type Gen 3 x4, which one is
// unknown: the port it sits behind is listed for Slot4 as available, and
// the occupied Slot3 names a port that doesn't exist. That leaves the
// Wi-Fi's slot open too: if the NVMe is in Slot4, the Wi-Fi could be in
// Slot3, so it is in a slot, Slot2 or Slot3, which one unknown (their
// generic types differ). The broken addresses are a capture warning.
func TestTheReferenceMachinesMountings(t *testing.T) {
	r := recordedAsRoot(t)
	c := &collector{r: r}
	check(t, c, map[string]want{
		"0000:00:02.0": {"onboard", "", "", "high", ""},
		"0000:00:1f.6": {"onboard", "", "", "high", ""},
		"0000:02:00.0": {"slot", "", "", "medium", "holds it is unknown"},
		"0000:01:00.0": {"slot", "", "PCI Express Gen 3 x4", "medium", "holds it is unknown"},
		"0000:00:17.0": {"unknown", "", "", "", "it has no PCIe link to match a slot's width"},
	})
	nvme := strings.Join(mountingOf(c, "0000:01:00.0").Evidence, "\n")
	if !strings.Contains(nvme, `"Slot4 / M2 SSD" (PCI Express Gen 3 x4, available) at its port's address 0000:00:1b.0`) ||
		!strings.Contains(nvme, `"Slot3 / M2 SSD" (PCI Express Gen 3 x4) at 0000:00:1b.4, in use`) || strings.Contains(nvme, "Slot5") {
		t.Errorf("nvme evidence %q", nvme)
	}
	if e := strings.Join(mountingOf(c, "0000:02:00.0").Evidence, "\n"); !strings.Contains(e, `"Slot2 / M2 WLAN/BT"`) || !strings.Contains(e, `"Slot3 / M2 SSD"`) {
		t.Errorf("wi-fi evidence %q", e)
	}
	if e := mountingOf(c, "0000:00:02.0").Evidence; len(e) != 1 || e[0] != `SMBIOS type 41 lists "Onboard IGD" (Video) at 0000:00:02.0` {
		t.Errorf("igd evidence %q", e)
	}
	if !hasWarning(r, `smbios slots: the firmware's addresses for "Slot1 / DGPU PCIEXP" (0000:00:01.0), "Slot2 / M2 WLAN/BT" (0000:00:1c.7), "Slot3 / M2 SSD" (0000:00:1b.4), "Slot5 / TBT Fiber Combo" (0000:00:1d.0) name no device`) {
		t.Errorf("warnings %q", r.Warnings)
	}
	for _, d := range r.PCI {
		if d.Mounting != nil && !reported(&d) {
			t.Errorf("%s (class %s) got a mounting", d.Address, d.ClassCode)
		}
	}
}

// A slot that names the device (its address, as DSP0134 7.10.8 says, its
// function 0, or its port's), is in use, and fits it by type and width is
// believed: an x16 GPU slot, an MXM module, a GPU's audio function, an x2
// NVMe behind an x4 port; Usage Unknown doesn't contradict.
func TestSlotsThatNameTheirDevice(t *testing.T) {
	for _, c := range []struct {
		name  string
		table []byte
		pci   []report.PCIDevice
		addr  string
		want  want
	}{
		{"the endpoint", smbiostest.Slot("PCIEX16_1", 0xBD, 0x0D, 0x04, 0x04, 1, 0, 0x01, 0x00),
			[]report.PCIDevice{port("0000:00:01.0", 16), dev("0000:01:00.0", "030000", "0000:00:01.0", 16)}, "0000:01:00.0",
			want{"slot", "PCIEX16_1", "PCI Express Gen 4 x16", "high", ""}},
		{"its port", smbiostest.Slot("PCIEX16_1", 0xBD, 0x0D, 0x04, 0x04, 1, 0, 0x00, 0x01<<3),
			[]report.PCIDevice{port("0000:00:01.0", 16), dev("0000:01:00.0", "030000", "0000:00:01.0", 16)}, "0000:01:00.0",
			want{"slot", "PCIEX16_1", "PCI Express Gen 4 x16", "high", ""}},
		{"the GPU's audio function", smbiostest.Slot("PCIEX16_1", 0xBD, 0x0D, 0x04, 0x04, 1, 0, 0x01, 0x00),
			[]report.PCIDevice{port("0000:00:01.0", 16), dev("0000:01:00.0", "030000", "0000:00:01.0", 16), dev("0000:01:00.1", "040300", "0000:00:01.0", 16)}, "0000:01:00.1",
			want{"slot", "PCIEX16_1", "PCI Express Gen 4 x16", "high", ""}},
		{"an MXM module", smbiostest.Slot("MXM", 0x1D, 0x0D, 0x04, 0x04, 1, 0, 0x00, 0x01<<3),
			[]report.PCIDevice{port("0000:00:01.0", 16), dev("0000:01:00.0", "030000", "0000:00:01.0", 16)}, "0000:01:00.0",
			want{"slot", "MXM", "MXM 3.0 Type A", "high", ""}},
		{"x2 NVMe behind an x4 port", smbiostest.Slot("M2_1", 0x17, 0x0A, 0x04, 0x01, 1, 0, 0x00, 0x1b<<3),
			[]report.PCIDevice{port("0000:00:1b.0", 4), dev("0000:01:00.0", "010802", "0000:00:1b.0", 2)}, "0000:01:00.0",
			want{"slot", "M2_1", "M.2 Socket 3 (Mechanical Key M)", "high", ""}},
		{"an x16 slot wired to an x4 port", smbiostest.Slot("PCIEX16_2", 0xB6, 0x0D, 0x04, 0x04, 2, 0, 0x00, 0x1b<<3),
			[]report.PCIDevice{port("0000:00:1b.0", 4), dev("0000:01:00.0", "010802", "0000:00:1b.0", 4)}, "0000:01:00.0",
			want{"slot", "PCIEX16_2", "PCI Express Gen 3 x16", "high", ""}},
		{"a slot of unknown width", smbiostest.Slot("M2_1", 0x17, 0x01, 0x04, 0x01, 1, 0, 0x00, 0x1b<<3),
			[]report.PCIDevice{port("0000:00:1b.0", 4), dev("0000:01:00.0", "010802", "0000:00:1b.0", 4)}, "0000:01:00.0",
			want{"slot", "M2_1", "M.2 Socket 3 (Mechanical Key M)", "high", ""}},
		{"a slot of unknown type", smbiostest.Slot("J1", 0x01, 0x0A, 0x04, 0x01, 1, 0, 0x00, 0x1b<<3),
			[]report.PCIDevice{port("0000:00:1b.0", 4), dev("0000:01:00.0", "010802", "0000:00:1b.0", 4)}, "0000:01:00.0",
			want{"slot", "J1", "Other", "high", ""}},
		{"usage unknown", smbiostest.Slot("M2_1", 0x17, 0x0A, 0x02, 0x01, 1, 0, 0x00, 0x1b<<3),
			[]report.PCIDevice{port("0000:00:1b.0", 4), dev("0000:01:00.0", "010802", "0000:00:1b.0", 4)}, "0000:01:00.0",
			want{"slot", "M2_1", "M.2 Socket 3 (Mechanical Key M)", "high", ""}},
		{"a laptop's soldered GPU", smbiostest.Onboard("dGPU", 0x80|0x03, 2, 0, 0x01, 0x00),
			[]report.PCIDevice{port("0000:00:01.0", 16), dev("0000:01:00.0", "030200", "0000:00:01.0", 16)}, "0000:01:00.0",
			want{"onboard", "", "", "high", ""}},
	} {
		t.Run(c.name, func(t *testing.T) {
			col := rootCollector(t, append(c.table, smbiostest.End()...), c.pci)
			col.mountings()
			check(t, col, map[string]want{c.addr: c.want})
			if m := mountingOf(col, c.addr); len(m.Evidence) != 1 {
				t.Errorf("evidence %q", m.Evidence)
			}
		})
	}
}

// Records that contradict give unknown with every claim, never one
// confident answer: a slot naming the device that is Available or
// Unavailable, of a type or width that can't hold it, or next to a type
// 41 entry; a type 41 entry whose device type isn't the device's class.
func TestContradictoryFirmware(t *testing.T) {
	nvme := []report.PCIDevice{port("0000:00:1b.0", 4), dev("0000:01:00.0", "010802", "0000:00:1b.0", 4)}
	for _, c := range []struct {
		name   string
		table  []byte
		pci    []report.PCIDevice
		reason string
		claims int
	}{
		{"onboard and in a slot", append(smbiostest.Onboard("SSD", 0x80|0x0F, 1, 0, 0x01, 0x00),
			smbiostest.Slot("M2_1", 0x17, 0x0A, 0x04, 0x01, 1, 0, 0x01, 0x00)...), nvme, "both onboard and in a slot", 2},
		{"in use here, available at the port", append(smbiostest.Slot("M2_1", 0xB4, 0x0A, 0x04, 0x01, 1, 0, 0x01, 0x00),
			smbiostest.Slot("M2_2", 0xB4, 0x0A, 0x03, 0x01, 2, 0, 0x00, 0x1b<<3)...), nvme, "contradict each other", 2},
		{"only an available slot at the port", smbiostest.Slot("M2_2", 0xB4, 0x0A, 0x03, 0x01, 2, 0, 0x00, 0x1b<<3), nvme,
			"so it may be soldered on", 2},
		{"only an unavailable slot at the port", smbiostest.Slot("M2_2", 0xB4, 0x0A, 0x05, 0x01, 2, 0, 0x00, 0x1b<<3), nvme,
			"so it may be soldered on", 2},
		{"onboard, and a slot that can't hold it names it", append(smbiostest.Onboard("SSD", 0x80|0x0F, 1, 0, 0x01, 0x00),
			smbiostest.Slot("M2 WLAN", 0x15, 0x0A, 0x04, 0x01, 1, 0, 0x01, 0x00)...), nvme, "both onboard and in a slot", 2},
		{"type 41 of the wrong type, and a slot", append(smbiostest.Onboard("VGA", 0x80|0x03, 1, 0, 0x01, 0x00),
			smbiostest.Slot("M2_1", 0x17, 0x0A, 0x04, 0x01, 1, 0, 0x01, 0x00)...), nvme, "onboard record doesn't match", 2},
		{"type 41 says LAN and VGA at a NIC", append(smbiostest.Onboard("LAN", 0x80|0x05, 1, 0, 0x01, 0x00), smbiostest.Onboard("VGA", 0x80|0x03, 1, 0, 0x01, 0x00)...),
			[]report.PCIDevice{port("0000:00:1c.0", 1), dev("0000:01:00.0", "020000", "0000:00:1c.0", 1)}, "onboard record doesn't match", 2},
		{"a Key E slot naming an NVMe", smbiostest.Slot("M2 WLAN", 0x15, 0x0A, 0x04, 0x01, 1, 0, 0x00, 0x1b<<3), nvme,
			"can't hold it as the firmware describes them", 2},
		{"an x1 slot naming an x4 port", smbiostest.Slot("PCIE1", 0xB2, 0x08, 0x04, 0x04, 1, 0, 0x00, 0x1b<<3), nvme,
			"can't hold it as the firmware describes them", 2},
		{"type 41 says Ethernet at the Wi-Fi", smbiostest.Onboard("LAN", 0x80|0x05, 1, 0, 0x02, 0x00),
			[]report.PCIDevice{port("0000:00:1c.0", 1), dev("0000:02:00.0", "028000", "0000:00:1c.0", 1)}, "onboard record doesn't match", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			col := rootCollector(t, append(c.table, smbiostest.End()...), c.pci)
			col.mountings()
			addr := c.pci[len(c.pci)-1].Address
			if m := mountingOf(col, addr); m == nil || m.Kind != "unknown" || !strings.Contains(m.Reason, c.reason) || len(m.Evidence) != c.claims {
				t.Errorf("%+v, want unknown: %s, with %d claims", m, c.reason, c.claims)
			}
		})
	}
}

// An empty slot at a type 41 device's address or port agrees with it
// being soldered on; so do two type 41 entries for it.
func TestOnboardWithAgreeingRecords(t *testing.T) {
	for _, c := range []struct {
		name  string
		table []byte
		n     int
	}{
		{"an available slot at its port", append(smbiostest.Onboard("SSD", 0x80|0x0F, 1, 0, 0x01, 0x00),
			smbiostest.Slot("M2_1", 0x17, 0x0A, 0x03, 0x01, 1, 0, 0x00, 0x1b<<3)...), 2},
		{"two entries", append(smbiostest.Onboard("SSD", 0x80|0x0F, 1, 0, 0x01, 0x00), smbiostest.Onboard("NVMe", 0x80|0x0F, 2, 0, 0x01, 0x00)...), 2},
	} {
		col := rootCollector(t, append(c.table, smbiostest.End()...), []report.PCIDevice{port("0000:00:1b.0", 4), dev("0000:01:00.0", "010802", "0000:00:1b.0", 4)})
		col.mountings()
		if m := mountingOf(col, "0000:01:00.0"); m.Kind != "onboard" || m.Confidence != "high" || len(m.Evidence) != c.n {
			t.Errorf("%s: %+v", c.name, m)
		}
	}
}

// Placing by elimination: every card the records don't place competes for
// the free slots in use at least as wide as its port, whatever its class;
// a card is placed only if every card in its group can have a slot of its
// own at once, and named only when every such placement gives it that
// slot and the slot's type can hold it. An all-zero address is no address.
func TestPlacingByElimination(t *testing.T) {
	x1 := func(name string, typ byte, devfn byte) []byte {
		return smbiostest.Slot(name, typ, 0x08, 0x04, 0x01, 1, 0, 0x00, devfn)
	}
	keyM := func(name string, devfn byte) []byte {
		return smbiostest.Slot(name, 0x17, 0x0A, 0x04, 0x01, 1, 0, 0x00, devfn)
	}
	pcie4 := func(name string, devfn byte) []byte {
		return smbiostest.Slot(name, 0xB4, 0x0A, 0x04, 0x04, 1, 0, 0x00, devfn)
	}
	cat := func(b ...[]byte) []byte {
		var out []byte
		for _, x := range b {
			out = append(out, x...)
		}
		return out
	}
	wifi := dev("0000:02:00.0", "028000", "0000:00:1c.0", 1)
	nvme := dev("0000:01:00.0", "010802", "0000:00:1b.0", 4)
	for _, c := range []struct {
		name  string
		table []byte
		pci   []report.PCIDevice
		want  map[string]want
	}{
		{"one Wi-Fi, one broken x1 slot", x1("M2 WLAN", 0xB2, 0x1c<<3|7),
			[]report.PCIDevice{port("0000:00:1c.0", 1), wifi},
			map[string]want{"0000:02:00.0": {"slot", "M2 WLAN", "PCI Express Gen 3 x1", "medium", "no slot names this device"}}},
		{"a soldered NIC and the Wi-Fi, one x1 slot", x1("M2 WLAN", 0xB2, 0x1c<<3|7),
			[]report.PCIDevice{port("0000:00:1c.0", 1), port("0000:00:1c.4", 1), wifi, dev("0000:03:00.0", "020000", "0000:00:1c.4", 1)},
			map[string]want{"0000:02:00.0": {"unknown", "", "", "", "2 cards compete for too few slots"}, "0000:03:00.0": {"unknown", "", "", "", "2 cards compete"}}},
		{"a soldered NIC and a USB card, one x1 slot", x1("PCIEX1_1", 0xB2, 0x1c<<3|7),
			[]report.PCIDevice{port("0000:00:1c.0", 1), port("0000:00:1c.4", 1), dev("0000:02:00.0", "0c0330", "0000:00:1c.0", 1), dev("0000:03:00.0", "020000", "0000:00:1c.4", 1)},
			map[string]want{"0000:03:00.0": {"unknown", "", "", "", "2 cards compete"}}},
		{"a soldered dGPU and a switch card, one x16 slot", smbiostest.Slot("PCIEX16", 0xB6, 0x0D, 0x04, 0x04, 1, 0, 0x00, 0x1d<<3|4),
			[]report.PCIDevice{port("0000:00:01.0", 16), port("0000:00:1d.0", 16), dev("0000:01:00.0", "030000", "0000:00:01.0", 16),
				dev("0000:05:00.0", "060400", "0000:00:1d.0", 16), dev("0000:06:00.0", "060400", "0000:05:00.0", 16)},
			map[string]want{"0000:01:00.0": {"unknown", "", "", "", "2 cards compete"}}},
		{"a NIC adapter and a soldered NVMe, one Key M slot", keyM("M2_1", 0x1b<<3|4),
			[]report.PCIDevice{port("0000:00:1b.0", 4), port("0000:00:1d.0", 4), nvme, dev("0000:04:00.0", "020000", "0000:00:1d.0", 4)},
			map[string]want{"0000:01:00.0": {"unknown", "", "", "", "2 cards compete"}, "0000:04:00.0": {"unknown", "", "", "", "2 cards compete"}}},
		{"two NICs and an NVMe, a PCIe x4 and two Key M slots", cat(pcie4("PCIE", 0x1b<<3|4), keyM("M2_1", 0x1b<<3|5), keyM("M2_2", 0x1b<<3|6)),
			[]report.PCIDevice{port("0000:00:1b.0", 4), port("0000:00:1d.0", 4), port("0000:00:1e.0", 4), nvme,
				dev("0000:04:00.0", "020000", "0000:00:1d.0", 4), dev("0000:05:00.0", "020000", "0000:00:1e.0", 4)},
			map[string]want{"0000:04:00.0": {"slot", "", "", "medium", "holds it is unknown"}, "0000:05:00.0": {"slot", "", "", "medium", "holds it is unknown"}, "0000:01:00.0": {"slot", "", "", "medium", "holds it is unknown"}}},
		{"in a slot in every placement, but not always the same", cat(smbiostest.Slot("S1", 0xB2, 0x08, 0x04, 0x01, 1, 0, 0x00, 0x1b<<3|4), pcie4("S2", 0x1b<<3|5), smbiostest.Slot("S3", 0xB4, 0x0A, 0x03, 0x04, 3, 0, 0x00, 0x1b<<3)),
			[]report.PCIDevice{port("0000:00:1b.0", 4), port("0000:00:1c.0", 1), nvme, wifi},
			map[string]want{"0000:02:00.0": {"slot", "", "", "medium", "holds it is unknown"}, "0000:01:00.0": {"slot", "", "PCI Express Gen 3 x4", "medium", "holds it is unknown"}}},
		{"three cards in a chain over two slots", cat(pcie4("S1", 0x1b<<3|4), pcie4("S2", 0x1b<<3|5)),
			[]report.PCIDevice{port("0000:00:1b.0", 4), port("0000:00:1c.0", 4), port("0000:00:1d.0", 4), nvme,
				dev("0000:02:00.0", "010802", "0000:00:1c.0", 4), dev("0000:04:00.0", "010802", "0000:00:1d.0", 4)},
			map[string]want{"0000:01:00.0": {"unknown", "", "", "", "3 cards compete"}, "0000:04:00.0": {"unknown", "", "", "", "3 cards compete"}}},
		{"an NVMe and only a Key E slot", smbiostest.Slot("M2 WLAN", 0x15, 0x0A, 0x04, 0x01, 1, 0, 0x00, 0x1c<<3|7),
			[]report.PCIDevice{port("0000:00:1b.0", 4), nvme},
			map[string]want{"0000:01:00.0": {"unknown", "", "", "", "can't hold this kind of device"}}},
		{"a NIC and only a Key M slot", keyM("M2_1", 0x1b<<3|4),
			[]report.PCIDevice{port("0000:00:1b.0", 4), dev("0000:01:00.0", "020000", "0000:00:1b.0", 4)},
			map[string]want{"0000:01:00.0": {"unknown", "", "", "", "can't hold this kind of device"}}},
		{"an NVMe and only an MXM slot", smbiostest.Slot("MXM", 0x1D, 0x0A, 0x04, 0x04, 1, 0, 0x00, 0x1b<<3|4),
			[]report.PCIDevice{port("0000:00:1b.0", 4), nvme},
			map[string]want{"0000:01:00.0": {"unknown", "", "", "", "can't hold this kind of device"}}},
		{"one NVMe, two broken x4 slots", cat(keyM("M2_1", 0x1b<<3|4), keyM("M2_2", 0x1b<<3|5)),
			[]report.PCIDevice{port("0000:00:1b.0", 4), nvme},
			map[string]want{"0000:01:00.0": {"unknown", "", "", "", "can't fill every slot in use"}}},
		{"one NVMe, three Key M slots in use", cat(keyM("M2_1", 0x1b<<3|4), keyM("M2_2", 0x1b<<3|5), keyM("M2_3", 0x1b<<3|6)),
			[]report.PCIDevice{port("0000:00:1b.0", 4), nvme},
			map[string]want{"0000:01:00.0": {"unknown", "", "", "", "can't fill every slot in use"}}},
		{"an all-zero address is no address", cat(keyM("M2_A", 0x00), smbiostest.Slot("M2_B", 0x17, 0x0A, 0x03, 0x01, 2, 0, 0x00, 0x1b<<3)),
			[]report.PCIDevice{port("0000:00:00.0", 4), port("0000:00:1b.0", 4), nvme},
			map[string]want{"0000:01:00.0": {"slot", "", "M.2 Socket 3 (Mechanical Key M)", "medium", "holds it is unknown"}}},
		{"two NVMe-sized cards, two broken Other slots", cat(smbiostest.Slot("J1", 0x01, 0x0A, 0x04, 0x01, 1, 0, 0x00, 0x1b<<3|4), smbiostest.Slot("J2", 0x01, 0x0A, 0x04, 0x01, 2, 0, 0x00, 0x1b<<3|5)),
			[]report.PCIDevice{port("0000:00:1b.0", 4), port("0000:00:1d.0", 4), nvme, dev("0000:04:00.0", "020000", "0000:00:1d.0", 4)},
			map[string]want{"0000:01:00.0": {"slot", "", "Other", "medium", "holds it is unknown"}, "0000:04:00.0": {"slot", "", "Other", "medium", "holds it is unknown"}}},
		{"x4, x1 and x16 slots in that order", cat(pcie4("PCIE_X4", 0x1b<<3|4), smbiostest.Slot("PCIE_X1", 0xB2, 0x08, 0x04, 0x01, 2, 0, 0x00, 0x1b<<3|5), smbiostest.Slot("PCIE_X16", 0xB6, 0x0D, 0x04, 0x04, 3, 0, 0x00, 0x1b<<3|6)),
			[]report.PCIDevice{port("0000:00:1b.0", 4), port("0000:00:1c.0", 1), port("0000:00:1d.0", 4), nvme, wifi, dev("0000:04:00.0", "020000", "0000:00:1d.0", 4)},
			map[string]want{"0000:02:00.0": {"slot", "PCIE_X1", "PCI Express Gen 3 x1", "medium", ""}, "0000:01:00.0": {"slot", "", "", "medium", "holds it is unknown"}, "0000:04:00.0": {"slot", "", "", "medium", "holds it is unknown"}}},
		{"x1, x4 and x16 slots in that order", cat(smbiostest.Slot("PCIE_X1", 0xB2, 0x08, 0x04, 0x01, 2, 0, 0x00, 0x1b<<3|5), pcie4("PCIE_X4", 0x1b<<3|4), smbiostest.Slot("PCIE_X16", 0xB6, 0x0D, 0x04, 0x04, 3, 0, 0x00, 0x1b<<3|6)),
			[]report.PCIDevice{port("0000:00:1b.0", 4), port("0000:00:1c.0", 1), port("0000:00:1d.0", 4), dev("0000:04:00.0", "020000", "0000:00:1d.0", 4), wifi, nvme},
			map[string]want{"0000:02:00.0": {"slot", "PCIE_X1", "PCI Express Gen 3 x1", "medium", ""}, "0000:01:00.0": {"slot", "", "", "medium", "holds it is unknown"}, "0000:04:00.0": {"slot", "", "", "medium", "holds it is unknown"}}},
		{"a soldered NIC and an empty x1 slot of unknown usage", smbiostest.Slot("PCIEX1_1", 0xB2, 0x08, 0x02, 0x01, 1, 0, 0x00, 0x1c<<3|4),
			[]report.PCIDevice{port("0000:00:1c.0", 1), dev("0000:02:00.0", "020000", "0000:00:1c.0", 1)},
			map[string]want{"0000:02:00.0": {"unknown", "", "", "", "so it may be soldered on"}}},
		{"a soldered NIC and an empty x1 slot of usage Other", smbiostest.Slot("PCIEX1_1", 0xB2, 0x08, 0x01, 0x01, 1, 0, 0x00, 0x1c<<3|4),
			[]report.PCIDevice{port("0000:00:1c.0", 1), dev("0000:02:00.0", "020000", "0000:00:1c.0", 1)},
			map[string]want{"0000:02:00.0": {"unknown", "", "", "", "so it may be soldered on"}}},
		{"an NVMe, a Key M slot in use and one of unknown usage", cat(keyM("M2_1", 0x1b<<3|4), smbiostest.Slot("M2_2", 0x17, 0x0A, 0x02, 0x01, 2, 0, 0x00, 0x1b<<3|5)),
			[]report.PCIDevice{port("0000:00:1b.0", 4), nvme},
			map[string]want{"0000:01:00.0": {"slot", "", "M.2 Socket 3 (Mechanical Key M)", "medium", `which of "M2_1" or "M2_2" holds it is unknown`}}},
		{"a GPU card and two MXM slots, one in use", cat(smbiostest.Slot("MXM1", 0x1D, 0x0D, 0x04, 0x04, 1, 0, 0x00, 0x01<<3|4), smbiostest.Slot("MXM2", 0x1D, 0x0D, 0x02, 0x04, 2, 0, 0x00, 0x01<<3|5)),
			[]report.PCIDevice{port("0000:00:01.0", 16), dev("0000:01:00.0", "030000", "0000:00:01.0", 16), dev("0000:01:00.1", "040300", "0000:00:01.0", 16)},
			map[string]want{"0000:01:00.0": {"slot", "", "MXM 3.0 Type A", "medium", "holds it is unknown"}, "0000:01:00.1": {"slot", "", "MXM 3.0 Type A", "medium", "holds it is unknown"}}},
		{"a USB card behind an onboard switch and a soldered Wi-Fi", smbiostest.Slot("PCIEX8", 0xB5, 0x0B, 0x04, 0x04, 1, 0, 0x00, 0x1c<<3|7),
			[]report.PCIDevice{port("0000:00:1c.0", 4), dev("0000:03:00.0", "060400", "0000:00:1c.0", 4), dev("0000:04:01.0", "060400", "0000:03:00.0", 8),
				dev("0000:04:02.0", "060400", "0000:03:00.0", 1), dev("0000:05:00.0", "0c0330", "0000:04:01.0", 8), dev("0000:06:00.0", "028000", "0000:04:02.0", 1)},
			map[string]want{"0000:06:00.0": {"unknown", "", "", "", "cards compete for too few slots"}}},
		{"a VMD NVMe and a soldered NIC", smbiostest.Slot("M2_SSD", 0xB4, 0x0A, 0x04, 0x01, 1, 0, 0x00, 0x1b<<3|4),
			[]report.PCIDevice{{Address: "0000:00:0e.0", ClassCode: "010400"}, dev("10000:e0:06.0", "060400", "0000:00:0e.0", 4),
				dev("10000:e1:00.0", "010802", "10000:e0:06.0", 4), port("0000:00:1c.0", 4), dev("0000:02:00.0", "020000", "0000:00:1c.0", 4)},
			map[string]want{"0000:02:00.0": {"unknown", "", "", "", "2 cards compete"}, "10000:e1:00.0": {"unknown", "", "", "", "2 cards compete"}}},
		{"a USB card its slot names doesn't compete", cat(smbiostest.Slot("PCIEX1", 0xB2, 0x08, 0x04, 0x01, 1, 0, 0x00, 0x1c<<3), keyM("M2_1", 0x1b<<3|4)),
			[]report.PCIDevice{port("0000:00:1b.0", 4), port("0000:00:1c.0", 1), nvme, dev("0000:02:00.0", "0c0330", "0000:00:1c.0", 1)},
			map[string]want{"0000:01:00.0": {"slot", "M2_1", "M.2 Socket 3 (Mechanical Key M)", "medium", ""}}},
		{"a GPU card in a broken MXM slot", smbiostest.Slot("MXM", 0x1D, 0x0D, 0x04, 0x04, 1, 0, 0x00, 0x01<<3|4),
			[]report.PCIDevice{port("0000:00:01.0", 16), dev("0000:01:00.0", "030000", "0000:00:01.0", 16), dev("0000:01:00.1", "040300", "0000:00:01.0", 16)},
			map[string]want{"0000:01:00.0": {"slot", "MXM", "MXM 3.0 Type A", "medium", ""}, "0000:01:00.1": {"slot", "MXM", "MXM 3.0 Type A", "medium", ""}}},
		{"a lone zero-address slot", keyM("M2_A", 0x00),
			[]report.PCIDevice{port("0000:00:00.0", 4), port("0000:00:1b.0", 4), nvme},
			map[string]want{"0000:01:00.0": {"slot", "M2_A", "M.2 Socket 3 (Mechanical Key M)", "medium", ""}}},
		{"a GPU and its audio function are one card", smbiostest.Slot("PCIEX16", 0xB6, 0x0D, 0x04, 0x04, 1, 0, 0x00, 0x01<<3|4),
			[]report.PCIDevice{port("0000:00:01.0", 16), dev("0000:01:00.0", "030000", "0000:00:01.0", 16), dev("0000:01:00.1", "040300", "0000:00:01.0", 16)},
			map[string]want{"0000:01:00.0": {"slot", "PCIEX16", "PCI Express Gen 3 x16", "medium", ""}, "0000:01:00.1": {"slot", "PCIEX16", "PCI Express Gen 3 x16", "medium", ""}}},
		{"an x4 card in a wider slot", smbiostest.Slot("PCIEX16", 0xB6, 0x0D, 0x04, 0x04, 1, 0, 0x00, 0x1b<<3|4),
			[]report.PCIDevice{port("0000:00:1b.0", 4), nvme},
			map[string]want{"0000:01:00.0": {"slot", "PCIEX16", "PCI Express Gen 3 x16", "medium", ""}}},
		{"a narrower slot doesn't fit", x1("PCIE1", 0xB2, 0x1b<<3|4),
			[]report.PCIDevice{port("0000:00:1b.0", 4), nvme},
			map[string]want{"0000:01:00.0": {"unknown", "", "", "", "no slot in use fits it"}}},
		{"a root-bus device with its own x4 link", keyM("M2_1", 0x1b<<3|4),
			[]report.PCIDevice{dev("0000:00:0e.0", "010802", "", 4)},
			map[string]want{"0000:00:0e.0": {"slot", "M2_1", "M.2 Socket 3 (Mechanical Key M)", "medium", ""}}},
		{"a slot of unknown width fits", smbiostest.Slot("M2_1", 0x17, 0x01, 0x04, 0x01, 1, 0, 0x00, 0x1b<<3|4),
			[]report.PCIDevice{port("0000:00:1b.0", 4), nvme},
			map[string]want{"0000:01:00.0": {"slot", "M2_1", "M.2 Socket 3 (Mechanical Key M)", "medium", ""}}},
		{"a slot of unknown type fits", smbiostest.Slot("J1", 0x01, 0x0A, 0x04, 0x01, 1, 0, 0x00, 0x1b<<3|4),
			[]report.PCIDevice{port("0000:00:1b.0", 4), nvme},
			map[string]want{"0000:01:00.0": {"slot", "J1", "Other", "medium", ""}}},
		{"a slot of unknown usage alone doesn't place it", smbiostest.Slot("M2_1", 0x17, 0x0A, 0x02, 0x01, 1, 0, 0x00, 0x1b<<3|4),
			[]report.PCIDevice{port("0000:00:1b.0", 4), nvme},
			map[string]want{"0000:01:00.0": {"unknown", "", "", "", "so it may be soldered on"}}},
		{"an available broken slot is no candidate", cat(keyM("M2_1", 0x1b<<3|4), smbiostest.Slot("M2_2", 0x17, 0x0A, 0x03, 0x01, 2, 0, 0x00, 0x1b<<3|5)),
			[]report.PCIDevice{port("0000:00:1b.0", 4), nvme},
			map[string]want{"0000:01:00.0": {"slot", "M2_1", "M.2 Socket 3 (Mechanical Key M)", "medium", ""}}},
		{"a slot with no address", smbiostest.Slot("M2_1", 0x17, 0x0A, 0x04, 0x01, 1, 0xFFFF, 0xFF, 0xFF),
			[]report.PCIDevice{port("0000:00:1b.0", 4), nvme},
			map[string]want{"0000:01:00.0": {"slot", "M2_1", "M.2 Socket 3 (Mechanical Key M)", "medium", ""}}},
		{"a slot naming another device holds that one", cat(keyM("M2_1", 0x1d<<3), keyM("M2_2", 0x1b<<3|4)),
			[]report.PCIDevice{port("0000:00:1b.0", 4), port("0000:00:1d.0", 4), nvme, dev("0000:04:00.0", "010802", "0000:00:1d.0", 4)},
			map[string]want{"0000:04:00.0": {"slot", "M2_1", "M.2 Socket 3 (Mechanical Key M)", "high", ""}, "0000:01:00.0": {"slot", "M2_2", "M.2 Socket 3 (Mechanical Key M)", "medium", ""}}},
		{"one card's only slot settles the other's", cat(pcie4("S4", 0x1b<<3|4), smbiostest.Slot("S1", 0xB2, 0x08, 0x04, 0x01, 2, 0, 0x00, 0x1b<<3|5)),
			[]report.PCIDevice{port("0000:00:1b.0", 4), port("0000:00:1c.0", 1), nvme, wifi},
			map[string]want{"0000:01:00.0": {"slot", "S4", "PCI Express Gen 3 x4", "medium", ""}, "0000:02:00.0": {"slot", "S1", "PCI Express Gen 3 x1", "medium", ""}}},
		{"slots of one type that can't hold it", cat(smbiostest.Slot("W1", 0x15, 0x0A, 0x04, 0x01, 1, 0, 0x00, 0x1b<<3|4), smbiostest.Slot("W2", 0x15, 0x0A, 0x04, 0x01, 2, 0, 0x00, 0x1b<<3|5)),
			[]report.PCIDevice{port("0000:00:1b.0", 4), nvme},
			map[string]want{"0000:01:00.0": {"unknown", "", "", "", "can't fill every slot in use"}}},
		{"two NVMe and two Key E slots", cat(smbiostest.Slot("W1", 0x15, 0x0A, 0x04, 0x01, 1, 0, 0x00, 0x1b<<3|4), smbiostest.Slot("W2", 0x15, 0x0A, 0x04, 0x01, 2, 0, 0x00, 0x1b<<3|5)),
			[]report.PCIDevice{port("0000:00:1b.0", 4), port("0000:00:1d.0", 4), nvme, dev("0000:04:00.0", "010802", "0000:00:1d.0", 4)},
			map[string]want{"0000:01:00.0": {"unknown", "", "", "", "none of the slots it could be in can hold this kind of device"}}},
		{"an unavailable broken slot is no candidate", cat(keyM("M2_1", 0x1b<<3|4), smbiostest.Slot("M2_2", 0x17, 0x0A, 0x05, 0x01, 2, 0, 0x00, 0x1b<<3|5)),
			[]report.PCIDevice{port("0000:00:1b.0", 4), nvme},
			map[string]want{"0000:01:00.0": {"slot", "M2_1", "M.2 Socket 3 (Mechanical Key M)", "medium", ""}}},
		{"the audio of an onboard GPU goes with it", cat(smbiostest.Onboard("dGPU", 0x80|0x03, 1, 0, 0x01, 0x00), smbiostest.Slot("PCIEX16", 0xB6, 0x0D, 0x04, 0x04, 1, 0, 0x00, 0x1d<<3|4)),
			[]report.PCIDevice{port("0000:00:01.0", 16), port("0000:00:1d.0", 16), dev("0000:01:00.0", "030000", "0000:00:01.0", 16),
				dev("0000:01:00.1", "040300", "0000:00:01.0", 16), dev("0000:04:00.0", "030000", "0000:00:1d.0", 16)},
			map[string]want{"0000:01:00.0": {"onboard", "", "", "high", ""}, "0000:01:00.1": {"onboard", "", "", "high", ""}, "0000:04:00.0": {"slot", "PCIEX16", "PCI Express Gen 3 x16", "medium", ""}}},
		{"devices on a switch card compete too", smbiostest.Slot("PCIEX16", 0xB6, 0x0D, 0x04, 0x04, 1, 0, 0x00, 0x1d<<3|4),
			[]report.PCIDevice{port("0000:00:1d.0", 16), dev("0000:05:00.0", "060400", "0000:00:1d.0", 16), dev("0000:06:01.0", "060400", "0000:05:00.0", 4),
				dev("0000:07:00.0", "010802", "0000:06:01.0", 4)},
			map[string]want{"0000:07:00.0": {"unknown", "", "", "", "2 cards compete"}}},
		{"a slot naming a card's bridge doesn't name what's behind it", smbiostest.Slot("PCI1", 0x06, 0x05, 0x04, 0x04, 1, 0, 0x05, 0x00),
			[]report.PCIDevice{port("0000:00:1c.0", 1), dev("0000:05:00.0", "060400", "0000:00:1c.0", 1), dev("0000:06:00.0", "020000", "0000:05:00.0", 0)},
			map[string]want{"0000:06:00.0": {"unknown", "", "", "", "behind a bridge on a card (0000:05:00.0)"}}},
		{"a claim is kept behind a card's bridge", cat(smbiostest.Slot("PCI1", 0x06, 0x05, 0x04, 0x04, 1, 0, 0x05, 0x00), smbiostest.Slot("X", 0x06, 0x05, 0x03, 0x04, 2, 0, 0x06, 0x00)),
			[]report.PCIDevice{port("0000:00:1c.0", 1), dev("0000:05:00.0", "060400", "0000:00:1c.0", 1), dev("0000:06:00.0", "020000", "0000:05:00.0", 0)},
			map[string]want{"0000:06:00.0": {"unknown", "", "", "", "behind a bridge on a card"}}},
		{"several slots name it", cat(keyM("M2_1", 0x1b<<3), smbiostest.Slot("M2_2", 0x17, 0x0A, 0x04, 0x01, 2, 0, 0x01, 0x00)),
			[]report.PCIDevice{port("0000:00:1b.0", 4), nvme},
			map[string]want{"0000:01:00.0": {"slot", "", "M.2 Socket 3 (Mechanical Key M)", "medium", "several slots name this device"}}},
		{"several slots of different types name it", cat(keyM("M2_1", 0x1b<<3), smbiostest.Slot("PCIE", 0xB4, 0x0A, 0x04, 0x04, 2, 0, 0x01, 0x00)),
			[]report.PCIDevice{port("0000:00:1b.0", 4), nvme},
			map[string]want{"0000:01:00.0": {"unknown", "", "", "", "several slots of different types"}}},
		{"a no-link device named by an available slot", smbiostest.Slot("M2_1", 0x17, 0x0A, 0x03, 0x01, 1, 0, 0x00, 0x17<<3),
			[]report.PCIDevice{dev("0000:00:17.0", "010601", "", 0)},
			map[string]want{"0000:00:17.0": {"unknown", "", "", "", "can't hold it as the firmware describes them"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			col := rootCollector(t, append(c.table, smbiostest.End()...), c.pci)
			col.mountings()
			check(t, col, c.want)
			if c.name == "a slot with no address" && len(col.r.Warnings) != 0 {
				t.Errorf("a slot without an address is no broken address: %q", col.r.Warnings)
			}
			if c.name == "an all-zero address is no address" && !strings.Contains(strings.Join(mountingOf(col, "0000:01:00.0").Evidence, "\n"), `"M2_A" (M.2 Socket 3 (Mechanical Key M)) at no address, in use`) {
				t.Errorf("the zero address isn't shown as no address: %q", mountingOf(col, "0000:01:00.0").Evidence)
			}
			if c.name == "the audio of an onboard GPU goes with it" {
				if e := mountingOf(col, "0000:01:00.1").Evidence; len(e) != 2 || e[0] != "function 0, 0000:01:00.0, has this answer from these records" || !strings.Contains(e[1], `"dGPU"`) {
					t.Errorf("function 0 evidence: %q", e)
				}
			}
			if c.name == "a claim is kept behind a card's bridge" {
				if e := mountingOf(col, "0000:06:00.0").Evidence; len(e) != 1 || !strings.Contains(e[0], `"X"`) {
					t.Errorf("claim behind a bridge: %q", e)
				}
			}
			for addr := range c.want {
				if m := mountingOf(col, addr); m.Kind != "onboard" && len(m.Evidence) == 0 && !strings.Contains(m.Reason, "no PCIe link") && !strings.Contains(m.Reason, "no slot in use fits it") && !strings.Contains(m.Reason, "behind a bridge on a card") {
					t.Errorf("%s: no evidence for %+v", addr, m)
				}
			}
		})
	}
}

// A slot naming function 0, which this device isn't but shares its bus
// and device with, names the device; it's then no broken address.
func TestSlotNamingAnAbsentFunctionZero(t *testing.T) {
	col := rootCollector(t, append(smbiostest.Slot("PCIEX16", 0xB6, 0x0D, 0x04, 0x04, 1, 0, 0x01, 0x00), smbiostest.End()...),
		[]report.PCIDevice{port("0000:00:01.0", 16), dev("0000:01:00.1", "040300", "0000:00:01.0", 16)})
	col.mountings()
	check(t, col, map[string]want{"0000:01:00.1": {"slot", "PCIEX16", "PCI Express Gen 3 x16", "high", ""}})
	if len(col.r.Warnings) != 0 {
		t.Errorf("warnings %q", col.r.Warnings)
	}
}

// Every slot-type row and every type 41 row, both ways.
func TestTypeFits(t *testing.T) {
	for _, c := range []struct {
		slotType, class string
		want            fit
	}{
		{"M.2 Socket 1-DP (Mechanical Key A)", "028000", yes}, {"M.2 Socket 1-SD (Mechanical Key E)", "0d1100", yes},
		{"M.2 Socket 1-SD (Mechanical Key E)", "010802", no}, {"M.2 Socket 2 (Mechanical Key B)", "010802", yes},
		{"M.2 Socket 2 (Mechanical Key B)", "028000", yes}, {"M.2 Socket 2 (Mechanical Key B)", "030000", no},
		{"M.2 Socket 3 (Mechanical Key M)", "010802", yes}, {"M.2 Socket 3 (Mechanical Key M)", "020000", no},
		{"PCI Express Gen 4 SFF-8639 (U.2)", "010802", yes}, {"PCI Express Gen 4 SFF-8639 (U.2)", "020000", no},
		{"EDSFF E1", "020000", no}, {"EDSFF E1", "010802", yes},
		{"MXM 3.0 Type A", "030000", yes}, {"AGP 8X", "010802", no}, {"AGP 8X", "030000", yes},
		{"OCP NIC 3.0 Small Form Factor (SFF)", "020000", yes}, {"OCP NIC Prior to 3.0", "030000", no},
		{"PCI Express Gen 3 x4", "0c0330", yes}, {"PCI", "040300", yes},
		{"Other", "010802", unsure}, {"Unknown", "020000", unsure}, {"Proprietary", "030000", unsure},
		{"I/O Riser Card Slot", "020000", unsure}, {"CXL Flexbus 1.0", "020000", unsure}, {"code 58h", "020000", unsure}, {"", "020000", unsure},
		{"ISA", "020000", no},
	} {
		if got := typeFits(c.slotType, c.class); got != c.want {
			t.Errorf("%q / %s: %d, want %d", c.slotType, c.class, got, c.want)
		}
	}
	for _, c := range []struct {
		typ, class string
		want       bool
	}{
		{"Video", "030000", true}, {"Video", "020000", false}, {"Ethernet", "020000", true}, {"Ethernet", "028000", false},
		{"Wireless LAN", "028000", true}, {"Wireless LAN", "0d1100", true}, {"Sound", "040300", true},
		{"SATA Controller", "010601", true}, {"SATA Controller", "010400", true}, {"SATA Controller", "010185", true},
		{"SAS Controller", "010700", true}, {"SAS Controller", "010400", true}, {"SAS Controller", "010601", false},
		{"SCSI Controller", "010400", true}, {"SCSI Controller", "010000", true}, {"SCSI Controller", "010700", true},
		{"SATA Controller", "010802", false}, {"NVMe Controller", "010802", true},
		{"NVMe Controller", "010601", false}, {"UFS Controller", "010900", true}, {"eMMC", "080501", true},
		{"Other", "020000", true}, {"Unknown", "030000", true}, {"Bluetooth", "0c0330", true}, {"WWAN", "0d1100", true}, {"", "020000", true},
	} {
		if got := onboardFits(c.typ, c.class); got != c.want {
			t.Errorf("type 41 %q / %s: %v, want %v", c.typ, c.class, got, c.want)
		}
	}
}

// What can't be decided without the table says why: root is needed, the
// firmware has no table, or it can't be read; a table without slots.
func TestWithoutTheSlotTable(t *testing.T) {
	lan := []report.PCIDevice{{Address: "0000:00:1f.6", ClassCode: "020000", Label: "LAN", LabelSource: "acpi"}}
	for _, c := range []struct {
		name       string
		err        error
		privileged bool
		reason     string
	}{
		{"no root", syscall.EACCES, false, "needs root (run with --full)"},
		{"no table", syscall.ENOENT, true, "the kernel exposes no SMBIOS table"},
		{"root but unreadable", syscall.EIO, true, "the firmware's slot table can't be read"},
		{"root but refused", syscall.EACCES, true, "the firmware's slot table can't be read"},
	} {
		t.Run(c.name, func(t *testing.T) {
			file, _ := fakeRoot(t)
			file("/sys/firmware/dmi/tables/DMI", "x")
			unreadable = map[string]error{"/sys/firmware/dmi/tables/DMI": c.err}
			col := &collector{r: &report.Report{PCI: append([]report.PCIDevice(nil), lan...)}, privileged: c.privileged}
			col.mountings()
			if m := col.r.PCI[0].Mounting; m.Kind != "unknown" || !strings.Contains(m.Reason, c.reason) {
				t.Errorf("%+v, want %q", m, c.reason)
			}
		})
	}
	col := rootCollector(t, smbiostest.End(), []report.PCIDevice{{Address: "0000:00:1f.6", ClassCode: "020000"}})
	col.mountings()
	if m := col.r.PCI[0].Mounting; m.Kind != "unknown" || !strings.Contains(m.Reason, "lists no expansion slots") {
		t.Errorf("no slots: %+v", m)
	}
}

// The kernel's label marks a device onboard without root only when it
// came from SMBIOS type 41 ("index"); an ACPI _DSM name ("acpi_index")
// says nothing about mounting; a label with neither has no source.
func TestLabelsThroughPCI(t *testing.T) {
	file, link := fakeRoot(t)
	unreadable = map[string]error{"/sys/firmware/dmi/tables/DMI": syscall.EACCES}
	for _, d := range []struct{ addr, label, extra string }{
		{"0000:00:02.0", "Onboard IGD", "index"}, {"0000:00:1f.6", "LAN", "acpi_index"}, {"0000:00:1f.3", "Audio", ""},
	} {
		dir := "/sys/devices/pci0000:00/" + d.addr
		file(dir+"/class", map[string]string{"0000:00:02.0": "0x030000", "0000:00:1f.6": "0x020000", "0000:00:1f.3": "0x040300"}[d.addr])
		file(dir+"/label", d.label+"\n")
		if d.extra != "" {
			file(dir+"/"+d.extra, "1\n")
		}
		link("/sys/bus/pci/devices/"+d.addr, "../../../devices/pci0000:00/"+d.addr)
	}
	c := &collector{r: &report.Report{}}
	c.pci()
	c.mountings()
	got := map[string]string{}
	for _, d := range c.r.PCI {
		got[d.Address] = d.LabelSource + "/" + d.Mounting.Kind
	}
	for addr, w := range map[string]string{"0000:00:02.0": "smbios/onboard", "0000:00:1f.6": "acpi/unknown", "0000:00:1f.3": "/unknown"} {
		if got[addr] != w {
			t.Errorf("%s: %s, want %s", addr, got[addr], w)
		}
	}
	if m := mountingOf(c, "0000:00:02.0"); m.Evidence[0] != `the kernel's label "Onboard IGD" comes from SMBIOS type 41` {
		t.Errorf("evidence %q", m.Evidence)
	}
}

// Memory modules by form factor, SMBIOS's and the SPD's names; LPDDR in a
// DIMM form factor contradicts itself. MMC cards by type.
func TestModuleAndCardMountings(t *testing.T) {
	for _, c := range []struct {
		m        report.MemoryModule
		kind     string
		slot     string
		evidence string
	}{
		{report.MemoryModule{Locator: "DIMM1", FormFactor: "SODIMM", Type: "DDR4"}, "slot", "DIMM1", "SMBIOS type 17 gives form factor SODIMM"},
		{report.MemoryModule{Locator: "SPD 7-0050", FormFactor: "SODIMM"}, "slot", "", "the module's SPD gives form factor SODIMM"},
		{report.MemoryModule{Locator: "SPD 3-0050", FormFactor: "UDIMM"}, "slot", "", ""},
		{report.MemoryModule{Locator: "SPD 3-0051", FormFactor: "CSODIMM"}, "slot", "", ""},
		{report.MemoryModule{Locator: "SPD 3-0052", FormFactor: "CAMM2", Type: "LPDDR5"}, "slot", "", ""},
		{report.MemoryModule{Locator: "DIMM 0", FormFactor: "CAMM"}, "slot", "DIMM 0", ""},
		{report.MemoryModule{Locator: "SPD 3-0053", FormFactor: "Solder down"}, "onboard", "", ""},
		{report.MemoryModule{Locator: "Controller0-ChannelA", FormFactor: "Row of chips"}, "onboard", "", ""},
		{report.MemoryModule{Locator: "DIMM 0", FormFactor: "Chip"}, "onboard", "", ""},
		{report.MemoryModule{Locator: "DIMM 0", FormFactor: "Die"}, "onboard", "", ""},
		{report.MemoryModule{Locator: "DIMM 0", FormFactor: "SODIMM", Type: "LPDDR4"}, "unknown", "", ""},
		{report.MemoryModule{Locator: "DIMM 0", FormFactor: "Row of chips", Type: "LPDDR4"}, "onboard", "", ""},
		{report.MemoryModule{Locator: "DIMM 0", FormFactor: "Chip", Type: "LPDDR5"}, "onboard", "", ""},
		{report.MemoryModule{Locator: "DIMM 0", FormFactor: "CAMM", Type: "LPDDR5"}, "slot", "DIMM 0", ""},
		{report.MemoryModule{Locator: "DIMM 0", FormFactor: ""}, "unknown", "", ""},
	} {
		m := moduleMounting(&c.m)
		if m.Kind != c.kind || m.Slot != c.slot || c.evidence != "" && (len(m.Evidence) != 1 || m.Evidence[0] != c.evidence) {
			t.Errorf("%+v: %+v", c.m, m)
		}
	}
	for typ, w := range map[string][2]string{"SD": {"slot", "high"}, "SDcombo": {"slot", "high"}, "MMC": {"onboard", "medium"}, "SDIO": {"unknown", ""}, "": {"unknown", ""}} {
		m := mmcMounting(typ)
		if m.Kind != w[0] || m.Confidence != w[1] || w[0] != "unknown" && m.Evidence[0] != "the kernel reports card type "+typ {
			t.Errorf("%q: %+v", typ, m)
		}
	}
	file, _ := fakeRoot(t)
	file("/sys/block/mmcblk0/device/type", "MMC\n")
	unreadable = map[string]error{"/sys/firmware/dmi/tables/DMI": syscall.EACCES}
	c := &collector{r: &report.Report{Storage: []report.Disk{{Name: "mmcblk0", Transport: "mmc"}, {Name: "sda", Transport: "usb"}}}}
	c.mountings()
	if c.r.Storage[0].Mounting == nil || c.r.Storage[0].Mounting.Kind != "onboard" || c.r.Storage[1].Mounting != nil {
		t.Errorf("disks %+v %+v", c.r.Storage[0].Mounting, c.r.Storage[1].Mounting)
	}
}

// A wireless controller of class 0d gets a mounting too; a bridge doesn't.
func TestMountingClasses(t *testing.T) {
	col := rootCollector(t, smbiostest.End(), []report.PCIDevice{{Address: "0000:05:00.0", ClassCode: "0d1100"}, port("0000:00:1c.0", 1)})
	col.mountings()
	if mountingOf(col, "0000:05:00.0") == nil || mountingOf(col, "0000:00:1c.0") != nil {
		t.Errorf("%+v", col.r.PCI)
	}
}

// Every memory form factor that names a module, SMBIOS's or the SPD's,
// is in a slot, for DDR5 as for any type that isn't LPDDR.
func TestEverySlottedFormFactor(t *testing.T) {
	n := 0
	for form, kind := range moduleForms {
		if kind != "slot" {
			continue
		}
		n++
		if m := moduleMounting(&report.MemoryModule{Locator: "SPD 1-0050", FormFactor: form, Type: "DDR5"}); m.Kind != "slot" {
			t.Errorf("%s: %+v", form, m)
		}
	}
	if n != 20 {
		t.Errorf("%d slotted form factors, want 20", n)
	}
}

// The cards that share a slot, even only through another card, are one
// group.
func TestGroupCardsIsTransitive(t *testing.T) {
	a, b, c, d := &card{key: "a", slots: []int{1}}, &card{key: "b", slots: []int{2}}, &card{key: "c", slots: []int{1, 2}}, &card{key: "d", slots: []int{3}}
	g := groupCards([]*card{a, b, c, d})
	if len(g) != 2 || len(g[0]) != 3 || len(g[1]) != 1 || g[1][0] != d {
		t.Errorf("groups %v", g)
	}
}

// maxMatching and fillSlots agree with brute force on every bipartite
// graph of up to 3 cards and 3 slots, with each forced pair and each
// skipped card; slot 0 can be taken over by an augmenting path.
func TestMatchingAgainstBruteForce(t *testing.T) {
	if got := maxMatching([]*card{{slots: []int{0, 1}}, {slots: []int{0}}}, nil); got != 2 {
		t.Errorf("slot 0 doesn't augment: %d", got)
	}
	var best func(cards []*card, i int, used map[int]bool, forced map[*card]int, skip *card, must []int) (int, bool)
	// best returns the largest number of cards placed and whether some
	// placement of that size fills every slot in must.
	best = func(cards []*card, i int, used map[int]bool, forced map[*card]int, skip *card, must []int) (int, bool) {
		if i == len(cards) {
			fills := true
			for _, s := range must {
				fills = fills && used[s]
			}
			return 0, fills
		}
		cd := cards[i]
		top, topFills := -1, false
		try := func(n int, fills bool) {
			if n > top || n == top && fills {
				top, topFills = n, fills
			}
		}
		if s, ok := forced[cd]; ok {
			if !used[s] && slices.Contains(cd.slots, s) {
				used[s] = true
				n, f := best(cards, i+1, used, forced, skip, must)
				used[s] = false
				try(n+1, f)
			}
			return top, topFills
		}
		if cd != skip {
			for _, s := range cd.slots {
				if !used[s] {
					used[s] = true
					n, f := best(cards, i+1, used, forced, skip, must)
					used[s] = false
					try(n+1, f)
				}
			}
		}
		n, f := best(cards, i+1, used, forced, skip, must)
		try(n, f)
		return top, topFills
	}
	// fillable says whether some placement (any size) fills every slot.
	var fillable func(cards []*card, i int, used map[int]bool, skip *card, must []int) bool
	fillable = func(cards []*card, i int, used map[int]bool, skip *card, must []int) bool {
		if i == len(cards) {
			for _, s := range must {
				if !used[s] {
					return false
				}
			}
			return true
		}
		cd := cards[i]
		if cd != skip {
			for _, s := range cd.slots {
				if !used[s] {
					used[s] = true
					ok := fillable(cards, i+1, used, skip, must)
					used[s] = false
					if ok {
						return true
					}
				}
			}
		}
		return fillable(cards, i+1, used, skip, must)
	}
	graphs := 0
	for n := 1; n <= 3; n++ {
		total := 1
		for range n {
			total *= 8
		}
		for code := range total {
			cards := make([]*card, n)
			for i := range cards {
				mask := code >> (3 * i) & 7
				cards[i] = &card{}
				for s := range 3 {
					if mask>>s&1 == 1 {
						cards[i].slots = append(cards[i].slots, s)
					}
				}
			}
			graphs++
			if want, _ := best(cards, 0, map[int]bool{}, nil, nil, nil); maxMatching(cards, nil) != want {
				t.Fatalf("%v: matching %d, want %d", slotsOf(cards), maxMatching(cards, nil), want)
			}
			for _, cd := range cards {
				for _, s := range cd.slots {
					forced := map[*card]int{cd: s}
					want, _ := best(cards, 0, map[int]bool{}, forced, nil, nil)
					if got := maxMatching(cards, forced); got != want {
						t.Fatalf("%v forced %v→%d: %d, want %d", slotsOf(cards), cd.slots, s, got, want)
					}
				}
			}
			must := []int{0, 1, 2}
			for _, skip := range append([]*card{nil}, cards...) {
				want := fillable(cards, 0, map[int]bool{}, skip, must)
				if got := fillSlots(must, cards, skip) == len(must); got != want {
					t.Fatalf("%v skip %v: fills %v, want %v", slotsOf(cards), skip, got, want)
				}
			}
		}
	}
	if graphs != 8+64+512 {
		t.Errorf("%d graphs", graphs)
	}
}

func slotsOf(cards []*card) [][]int {
	var out [][]int
	for _, cd := range cards {
		out = append(out, cd.slots)
	}
	return out
}

// A laptop's GPU in type 41, with no type 9 slots at all: its audio
// function goes with it rather than getting "no expansion slots".
func TestFunctionZeroBeforeTheFallbacks(t *testing.T) {
	col := rootCollector(t, append(smbiostest.Onboard("dGPU", 0x80|0x03, 1, 0, 0x01, 0x00), smbiostest.End()...),
		[]report.PCIDevice{port("0000:00:01.0", 16), dev("0000:01:00.0", "030000", "0000:00:01.0", 16), dev("0000:01:00.1", "040300", "0000:00:01.0", 16),
			dev("0000:02:00.0", "020000", "0000:00:1c.0", 1)})
	col.mountings()
	check(t, col, map[string]want{
		"0000:01:00.0": {"onboard", "", "", "high", ""},
		"0000:01:00.1": {"onboard", "", "", "high", ""},
		"0000:02:00.0": {"unknown", "", "", "", "lists no expansion slots"},
	})
}
