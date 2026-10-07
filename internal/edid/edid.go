// Package edid decodes the parts of a monitor's EDID block that identify it.
package edid

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
)

type Info struct {
	ManufacturerID string // 3-letter PNP ID, e.g. "DEL"
	ProductCode    uint16
	SerialNumber   uint32
	Name           string // from the 0xFC descriptor
	SerialText     string // from the 0xFF descriptor
	Week, Year     int
	Version        string
	WidthMM        int
	HeightMM       int
	// Native (preferred) mode from the first detailed timing descriptor.
	NativeWidth, NativeHeight int
	NativeRefreshHz           float64
}

var header = []byte{0x00, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x00}

// ErrChecksum is a base block whose bytes don't sum to zero: Parse then
// returns only its vendor and product identification (bytes 8-19), and
// this error, since the rest can't be trusted.
var ErrChecksum = errors.New("edid: checksum mismatch, only the vendor and product block is used")

// Parse decodes the 128-byte base block. Extension blocks are ignored.
func Parse(b []byte) (*Info, error) {
	if len(b) < 128 {
		return nil, errors.New("edid: shorter than 128 bytes")
	}
	if string(b[:8]) != string(header) {
		return nil, errors.New("edid: bad header")
	}
	info := &Info{}

	// The manufacturer is three letters, each 1-26 in five bits.
	m := binary.BigEndian.Uint16(b[8:10])
	var letters []byte
	for _, shift := range []uint16{10, 5, 0} {
		c := m >> shift & 0x1F
		if c < 1 || c > 26 {
			return nil, fmt.Errorf("edid: manufacturer ID %#04x isn't three letters", m)
		}
		letters = append(letters, byte('A'-1+c))
	}
	info.ManufacturerID = string(letters)
	info.ProductCode = binary.LittleEndian.Uint16(b[10:12])
	info.SerialNumber = binary.LittleEndian.Uint32(b[12:16])
	info.Week = int(b[16])
	if b[17] > 0 {
		info.Year = 1990 + int(b[17])
	}
	info.Version = fmt.Sprintf("%d.%d", b[18], b[19])
	var sum byte
	for _, v := range b[:128] {
		sum += v
	}
	if sum != 0 {
		return info, ErrChecksum
	}
	info.WidthMM = int(b[21]) * 10 // cm in the base block
	info.HeightMM = int(b[22]) * 10

	for i, off := range []int{54, 72, 90, 108} {
		d := b[off : off+18]
		if d[0] == 0 && d[1] == 0 {
			text := descriptorText(d[5:18])
			switch d[3] {
			case 0xFC:
				info.Name = text
			case 0xFF:
				info.SerialText = text
			}
			continue
		}
		if i != 0 {
			continue
		}
		// Detailed timing descriptor: the first one is the preferred mode.
		clock := float64(binary.LittleEndian.Uint16(d[0:2])) * 10000
		hActive := int(d[2]) | int(d[4]&0xF0)<<4
		hBlank := int(d[3]) | int(d[4]&0x0F)<<8
		vActive := int(d[5]) | int(d[7]&0xF0)<<4
		vBlank := int(d[6]) | int(d[7]&0x0F)<<8
		info.NativeWidth, info.NativeHeight = hActive, vActive
		if total := float64((hActive + hBlank) * (vActive + vBlank)); total > 0 {
			info.NativeRefreshHz = math.Round(clock/total*100) / 100
		}
		// Image size in mm is more precise than the cm values above.
		if w, h := int(d[12])|int(d[14]&0xF0)<<4, int(d[13])|int(d[14]&0x0F)<<8; w > 0 && h > 0 {
			info.WidthMM, info.HeightMM = w, h
		}
	}
	return info, nil
}

func descriptorText(b []byte) string {
	s := string(b)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// DiagonalInches returns the screen diagonal rounded to 0.1", or 0.
func (i *Info) DiagonalInches() float64 {
	if i.WidthMM == 0 || i.HeightMM == 0 {
		return 0
	}
	d := math.Hypot(float64(i.WidthMM), float64(i.HeightMM)) / 25.4
	return math.Round(d*10) / 10
}
