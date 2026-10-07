package advisor

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Firmware versions as fwupd formats and compares them
// (libfwupdplugin/fu-version-common.c, fwupd 2.1.8), under LVFS's
// LVFS::VersionFormat names. hwspec orders two versions only when both are
// written the way their format says, with as many numbers as it has;
// anything else is "differs", never "newer" (ADR 0012).

// versionFromUint32 writes a 32-bit version (an ESRT's) in a format, as
// fu_version_from_uint32 does.
func versionFromUint32(v uint32, format string) (string, bool) {
	b := func(shift uint) uint32 { return v >> shift & 0xff }
	bcd := func(x uint32) uint32 { return (x>>4&0x0f)*10 + x&0x0f }
	switch format {
	case "quad":
		return fmt.Sprintf("%d.%d.%d.%d", b(24), b(16), b(8), b(0)), true
	case "triplet":
		return fmt.Sprintf("%d.%d.%d", b(24), b(16), v&0xffff), true
	case "pair":
		return fmt.Sprintf("%d.%d", v>>16, v&0xffff), true
	case "compal-bios":
		return fmt.Sprintf("%02x.%02x", b(8), b(0)), true
	case "number", "plain":
		return strconv.FormatUint(uint64(v), 10), true
	case "bcd":
		return fmt.Sprintf("%d.%d.%d.%d", bcd(b(24)), bcd(b(16)), bcd(b(8)), bcd(b(0))), true
	case "intel-me":
		return fmt.Sprintf("%d.%d.%d.%d", v>>29&0x07+0x0b, v>>24&0x1f, b(16), v&0xffff), true
	case "intel-me2":
		return fmt.Sprintf("%d.%d.%d.%d", v>>28&0x0f, v>>24&0x0f, b(16), v&0xffff), true
	case "intel-csme19":
		return fmt.Sprintf("%d.%d.%d.%d", v>>29&0x07+19, v>>24&0x1f, b(16), v&0xffff), true
	case "surface-legacy":
		return fmt.Sprintf("%d.%d.%d", v>>22&0x3ff, v>>10&0xfff, v&0x3ff), true
	case "surface":
		return fmt.Sprintf("%d.%d.%d", b(24), v>>8&0xffff, b(0)), true
	case "dell-bios":
		return fmt.Sprintf("%d.%d.%d", b(16), b(8), b(0)), true
	case "dell-bios-msb":
		return fmt.Sprintf("%d.%d.%d", b(24), b(16), b(8)), true
	case "hex":
		return fmt.Sprintf("0x%08x", v), true
	}
	return "", false
}

// formatSections are how many dot-separated numbers each ordered format
// has, as fu_version_format_number_sections counts them (bcd 2, though
// fu_version_from_uint32 writes it with 4: such a version isn't ordered).
var formatSections = map[string][]int{
	"number": {1}, "hex": {1}, "pair": {2}, "bcd": {2},
	"triplet": {3}, "surface-legacy": {3}, "surface": {3}, "dell-bios": {3}, "dell-bios-msb": {3},
	"quad": {4}, "intel-me": {4}, "intel-me2": {4}, "intel-csme19": {4},
}

// ordersVersions says whether a format orders versions: "plain" (and a
// missing or unknown format) doesn't; fwupd compares plain versions as
// strings, which says nothing about which is newer. Nor does
// "compal-bios", whose hex sections fwupd compares as decimal.
func ordersVersions(format string) bool { return formatSections[format] != nil }

// fits says whether a version is written as its format has it: one 0x
// number for hex, else the format's count of plain decimal numbers.
func fits(v, format string) bool {
	if format == "hex" {
		_, ok := hexNumber(v)
		return ok
	}
	parts, ok := sections(v)
	return ok && slices.Contains(formatSections[format], len(parts))
}

// compareVersions orders a and b in a format: -1, 0 or 1, and whether they
// could be ordered. Equal strings are equal in any format; otherwise both
// must fit an ordered format, whose numbers are compared in turn, as
// fu_version_compare does.
func compareVersions(a, b, format string) (int, bool) {
	if a == b {
		return 0, true
	}
	if !ordersVersions(format) || !fits(a, format) || !fits(b, format) {
		return 0, false
	}
	if format == "hex" {
		x, _ := hexNumber(a)
		y, _ := hexNumber(b)
		return cmp.Compare(x, y), true
	}
	x, _ := sections(a)
	y, _ := sections(b)
	return slices.Compare(x, y), true
}

func hexNumber(s string) (uint64, bool) {
	digits, ok := strings.CutPrefix(s, "0x")
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseUint(digits, 16, 64)
	return v, err == nil
}

// sections splits "1.2.3" into numbers; false if any part isn't a plain
// decimal number (a letter, a sign, a space, an empty part: ParseUint
// refuses each).
func sections(s string) ([]uint64, bool) {
	var out []uint64
	for part := range strings.SplitSeq(s, ".") {
		v, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return nil, false
		}
		out = append(out, v)
	}
	return out, true
}
