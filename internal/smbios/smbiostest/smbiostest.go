// Package smbiostest builds SMBIOS tables for tests.
package smbiostest

import "encoding/binary"

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
