package spd

import "testing"

// A 16 GiB DDR4 SO-DIMM (16 Gb ×8 chips, one rank) made by Samsung in week
// 10 of 2021, with SK Hynix DRAM.
func ddr4Image() []byte {
	b := make([]byte, 512)
	b[2], b[3], b[4], b[12], b[13] = 0x0C, 0x03, 0x86, 0x01, 0x03
	b[320], b[321] = 0x80, 0xCE // Samsung
	b[323], b[324] = 0x21, 0x10 // BCD 2021, week 10
	copy(b[325:329], []byte{0x12, 0x34, 0x56, 0x78})
	copy(b[329:349], "M471A2K43DB1-CTD    ")
	b[349] = 0x31
	b[350], b[351] = 0x80, 0xAD // SK Hynix
	return b
}

func TestDDR4ModuleIdentityAndSize(t *testing.T) {
	i, err := Parse(ddr4Image())
	if err != nil {
		t.Fatal(err)
	}
	want := Info{
		Type: "DDR4", FormFactor: "SODIMM", SizeBytes: 16 << 30,
		ModuleVendorCode: "80CE", DRAMVendorCode: "80AD",
		PartNumber: "M471A2K43DB1-CTD", Serial: "12345678", Revision: "31",
		ManufactureYear: 2021, ManufactureWeek: 10,
	}
	if *i != want {
		t.Errorf("got  %+v\nwant %+v", *i, want)
	}
}

// Many modules (the maintainer's Avant SO-DIMMs included) leave the date
// and DRAM maker blank: they must stay empty, never 2000-W00.
func TestBlankFieldsStayEmpty(t *testing.T) {
	b := ddr4Image()
	b[323], b[324], b[350], b[351] = 0, 0, 0, 0
	copy(b[325:329], []byte{0xFF, 0xFF, 0xFF, 0xFF})
	i, _ := Parse(b)
	if i.ManufactureYear != 0 || i.ManufactureWeek != 0 || i.DRAMVendorCode != "" || i.Serial != "" {
		t.Errorf("blank fields decoded as %+v", i)
	}
}

func TestDDR5(t *testing.T) {
	b := make([]byte, 1024)
	b[2], b[3] = 0x12, 0x02
	b[512], b[513] = 0x80, 0x2C // Micron
	b[515], b[516] = 0x24, 0x33 // 2024, week 33
	copy(b[517:521], []byte{0xAA, 0xBB, 0xCC, 0xDD})
	copy(b[521:551], "MTC8C1084S1UC48BA1")
	i, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if i.Type != "DDR5" || i.FormFactor != "UDIMM" || i.ModuleVendorCode != "802C" || i.PartNumber != "MTC8C1084S1UC48BA1" ||
		i.Serial != "AABBCCDD" || i.ManufactureYear != 2024 || i.ManufactureWeek != 33 {
		t.Errorf("got %+v", i)
	}
}

// Readers fetch only Layout's spans from a slow EEPROM, so Parse must not
// depend on any other byte: changing one outside the spans changes nothing.
func TestParseReadsOnlyTheLayoutsSpans(t *testing.T) {
	ddr5 := make([]byte, 1024)
	ddr5[2], ddr5[3] = 0x12, 0x02
	ddr5[512], ddr5[513], ddr5[515], ddr5[516] = 0x80, 0x2C, 0x24, 0x33
	copy(ddr5[517:521], []byte{0xAA, 0xBB, 0xCC, 0xDD})
	copy(ddr5[521:551], "MTC8C1084S1UC48BA1")
	ddr5[551], ddr5[552], ddr5[553] = 0x01, 0x80, 0x2C
	for _, img := range [][]byte{ddr4Image(), ddr5} {
		size, spans, ok := Layout(img[2])
		if !ok || size != len(img) {
			t.Fatalf("Layout(0x%02x) = %d, %v, %v", img[2], size, spans, ok)
		}
		want, err := Parse(img)
		if err != nil {
			t.Fatal(err)
		}
		inSpan := make([]bool, size)
		for _, s := range spans {
			for i := s.Off; i < s.Off+s.Len; i++ {
				inSpan[i] = true
			}
		}
		for i := range img {
			if inSpan[i] {
				continue
			}
			changed := append([]byte(nil), img...)
			changed[i] ^= 0xFF
			if got, _ := Parse(changed); *got != *want {
				t.Fatalf("type 0x%02x: byte %d outside the layout changed the result: %+v", img[2], i, *got)
			}
		}
	}
	if _, _, ok := Layout(0x0B); ok {
		t.Error("Layout claims DDR3, which Parse doesn't decode")
	}
}

func TestUnsupportedAndShortImages(t *testing.T) {
	for name, b := range map[string][]byte{
		"empty": nil, "ddr3": {0x92, 0x11, 0x0B}, "short ddr4": make([]byte, 300), "short ddr5": append([]byte{0, 0, 0x12}, make([]byte, 500)...),
	} {
		if name == "short ddr4" {
			b[2] = 0x0C
		}
		if _, err := Parse(b); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestGarbagePartNumberIsDropped(t *testing.T) {
	b := ddr4Image()
	copy(b[329:349], []byte{0x01, 0xC3, 'X'})
	if i, _ := Parse(b); i.PartNumber != "" {
		t.Errorf("part number %q from binary garbage", i.PartNumber)
	}
}

// 12 and 24 Gbit dies (codes 8 and 9) aren't powers of two: a single-rank
// x8 module of 12 Gbit dies holds 12 GiB, not 64.
func TestNonPowerOfTwoDieDensity(t *testing.T) {
	for code, want := range map[byte]uint64{0x08: 12 << 30, 0x09: 24 << 30, 0x0A: 0} {
		b := ddr4Image()
		b[4] = 0x80 | code
		if i, _ := Parse(b); i.SizeBytes != want {
			t.Errorf("density code %d: %d bytes, want %d", code, i.SizeBytes, want)
		}
	}
}

// DDR5 reuses the module type codes for new form factors.
func TestDDR5FormFactors(t *testing.T) {
	for code, want := range map[byte]string{0x05: "CUDIMM", 0x06: "CSODIMM", 0x07: "MRDIMM", 0x08: "CAMM2"} {
		b := make([]byte, 1024)
		b[2], b[3] = 0x12, code
		if i, _ := Parse(b); i.FormFactor != want {
			t.Errorf("module type 0x%02x: %q, want %q", code, i.FormFactor, want)
		}
	}
}

// A 3DS (stacked) module holds several dies per package: a single-rank x4
// module of 16 Gbit dies in 4-high stacks holds 128 GiB.
func TestStackedDiesMultiplyCapacity(t *testing.T) {
	b := ddr4Image()
	b[4], b[6], b[12], b[13] = 0x86, 0x32, 0x00, 0x03 // 16 Gb, 3DS 4 dies, x4, 64-bit
	if i, _ := Parse(b); i.SizeBytes != 128<<30 {
		t.Errorf("3DS = %d GiB", i.SizeBytes>>30)
	}
}

// A manufacture date that isn't BCD is left unset, not misread.
func TestNonBCDDatesAreUnset(t *testing.T) {
	b := ddr4Image()
	b[323], b[324] = 0x2A, 0x1F
	if i, _ := Parse(b); i.ManufactureYear != 0 || i.ManufactureWeek != 0 {
		t.Errorf("date = %d-W%d", i.ManufactureYear, i.ManufactureWeek)
	}
}
