package report

import (
	"reflect"
	"regexp"
	"strings"
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
	// manufacture day plus a model narrows a part to one production batch,
	// so dates are kept to the month.
	forEachIdentity(reflect.ValueOf(r).Elem(), func(id *Identity) {
		id.Serial = ""
		if d := id.ManufactureDate; len(d) == len("2006-01-02") && d[4] == '-' && d[7] == '-' {
			id.ManufactureDate = d[:7]
		}
	})
	r.System.UUID, r.System.ChassisSerial = "", ""
	r.Board.AssetTag = ""
	for i := range r.Storage {
		d := &r.Storage[i]
		d.WWN = ""
		for j := range d.Partitions {
			p := &d.Partitions[j]
			p.UUID, p.Label = "", ""
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

var identityType = reflect.TypeOf(Identity{})

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
