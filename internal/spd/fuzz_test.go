package spd

import "testing"

// SPD bytes come from a module's EEPROM, so Parse must handle anything
// without panicking, and must decode the same from just the bytes Layout
// names: the collector reads only those (#139).
func FuzzParse(f *testing.F) {
	f.Add(ddr4Image())
	ddr5 := make([]byte, 1024)
	ddr5[2], ddr5[3] = 0x12, 0x02
	copy(ddr5[521:551], "MTC8C1084S1UC48BA1")
	f.Add(ddr5)
	f.Add([]byte{0, 0, 0x0B})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		info, err := Parse(b)
		if err != nil || len(b) < 3 {
			return
		}
		size, spans, ok := Layout(b[2])
		if !ok {
			t.Fatalf("Parse decoded memory type 0x%02x that Layout doesn't describe", b[2])
		}
		masked := make([]byte, size)
		for _, s := range spans {
			copy(masked[s.Off:s.Off+s.Len], b[s.Off:s.Off+s.Len])
		}
		got, err := Parse(masked)
		if err != nil || *got != *info {
			t.Fatalf("decoding only Layout's spans differs: %+v (%v), whole image %+v", got, err, info)
		}
	})
}
