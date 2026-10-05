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
