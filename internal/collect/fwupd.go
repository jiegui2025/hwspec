package collect

import (
	"cmp"
	"crypto/sha1" //nolint:gosec // UUID version 5 is defined over SHA-1; nothing here is secret or signed
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jiegui2025/hwspec/internal/report"
)

// The keys a firmware catalogue names parts by (ADR 0012, #224): fwupd's
// instance IDs with their GUIDs, and the UEFI ESRT. Captures record them so
// `hwspec advise` can compare a machine with LVFS offline, without fwupd
// or D-Bus.

// dnsNamespace is RFC 4122's namespace for DNS names, the one fwupd
// hashes instance IDs in (fwupd_guid_hash_string).
var dnsNamespace = [16]byte{0x6b, 0xa7, 0xb8, 0x10, 0x9d, 0xad, 0x11, 0xd1, 0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8}

// instanceGUID is fwupd's GUID for an instance ID: UUID version 5 (SHA-1)
// in the DNS namespace over the ID's bytes.
func instanceGUID(id string) string {
	h := sha1.New() //nolint:gosec // see the import
	h.Write(dnsNamespace[:])
	h.Write([]byte(id))
	s := h.Sum(nil)
	s[6] = s[6]&0x0f | 0x50 // version 5
	s[8] = s[8]&0x3f | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", s[0:4], s[4:6], s[6:8], s[8:10], s[10:16])
}

func instanceIDs(ids ...string) []report.InstanceID {
	out := make([]report.InstanceID, 0, len(ids))
	for _, id := range ids {
		out = append(out, report.InstanceID{ID: id, GUID: instanceGUID(id)})
	}
	return out
}

// nvmeInstanceIDs are the instance IDs fwupd's nvme plugin gives the NVMe
// controller at ctrl (/sys/block/nvme0n1/device) when it sits on PCI
// (fwupd 2.1.8, plugins/nvme/fu-nvme-device.c): NVME\VEN_xxxx&DEV_xxxx,
// the same with &SUBSYS_ (subsystem vendor and device), and the model
// number. fwupd adds the model number only when the drive's identify data
// holds no vendor GUID (a FRU GUID, Dell's component ID), which needs
// root to read; listing it always can only add a match, never lose one.
// IDs fwupd keeps for quirks only (NVME\VEN_xxxx, ...&VER_) aren't GUIDs
// LVFS releases name, and aren't listed.
func nvmeInstanceIDs(ctrl string) []report.InstanceID {
	pci := ctrl + "/device"
	vendor, device := upperHex4(readStr(pci+"/vendor")), upperHex4(readStr(pci+"/device"))
	if vendor == "" || device == "" {
		return nil // not a PCI controller (NVMe over fabrics, Apple's)
	}
	ids := []string{`NVME\VEN_` + vendor + "&DEV_" + device}
	if sv, sd := upperHex4(readStr(pci+"/subsystem_vendor")), upperHex4(readStr(pci+"/subsystem_device")); sv != "" && sd != "" {
		ids = append(ids, ids[0]+"&SUBSYS_"+sv+sd)
	}
	if model := readStr(ctrl + "/model"); printableASCII(model) {
		ids = append(ids, model)
	}
	return instanceIDs(ids...)
}

// upperHex4 turns sysfs's "0x144d" into fwupd's "144D", or "" if it isn't
// a 16-bit hex number.
func upperHex4(s string) string {
	v, err := strconv.ParseUint(strings.TrimPrefix(s, "0x"), 16, 16)
	if err != nil || !strings.HasPrefix(s, "0x") {
		return ""
	}
	return fmt.Sprintf("%04X", v)
}

// printableASCII reports a non-empty string of printable ASCII: an ID the
// capture's sanitising leaves as it is, so its GUID stays the ID's.
func printableASCII(s string) bool {
	return s != "" && strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r > 0x7e }) < 0
}

const esrtDir = "/sys/firmware/efi/esrt/entries"

// esrtTypes names fw_type (UEFI 2.10 §23.4, ESRT FwType).
var esrtTypes = map[uint64]string{0: "unknown", 1: "system", 2: "device", 3: "uefi-driver"}

var guidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// esrt reads the EFI System Resource Table (Documentation/ABI/testing/
// sysfs-firmware-efi-esrt). Its entries are root-only, so without root
// the block says so; without an ESRT there is no block.
func (c *collector) esrt() *report.ESRT {
	entries := list(esrtDir)
	if len(entries) == 0 {
		return nil
	}
	// entry0, entry1, ... entry10: in the table's order.
	slices.SortFunc(entries, func(a, b string) int { return cmp.Or(cmp.Compare(len(a), len(b)), strings.Compare(a, b)) })
	out := &report.ESRT{}
	for _, name := range entries {
		e, err := esrtEntry(esrtDir + "/" + name)
		if errors.Is(err, fs.ErrPermission) && !c.privileged {
			return &report.ESRT{Status: report.FirmwareUnknown, Reason: "needs --full"}
		}
		if err != nil {
			c.warn("esrt %s: %v", name, err)
			return &report.ESRT{Status: report.FirmwareUnknown, Reason: name + ": " + err.Error()}
		}
		out.Entries = append(out.Entries, e)
	}
	return out
}

func esrtEntry(dir string) (report.ESRTEntry, error) {
	var e report.ESRTEntry
	class, err := readStrErr(dir + "/fw_class")
	if err != nil {
		return e, err
	}
	if e.FWClass = strings.ToLower(class); !guidPattern.MatchString(e.FWClass) {
		return e, fmt.Errorf("fw_class %q isn't a GUID", class)
	}
	var nums [3]uint64
	for i, f := range []string{"fw_type", "fw_version", "lowest_supported_fw_version"} {
		s, err := readStrErr(dir + "/" + f)
		if err != nil {
			return e, err
		}
		if nums[i], err = strconv.ParseUint(s, 10, 32); err != nil {
			return e, fmt.Errorf("%s %q isn't a 32-bit number", f, s)
		}
	}
	e.FWType = esrtTypes[nums[0]]
	if e.FWType == "" {
		e.FWType = "unknown"
	}
	e.FWVersion, e.LowestSupportedFWVersion = uint32(nums[1]), uint32(nums[2])
	return e, nil
}
