package smbios

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/jiegui2025/hwspec/internal/smbios/smbiostest"
)

// The reference machine's records decode as dmidecode printed them, with
// the specification's names (DSP0134 3.8.0) and the firmware's own,
// partly wrong, addresses.
func TestTheReferenceMachinesSlotsSocketAndOnboardDevices(t *testing.T) {
	structs := Parse(smbiostest.EliteDesk800G5Mini())
	if got, want := Processors(structs), []Processor{{Designation: "U3E1", Package: "Socket LGA1151", Mounting: "socket", Populated: true}}; !reflect.DeepEqual(got, want) {
		t.Errorf("processors %+v, want %+v", got, want)
	}
	wantSlots := []Slot{
		{"Slot1 / DGPU PCIEXP", "PCI Express Gen 3 x8", "x8", "Available", "Long Length", 1, "0000:00:01.0"},
		{"Slot2 / M2 WLAN/BT", "PCI Express Gen 3 x1", "x1", "In use", "Other", 2, "0000:00:1c.7"},
		{"Slot3 / M2 SSD", "PCI Express Gen 3 x4", "x4", "In use", "Other", 3, "0000:00:1b.4"},
		{"Slot4 / M2 SSD", "PCI Express Gen 3 x4", "x4", "Available", "Other", 4, "0000:00:1b.0"},
		{"Slot5 / TBT Fiber Combo", "PCI Express Gen 3 x4", "x4", "Available", "Long Length", 5, "0000:00:1d.0"},
	}
	if got := Slots(structs); !reflect.DeepEqual(got, wantSlots) {
		t.Errorf("slots\n got %+v\nwant %+v", got, wantSlots)
	}
	wantOnboard := []Onboard{
		{"Onboard IGD", "Video", true, 1, "0000:00:02.0"},
		{"Onboard Lan", "Ethernet", true, 1, "0000:00:1f.6"},
	}
	if got := OnboardDevices(structs); !reflect.DeepEqual(got, wantOnboard) {
		t.Errorf("onboard\n got %+v\nwant %+v", got, wantOnboard)
	}
}

// table25 is DSP0134 3.8.0 Table 25, transcribed from the specification
// independently of the decoder, with the mounting each name states.
var table25 = map[byte][2]string{
	0x01: {"Other", ""},
	0x02: {"Unknown", ""},
	0x03: {"Daughter Board", ""},
	0x04: {"ZIF Socket", "socket"},
	0x05: {"Replaceable Piggy Back", ""},
	0x06: {"None", ""},
	0x07: {"LIF Socket", "socket"},
	0x08: {"Slot 1", "slot"},
	0x09: {"Slot 2", "slot"},
	0x0A: {"370-pin socket", ""},
	0x0B: {"Slot A", "slot"},
	0x0C: {"Slot M", "slot"},
	0x0D: {"Socket 423", ""},
	0x0E: {"Socket A (Socket 462)", ""},
	0x0F: {"Socket 478", ""},
	0x10: {"Socket 754", ""},
	0x11: {"Socket 940", ""},
	0x12: {"Socket 939", ""},
	0x13: {"Socket mPGA604", "socket"},
	0x14: {"Socket LGA771", "socket"},
	0x15: {"Socket LGA775", "socket"},
	0x16: {"Socket S1", ""},
	0x17: {"Socket AM2", ""},
	0x18: {"Socket F (1207)", ""},
	0x19: {"Socket LGA1366", "socket"},
	0x1A: {"Socket G34", ""},
	0x1B: {"Socket AM3", ""},
	0x1C: {"Socket C32", ""},
	0x1D: {"Socket LGA1156", "socket"},
	0x1E: {"Socket LGA1567", "socket"},
	0x1F: {"Socket PGA988A", "socket"},
	0x20: {"Socket BGA1288", "onboard"},
	0x21: {"Socket rPGA988B", "socket"},
	0x22: {"Socket BGA1023", "onboard"},
	0x23: {"Socket BGA1224", "onboard"},
	0x24: {"Socket LGA1155", "socket"},
	0x25: {"Socket LGA1356", "socket"},
	0x26: {"Socket LGA2011", "socket"},
	0x27: {"Socket FS1", ""},
	0x28: {"Socket FS2", ""},
	0x29: {"Socket FM1", ""},
	0x2A: {"Socket FM2", ""},
	0x2B: {"Socket LGA2011-3", "socket"},
	0x2C: {"Socket LGA1356-3", "socket"},
	0x2D: {"Socket LGA1150", "socket"},
	0x2E: {"Socket BGA1168", "onboard"},
	0x2F: {"Socket BGA1234", "onboard"},
	0x30: {"Socket BGA1364", "onboard"},
	0x31: {"Socket AM4", ""},
	0x32: {"Socket LGA1151", "socket"},
	0x33: {"Socket BGA1356", "onboard"},
	0x34: {"Socket BGA1440", "onboard"},
	0x35: {"Socket BGA1515", "onboard"},
	0x36: {"Socket LGA3647-1", "socket"},
	0x37: {"Socket SP3", ""},
	0x38: {"Socket SP3r2", ""},
	0x39: {"Socket LGA2066", "socket"},
	0x3A: {"Socket BGA1392", "onboard"},
	0x3B: {"Socket BGA1510", "onboard"},
	0x3C: {"Socket BGA1528", "onboard"},
	0x3D: {"Socket LGA4189", "socket"},
	0x3E: {"Socket LGA1200", "socket"},
	0x3F: {"Socket LGA4677", "socket"},
	0x40: {"Socket LGA1700", "socket"},
	0x41: {"Socket BGA1744", "onboard"},
	0x42: {"Socket BGA1781", "onboard"},
	0x43: {"Socket BGA1211", "onboard"},
	0x44: {"Socket BGA2422", "onboard"},
	0x45: {"Socket LGA1211", "socket"},
	0x46: {"Socket LGA2422", "socket"},
	0x47: {"Socket LGA5773", "socket"},
	0x48: {"Socket BGA5773", "onboard"},
	0x49: {"Socket AM5", ""},
	0x4A: {"Socket SP5", ""},
	0x4B: {"Socket SP6", ""},
	0x4C: {"Socket BGA883", "onboard"},
	0x4D: {"Socket BGA1190", "onboard"},
	0x4E: {"Socket BGA4129", "onboard"},
	0x4F: {"Socket LGA4710", "socket"},
	0x50: {"Socket LGA7529", "socket"},
	0x51: {"Socket BGA1964", "onboard"},
	0x52: {"Socket BGA1792", "onboard"},
	0x53: {"Socket BGA2049", "onboard"},
	0x54: {"Socket BGA2551", "onboard"},
	0x55: {"Socket LGA1851", "socket"},
	0x56: {"Socket BGA2114", "onboard"},
	0x57: {"Socket BGA2833", "onboard"},
}

// Every Table 25 code decodes to the specification's name and the
// mounting its name states; codes outside the table give nothing.
// 24h is "Socket LGA1155" in the specification (dmidecode prints
// BGA1155).
func TestEveryProcessorUpgradeCode(t *testing.T) {
	if len(table25) != 87 || len(processorUpgrades) != 87 {
		t.Fatalf("Table 25 has 87 codes: test %d, decoder %d", len(table25), len(processorUpgrades))
	}
	for code := range 0xFF {
		want, ok := table25[byte(code)]
		if !ok && code != 0 {
			want[0] = fmt.Sprintf("code %02Xh", code)
		}
		got := Processors(Parse(append(smbiostest.Processor("CPU0", 0x41, byte(code)), smbiostest.End()...)))
		if len(got) != 1 || got[0].Package != want[0] || got[0].Mounting != want[1] {
			t.Errorf("upgrade %#02x: %+v, want %q %q", code, got, want[0], want[1])
		}
	}
}

// FFh means "no other valid enumeration": the Socket Type string at 32h
// (SMBIOS 3.8+) names the package, and it is classed the same way.
func TestSocketTypeStringForFFh(t *testing.T) {
	for _, c := range []struct {
		socket, want string
		length       int
	}{{"Socket BGA2049", "onboard", 0x34}, {"Socket LGA9999", "socket", 0x34}, {"Socket XYZ", "", 0x34}, {"", "", 0x30}} {
		st := smbiostest.Structure(4, c.length, func(f []byte) {
			f[0x04], f[0x05], f[0x18], f[0x19] = 1, 0x03, 0x41, 0xFF
			if c.length > 0x32 {
				f[0x32] = 2
			}
		}, "CPU0", c.socket)
		got := Processors(Parse(append(st, smbiostest.End()...)))
		if len(got) != 1 || got[0].Package != c.socket || got[0].Mounting != c.want {
			t.Errorf("FFh with %q (length %#x): %+v, want %q", c.socket, c.length, got, c.want)
		}
	}
}

// A processor that isn't a central processor (a math coprocessor, say)
// and an unpopulated socket are told apart.
func TestOnlyCentralProcessorsAndTheirStatus(t *testing.T) {
	cop := smbiostest.Structure(4, 0x30, func(f []byte) { f[0x04], f[0x05], f[0x19] = 1, 0x04, 0x32 }, "FPU")
	empty := smbiostest.Processor("CPU1", 0x00, 0x32)
	disabled := smbiostest.Processor("CPU2", 0x02, 0x32) // bit 6 clear: not populated, whatever the status bits say
	onlyBit6 := smbiostest.Processor("CPU3", 0x40, 0x32)
	enabled := smbiostest.Processor("CPU4", 0x01, 0x32)
	got := Processors(Parse(append(append(append(append(append(cop, empty...), disabled...), onlyBit6...), enabled...), smbiostest.End()...)))
	var populated []bool
	for _, p := range got {
		populated = append(populated, p.Populated)
	}
	if len(got) != 4 || got[0].Designation != "CPU1" || !reflect.DeepEqual(populated, []bool{false, false, true, false}) {
		t.Errorf("processors %+v, want CPU1–CPU4 populated false, false, true, false", got)
	}
}

// Firmware that gives no bus address writes 0FFh (DSP0134 7.10.8,
// 7.42.4); structures from before SMBIOS 2.6 are too short to hold one.
func TestMissingAndShortAddresses(t *testing.T) {
	none := smbiostest.Slot("SATA1", 0x02, 0x02, 0x04, 0x02, 0, 0xFFFF, 0xFF, 0xFF)
	old := smbiostest.Structure(9, 0x0D, func(f []byte) { f[0x04], f[0x05] = 1, 0xA6 }, "PCIE1")
	dev := smbiostest.Onboard("Audio", 0x07, 1, 0xFFFF, 0xFF, 0xFF) // disabled
	structs := Parse(append(append(append(none, old...), dev...), smbiostest.End()...))
	slots := Slots(structs)
	if len(slots) != 2 || slots[0].Address != "" || slots[1].Address != "" || slots[1].Type != "PCI Express x1" {
		t.Errorf("slots %+v", slots)
	}
	if d := OnboardDevices(structs); len(d) != 1 || d[0].Address != "" || d[0].Enabled || d[0].Type != "Sound" {
		t.Errorf("onboard %+v", d)
	}
}

// A non-zero segment (a second PCI segment group) is kept in the
// address; bus FFh is a real bus when a device/function is given, and
// device/function FFh with a real bus is device 1fh, function 7.
func TestSegmentGroupAndBusFFInTheAddress(t *testing.T) {
	t1 := smbiostest.Slot("NVMe0", 0xBB, 0x0A, 0x04, 0x01, 0, 1, 0x3a, 0x02<<3|1)
	t2 := smbiostest.Slot("NVMe1", 0xBB, 0x0A, 0x04, 0x01, 0, 0, 0xFF, 0x01<<3)
	t3 := smbiostest.Slot("NVMe2", 0xBB, 0x0A, 0x04, 0x01, 0, 0, 0x00, 0xFF) // device/function FFh alone is 1f.7
	s := Slots(Parse(append(append(append(t1, t2...), t3...), smbiostest.End()...)))
	if len(s) != 3 || s[0].Address != "0001:3a:02.1" || s[1].Address != "0000:ff:01.0" || s[2].Address != "0000:00:1f.7" {
		t.Errorf("slots %+v", s)
	}
}

// Each field is read only when the structure is long enough to hold it:
// a structure that ends just before a field leaves it out, one that ends
// just after it has it.
func TestFieldsAtTheEndOfShortStructures(t *testing.T) {
	cpu := func(length int) Processor {
		st := smbiostest.Structure(4, length, func(f []byte) {
			f[0x04] = 1
			for off, v := range map[int]byte{0x05: 0x03, 0x18: 0x41, 0x19: 0x32} {
				if off < length {
					f[off] = v
				}
			}
		}, "CPU0")
		got := Processors(Parse(append(st, smbiostest.End()...)))
		if len(got) == 0 {
			return Processor{}
		}
		return got[0]
	}
	if p := cpu(0x05); p != (Processor{}) {
		t.Errorf("length 05h (no processor type): %+v, want no processor", p)
	}
	if p := cpu(0x18); p != (Processor{Designation: "CPU0"}) {
		t.Errorf("length 18h (no status): %+v", p)
	}
	if p := cpu(0x19); p != (Processor{Designation: "CPU0", Populated: true}) {
		t.Errorf("length 19h (no upgrade): %+v", p)
	}
	if p := cpu(0x1A); p != (Processor{Designation: "CPU0", Package: "Socket LGA1151", Mounting: "socket", Populated: true}) {
		t.Errorf("length 1Ah: %+v", p)
	}

	slot := func(length int) Slot {
		st := smbiostest.Structure(9, length, func(f []byte) {
			f[0x04] = 1
			for off, v := range map[int]byte{0x05: 0xB4, 0x06: 0x0A, 0x07: 0x04, 0x08: 0x04, 0x09: 3, 0x0D: 0, 0x0F: 0x01, 0x10: 0x08} {
				if off < length {
					f[off] = v
				}
			}
		}, "M2")
		return Slots(Parse(append(st, smbiostest.End()...)))[0]
	}
	if s := slot(0x08); s.Usage != "In use" || s.Length != "" {
		t.Errorf("length 08h: %+v", s)
	}
	if s := slot(0x09); s.Length != "Long Length" || s.ID != 0 {
		t.Errorf("length 09h: %+v", s)
	}
	if s := slot(0x10); s.Address != "" {
		t.Errorf("length 10h (no device/function): %+v", s)
	}
	if s := slot(0x11); s.Address != "0000:01:01.0" {
		t.Errorf("length 11h: %+v", s)
	}

	onboard := func(length int) Onboard {
		st := smbiostest.Structure(41, length, func(f []byte) {
			f[0x04] = 1
			for off, v := range map[int]byte{0x05: 0x85, 0x06: 2, 0x09: 0x00, 0x0A: 0xFE} {
				if off < length {
					f[off] = v
				}
			}
		}, "LAN")
		return OnboardDevices(Parse(append(st, smbiostest.End()...)))[0]
	}
	if d := onboard(0x06); d.Type != "Ethernet" || d.Instance != 0 {
		t.Errorf("length 06h: %+v", d)
	}
	if d := onboard(0x0A); d.Instance != 2 || d.Address != "" {
		t.Errorf("length 0Ah (no device/function): %+v", d)
	}
	if d := onboard(0x0B); d.Address != "0000:00:1f.6" {
		t.Errorf("length 0Bh: %+v", d)
	}

	// A string index past the end of the structure is no string.
	short := smbiostest.Structure(9, 0x04, func([]byte) {}, "x")
	if s := Slots(Parse(append(short, smbiostest.End()...))); len(s) != 1 || s[0] != (Slot{}) {
		t.Errorf("length 04h: %+v", s)
	}
}

// The codes the reference machine doesn't use decode too, and a code a
// table doesn't have is kept, not dropped.
func TestOtherSlotAndOnboardCodes(t *testing.T) {
	slot := func(typ, width, usage, length byte) Slot {
		return Slots(Parse(append(smbiostest.Slot("S", typ, width, usage, length, 0, 0, 0, 0), smbiostest.End()...)))[0]
	}
	for _, c := range []struct {
		typ, width, usage, length byte
		want                      [4]string
	}{
		{0x14, 0x0D, 0x05, 0x05, [4]string{"M.2 Socket 1-DP (Mechanical Key A)", "x16", "Unavailable", `2.5" drive form factor`}},
		{0x15, 0x08, 0x03, 0x06, [4]string{"M.2 Socket 1-SD (Mechanical Key E)", "x1", "Available", `3.5" drive form factor`}},
		{0x16, 0x09, 0x04, 0x03, [4]string{"M.2 Socket 2 (Mechanical Key B)", "x2", "In use", "Short Length"}},
		{0x17, 0x0A, 0x04, 0x04, [4]string{"M.2 Socket 3 (Mechanical Key M)", "x4", "In use", "Long Length"}},
		{0x1D, 0x0D, 0x04, 0x04, [4]string{"MXM 3.0 Type A", "x16", "In use", "Long Length"}},
		{0x58, 0x0F, 0x06, 0x07, [4]string{"code 58h", "code 0Fh", "code 06h", "code 07h"}},
	} {
		s := slot(c.typ, c.width, c.usage, c.length)
		if got := [4]string{s.Type, s.Width, s.Usage, s.Length}; got != c.want {
			t.Errorf("slot %#02x: %q, want %q", c.typ, got, c.want)
		}
	}
	for code, want := range map[byte]string{0x0E: "eMMC", 0x0F: "NVMe Controller", 0x10: "UFS Controller", 0x0B: "Wireless LAN", 0x11: "code 11h"} {
		d := OnboardDevices(Parse(append(smbiostest.Onboard("D", 0x80|code, 1, 0, 0, 0), smbiostest.End()...)))
		if len(d) != 1 || d[0].Type != want || !d[0].Enabled {
			t.Errorf("onboard %#02x: %+v, want %q", code, d, want)
		}
	}
}
