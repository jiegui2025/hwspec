package collect

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/jiegui2025/hwspec/internal/report"
	"github.com/jiegui2025/hwspec/internal/smbios"
	"github.com/jiegui2025/hwspec/internal/spd"
)

func (c *collector) memory() {
	m := &c.r.Memory
	m.Modules = []report.MemoryModule{}
	for _, line := range strings.Split(readStr("/proc/meminfo"), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		kb, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
		if err != nil {
			continue
		}
		switch k {
		case "MemTotal":
			m.TotalBytes = kb << 10
		case "SwapTotal":
			m.SwapBytes = kb << 10
		}
	}
	c.smbiosModules()
	c.spdModules()
	c.edac()
}

// smbiosModules lists installed modules from the firmware's SMBIOS table
// (root only).
func (c *collector) smbiosModules() {
	m := &c.r.Memory
	table, err := os.ReadFile(p("/sys/firmware/dmi/tables/DMI"))
	if err != nil {
		if !os.IsNotExist(err) {
			c.warnRead("memory modules (SMBIOS)", err)
		}
		return
	}
	structs := smbios.Parse(table)
	for _, a := range smbios.MemoryArrays(structs) {
		m.MaxCapacityBytes += a.MaxCapacityBytes
		m.Slots += a.Slots
		if m.ECC == "" {
			m.ECC = a.ErrorCorrection
		}
	}
	for _, d := range smbios.MemoryDevices(structs) {
		if d.SizeBytes == 0 {
			continue // empty slot
		}
		m.InstalledBytes += d.SizeBytes
		mod := report.MemoryModule{
			Locator:       d.Locator,
			BankLocator:   d.BankLocator,
			SizeBytes:     d.SizeBytes,
			Type:          d.Type,
			FormFactor:    d.FormFactor,
			SpeedMTs:      d.SpeedMTs,
			ConfiguredMTs: d.ConfiguredMTs,
			DataWidth:     d.DataWidth,
			TotalWidth:    d.TotalWidth,
			Rank:          d.Rank,
			VoltageMV:     d.ConfiguredMV,
		}
		// The firmware's manufacturer string may be a name or a JEDEC code;
		// resolve decodes codes into Identity.Vendor.
		if id := (&report.Identity{Vendor: d.Manufacturer, PartNumber: d.PartNumber, Serial: d.Serial}); !id.Empty() {
			mod.Identity = id
		}
		m.Modules = append(m.Modules, mod)
	}
}

// spdDrivers are the kernel drivers that expose module EEPROMs.
var spdDrivers = []string{"ee1004", "spd5118"}

// spdModules adds what each module's own SPD EEPROM says: maker (as a JEDEC
// code), DRAM maker, part number, serial, manufacture date. Matched to the
// SMBIOS modules by serial or part number; without SMBIOS (no root) the
// SPD modules are listed on their own.
func (c *collector) spdModules() {
	m := &c.r.Memory
	var found []*spd.Info
	var where []string
	for _, drv := range spdDrivers {
		for _, dev := range list("/sys/bus/i2c/drivers/" + drv) {
			if !strings.Contains(dev, "-") {
				continue // bind, unbind, module, uevent
			}
			raw, err := os.ReadFile(p("/sys/bus/i2c/drivers/" + drv + "/" + dev + "/eeprom"))
			if err != nil {
				c.warnRead("memory module SPD "+dev, err)
				continue
			}
			info, err := spd.Parse(raw)
			if err != nil {
				c.warn("memory module SPD %s: %v", dev, err)
				continue
			}
			found = append(found, info)
			where = append(where, "SPD "+dev)
		}
	}
	smbiosCount := len(m.Modules)
	used := map[int]bool{}
	for k, info := range found {
		i := matchModule(m.Modules[:smbiosCount], info, used)
		if i < 0 {
			if smbiosCount > 0 {
				// The firmware's list is authoritative; an SPD it doesn't
				// match (e.g. a different serial format) adds no module.
				continue
			}
			m.Modules = append(m.Modules, report.MemoryModule{
				Locator:    where[k],
				SizeBytes:  info.SizeBytes,
				Type:       info.Type,
				FormFactor: info.FormFactor,
			})
			i = len(m.Modules) - 1
		}
		used[i] = true
		applySPD(&m.Modules[i], info)
	}
}

// matchModule finds the SMBIOS module an SPD describes: by serial, then by
// part number when that is unambiguous.
func matchModule(mods []report.MemoryModule, info *spd.Info, used map[int]bool) int {
	byPart := -1
	for i, mod := range mods {
		if used[i] || mod.Identity == nil {
			continue
		}
		if info.Serial != "" && strings.EqualFold(strings.TrimLeft(mod.Identity.Serial, "0"), strings.TrimLeft(info.Serial, "0")) {
			return i
		}
		if info.PartNumber != "" && strings.EqualFold(mod.Identity.PartNumber, info.PartNumber) {
			if byPart >= 0 {
				byPart = -2 // ambiguous
			} else if byPart == -1 {
				byPart = i
			}
		}
	}
	if byPart >= 0 {
		return byPart
	}
	return -1
}

// applySPD fills what SMBIOS left out; where both report something, the
// firmware's value is kept.
func applySPD(mod *report.MemoryModule, info *spd.Info) {
	id := report.EnsureIdentity(&mod.Identity)
	if id.Vendor == "" {
		id.Vendor = info.ModuleVendorCode // decoded by resolve
	}
	if id.PartNumber == "" {
		id.PartNumber = info.PartNumber
	}
	if id.Serial == "" {
		id.Serial = info.Serial
	}
	if id.Revision == "" {
		id.Revision = info.Revision
	}
	if d := isoWeek(info.ManufactureYear, info.ManufactureWeek); d != "" {
		id.ManufactureDate, id.ManufactureDateSource = d, "spd"
	}
	mod.DRAMVendorID = info.DRAMVendorCode
	if mod.SizeBytes == 0 {
		mod.SizeBytes = info.SizeBytes
	}
}

// edac adds corrected and uncorrected memory error counts per module on
// systems with ECC memory and an EDAC driver.
func (c *collector) edac() {
	const base = "/sys/devices/system/edac/mc/"
	for _, mc := range list(base) {
		if !strings.HasPrefix(mc, "mc") {
			continue
		}
		for _, dimm := range list(base + mc) {
			if !strings.HasPrefix(dimm, "dimm") && !strings.HasPrefix(dimm, "rank") {
				continue
			}
			d := base + mc + "/" + dimm + "/"
			ce, err1 := strconv.ParseUint(readStr(d+"dimm_ce_count"), 10, 64)
			ue, err2 := strconv.ParseUint(readStr(d+"dimm_ue_count"), 10, 64)
			if err1 != nil || err2 != nil {
				continue
			}
			label := readStr(d + "dimm_label")
			mod := c.moduleByLabel(label)
			if mod == nil {
				c.warn("memory: EDAC %s/%s (%q) doesn't match a module; %d corrected, %d uncorrected errors", mc, dimm, label, ce, ue)
				continue
			}
			h := &report.Health{Status: report.StatusOK, Source: "edac"}
			metric(h, report.MetricECCCorrected, float64(ce), true)
			metric(h, report.MetricECCUncorrected, float64(ue), true)
			switch {
			case ue > 0:
				h.Status = report.StatusFailing
				h.Reasons = append(h.Reasons, fmt.Sprintf("%d uncorrectable memory errors since boot: replace this module", ue))
			case ce > 0:
				h.Status = report.StatusWarning
				h.Reasons = append(h.Reasons, fmt.Sprintf("%d corrected memory errors since boot: the module may be failing", ce))
			}
			mod.Health = h
		}
	}
}

// moduleByLabel matches an EDAC DIMM label ("CPU_SrcID#0_MC#0_Chan#0_DIMM#0",
// or a BIOS-provided name like "DIMM_A1") to a module's locator.
func (c *collector) moduleByLabel(label string) *report.MemoryModule {
	norm := func(s string) string {
		return strings.ToLower(strings.NewReplacer(" ", "", "_", "", "-", "").Replace(s))
	}
	l := norm(label)
	if l == "" {
		return nil
	}
	for i := range c.r.Memory.Modules {
		mod := &c.r.Memory.Modules[i]
		if loc := norm(mod.Locator); loc != "" && (strings.HasSuffix(l, loc) || strings.Contains(l, loc)) {
			return mod
		}
	}
	return nil
}
