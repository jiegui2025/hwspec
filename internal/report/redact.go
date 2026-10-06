package report

import (
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// systemMounts are mount points that say nothing about the user.
var systemMounts = map[string]bool{
	"/": true, "/boot": true, "/boot/efi": true, "/efi": true, "/home": true, "/var": true,
	"/tmp": true, "/usr": true, "/opt": true, "/srv": true, "/nix": true, "/nix/store": true, "[SWAP]": true,
}

// macInterfaceName matches names udev derives from the MAC address
// (enx001122334455, wlx…, wwx…), which would leak the redacted MAC.
var macInterfaceName = regexp.MustCompile(`^(enx|wlx|wwx)[0-9a-f]{12}$`)

// Redact clears identifiers that tie a report to one physical machine or
// person (serial numbers, UUIDs, MAC addresses, hostname, personal paths),
// so the file can be shared publicly. Models, versions, sizes and dates are
// kept.
func (r *Report) Redact() {
	r.Redacted = true
	r.Hostname = ""
	// Every serial number lives in an Identity block (ADR 0008). A
	// manufacture day or week plus a model narrows a part to one production
	// batch, so dates are kept to the month.
	forEachIdentity(reflect.ValueOf(r).Elem(), func(id *Identity) {
		id.Serial = ""
		id.ManufactureDate = monthOf(id.ManufactureDate)
	})
	r.System.UUID, r.System.ChassisSerial = "", ""
	r.Board.AssetTag = ""
	for i := range r.Storage {
		d := &r.Storage[i]
		d.WWN = ""
		for j := range d.Partitions {
			p := &d.Partitions[j]
			p.UUID, p.PartUUID, p.Label = "", "", ""
			// /home/alice, /run/media/alice/Backup: user names and labels.
			if !systemMounts[p.MountPoint] {
				p.MountPoint = ""
			}
		}
	}
	for i := range r.Network {
		n := &r.Network[i]
		n.MAC = ""
		if m := macInterfaceName.FindStringSubmatch(n.Name); m != nil {
			n.Name = m[1] + strings.Repeat("x", 12)
		}
	}
	for i := range r.Bluetooth {
		// The local name usually defaults to the hostname.
		r.Bluetooth[i].Address, r.Bluetooth[i].LocalName = "", ""
	}
}

var identityType = reflect.TypeFor[Identity]()

// forEachIdentity calls fn for every Identity reachable from v.
func forEachIdentity(v reflect.Value, fn func(*Identity)) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			forEachIdentity(v.Elem(), fn)
		}
	case reflect.Struct:
		if v.Type() == identityType {
			if v.CanAddr() {
				fn(v.Addr().Interface().(*Identity))
			}
			return
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				forEachIdentity(v.Field(i), fn)
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			forEachIdentity(v.Index(i), fn)
		}
	}
}

// monthOf shortens a manufacture date to its month: "2021-03-17" and
// "2021-W11" become "2021-03". An ISO week belongs to the month of its
// Thursday (ISO 8601 puts week 1 around the year's first Thursday). Other
// forms ("2021-03", a year alone, anything malformed) are returned as is.
func monthOf(d string) string {
	if len(d) == len("2006-01-02") && d[4] == '-' && d[7] == '-' {
		return d[:7]
	}
	if len(d) != len("2006-W01") || d[4:6] != "-W" {
		return d
	}
	year, err1 := strconv.Atoi(d[:4])
	week, err2 := strconv.Atoi(d[6:])
	if err1 != nil || err2 != nil || week < 1 || week > 53 {
		return d
	}
	// January 4th is always in week 1; step back to that week's Monday.
	jan4 := time.Date(year, time.January, 4, 0, 0, 0, 0, time.UTC)
	monday1 := jan4.AddDate(0, 0, -((int(jan4.Weekday()) + 6) % 7))
	thursday := monday1.AddDate(0, 0, (week-1)*7+3)
	if thursday.Year() != year {
		// Week 53 of a 52-week year (EDID allows it): the year's last month,
		// rather than a month of the next year.
		return d[:4] + "-12"
	}
	return thursday.Format("2006-01")
}
