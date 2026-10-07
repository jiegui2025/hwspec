// Package smbios parses the raw SMBIOS table the kernel exposes at
// /sys/firmware/dmi/tables/DMI (root only). It decodes what sysfs doesn't
// expose any other way: memory (types 16 and 17), processor sockets (4),
// expansion slots (9) and onboard devices (41).
package smbios

import (
	"bytes"
	"encoding/binary"
	"slices"
	"strings"
)

type Structure struct {
	Type      byte
	Formatted []byte // includes the 4-byte header
	Strings   []string
}

// str returns the string referenced by the byte at off (1-based index; 0 = none).
func (s *Structure) str(off int) string {
	if off >= len(s.Formatted) {
		return ""
	}
	i := int(s.Formatted[off])
	if i == 0 || i > len(s.Strings) {
		return ""
	}
	return Clean(s.Strings[i-1])
}

func (s *Structure) u8(off int) (byte, bool) {
	if off >= len(s.Formatted) {
		return 0, false
	}
	return s.Formatted[off], true
}

func (s *Structure) u16(off int) (uint16, bool) {
	if off+2 > len(s.Formatted) {
		return 0, false
	}
	return binary.LittleEndian.Uint16(s.Formatted[off:]), true
}

func (s *Structure) u32(off int) (uint32, bool) {
	if off+4 > len(s.Formatted) {
		return 0, false
	}
	return binary.LittleEndian.Uint32(s.Formatted[off:]), true
}

func (s *Structure) u64(off int) (uint64, bool) {
	if off+8 > len(s.Formatted) {
		return 0, false
	}
	return binary.LittleEndian.Uint64(s.Formatted[off:]), true
}

var placeholders = []string{
	"not specified", "unknown", "to be filled by o.e.m.", "default string",
	"none", "no dimm", "not available", "undefined",
	// ASUS and AMI firmware defaults: the kernel logs "Hardware name:
	// System manufacturer System Product Name/<board>" on such machines.
	"system manufacturer", "system product name", "system version",
	"system serial number", "sku",
}

func Clean(s string) string {
	s = strings.TrimSpace(s)
	l := strings.ToLower(s)
	if slices.Contains(placeholders, l) {
		return ""
	}
	// Some firmware fills unset serials/part numbers with '0', 0x00 or 0xFF.
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '0', 0x00, 0xFF, ' ':
		default:
			return s
		}
	}
	return ""
}

// Parse splits the table into structures. It stops at the end-of-table
// marker (type 127) or at the first malformed structure.
func Parse(table []byte) []Structure {
	var out []Structure
	for len(table) >= 4 {
		typ, length := table[0], int(table[1])
		if length < 4 || length > len(table) {
			break
		}
		s := Structure{Type: typ, Formatted: table[:length]}
		rest := table[length:]
		end := bytes.Index(rest, []byte{0, 0})
		if end < 0 {
			break
		}
		if end > 0 {
			for part := range bytes.SplitSeq(rest[:end], []byte{0}) {
				s.Strings = append(s.Strings, string(part))
			}
		}
		out = append(out, s)
		if typ == 127 {
			break
		}
		table = rest[end+2:]
	}
	return out
}

type MemoryArray struct {
	MaxCapacityBytes uint64
	Slots            int
	ErrorCorrection  string
}

type MemoryDevice struct {
	Locator     string
	BankLocator string
	SizeBytes   uint64 // 0 = empty slot, or a module of unknown size (see Installed)
	// Installed says whether a module is in the slot: true for a size or
	// "unknown" (FFFFh, or 7FFFh without the Extended Size field), false
	// for 0 (DSP0134 7.18, Size), nil when the structure has no Size.
	Installed     *bool
	FormFactor    string
	Type          string
	SpeedMTs      int
	ConfiguredMTs int
	Manufacturer  string
	Serial        string
	PartNumber    string
	TotalWidth    int
	DataWidth     int
	Rank          int
	ConfiguredMV  int
}

var ecc = map[byte]string{
	3: "None", 4: "Parity", 5: "Single-bit ECC", 6: "Multi-bit ECC", 7: "CRC",
}

// MemoryArrays decodes type 16 (Physical Memory Array) entries used for
// system memory (use = 0x03).
func MemoryArrays(structs []Structure) []MemoryArray {
	var out []MemoryArray
	for i := range structs {
		s := &structs[i]
		if s.Type != 16 {
			continue
		}
		if use, ok := s.u8(0x05); ok && use != 0x03 {
			continue
		}
		var a MemoryArray
		if e, ok := s.u8(0x06); ok {
			a.ErrorCorrection = ecc[e]
		}
		if kb, ok := s.u32(0x07); ok {
			if kb == 0x80000000 {
				if b, ok := s.u64(0x0F); ok {
					a.MaxCapacityBytes = b
				}
			} else {
				a.MaxCapacityBytes = uint64(kb) * 1024
			}
		}
		if n, ok := s.u16(0x0D); ok {
			a.Slots = int(n)
		}
		out = append(out, a)
	}
	return out
}

var memTypes = map[byte]string{
	0x03: "DRAM", 0x0F: "SDRAM", 0x12: "DDR", 0x13: "DDR2", 0x14: "DDR2 FB-DIMM",
	0x18: "DDR3", 0x19: "FBD2", 0x1A: "DDR4", 0x1B: "LPDDR", 0x1C: "LPDDR2",
	0x1D: "LPDDR3", 0x1E: "LPDDR4", 0x1F: "Logical non-volatile", 0x20: "HBM",
	0x21: "HBM2", 0x22: "DDR5", 0x23: "LPDDR5", 0x24: "HBM3",
}

var formFactors = map[byte]string{
	0x03: "SIMM", 0x05: "Chip", 0x08: "Proprietary Card", 0x09: "DIMM",
	0x0B: "Row of chips", 0x0C: "RIMM", 0x0D: "SODIMM", 0x0F: "FB-DIMM",
	0x10: "Die", 0x11: "CAMM",
}

// Handle is the structure's handle, which other structures refer to.
func (s *Structure) Handle() uint16 {
	h, _ := s.u16(0x02)
	return h
}

// systemArrays returns the handles of type 16 arrays used for system
// memory, and whether the table has any type 16 at all.
func systemArrays(structs []Structure) (handles map[uint16]bool, anyArrays bool) {
	handles = map[uint16]bool{}
	for i := range structs {
		s := &structs[i]
		if s.Type != 16 {
			continue
		}
		anyArrays = true
		if use, ok := s.u8(0x05); ok && use == 0x03 {
			handles[s.Handle()] = true
		}
	}
	return handles, anyArrays
}

// MemoryDevices decodes type 17 (Memory Device) entries, including empty
// slots, that belong to system memory. Devices in other arrays (video
// memory, flash) are left out; firmware without type 16 arrays keeps all.
func MemoryDevices(structs []Structure) []MemoryDevice {
	system, anyArrays := systemArrays(structs)
	var out []MemoryDevice
	for i := range structs {
		s := &structs[i]
		if s.Type != 17 {
			continue
		}
		if parent, ok := s.u16(0x04); anyArrays && (!ok || !system[parent]) {
			continue
		}
		d := MemoryDevice{
			Locator:      s.str(0x10),
			BankLocator:  s.str(0x11),
			Manufacturer: s.str(0x17),
			Serial:       s.str(0x18),
			PartNumber:   s.str(0x1A),
		}
		if v, ok := s.u16(0x08); ok && v != 0xFFFF {
			d.TotalWidth = int(v)
		}
		if v, ok := s.u16(0x0A); ok && v != 0xFFFF {
			d.DataWidth = int(v)
		}
		if size, ok := s.u16(0x0C); ok {
			installed := size != 0
			d.Installed = &installed
			switch {
			case size == 0 || size == 0xFFFF:
				// empty, or installed with its size unknown
			case size == 0x7FFF:
				if ext, ok := s.u32(0x1C); ok {
					d.SizeBytes = uint64(ext&0x7FFFFFFF) << 20
				}
			case size&0x8000 != 0:
				d.SizeBytes = uint64(size&0x7FFF) << 10
			default:
				d.SizeBytes = uint64(size) << 20
			}
		}
		if v, ok := s.u8(0x0E); ok {
			d.FormFactor = formFactors[v]
		}
		if v, ok := s.u8(0x12); ok {
			d.Type = memTypes[v]
		}
		d.SpeedMTs = speed(s, 0x15, 0x54)
		d.ConfiguredMTs = speed(s, 0x20, 0x58)
		if v, ok := s.u8(0x1B); ok {
			d.Rank = int(v & 0x0F)
		}
		if v, ok := s.u16(0x26); ok {
			d.ConfiguredMV = int(v)
		}
		out = append(out, d)
	}
	return out
}

func speed(s *Structure, off, extOff int) int {
	v, ok := s.u16(off)
	if !ok || v == 0 {
		return 0
	}
	if v == 0xFFFF {
		if ext, ok := s.u32(extOff); ok {
			return int(ext & 0x7FFFFFFF)
		}
		return 0
	}
	return int(v)
}

var chassisTypes = []string{
	"", "Other", "Unknown", "Desktop", "Low Profile Desktop", "Pizza Box",
	"Mini Tower", "Tower", "Portable", "Laptop", "Notebook", "Hand Held",
	"Docking Station", "All in One", "Sub Notebook", "Space-saving",
	"Lunch Box", "Main Server Chassis", "Expansion Chassis", "SubChassis",
	"Bus Expansion Chassis", "Peripheral Chassis", "RAID Chassis",
	"Rack Mount Chassis", "Sealed-case PC", "Multi-system chassis",
	"Compact PCI", "Advanced TCA", "Blade", "Blade Enclosure", "Tablet",
	"Convertible", "Detachable", "IoT Gateway", "Embedded PC", "Mini PC",
	"Stick PC",
}

// ChassisTypeNumber is ChassisType the other way: the number for a name it
// gives, and whether there is one.
func ChassisTypeNumber(name string) (int, bool) {
	n := slices.Index(chassisTypes, name) // 0 is "", no type
	return n, n > 0
}

// ChassisType maps the SMBIOS chassis type number to its name.
func ChassisType(n int) string {
	n &= 0x7F // bit 7 is the "chassis lock present" flag
	if n > 0 && n < len(chassisTypes) {
		return chassisTypes[n]
	}
	return ""
}
