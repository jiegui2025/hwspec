package advisor

import "testing"

// fwupd's own vectors (libfwupdplugin/fu-version-test.c, fwupd 2.1.8),
// plus bcd and pair, which it doesn't test from a uint32.
func TestVersionFromUint32IsFwupds(t *testing.T) {
	for _, c := range []struct {
		v      uint32
		want   string
		format string
	}{
		{0x0, "0.0.0.0", "quad"}, {0xff, "0.0.0.255", "quad"}, {0xff01, "0.0.255.1", "quad"},
		{0xff0001, "0.255.0.1", "quad"}, {0xff000100, "255.0.1.0", "quad"},
		{0x0, "0.0.0", "triplet"}, {0xff, "0.0.255", "triplet"}, {0xff01, "0.0.65281", "triplet"},
		{0xff0001, "0.255.1", "triplet"}, {0xff000100, "255.0.256", "triplet"},
		{0x0, "0", "number"}, {0xff000100, "4278190336", "number"}, {0xff000100, "4278190336", "plain"},
		{0x0, "11.0.0.0", "intel-me"}, {0xffffffff, "18.31.255.65535", "intel-me"}, {0x0b32057a, "11.11.50.1402", "intel-me"},
		{0xb8320d84, "11.8.50.3460", "intel-me2"}, {0x00000741, "19.0.0.1857", "intel-csme19"},
		{0x226a4b00, "137.2706.768", "surface-legacy"}, {0x6001988, "6.25.136", "surface"},
		{0x00ff0001, "255.0.1", "dell-bios"}, {0x010f0201, "1.15.2", "dell-bios-msb"},
		{0xc8, "0x000000c8", "hex"},
		{0x0, "00.00", "compal-bios"}, {0xff, "00.ff", "compal-bios"}, {0x0000ff01, "ff.01", "compal-bios"}, {0x00001234, "12.34", "compal-bios"},
		{0x12345678, "12.34.56.78", "bcd"}, {0x00010002, "1.2", "pair"},
	} {
		if got, ok := versionFromUint32(c.v, c.format); !ok || got != c.want {
			t.Errorf("%#x as %s: %q, want %q", c.v, c.format, got, c.want)
		}
	}
	if _, ok := versionFromUint32(1, "semver"); ok {
		t.Error("an unknown format was formatted")
	}
}

// Versions are ordered only in a format that orders them, and only when
// both are written in it.
func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b, format string
		want         int
		ok           bool
	}{
		{"1L2QEXD7", "1L2QEXD7", "", 0, true},
		{"1L2QEXD7", "1L2QEXD8", "", 0, false},
		{"1L2QEXD7", "1L2QEXD8", "plain", 0, false},
		{"1.2.3", "1.2.4", "triplet", -1, true},
		{"001.002.009", "001.002.000", "triplet", 1, true},

		{"1.2.3.1", "1.2.4.0", "quad", -1, true},
		{"1.2.3.1", "1.2.3.0", "quad", 1, true},
		{"1.02", "1.2", "pair", 0, true},
		{"1.2", "1.2.0", "triplet", 0, false},
		{"1.3", "1.2.9.9", "quad", 0, false},
		{"12.34", "12.34.56.78", "bcd", 0, false},
		{"12.34", "12.35", "bcd", -1, true},
		{"1.2.3", "1.2.3.0", "quad", 0, false},
		{"1.10", "1.9", "pair", 1, true},
		{"1.2.3", "1.2.3a", "triplet", 0, false},
		{"1.2.3~rc1", "1.2.3", "triplet", 0, false},
		{"1..3", "1.2.3", "triplet", 0, false},
		{"1.-2.3", "1.2.3", "triplet", 0, false},
		{"1.2.3", "1.2.3", "semver", 0, true},
		{"1.2.3", "1.2.4", "semver", 0, false},
		{"0x00000002", "0x2", "hex", 0, true},
		{"0x10", "0x9", "hex", 1, true},
		{"0x10", "16", "hex", 0, false},
		{"0a.10", "0a.09", "compal-bios", 0, false},
		{"12.10", "12.09", "compal-bios", 0, false},
		{"4278190336", "4278190337", "number", -1, true},
	} {
		got, ok := compareVersions(c.a, c.b, c.format)
		if got != c.want || ok != c.ok {
			t.Errorf("%s vs %s (%s): %d %v, want %d %v", c.a, c.b, c.format, got, ok, c.want, c.ok)
		}
	}
}
