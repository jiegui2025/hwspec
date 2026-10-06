package collect

import (
	"github.com/jiegui2025/hwspec/internal/report"
	"github.com/jiegui2025/hwspec/internal/smbios"
)

// smbiosTable is the raw SMBIOS table, read once per capture.
type smbiosTable struct {
	structs []smbios.Structure
	err     error
}

// smbiosStructures reads and parses /sys/firmware/dmi/tables/DMI (root
// only) on first use, for every collector that needs it.
func (c *collector) smbiosStructures() ([]smbios.Structure, error) {
	if c.smbios == nil {
		table, err := readFile("/sys/firmware/dmi/tables/DMI")
		c.smbios = &smbiosTable{err: err}
		if err == nil {
			c.smbios.structs = smbios.Parse(table)
		}
	}
	return c.smbios.structs, c.smbios.err
}

// firmwareTables records the firmware's processor sockets, expansion
// slots and onboard devices, as it states them. Without the table (no
// root) they are left out: smbiosModules' warning names all of them.
func (c *collector) firmwareTables() {
	structs, err := c.smbiosStructures()
	if err != nil {
		return
	}
	for _, p := range smbios.Processors(structs) {
		c.r.CPU.Packages = append(c.r.CPU.Packages, report.CPUPackage{
			Designation: p.Designation, Package: p.Package, Mounting: p.Mounting, Populated: p.Populated,
		})
	}
	for _, s := range smbios.Slots(structs) {
		c.r.Board.Slots = append(c.r.Board.Slots, report.Slot{
			Designation: s.Designation, Type: s.Type, Width: s.Width, Usage: s.Usage,
			Length: s.Length, ID: s.ID, Address: s.Address,
		})
	}
	for _, d := range smbios.OnboardDevices(structs) {
		c.r.Board.OnboardDevices = append(c.r.Board.OnboardDevices, report.OnboardDevice{
			Designation: d.Designation, Type: d.Type, Enabled: d.Enabled, Instance: d.Instance, Address: d.Address,
		})
	}
}
