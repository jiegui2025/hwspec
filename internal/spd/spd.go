// Package spd decodes the Serial Presence Detect EEPROM on DDR4 and DDR5
// memory modules: the module's own record of who made it, when, its part
// number and serial, and its size. The kernel's ee1004 (DDR4) and spd5118
// (DDR5) drivers expose the EEPROM, often readable without root.
package spd

import (
	"errors"
	"fmt"
	"strings"
)

type Info struct {
	Type       string // DDR4, DDR5
	FormFactor string // UDIMM, SODIMM, RDIMM, ...
	SizeBytes  uint64 // 0 if not decoded
	// JEDEC manufacturer codes as "BBII" hex (bank byte with parity, ID),
	// the same form firmware uses in SMBIOS ("80CE" = Samsung).
	ModuleVendorCode string
	DRAMVendorCode   string
	PartNumber       string
	Serial           string // 8 hex digits
	Revision         string
	// ManufactureYear and ManufactureWeek are 0 when the module leaves
	// them blank, as many do.
	ManufactureYear int
	ManufactureWeek int
}

// Module types (byte 3, bits 3:0) differ between generations.
var (
	ddr4FormFactors = map[byte]string{
		0x01: "RDIMM", 0x02: "UDIMM", 0x03: "SODIMM", 0x04: "LRDIMM",
		0x05: "Mini-RDIMM", 0x06: "Mini-UDIMM", 0x08: "72b-SO-RDIMM", 0x09: "72b-SO-UDIMM",
		0x0C: "16b-SO-DIMM", 0x0D: "32b-SO-DIMM",
	}
	ddr5FormFactors = map[byte]string{
		0x01: "RDIMM", 0x02: "UDIMM", 0x03: "SODIMM", 0x04: "LRDIMM",
		0x05: "CUDIMM", 0x06: "CSODIMM", 0x07: "MRDIMM", 0x08: "CAMM2",
		0x0A: "DDIMM", 0x0B: "Solder down",
	}
)

// ddr4DieMbit maps byte 4 bits 3:0 to the die density in Mbit; codes 8
// and 9 (12 and 24 Gbit) break the power-of-two sequence.
var ddr4DieMbit = map[byte]uint64{
	0: 256, 1: 512, 2: 1 << 10, 3: 2 << 10, 4: 4 << 10, 5: 8 << 10,
	6: 16 << 10, 7: 32 << 10, 8: 12 << 10, 9: 24 << 10,
}

// Span is the byte range [Off, Off+Len) of an SPD image.
type Span struct{ Off, Len int }

// Layout returns the image size and every byte range Parse reads for the
// memory type in byte 2, or ok false for a type Parse doesn't decode. An
// EEPROM read over SMBus costs about 0.16 ms a byte, so a caller can read
// just these spans into a zeroed image of that size and parse it.
func Layout(memType byte) (size int, spans []Span, ok bool) {
	switch memType {
	case 0x0C: // DDR4: bytes 2-13, then the manufacturing block 320-351
		return 512, []Span{{0, 16}, {320, 32}}, true
	case 0x12: // DDR5: bytes 2-3, then the manufacturing block 512-553
		return 1024, []Span{{0, 16}, {512, 42}}, true
	}
	return 0, nil, false
}

// Parse decodes an SPD image. Only the DDR4 (512-byte) and DDR5
// (1024-byte) layouts are supported.
func Parse(b []byte) (*Info, error) {
	if len(b) < 3 {
		return nil, errors.New("spd: too short")
	}
	switch b[2] {
	case 0x0C:
		if len(b) < 512 {
			return nil, errors.New("spd: DDR4 image shorter than 512 bytes")
		}
		return ddr4(b), nil
	case 0x12:
		if len(b) < 1024 {
			return nil, errors.New("spd: DDR5 image shorter than 1024 bytes")
		}
		return ddr5(b), nil
	}
	return nil, fmt.Errorf("spd: memory type 0x%02x not supported", b[2])
}

// ddr4 follows JEDEC SPD Annex L (4.1.2.L-4).
func ddr4(b []byte) *Info {
	i := &Info{
		Type:             "DDR4",
		FormFactor:       ddr4FormFactors[b[3]&0x0F],
		ModuleVendorCode: code(b[320], b[321]),
		DRAMVendorCode:   code(b[350], b[351]),
		PartNumber:       text(b[329:349]),
		Serial:           serial(b[325:329]),
		Revision:         revision(b[349]),
		ManufactureYear:  year(b[323]),
		ManufactureWeek:  week(b[324]),
	}
	// Capacity = die density / 8 × bus width / device width × ranks
	// (× dies per package for 3DS stacks).
	dieMbit, knownDensity := ddr4DieMbit[b[4]&0x0F]
	deviceWidth := uint64(4) << (b[12] & 0x07)
	ranks := uint64((b[12]>>3)&0x07) + 1
	busWidth := uint64(8) << (b[13] & 0x07)
	dies := uint64(1)
	if b[6]&0x03 == 0x02 { // 3DS
		dies = uint64((b[6]>>4)&0x07) + 1
	}
	if knownDensity && b[12]&0x07 <= 3 && b[13]&0x07 <= 3 {
		i.SizeBytes = dieMbit << 20 / 8 * busWidth / deviceWidth * ranks * dies
	}
	return i
}

// ddr5 follows JEDEC JESD400-5 (manufacturing section from byte 512).
func ddr5(b []byte) *Info {
	return &Info{
		Type:             "DDR5",
		FormFactor:       ddr5FormFactors[b[3]&0x0F],
		ModuleVendorCode: code(b[512], b[513]),
		DRAMVendorCode:   code(b[552], b[553]),
		PartNumber:       text(b[521:551]),
		Serial:           serial(b[517:521]),
		Revision:         revision(b[551]),
		ManufactureYear:  year(b[515]),
		ManufactureWeek:  week(b[516]),
	}
}

func code(bank, id byte) string {
	if bank == 0 && id == 0 || bank == 0xFF && id == 0xFF {
		return ""
	}
	return fmt.Sprintf("%02X%02X", bank, id)
}

func text(b []byte) string {
	end := len(b) // unset bytes are 0x00 or 0xFF; padding is spaces
	for end > 0 && (b[end-1] == 0x00 || b[end-1] == 0xFF || b[end-1] == ' ') {
		end--
	}
	s := string(b[:end])
	for _, r := range s {
		if r < 0x20 || r > 0x7e {
			return "" // not ASCII: an unset or corrupt field
		}
	}
	return strings.TrimSpace(s)
}

func serial(b []byte) string {
	if allEqual(b, 0x00) || allEqual(b, 0xFF) {
		return ""
	}
	return fmt.Sprintf("%02X%02X%02X%02X", b[0], b[1], b[2], b[3])
}

func revision(b byte) string {
	if b == 0x00 || b == 0xFF {
		return ""
	}
	return fmt.Sprintf("%02X", b)
}

// year and week are BCD; 0 (or invalid BCD) means unset.
func year(b byte) int {
	v, ok := bcd(b)
	if !ok || v == 0 {
		return 0
	}
	return 2000 + v
}

func week(b byte) int {
	v, ok := bcd(b)
	if !ok || v < 1 || v > 53 {
		return 0
	}
	return v
}

func bcd(b byte) (int, bool) {
	hi, lo := b>>4, b&0x0F
	if hi > 9 || lo > 9 {
		return 0, false
	}
	return int(hi)*10 + int(lo), true
}

func allEqual(b []byte, v byte) bool {
	for _, c := range b {
		if c != v {
			return false
		}
	}
	return true
}
