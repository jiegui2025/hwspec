package edid

import (
	"errors"
	"strings"
	"testing"
)

// testEDID builds a base block for a 27" 3840x2160@60 Dell monitor.
func testEDID() []byte {
	b := make([]byte, 128)
	copy(b, header)
	b[8], b[9] = 0x10, 0xAC   // "DEL"
	b[10], b[11] = 0x98, 0xA1 // product 0xA198
	b[12] = 0x01              // serial 1
	b[16], b[17] = 10, 30     // week 10, 2020
	b[18], b[19] = 1, 4       // EDID 1.4
	b[21], b[22] = 60, 34     // cm

	// Preferred timing: 533.25 MHz, 3840+160 x 2160+62, 597x336 mm.
	d := b[54:72]
	d[0], d[1] = 0x4D, 0xD0
	d[2], d[3], d[4] = 0x00, 0xA0, 0xF0
	d[5], d[6], d[7] = 0x70, 0x3E, 0x80
	d[12], d[13], d[14] = 0x55, 0x50, 0x21

	name := b[72:90]
	name[3] = 0xFC
	copy(name[5:], "DELL S2721QS\n ")
	serial := b[90:108]
	serial[3] = 0xFF
	copy(serial[5:], "ABC1234\n     ")
	return withChecksum(b)
}

// withChecksum sets the last byte so the block sums to zero.
func withChecksum(b []byte) []byte {
	var sum byte
	for _, v := range b[:127] {
		sum += v
	}
	b[127] = -sum
	return b
}

func TestParse(t *testing.T) {
	e, err := Parse(testEDID())
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"manufacturer", e.ManufacturerID, "DEL"},
		{"product", e.ProductCode, uint16(0xA198)},
		{"name", e.Name, "DELL S2721QS"},
		{"serial", e.SerialText, "ABC1234"},
		{"year", e.Year, 2020},
		{"version", e.Version, "1.4"},
		{"width", e.NativeWidth, 3840},
		{"height", e.NativeHeight, 2160},
		{"refresh", e.NativeRefreshHz, 60.0},
		{"width mm", e.WidthMM, 597},
		{"height mm", e.HeightMM, 336},
		{"diagonal", e.DiagonalInches(), 27.0},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, err := Parse(make([]byte, 128)); err == nil {
		t.Error("zero block: expected error")
	}
	if _, err := Parse(header); err == nil {
		t.Error("short block: expected error")
	}
}

// Projectors and some TVs report no physical size: no diagonal is made up.
func TestNoSizeMeansNoDiagonal(t *testing.T) {
	if d := (&Info{WidthMM: 0, HeightMM: 340}).DiagonalInches(); d != 0 {
		t.Errorf("diagonal = %v", d)
	}
}

// A block that fails its checksum gives only the vendor and product
// block, and ErrChecksum; a manufacturer ID that isn't three letters is
// refused (#143).
func TestParseChecksumAndManufacturer(t *testing.T) {
	b := testEDID()
	b[127]++
	info, err := Parse(b)
	if !errors.Is(err, ErrChecksum) || info == nil || info.ManufacturerID != "DEL" || info.ProductCode != 0xA198 || info.Year != 2020 ||
		info.Version != "1.4" || info.Name != "" || info.WidthMM != 0 || info.NativeWidth != 0 || info.SerialText != "" {
		t.Errorf("%+v, %v", info, err)
	}
	for _, id := range []uint16{0x0000, 0x7C00 | 1<<5 | 1, 1<<10 | 0x1B<<5 | 1, 1<<10 | 1<<5 | 0x1F} {
		b := testEDID()
		b[8], b[9] = byte(id>>8), byte(id)
		if _, err := Parse(withChecksum(b)); err == nil || !strings.Contains(err.Error(), "isn't three letters") {
			t.Errorf("%#04x: %v", id, err)
		}
	}
}
