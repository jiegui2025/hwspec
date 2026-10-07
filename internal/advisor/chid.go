package advisor

import (
	"crypto/sha1" //nolint:gosec // G505: UUIDv5 is SHA-1 by definition (RFC 9562); this names, it doesn't protect
	"fmt"
	"slices"
	"strings"
	"unicode/utf16"

	"github.com/jiegui2025/hwspec/internal/report"
	"github.com/jiegui2025/hwspec/internal/smbios"
)

// Hardware IDs (CHIDs, #237): Microsoft's ComputerHardwareIds, which fwupd
// computes from SMBIOS and LVFS uses to limit a release to some machines
// (<requires><hardware>) or keep it off others (<not_hardware>). Each is a
// UUIDv5, in Microsoft's namespace, of some of the fields below joined by
// "&" in UTF-16LE (fwupd's libfwupdplugin/fu-hwids.c).

// chidNamespace is the namespace of the UUIDv5s: 70ffd812-4c7f-4c7d-0000-000000000000.
var chidNamespace = []byte{0x70, 0xff, 0xd8, 0x12, 0x4c, 0x7f, 0x4c, 0x7d, 0, 0, 0, 0, 0, 0, 0, 0}

// chidFields are fwupd's 15 CHIDs, HardwareID-0 to HardwareID-14, by their
// fields.
var chidFields = [][]string{
	{"Manufacturer", "Family", "ProductName", "ProductSku", "BiosVendor", "BiosVersion", "BiosMajorRelease", "BiosMinorRelease"},
	{"Manufacturer", "Family", "ProductName", "BiosVendor", "BiosVersion", "BiosMajorRelease", "BiosMinorRelease"},
	{"Manufacturer", "ProductName", "BiosVendor", "BiosVersion", "BiosMajorRelease", "BiosMinorRelease"},
	{"Manufacturer", "Family", "ProductName", "ProductSku", "BaseboardManufacturer", "BaseboardProduct"},
	{"Manufacturer", "Family", "ProductName", "ProductSku"},
	{"Manufacturer", "Family", "ProductName"},
	{"Manufacturer", "ProductSku", "BaseboardManufacturer", "BaseboardProduct"},
	{"Manufacturer", "ProductSku"},
	{"Manufacturer", "ProductName", "BaseboardManufacturer", "BaseboardProduct"},
	{"Manufacturer", "ProductName"},
	{"Manufacturer", "Family", "BaseboardManufacturer", "BaseboardProduct"},
	{"Manufacturer", "Family"},
	{"Manufacturer", "EnclosureKind"},
	{"Manufacturer", "BaseboardManufacturer", "BaseboardProduct"},
	{"Manufacturer"},
}

// chidValues are the capture's values of the CHID fields, as fwupd writes
// them: trimmed, the BIOS release's major and minor in two hex digits, the
// enclosure kind (chassis type) as a hex number. A field the capture
// doesn't have is missing.
func chidValues(r *report.Report) map[string]string {
	v := map[string]string{}
	set := func(k, s string) {
		if s = strings.TrimSpace(s); s != "" {
			v[k] = s
		}
	}
	if id := r.System.Identity; id != nil {
		set("Manufacturer", id.Vendor)
		set("ProductName", id.Model)
		set("ProductSku", id.PartNumber)
	}
	set("Family", r.System.Family)
	if fw := r.System.Firmware; fw != nil {
		set("BiosVendor", fw.Vendor)
		set("BiosVersion", fw.Version)
		var major, minor int
		if n, _ := fmt.Sscanf(fw.Release, "%d.%d", &major, &minor); n == 2 && major >= 0 && major < 256 && minor >= 0 && minor < 256 {
			set("BiosMajorRelease", fmt.Sprintf("%02x", major))
			set("BiosMinorRelease", fmt.Sprintf("%02x", minor))
		}
	}
	if n, ok := smbios.ChassisTypeNumber(r.System.ChassisType); ok {
		set("EnclosureKind", fmt.Sprintf("%x", n))
	}
	if id := r.Board.Identity; id != nil {
		set("BaseboardManufacturer", id.Vendor)
		set("BaseboardProduct", id.Model)
	}
	return v
}

// machineCHIDs are the CHIDs the capture's fields give, lower-case, and
// the fields some other CHID needs but the capture lacks.
func machineCHIDs(r *report.Report) (ids map[string]bool, missing []string) {
	v := chidValues(r)
	ids = map[string]bool{}
	for _, fields := range chidFields {
		vals := make([]string, 0, len(fields))
		for _, f := range fields {
			if v[f] == "" {
				if !slices.Contains(missing, f) {
					missing = append(missing, f)
				}
				vals = nil
				break
			}
			vals = append(vals, v[f])
		}
		if vals != nil {
			ids[chid(strings.Join(vals, "&"))] = true
		}
	}
	return ids, missing
}

// chid is the UUIDv5 of s in UTF-16LE.
func chid(s string) string {
	units := utf16.Encode([]rune(s))
	data := make([]byte, 0, len(chidNamespace)+2*len(units))
	data = append(data, chidNamespace...)
	for _, u := range units {
		data = append(data, byte(u), byte(u>>8))
	}
	h := sha1.Sum(data) //nolint:gosec // G401: see the import
	h[6] = h[6]&0x0f | 0x50
	h[8] = h[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}
