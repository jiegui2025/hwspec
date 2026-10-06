package smbios

import (
	"encoding/binary"
	"testing"

	"github.com/jiegui2025/hwspec/internal/smbios/smbiostest"
)

var structure = smbiostest.Structure

func testTable() []byte {
	var t []byte
	// Type 16: 2 slots, 64 GiB max, no ECC. Handle 0x1000.
	t = append(t, structure(16, 0x17, func(f []byte) {
		binary.LittleEndian.PutUint16(f[0x02:], 0x1000)
		f[0x05] = 0x03
		f[0x06] = 0x03
		binary.LittleEndian.PutUint32(f[0x07:], 64<<20) // KiB
		binary.LittleEndian.PutUint16(f[0x0D:], 2)
	})...)
	// Type 17: 16 GiB DDR4 SODIMM.
	t = append(t, structure(17, 0x28, func(f []byte) {
		binary.LittleEndian.PutUint16(f[0x04:], 0x1000)
		binary.LittleEndian.PutUint16(f[0x08:], 64)
		binary.LittleEndian.PutUint16(f[0x0A:], 64)
		binary.LittleEndian.PutUint16(f[0x0C:], 16384) // MiB
		f[0x0E] = 0x0D
		f[0x10], f[0x11] = 1, 2
		f[0x12] = 0x1A
		binary.LittleEndian.PutUint16(f[0x15:], 2667)
		f[0x17], f[0x18], f[0x1A] = 3, 4, 5
		f[0x1B] = 2
		binary.LittleEndian.PutUint16(f[0x20:], 2400)
		binary.LittleEndian.PutUint16(f[0x26:], 1200)
	}, "DIMM 1", "BANK 0", "Samsung", "12345678", "M471A2K43DB1-CTD  ")...)
	// Type 17: empty slot with placeholder strings.
	t = append(t, structure(17, 0x28, func(f []byte) {
		binary.LittleEndian.PutUint16(f[0x04:], 0x1000)
		f[0x10], f[0x17] = 1, 2
	}, "DIMM 2", "Not Specified")...)
	// A video-memory array (use 0x04) with a 16 MiB chip: not system RAM.
	t = append(t, structure(16, 0x17, func(f []byte) {
		binary.LittleEndian.PutUint16(f[0x02:], 0x2000)
		f[0x05] = 0x04
	})...)
	t = append(t, structure(17, 0x28, func(f []byte) {
		binary.LittleEndian.PutUint16(f[0x04:], 0x2000)
		binary.LittleEndian.PutUint16(f[0x0C:], 16)
		f[0x10] = 1
	}, "VRAM")...)
	t = append(t, structure(127, 4, func([]byte) {})...)
	return t
}

func TestMemory(t *testing.T) {
	structs := Parse(testTable())
	if len(structs) != 6 {
		t.Fatalf("parsed %d structures, want 6", len(structs))
	}

	arrays := MemoryArrays(structs)
	if len(arrays) != 1 || arrays[0].Slots != 2 || arrays[0].MaxCapacityBytes != 64<<30 || arrays[0].ErrorCorrection != "None" {
		t.Errorf("arrays = %+v", arrays)
	}

	devs := MemoryDevices(structs)
	if len(devs) != 2 {
		t.Fatalf("got %d devices, want 2", len(devs))
	}
	want := MemoryDevice{
		Locator: "DIMM 1", BankLocator: "BANK 0", SizeBytes: 16 << 30,
		FormFactor: "SODIMM", Type: "DDR4", SpeedMTs: 2667, ConfiguredMTs: 2400,
		Manufacturer: "Samsung", Serial: "12345678", PartNumber: "M471A2K43DB1-CTD",
		TotalWidth: 64, DataWidth: 64, Rank: 2, ConfiguredMV: 1200,
	}
	if devs[0].Installed == nil || !*devs[0].Installed {
		t.Errorf("device 0 not installed: %+v", devs[0].Installed)
	}
	devs[0].Installed = nil
	if devs[0] != want {
		t.Errorf("device 0:\n got %+v\nwant %+v", devs[0], want)
	}
	if devs[1].SizeBytes != 0 || devs[1].Manufacturer != "" || devs[1].Locator != "DIMM 2" || devs[1].Installed == nil || *devs[1].Installed {
		t.Errorf("empty slot = %+v", devs[1])
	}
}

// Firmware that omits type 16 arrays still lists its modules.
func TestMemoryDevicesWithoutArrays(t *testing.T) {
	var table []byte
	table = append(table, structure(17, 0x28, func(f []byte) {
		binary.LittleEndian.PutUint16(f[0x0C:], 8192)
		f[0x10] = 1
	}, "SODIMM0")...)
	table = append(table, structure(127, 4, func([]byte) {})...)
	devs := MemoryDevices(Parse(table))
	if len(devs) != 1 || devs[0].SizeBytes != 8<<30 {
		t.Errorf("devices = %+v, want one 8 GiB module", devs)
	}
}

func TestParseTruncated(t *testing.T) {
	table := testTable()
	for n := range table {
		Parse(table[:n]) // must not panic
		MemoryDevices(Parse(table[:n]))
	}
}

func TestChassisType(t *testing.T) {
	for n, want := range map[int]string{3: "Desktop", 10: "Notebook", 0x23: "Mini PC", 0x89: "Laptop", 0: "", 99: ""} {
		if got := ChassisType(n); got != want {
			t.Errorf("ChassisType(%d) = %q, want %q", n, got, want)
		}
	}
}

// Firmware from older SMBIOS versions writes shorter structures, and large
// values move to extended fields: both are read correctly.
func TestShortStructuresAndExtendedFields(t *testing.T) {
	var table []byte
	// A 2.1-era array: too short for the extended capacity it points to.
	table = append(table, structure(16, 0x0F, func(f []byte) {
		binary.LittleEndian.PutUint16(f[0x02:], 0x1000)
		f[0x05], f[0x06] = 0x03, 0x05
		binary.LittleEndian.PutUint32(f[0x07:], 0x80000000)
	})...)
	// A 2.7+ array with a 4 TiB extended maximum.
	table = append(table, structure(16, 0x17, func(f []byte) {
		binary.LittleEndian.PutUint16(f[0x02:], 0x1001)
		f[0x05] = 0x03
		binary.LittleEndian.PutUint32(f[0x07:], 0x80000000)
		binary.LittleEndian.PutUint64(f[0x0F:], 4<<40)
	})...)
	// 64 GiB through the extended size, 8 MiB in KiB units, extended speeds.
	table = append(table, structure(17, 0x5C, func(f []byte) {
		binary.LittleEndian.PutUint16(f[0x04:], 0x1001)
		binary.LittleEndian.PutUint16(f[0x0C:], 0x7FFF)
		binary.LittleEndian.PutUint32(f[0x1C:], 64<<10)
		binary.LittleEndian.PutUint16(f[0x15:], 0xFFFF)
		binary.LittleEndian.PutUint32(f[0x54:], 70000)
		binary.LittleEndian.PutUint16(f[0x20:], 0xFFFF)
		binary.LittleEndian.PutUint32(f[0x58:], 68000)
		f[0x10], f[0x18] = 1, 2
	}, "DIMM 1", "00000000")...)
	table = append(table, structure(17, 0x1C, func(f []byte) {
		binary.LittleEndian.PutUint16(f[0x04:], 0x1001)
		binary.LittleEndian.PutUint16(f[0x0C:], 0x8000|8192) // 8192 KiB (KiB units cover small chips)
		binary.LittleEndian.PutUint16(f[0x15:], 0xFFFF)      // extended speed field absent
		f[0x10], f[0x17] = 1, 9                              // string 9 doesn't exist
	}, "DIMM 2")...)
	// A truncated memory device: just the header and handle.
	table = append(table, structure(17, 0x06, func(f []byte) {
		binary.LittleEndian.PutUint16(f[0x04:], 0x1001)
	})...)
	table = append(table, structure(127, 4, func([]byte) {})...)

	structs := Parse(table)
	arrays := MemoryArrays(structs)
	if len(arrays) != 2 || arrays[0].MaxCapacityBytes != 0 || arrays[0].ErrorCorrection != "Single-bit ECC" || arrays[1].MaxCapacityBytes != 4<<40 {
		t.Errorf("arrays = %+v", arrays)
	}
	devs := MemoryDevices(structs)
	if len(devs) != 3 {
		t.Fatalf("devices = %+v", devs)
	}
	if d := devs[0]; d.SizeBytes != 64<<30 || d.SpeedMTs != 70000 || d.ConfiguredMTs != 68000 || d.Serial != "" {
		t.Errorf("extended = %+v", d)
	}
	if d := devs[1]; d.SizeBytes != 8<<20 || d.SpeedMTs != 0 || d.Manufacturer != "" || d.Locator != "DIMM 2" {
		t.Errorf("KiB units = %+v", d)
	}
	if d := devs[2]; d != (MemoryDevice{}) {
		t.Errorf("truncated = %+v", d)
	}
}

func TestChassisTypeIgnoresTheLockBit(t *testing.T) {
	for n, want := range map[int]string{0x8A: "Notebook", 0x0A: "Notebook", 0: "", 0x7F: "", 35: "Mini PC"} {
		if got := ChassisType(n); got != want {
			t.Errorf("ChassisType(%#x) = %q, want %q", n, got, want)
		}
	}
}

// A table that ends early or is malformed yields what was complete.
func TestTruncatedTables(t *testing.T) {
	full := append(structure(1, 0x08, func([]byte) {}, "Vendor"), structure(127, 4, func([]byte) {})...)
	for name, table := range map[string][]byte{
		"empty": nil, "header only": full[:3], "bad length": {1, 2, 0, 0}, "length past end": {1, 0x40, 0, 0},
		"no string terminator": full[:0x08+3],
	} {
		if got := Parse(table); len(got) != 0 {
			t.Errorf("%s: %d structures", name, len(got))
		}
	}
	if got := Parse(full); len(got) != 2 || got[0].Strings[0] != "Vendor" {
		t.Errorf("full = %+v", got)
	}
}

// Firmware placeholders mean "unknown" in any case and spacing; real names
// that merely contain one are kept.
func TestCleanDropsPlaceholders(t *testing.T) {
	for _, s := range []string{
		"Not Specified", "To Be Filled By O.E.M.", "Default string", "0000000", "\xff\xff",
		"System manufacturer", "System Product Name", "System Version", "System Serial Number", "SKU",
		"SYSTEM MANUFACTURER", "  system product name\t", " sku ",
	} {
		if got := Clean(s); got != "" {
			t.Errorf("Clean(%q) = %q, want \"\"", s, got)
		}
	}
	for _, s := range []string{"System76", "System Product Name 2", "SKU1234", "Thelio", "ASUS", "ROG STRIX X570-E GAMING WIFI II"} {
		if got := Clean(s); got != s {
			t.Errorf("Clean(%q) = %q, want it kept", s, got)
		}
	}
}

// Size 0 is an empty socket; FFFFh, and 7FFFh without the Extended Size
// field, are a module of unknown size; a structure too short for Size
// doesn't say (DSP0134 7.18, Size).
func TestInstalledFromTheSizeField(t *testing.T) {
	dev := func(length int, size uint16) []byte {
		return structure(17, length, func(f []byte) {
			if length > 0x10 {
				f[0x10] = 1
			}
			if length >= 0x0E {
				binary.LittleEndian.PutUint16(f[0x0C:], size)
			}
		}, "DIMM")
	}
	for _, c := range []struct {
		name   string
		table  []byte
		want   string // "true", "false" or "nil"
		sizeGB uint64
	}{
		{"empty", dev(0x28, 0), "false", 0},
		{"8 GiB", dev(0x28, 8192), "true", 8},
		{"size unknown", dev(0x28, 0xFFFF), "true", 0},
		{"extended size missing", dev(0x1C, 0x7FFF), "true", 0},
		{"too short for Size", dev(0x0C, 0), "nil", 0},
	} {
		got := MemoryDevices(Parse(append(c.table, 127, 4, 0, 0, 0, 0)))
		if len(got) != 1 {
			t.Fatalf("%s: %d devices", c.name, len(got))
		}
		installed := "nil"
		if got[0].Installed != nil {
			installed = map[bool]string{true: "true", false: "false"}[*got[0].Installed]
		}
		if installed != c.want || got[0].SizeBytes != c.sizeGB<<30 {
			t.Errorf("%s: installed %s, size %d; want %s, %d GiB", c.name, installed, got[0].SizeBytes, c.want, c.sizeGB)
		}
	}
}
