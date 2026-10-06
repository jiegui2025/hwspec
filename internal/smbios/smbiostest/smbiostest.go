// Package smbiostest builds SMBIOS tables for tests.
package smbiostest

import (
	"encoding/binary"
	"strconv"
	"strings"
)

// Structure builds one SMBIOS structure: a formatted area of length bytes
// (type and length filled in, the rest set by set) followed by its strings.
func Structure(typ byte, length int, set func(f []byte), strs ...string) []byte {
	f := make([]byte, length)
	f[0], f[1] = typ, byte(length)
	set(f)
	out := f
	for _, s := range strs {
		out = append(out, s...)
		out = append(out, 0)
	}
	if len(strs) == 0 {
		out = append(out, 0)
	}
	return append(out, 0)
}

// End is the end-of-table structure (type 127).
func End() []byte { return Structure(127, 4, func([]byte) {}) }

// MemoryArray is a type 16 system-memory array with the given handle.
func MemoryArray(handle uint16, maxKiB uint32, slots uint16, ecc byte) []byte {
	return Structure(16, 0x17, func(f []byte) {
		binary.LittleEndian.PutUint16(f[0x02:], handle)
		f[0x05], f[0x06] = 0x03, ecc
		binary.LittleEndian.PutUint32(f[0x07:], maxKiB)
		binary.LittleEndian.PutUint16(f[0x0D:], slots)
	})
}

// Module describes a type 17 memory device.
type Module struct {
	Array                                     uint16 // parent array handle
	SizeMiB                                   uint16 // 0 = empty slot
	Locator, Bank, Manufacturer, Serial, Part string
	SpeedMTs                                  uint16
	FormFactor, Type                          byte // e.g. 0x0D SODIMM, 0x1A DDR4
}

// MemoryDevice builds a type 17 structure for m. Empty strings get index
// 0 ("none"), as in real tables: an empty entry would end the string list.
func MemoryDevice(m Module) []byte {
	var strs []string
	index := func(s string) byte {
		if s == "" {
			return 0
		}
		strs = append(strs, s)
		return byte(len(strs))
	}
	loc, bank, mfr, serial, part := index(m.Locator), index(m.Bank), index(m.Manufacturer), index(m.Serial), index(m.Part)
	return Structure(17, 0x28, func(f []byte) {
		binary.LittleEndian.PutUint16(f[0x04:], m.Array)
		binary.LittleEndian.PutUint16(f[0x08:], 64)
		binary.LittleEndian.PutUint16(f[0x0A:], 64)
		binary.LittleEndian.PutUint16(f[0x0C:], m.SizeMiB)
		f[0x0E], f[0x12] = m.FormFactor, m.Type
		f[0x10], f[0x11], f[0x17], f[0x18], f[0x1A] = loc, bank, mfr, serial, part
		binary.LittleEndian.PutUint16(f[0x15:], m.SpeedMTs)
		f[0x1B] = 1
	}, strs...)
}

// Processor builds a type 4 central-processor structure (SMBIOS 3.x
// length) with the given socket designation, status byte (0x41 =
// populated, enabled) and Processor Upgrade code.
func Processor(designation string, status, upgrade byte) []byte {
	return Structure(4, 0x30, func(f []byte) {
		f[0x04], f[0x05], f[0x18], f[0x19] = 1, 0x03, status, upgrade
	}, designation)
}

// Slot builds a type 9 structure (SMBIOS 2.6 length, 17 bytes) with the
// given segment, bus and device/function bytes.
func Slot(designation string, typ, width, usage, length byte, id, segment uint16, bus, devfn byte) []byte {
	return Structure(9, 0x11, func(f []byte) {
		f[0x04], f[0x05], f[0x06], f[0x07], f[0x08] = 1, typ, width, usage, length
		binary.LittleEndian.PutUint16(f[0x09:], id)
		binary.LittleEndian.PutUint16(f[0x0D:], segment)
		f[0x0F], f[0x10] = bus, devfn
	}, designation)
}

// Onboard builds a type 41 structure; typ includes bit 7 (enabled).
func Onboard(designation string, typ, instance byte, segment uint16, bus, devfn byte) []byte {
	return Structure(41, 0x0B, func(f []byte) {
		f[0x04], f[0x05], f[0x06] = 1, typ, instance
		binary.LittleEndian.PutUint16(f[0x07:], segment)
		f[0x09], f[0x0A] = bus, devfn
	}, designation)
}

// EliteDesk800G5Mini is the reference machine's types 4, 9 and 41,
// byte for byte as `dmidecode -u -t 4 -t 9 -t 41` dumped them on
// 2026-10-06 (BIOS R21 02.27.00, SMBIOS 3.1). Its slot addresses name
// root ports that don't exist (00:1b.4, 00:1c.7), as the firmware wrote
// them. Type 4's serial, asset tag and part number strings are the
// firmware's placeholder "To Be Filled By O.E.M.".
func EliteDesk800G5Mini() []byte {
	var t []byte
	add := func(hexdump string, strs ...string) {
		for f := range strings.FieldsSeq(hexdump) {
			b, err := strconv.ParseUint(f, 16, 8)
			if err != nil {
				panic(err)
			}
			t = append(t, byte(b))
		}
		for _, s := range strs {
			t = append(append(t, s...), 0)
		}
		t = append(t, 0)
	}
	const oem = "To Be Filled By O.E.M."
	add(`04 30 17 00 01 03 CD 02 EA 06 09 00 FF FB EB BF
		03 88 64 00 98 08 34 08 41 32 14 00 15 00 16 00
		04 05 06 06 06 06 EC 00 CD 00 06 00 06 00 06 00`,
		"U3E1", "Intel(R) Corporation", "Intel(R) Core(TM) i5-9500T CPU @ 2.20GHz", oem, oem, oem)
	add(`09 11 2F 00 01 B5 0B 03 04 01 00 04 05 00 00 00 08`, "Slot1 / DGPU PCIEXP")
	add(`09 11 30 00 01 B2 08 04 01 02 00 04 05 00 00 00 E7`, "Slot2 / M2 WLAN/BT")
	add(`09 11 31 00 01 B4 0A 04 01 03 00 04 05 00 00 00 DC`, "Slot3 / M2 SSD")
	add(`09 11 32 00 01 B4 0A 03 01 04 00 04 05 00 00 00 D8`, "Slot4 / M2 SSD")
	add(`09 11 33 00 01 B4 0A 03 04 05 00 04 05 00 00 00 E8`, "Slot5 / TBT Fiber Combo")
	add(`29 0B 35 00 01 83 01 00 00 00 10`, "Onboard IGD")
	add(`29 0B 36 00 01 85 01 00 00 00 FE`, "Onboard Lan")
	return append(t, End()...)
}
