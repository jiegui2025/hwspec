// Package resolve fills in human-readable names from the raw hardware IDs
// stored in a report. It runs after every capture and again whenever a
// saved capture is shown, so older files pick up newer ID databases and the
// user's overrides.
//
// A name is only replaced when a database has one; otherwise the existing
// value (e.g. a USB device's own product string) is kept.
package resolve

import (
	"github.com/jiegui2025/hwspec/internal/ids"
	"github.com/jiegui2025/hwspec/internal/report"
)

func Names(r *report.Report) {
	for i := range r.PCI {
		d := &r.PCI[i]
		name(&d.Identity, ids.PCIVendor(d.VendorID), ids.PCIDevice(d.VendorID, d.DeviceID))
		set(&d.Subsystem, ids.PCISubsystem(d.VendorID, d.DeviceID, d.SubVendorID, d.SubDeviceID))
		set(&d.Class, ids.PCIClass(d.ClassCode))
	}

	for i := range r.GPUs {
		g := &r.GPUs[i]
		if dev := pciByAddress(r, g.PCIAddress); dev != nil {
			set(&g.Subsystem, dev.Subsystem)
		}
		set(&g.Chip, ids.PCIDevice(g.VendorID, g.DeviceID))
		model := g.Chip
		if g.VendorID == "1002" && g.Identity != nil {
			if retail := ids.AMDGPUName(g.DeviceID, g.Identity.Revision); retail != "" {
				model = retail
			}
		}
		if model == "" && g.VendorID != "" {
			model = g.VendorID + ":" + g.DeviceID
		}
		name(&g.Identity, ids.PCIVendor(g.VendorID), model)
	}

	for i := range r.USB {
		u := &r.USB[i]
		name(&u.Identity, ids.USBVendor(u.VendorID), ids.USBProduct(u.VendorID, u.ProductID))
		set(&u.Class, ids.USBClass(u.ClassCode))
	}

	for i := range r.Network {
		n := &r.Network[i]
		vendor, model := adapterName(r, n.Bus, n.BusAddress)
		name(&n.Identity, vendor, model)
		// A redacted file has no MAC; keep the vendor recorded at capture.
		if n.MAC != "" {
			n.MACVendor = ids.MACVendor(n.MAC)
		}
	}

	if r.CPU.Family > 0 && r.CPU.Identity != nil {
		codename, uarch := ids.CPUCodename(r.CPU.Identity.Vendor, r.CPU.Family, r.CPU.ModelID, r.CPU.Stepping)
		set(&r.CPU.Codename, codename)
		set(&r.CPU.Microarchitecture, uarch)
	}

	for i := range r.Bluetooth {
		b := &r.Bluetooth[i]
		if b.ManufacturerID > 0 || b.Version != "" {
			set(&b.Manufacturer, ids.BluetoothCompany(uint16(b.ManufacturerID)))
		}
		if b.Address != "" {
			b.AddressVendor = ids.MACVendor(b.Address)
		}
		vendor, model := adapterName(r, b.Bus, b.BusAddress)
		name(&b.Identity, vendor, model)
	}

	for i := range r.Audio {
		for j := range r.Audio[i].Codecs {
			c := &r.Audio[i].Codecs[j]
			// HDA vendor IDs are the PCI vendor in the top 16 bits.
			if len(c.VendorID) == 8 {
				name(&c.Identity, ids.PCIVendor(c.VendorID[:4]), "")
			}
		}
	}

	for i := range r.Displays {
		d := &r.Displays[i]
		name(&d.Identity, ids.PNPVendor(d.ManufacturerID), "")
	}

	for i := range r.Memory.Modules {
		m := &r.Memory.Modules[i]
		if m.Identity != nil {
			raw := m.ManufacturerRaw
			if raw == "" {
				raw = m.Identity.Vendor
			}
			if n, _, ok := ids.MemoryManufacturer(raw); ok {
				m.Identity.Vendor, m.ManufacturerRaw = n, raw
			}
		}
		if n, _, ok := ids.MemoryManufacturer(m.DRAMVendorID); ok {
			m.DRAMVendor = n
		}
	}

	// Rebuilt, not merged: the names now come from these sources, and a
	// saved capture's old entries (possibly with paths) must not linger.
	r.Tool.IDDatabases = map[string]string{}
	for k, v := range ids.Loaded() {
		r.Tool.IDDatabases[string(k)] = v
	}
}

// name sets an identity's vendor and model from database names, keeping
// what's there (e.g. a device's own strings) when the database has none.
func name(p **report.Identity, vendor, model string) {
	if vendor == "" && model == "" {
		return
	}
	id := report.EnsureIdentity(p)
	set(&id.Vendor, vendor)
	set(&id.Model, model)
}

// adapterName returns the vendor and model of the PCI or USB device behind
// an interface.
func adapterName(r *report.Report, bus, addr string) (vendor, model string) {
	var id *report.Identity
	switch bus {
	case "pci":
		if dev := pciByAddress(r, addr); dev != nil {
			id = dev.Identity
		}
	case "usb":
		for i := range r.USB {
			if r.USB[i].Path == addr {
				id = r.USB[i].Identity
			}
		}
	}
	if id == nil {
		return "", ""
	}
	return id.Vendor, id.Model
}

func set(field *string, name string) {
	if name != "" {
		*field = name
	}
}

func pciByAddress(r *report.Report, addr string) *report.PCIDevice {
	for i := range r.PCI {
		if r.PCI[i].Address == addr {
			return &r.PCI[i]
		}
	}
	return nil
}
