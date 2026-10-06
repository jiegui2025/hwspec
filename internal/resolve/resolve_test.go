package resolve

import (
	"maps"
	"slices"
	"testing"

	"github.com/jiegui2025/hwspec/internal/ids"
	"github.com/jiegui2025/hwspec/internal/report"
)

func model(id *report.Identity) string {
	if id == nil {
		return ""
	}
	return id.Model
}

func vendor(id *report.Identity) string {
	if id == nil {
		return ""
	}
	return id.Vendor
}

// A capture made before names were available (or with older databases)
// gets them from its raw IDs, into each device's identity.
func TestNamesFromRawIDs(t *testing.T) {
	r := &report.Report{
		PCI: []report.PCIDevice{
			{Address: "0000:00:02.0", VendorID: "8086", DeviceID: "3e92", ClassCode: "030000"},
			{Address: "0000:03:00.0", VendorID: "1002", DeviceID: "1114", ClassCode: "030000", Identity: &report.Identity{Revision: "c2"}},
			{Address: "0000:00:1f.6", VendorID: "8086", DeviceID: "15bb", ClassCode: "020000"},
		},
		GPUs: []report.GPU{
			{PCIAddress: "0000:00:02.0", VendorID: "8086", DeviceID: "3e92"},
			{PCIAddress: "0000:03:00.0", VendorID: "1002", DeviceID: "1114", Identity: &report.Identity{Revision: "c2"}},
		},
		USB: []report.USBDevice{
			{Path: "1-2", VendorID: "046d", ProductID: "ffff", ClassCode: "03", Identity: &report.Identity{Model: "Device's own name"}},
		},
		Network: []report.NIC{
			{Name: "eno1", Bus: "pci", BusAddress: "0000:00:1f.6", MAC: "04:0e:3c:00:00:01"},
			{Name: "usb0", Bus: "usb", BusAddress: "1-2", MAC: "02:00:00:00:00:01"},
		},
		Displays: []report.Display{{ManufacturerID: "DEL"}},
		Memory: report.Memory{Modules: []report.MemoryModule{
			{Identity: &report.Identity{Vendor: "Unknown - [0xF785]"}},
			{Identity: &report.Identity{Vendor: "Samsung"}, DRAMVendorID: "80AD"},
		}},
		CPU: report.CPU{Identity: &report.Identity{Vendor: "GenuineIntel"}, Family: 6, ModelID: 0x9e, Stepping: 10},
	}
	Names(r)

	checks := []struct{ name, got, want string }{
		{"pci vendor", vendor(r.PCI[0].Identity), "Intel Corporation"},
		{"pci revision kept", r.PCI[1].Identity.Revision, "c2"},
		{"pci class", r.PCI[0].Class, "VGA compatible controller"},
		{"intel gpu model", model(r.GPUs[0].Identity), r.GPUs[0].Chip},
		{"intel gpu vendor", vendor(r.GPUs[0].Identity), "Intel Corporation"},
		{"amd gpu retail name", model(r.GPUs[1].Identity), "AMD Radeon 860M Graphics"},
		{"usb vendor", vendor(r.USB[0].Identity), "Logitech, Inc."},
		{"usb product kept", model(r.USB[0].Identity), "Device's own name"},
		{"usb class", r.USB[0].Class, "Human Interface Device"},
		{"nic from pci", vendor(r.Network[0].Identity), "Intel Corporation"},
		{"nic mac vendor", r.Network[0].MACVendor, "HP Inc."},
		{"nic from usb", model(r.Network[1].Identity), "Device's own name"},
		{"local mac", r.Network[1].MACVendor, ""},
		{"display", vendor(r.Displays[0].Identity), "Dell Inc."},
		{"memory decoded", vendor(r.Memory.Modules[0].Identity), "Avant Technology"},
		{"memory raw kept", r.Memory.Modules[0].ManufacturerRaw, "Unknown - [0xF785]"},
		{"memory plain name", vendor(r.Memory.Modules[1].Identity), "Samsung"},
		{"memory plain raw", r.Memory.Modules[1].ManufacturerRaw, ""},
		{"dram vendor", r.Memory.Modules[1].DRAMVendor, "SK Hynix (former Hyundai Electronics)"},
		{"cpu codename", r.CPU.Codename, "Coffee Lake"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
	if r.GPUs[0].Chip == "" {
		t.Error("intel gpu chip name empty")
	}
	if r.Tool.IDDatabases["pci"] == "" || r.Tool.IDDatabases["jedec"] == "" {
		t.Errorf("id_databases not recorded: %v", r.Tool.IDDatabases)
	}

	// Running again (e.g. `hwspec show` on the saved file) is stable.
	before := *r.Memory.Modules[0].Identity
	Names(r)
	if *r.Memory.Modules[0].Identity != before {
		t.Errorf("second pass changed memory module: %+v", r.Memory.Modules[0].Identity)
	}
}

// Names loads every database up front, in parallel, but the report lists
// only the ones its lookups used: a capture with PCI devices alone names
// the PCI database and nothing else.
func TestNamesListsOnlyTheDatabasesItUsed(t *testing.T) {
	ids.UseEmbeddedOnly()
	t.Cleanup(ids.UseEnvironment)
	r := &report.Report{PCI: []report.PCIDevice{{Address: "0000:00:02.0", VendorID: "8086", DeviceID: "3e92", ClassCode: "030000"}}}
	Names(r)
	if got := slices.Sorted(maps.Keys(r.Tool.IDDatabases)); !slices.Equal(got, []string{"pci"}) {
		t.Errorf("id_databases = %v, want only pci", r.Tool.IDDatabases)
	}
}
