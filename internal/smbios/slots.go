package smbios

import (
	"fmt"
	"strings"
)

// The tables below follow DMTF DSP0134 3.8.0. Where dmidecode's names
// differ from the specification (it calls 24h "Socket BGA1155"), the
// specification wins.

// Processor is a type 4 (Processor Information) entry: the socket's
// designation, the package the firmware says it takes, and whether that
// package is soldered or socketed.
type Processor struct {
	Designation string // the socket's silkscreen label, e.g. U3E1
	Package     string // Table 25 name, e.g. "Socket LGA1151", or the Socket Type string for FFh
	Mounting    string // MountOnboard, MountSocket, MountSlot, or "" when the name doesn't say
	Populated   bool
}

// Mountings, in the words #103 uses: onboard means soldered onto the
// board (DSP0134 7.42's wording for type 41).
const (
	MountOnboard = "onboard"
	MountSocket  = "socket"
	MountSlot    = "slot"
)

// processorUpgrades is DSP0134 3.8.0 Table 25 (FFh: see Processors).
var processorUpgrades = map[byte]string{
	0x01: "Other",
	0x02: "Unknown",
	0x03: "Daughter Board",
	0x04: "ZIF Socket",
	0x05: "Replaceable Piggy Back",
	0x06: "None",
	0x07: "LIF Socket",
	0x08: "Slot 1",
	0x09: "Slot 2",
	0x0A: "370-pin socket",
	0x0B: "Slot A",
	0x0C: "Slot M",
	0x0D: "Socket 423",
	0x0E: "Socket A (Socket 462)",
	0x0F: "Socket 478",
	0x10: "Socket 754",
	0x11: "Socket 940",
	0x12: "Socket 939",
	0x13: "Socket mPGA604",
	0x14: "Socket LGA771",
	0x15: "Socket LGA775",
	0x16: "Socket S1",
	0x17: "Socket AM2",
	0x18: "Socket F (1207)",
	0x19: "Socket LGA1366",
	0x1A: "Socket G34",
	0x1B: "Socket AM3",
	0x1C: "Socket C32",
	0x1D: "Socket LGA1156",
	0x1E: "Socket LGA1567",
	0x1F: "Socket PGA988A",
	0x20: "Socket BGA1288",
	0x21: "Socket rPGA988B",
	0x22: "Socket BGA1023",
	0x23: "Socket BGA1224",
	0x24: "Socket LGA1155",
	0x25: "Socket LGA1356",
	0x26: "Socket LGA2011",
	0x27: "Socket FS1",
	0x28: "Socket FS2",
	0x29: "Socket FM1",
	0x2A: "Socket FM2",
	0x2B: "Socket LGA2011-3",
	0x2C: "Socket LGA1356-3",
	0x2D: "Socket LGA1150",
	0x2E: "Socket BGA1168",
	0x2F: "Socket BGA1234",
	0x30: "Socket BGA1364",
	0x31: "Socket AM4",
	0x32: "Socket LGA1151",
	0x33: "Socket BGA1356",
	0x34: "Socket BGA1440",
	0x35: "Socket BGA1515",
	0x36: "Socket LGA3647-1",
	0x37: "Socket SP3",
	0x38: "Socket SP3r2",
	0x39: "Socket LGA2066",
	0x3A: "Socket BGA1392",
	0x3B: "Socket BGA1510",
	0x3C: "Socket BGA1528",
	0x3D: "Socket LGA4189",
	0x3E: "Socket LGA1200",
	0x3F: "Socket LGA4677",
	0x40: "Socket LGA1700",
	0x41: "Socket BGA1744",
	0x42: "Socket BGA1781",
	0x43: "Socket BGA1211",
	0x44: "Socket BGA2422",
	0x45: "Socket LGA1211",
	0x46: "Socket LGA2422",
	0x47: "Socket LGA5773",
	0x48: "Socket BGA5773",
	0x49: "Socket AM5",
	0x4A: "Socket SP5",
	0x4B: "Socket SP6",
	0x4C: "Socket BGA883",
	0x4D: "Socket BGA1190",
	0x4E: "Socket BGA4129",
	0x4F: "Socket LGA4710",
	0x50: "Socket LGA7529",
	0x51: "Socket BGA1964",
	0x52: "Socket BGA1792",
	0x53: "Socket BGA2049",
	0x54: "Socket BGA2551",
	0x55: "Socket LGA1851",
	0x56: "Socket BGA2114",
	0x57: "Socket BGA2833",
}

// packageMounting says what a package's name says, and nothing more: a
// BGA package is soldered; LGA, PGA, ZIF and LIF packages sit in sockets;
// "Slot 1" and the like are cartridges in a slot. Names that don't say
// (Socket AM4, Socket 478, Other…) give "": the package is still
// recorded, and #119 may cite vendor data for it.
func packageMounting(name string) string {
	switch {
	case strings.Contains(name, "BGA"):
		return MountOnboard
	case strings.Contains(name, "LGA"), strings.Contains(name, "PGA"),
		strings.HasPrefix(name, "ZIF "), strings.HasPrefix(name, "LIF "):
		return MountSocket
	case strings.HasPrefix(name, "Slot "):
		return MountSlot
	}
	return ""
}

// Processors decodes type 4 entries of central processors (Processor
// Type 03h).
func Processors(structs []Structure) []Processor {
	var out []Processor
	for i := range structs {
		s := &structs[i]
		if s.Type != 4 {
			continue
		}
		if t, ok := s.u8(0x05); !ok || t != 0x03 {
			continue
		}
		p := Processor{Designation: s.str(0x04)}
		if st, ok := s.u8(0x18); ok {
			p.Populated = st&0x40 != 0
		}
		if code, ok := s.u8(0x19); ok {
			p.Package = name(processorUpgrades, code)
			if code == 0xFF {
				// "Use this when no other valid enumeration is available";
				// the Socket Type string at 32h (3.8+) names it.
				p.Package = s.str(0x32)
			}
			p.Mounting = packageMounting(p.Package)
		}
		out = append(out, p)
	}
	return out
}

// Slot is a type 9 (System Slots) entry, as the firmware states it.
type Slot struct {
	Designation string // e.g. "Slot3 / M2 SSD"
	Type        string // Table 45, e.g. "PCI Express Gen 3 x4"
	Width       string // Table 46, e.g. "x4"
	Usage       string // Table 47: Available, In use, Unavailable…
	Length      string // Table 48
	ID          int
	// Address is the PCI address the firmware gives (domain:bus:dev.fn),
	// or "" when it gives none. DSP0134 7.10.8 says it names the endpoint
	// in the slot, but firmware often gives the upstream port, or an
	// address that doesn't exist: never trust it without the topology.
	Address string
}

// slotTypes is DSP0134 3.8.0 Table 45. Long names are shortened where
// the table adds usage notes (21h–23h, 30h, C5h, C6h).
var slotTypes = map[byte]string{
	0x01: "Other", 0x02: "Unknown", 0x03: "ISA", 0x04: "MCA", 0x05: "EISA", 0x06: "PCI",
	0x07: "PC Card (PCMCIA)", 0x08: "VL-VESA", 0x09: "Proprietary", 0x0A: "Processor Card Slot",
	0x0B: "Proprietary Memory Card Slot", 0x0C: "I/O Riser Card Slot", 0x0D: "NuBus",
	0x0E: "PCI – 66MHz Capable", 0x0F: "AGP", 0x10: "AGP 2X", 0x11: "AGP 4X", 0x12: "PCI-X",
	0x13: "AGP 8X", 0x14: "M.2 Socket 1-DP (Mechanical Key A)", 0x15: "M.2 Socket 1-SD (Mechanical Key E)",
	0x16: "M.2 Socket 2 (Mechanical Key B)", 0x17: "M.2 Socket 3 (Mechanical Key M)",
	0x18: "MXM Type I", 0x19: "MXM Type II", 0x1A: "MXM Type III (standard connector)",
	0x1B: "MXM Type III (HE connector)", 0x1C: "MXM Type IV", 0x1D: "MXM 3.0 Type A", 0x1E: "MXM 3.0 Type B",
	0x1F: "PCI Express Gen 2 SFF-8639 (U.2)", 0x20: "PCI Express Gen 3 SFF-8639 (U.2)",
	0x21: "PCI Express Mini 52-pin with bottom-side keep-outs", 0x22: "PCI Express Mini 52-pin without bottom-side keep-outs",
	0x23: "PCI Express Mini 76-pin", 0x24: "PCI Express Gen 4 SFF-8639 (U.2)", 0x25: "PCI Express Gen 5 SFF-8639 (U.2)",
	0x26: "OCP NIC 3.0 Small Form Factor (SFF)", 0x27: "OCP NIC 3.0 Large Form Factor (LFF)", 0x28: "OCP NIC Prior to 3.0",
	0x30: "CXL Flexbus 1.0", 0xA0: "PC-98/C20", 0xA1: "PC-98/C24", 0xA2: "PC-98/E", 0xA3: "PC-98/Local Bus", 0xA4: "PC-98/Card",
	0xA5: "PCI Express", 0xA6: "PCI Express x1", 0xA7: "PCI Express x2", 0xA8: "PCI Express x4",
	0xA9: "PCI Express x8", 0xAA: "PCI Express x16",
	0xAB: "PCI Express Gen 2", 0xAC: "PCI Express Gen 2 x1", 0xAD: "PCI Express Gen 2 x2", 0xAE: "PCI Express Gen 2 x4",
	0xAF: "PCI Express Gen 2 x8", 0xB0: "PCI Express Gen 2 x16",
	0xB1: "PCI Express Gen 3", 0xB2: "PCI Express Gen 3 x1", 0xB3: "PCI Express Gen 3 x2", 0xB4: "PCI Express Gen 3 x4",
	0xB5: "PCI Express Gen 3 x8", 0xB6: "PCI Express Gen 3 x16",
	0xB8: "PCI Express Gen 4", 0xB9: "PCI Express Gen 4 x1", 0xBA: "PCI Express Gen 4 x2", 0xBB: "PCI Express Gen 4 x4",
	0xBC: "PCI Express Gen 4 x8", 0xBD: "PCI Express Gen 4 x16",
	0xBE: "PCI Express Gen 5", 0xBF: "PCI Express Gen 5 x1", 0xC0: "PCI Express Gen 5 x2", 0xC1: "PCI Express Gen 5 x4",
	0xC2: "PCI Express Gen 5 x8", 0xC3: "PCI Express Gen 5 x16",
	0xC4: "PCI Express Gen 6 and Beyond", 0xC5: "EDSFF E1", 0xC6: "EDSFF E3",
}

// slotWidths is Table 46; usages Table 47; lengths Table 48.
var (
	slotWidths = map[byte]string{
		0x01: "Other", 0x02: "Unknown", 0x03: "8 bit", 0x04: "16 bit", 0x05: "32 bit", 0x06: "64 bit", 0x07: "128 bit",
		0x08: "x1", 0x09: "x2", 0x0A: "x4", 0x0B: "x8", 0x0C: "x12", 0x0D: "x16", 0x0E: "x32",
	}
	slotUsages  = map[byte]string{0x01: "Other", 0x02: "Unknown", 0x03: "Available", 0x04: "In use", 0x05: "Unavailable"}
	slotLengths = map[byte]string{
		0x01: "Other", 0x02: "Unknown", 0x03: "Short Length", 0x04: "Long Length",
		0x05: `2.5" drive form factor`, 0x06: `3.5" drive form factor`,
	}
)

// Slots decodes type 9 entries.
func Slots(structs []Structure) []Slot {
	var out []Slot
	for i := range structs {
		s := &structs[i]
		if s.Type != 9 {
			continue
		}
		sl := Slot{Designation: s.str(0x04)}
		if v, ok := s.u8(0x05); ok {
			sl.Type = name(slotTypes, v)
		}
		if v, ok := s.u8(0x06); ok {
			sl.Width = name(slotWidths, v)
		}
		if v, ok := s.u8(0x07); ok {
			sl.Usage = name(slotUsages, v)
		}
		if v, ok := s.u8(0x08); ok {
			sl.Length = name(slotLengths, v)
		}
		if v, ok := s.u16(0x09); ok {
			sl.ID = int(v)
		}
		sl.Address = s.pciAddress(0x0D)
		out = append(out, sl)
	}
	return out
}

// Onboard is a type 41 (Onboard Devices Extended Information) entry: a
// device "onboard (soldered onto) a system element" (DSP0134 7.42).
type Onboard struct {
	Designation string // e.g. "Onboard IGD"
	Type        string // Table 121, e.g. "Video"
	Enabled     bool
	Instance    int
	Address     string // as for Slot.Address
}

// onboardTypes is DSP0134 3.8.0 Table 121.
var onboardTypes = map[byte]string{
	0x01: "Other", 0x02: "Unknown", 0x03: "Video", 0x04: "SCSI Controller", 0x05: "Ethernet",
	0x06: "Token Ring", 0x07: "Sound", 0x08: "PATA Controller", 0x09: "SATA Controller",
	0x0A: "SAS Controller", 0x0B: "Wireless LAN", 0x0C: "Bluetooth", 0x0D: "WWAN",
	0x0E: "eMMC", 0x0F: "NVMe Controller", 0x10: "UFS Controller",
}

// OnboardDevices decodes type 41 entries.
func OnboardDevices(structs []Structure) []Onboard {
	var out []Onboard
	for i := range structs {
		s := &structs[i]
		if s.Type != 41 {
			continue
		}
		d := Onboard{Designation: s.str(0x04)}
		if v, ok := s.u8(0x05); ok {
			d.Enabled = v&0x80 != 0
			d.Type = name(onboardTypes, v&0x7F)
		}
		if v, ok := s.u8(0x06); ok {
			d.Instance = int(v)
		}
		d.Address = s.pciAddress(0x07)
		out = append(out, d)
	}
	return out
}

// name looks code up in a DSP0134 table; a code the table doesn't have
// is kept as "code XXh", and 00h (not given) as "".
func name(table map[byte]string, code byte) string {
	if n, ok := table[code]; ok {
		return n
	}
	if code == 0 {
		return ""
	}
	return fmt.Sprintf("code %02Xh", code)
}

// pciAddress formats the segment (WORD), bus and device/function bytes
// at off as domain:bus:dev.fn, or "" when the structure is too short or
// the firmware gives none (0FFh in the bus and device/function fields,
// DSP0134 7.10.8 and 7.42.4).
func (s *Structure) pciAddress(off int) string {
	seg, ok1 := s.u16(off)
	bus, ok2 := s.u8(off + 2)
	devfn, ok3 := s.u8(off + 3)
	if !ok1 || !ok2 || !ok3 || bus == 0xFF && devfn == 0xFF {
		return ""
	}
	return fmt.Sprintf("%04x:%02x:%02x.%d", seg, bus, devfn>>3, devfn&7)
}
