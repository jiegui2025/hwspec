package advisor

import (
	"slices"
	"testing"

	"github.com/jiegui2025/hwspec/internal/kb"
	"github.com/jiegui2025/hwspec/internal/report"
)

// reference is the reference machine as its capture names it (#25).
func reference() *report.Report {
	return &report.Report{
		System: report.System{
			Identity: &report.Identity{Vendor: "HP", Model: "HP EliteDesk 800 G5 Desktop Mini", PartNumber: "7LL88UT#ABA"},
			Family:   "103C_53307F HP EliteDesk",
			Firmware: &report.Firmware{Version: "R21 Ver. 02.27.00", Source: "dmi"},
		},
		Board: report.Board{Identity: &report.Identity{Model: "8595"}},
		CPU:   report.CPU{Identity: &report.Identity{Vendor: "GenuineIntel", Model: "Intel(R) Core(TM) i5-9500T CPU @ 2.20GHz"}},
	}
}

func TestModelFor(t *testing.T) {
	m := func(id, board, sku string) kb.Model {
		return kb.Model{ID: id, Match: kb.ModelMatch{SysVendor: "HP", ProductName: "HP EliteDesk 800 G5 Desktop Mini", BoardName: board, SKU: sku}}
	}
	k := &kb.KB{Models: []kb.Model{m("a.any", "", ""), m("b.board", "8595", ""), m("c.other-board", "8596", ""), m("d.sku", "8595", "7LL88UT#ABA")}}
	if got := modelFor(k, reference()); got == nil || got.ID != "d.sku" {
		t.Errorf("the most specific: %+v", got)
	}
	k.Models = k.Models[:3]
	if got := modelFor(k, reference()); got == nil || got.ID != "b.board" {
		t.Errorf("board over any: %+v", got)
	}
	k.Models = []kb.Model{m("a.any", "", "")}
	if got := modelFor(k, reference()); got == nil || got.ID != "a.any" {
		t.Errorf("an entry for every board: %+v", got)
	}
	k.Models = []kb.Model{m("a.board", "8595", ""), m("b.sku", "", "7LL88UT#ABA")}
	if got := modelFor(k, reference()); got != nil {
		t.Errorf("a tie can't be told apart, got %+v", got)
	}
	k.Models = []kb.Model{m("a.any", "", ""), m("b.board", "8595", ""), m("c.sku", "", "7LL88UT#ABA")}
	if got := modelFor(k, reference()); got != nil {
		t.Errorf("a tie above a generic entry, got %+v", got)
	}
	k.Models = append(k.Models, m("d.both", "8595", "7LL88UT#ABA"))
	if got := modelFor(k, reference()); got == nil || got.ID != "d.both" {
		t.Errorf("a more specific entry above a tie, got %+v", got)
	}
	k.Models = []kb.Model{m("a.any", "", ""), m("d.sku", "", "OTHER")}
	r := reference()
	r.Board.Identity = nil
	if got := modelFor(k, r); got == nil || got.ID != "a.any" {
		t.Errorf("no board, another SKU: %+v", got)
	}
	r.System.Identity = &report.Identity{Vendor: "HP", Model: "HP ProDesk"}
	if got := modelFor(k, r); got != nil {
		t.Errorf("another product: %+v", got)
	}
	if got := modelFor(k, &report.Report{}); got != nil {
		t.Errorf("no identity: %+v", got)
	}
}

func TestPCIDeviceFor(t *testing.T) {
	d := func(id, bus string) kb.Device { return kb.Device{ID: id, Match: kb.DeviceMatch{Bus: bus, ID: id}} }
	k := &kb.KB{Devices: []kb.Device{d("8086:2723:8086:0080", "pci"), d("8086:2723", "pci"), d("046d:c52b", "usb")}}
	wifi := &report.PCIDevice{VendorID: "8086", DeviceID: "2723", SubVendorID: "8086", SubDeviceID: "0080"}
	if got := pciDeviceFor(k, wifi); got == nil || got.ID != "8086:2723:8086:0080" {
		t.Errorf("the subsystem's entry: %+v", got)
	}
	other := &report.PCIDevice{VendorID: "8086", DeviceID: "2723", SubVendorID: "8086", SubDeviceID: "0084"}
	if got := pciDeviceFor(k, other); got == nil || got.ID != "8086:2723" {
		t.Errorf("the chip's entry: %+v", got)
	}
	upper := &report.PCIDevice{VendorID: "8086", DeviceID: "2723", SubVendorID: "8086", SubDeviceID: "0080"}
	k.Devices = []kb.Device{d("8086:2723:8086:0080", "pci")}
	upper.VendorID, upper.DeviceID = "8086", "2723"
	if got := pciDeviceFor(k, upper); got == nil {
		t.Error("the subsystem alone")
	}
	usbID := &report.PCIDevice{VendorID: "046d", DeviceID: "c52b"}
	k.Devices = []kb.Device{d("046d:c52b", "usb")}
	if got := pciDeviceFor(k, usbID); got != nil {
		t.Errorf("a USB entry matched a PCI device: %+v", got)
	}
	k.Devices = []kb.Device{d("8086:3e92", "pci")}
	if got := pciDeviceFor(k, &report.PCIDevice{VendorID: "8086", DeviceID: "3E92"}); got == nil {
		t.Error("upper-case hex in a hand-edited capture")
	}
}

func TestCPUFor(t *testing.T) {
	k := &kb.KB{CPUs: []kb.CPU{{ID: "intel.i5-9500", Match: kb.CPUMatch{Vendor: "intel", Processor: "i5-9500"}},
		{ID: "intel.i5-9500t", Match: kb.CPUMatch{Vendor: "intel", Processor: "i5-9500T"}}}}
	if got := cpuFor(k, reference()); got == nil || got.ID != "intel.i5-9500t" {
		t.Errorf("i5-9500T: %+v", got)
	}
	for _, id := range []*report.Identity{nil, {Vendor: "AuthenticAMD", Model: "Intel(R) Core(TM) i5-9500T CPU @ 2.20GHz"},
		{Vendor: "GenuineIntel", Model: "Intel(R) Core(TM) i7-8700 CPU @ 3.20GHz"}, {Vendor: "GenuineIntel", Model: "Genuine Intel(R) CPU 0000"}} {
		r := reference()
		r.CPU.Identity = id
		if got := cpuFor(k, r); got != nil {
			t.Errorf("%+v matched %+v", id, got)
		}
	}
}

func TestAllowlistsFor(t *testing.T) {
	a := func(id string, m kb.AllowlistMatch) kb.Allowlist { return kb.Allowlist{ID: id, Match: m} }
	k := &kb.KB{Allowlists: []kb.Allowlist{
		a("by-family", kb.AllowlistMatch{SysVendor: "HP", Family: "103C_53307F HP EliteDesk"}),
		a("by-product", kb.AllowlistMatch{SysVendor: "HP", ProductName: []string{"HP ProDesk", "HP EliteDesk 800 G5 Desktop Mini"}}),
		a("by-board", kb.AllowlistMatch{SysVendor: "HP", BoardName: []string{"8595"}}),
		a("in-range", kb.AllowlistMatch{SysVendor: "HP", Family: "103C_53307F HP EliteDesk", BIOSVersion: &kb.Range{From: "R21 Ver. 02.00.00"}}),
		a("lifted", kb.AllowlistMatch{SysVendor: "HP", Family: "103C_53307F HP EliteDesk", BIOSVersion: &kb.Range{To: "R21 Ver. 02.10.00"}}),
		a("other-vendor", kb.AllowlistMatch{SysVendor: "LENOVO", Family: "103C_53307F HP EliteDesk"}),
		a("other-family", kb.AllowlistMatch{SysVendor: "HP", Family: "ProBook"}),
		a("empty-board", kb.AllowlistMatch{SysVendor: "HP", BoardName: []string{""}}),
	}}
	ids := func(l []*kb.Allowlist) []string {
		var out []string
		for _, e := range l {
			out = append(out, e.ID)
		}
		return out
	}
	applies, undetermined := allowlistsFor(k, reference())
	if want := []string{"by-family", "by-product", "by-board", "in-range"}; !slices.Equal(ids(applies), want) || undetermined != nil {
		t.Errorf("applies %q, want %q; undetermined %q", ids(applies), want, ids(undetermined))
	}
	// Without a BIOS version (or another family's), a range can't be
	// judged: those entries come back apart, never as "no policy".
	r := reference()
	r.System.Firmware = nil
	r.Board.Identity = nil
	applies, undetermined = allowlistsFor(k, r)
	if want := []string{"by-family", "by-product"}; !slices.Equal(ids(applies), want) || !slices.Equal(ids(undetermined), []string{"in-range", "lifted"}) {
		t.Errorf("without a BIOS version or board: applies %q, undetermined %q", ids(applies), ids(undetermined))
	}
	r = reference()
	r.System.Firmware = &report.Firmware{Version: "Q21 Ver. 02.27.00"}
	if _, undetermined = allowlistsFor(k, r); !slices.Equal(ids(undetermined), []string{"in-range", "lifted"}) {
		t.Errorf("another firmware family: undetermined %q", ids(undetermined))
	}
	if applies, undetermined := allowlistsFor(k, &report.Report{}); applies != nil || undetermined != nil {
		t.Errorf("no identity: %+v %+v", applies, undetermined)
	}
}
