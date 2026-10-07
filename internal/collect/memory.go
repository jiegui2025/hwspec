package collect

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode"

	"github.com/jiegui2025/hwspec/internal/report"
	"github.com/jiegui2025/hwspec/internal/smbios"
	"github.com/jiegui2025/hwspec/internal/spd"
)

func (c *collector) memory() {
	m := &c.r.Memory
	m.Modules = []report.MemoryModule{}
	for line := range strings.SplitSeq(readStr("/proc/meminfo"), "\n") {
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
	structs, err := c.smbiosStructures()
	if err != nil {
		if !os.IsNotExist(err) {
			c.warnRead("SMBIOS table (memory modules and slots, CPU sockets, expansion slots, onboard devices)", err)
		}
		return
	}
	for _, a := range smbios.MemoryArrays(structs) {
		m.MaxCapacityBytes += a.MaxCapacityBytes
		m.Slots += a.Slots
		if m.ECC == "" {
			m.ECC = a.ErrorCorrection
		}
	}
	for _, d := range smbios.MemoryDevices(structs) {
		m.SlotUsage = append(m.SlotUsage, report.MemorySlot{
			Locator: d.Locator, BankLocator: d.BankLocator, Populated: d.Installed, FormFactor: d.FormFactor,
		})
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

// readSPD reads only the bytes spd.Parse uses into a zeroed image of the
// full size. EEPROM reads go over SMBus at about 0.16 ms a byte, so a whole
// DDR4 image costs about 80 ms per module, the spans about 8 ms. An image
// too short for its type comes back short, for spd.Parse to report. A
// module that doesn't answer within slowAnswer is given up on.
func readSPD(path string) ([]byte, error) {
	full, err := under(path)
	if err != nil {
		return nil, err
	}
	return within(slowAnswer, func() ([]byte, error) { return readSPDFile(full) })
}

func readSPDFile(full string) ([]byte, error) {
	f, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	head := make([]byte, 3)
	n, err := f.ReadAt(head, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	size, spans, ok := spd.Layout(head[2])
	if n < len(head) || !ok {
		return head[:n], nil // too short, or a type spd doesn't decode
	}
	// sysfs gives the EEPROM's size; a smaller file is a truncated image.
	if st, err := f.Stat(); err == nil && st.Size() > 0 && st.Size() < int64(size) {
		return head, nil
	}
	img := make([]byte, size)
	for _, s := range spans {
		got, err := f.ReadAt(img[s.Off:s.Off+s.Len], int64(s.Off))
		if errors.Is(err, io.EOF) {
			return img[:s.Off+got], nil
		}
		if err != nil {
			return nil, err
		}
	}
	return img, nil
}

// spdModules adds what each module's own SPD EEPROM says: maker (as a JEDEC
// code), DRAM maker, part number, serial, manufacture date. Matched to the
// SMBIOS modules by serial or part number; without SMBIOS (no root) the
// SPD modules are listed on their own.
func (c *collector) spdModules() {
	m := &c.r.Memory
	var found []*spd.Info
	var where []string
	exposed := 0
	for _, drv := range spdDrivers {
		for _, dev := range list("/sys/bus/i2c/drivers/" + drv) {
			if !strings.Contains(dev, "-") {
				continue // bind, unbind, module, uevent
			}
			exposed++
			raw, err := readSPD("/sys/bus/i2c/drivers/" + drv + "/" + dev + "/eeprom")
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
				// match (a different serial format, two identical modules)
				// adds no module, but its details are lost, so say so.
				c.warn("memory: %s (part %q) doesn't match a firmware-listed module; its manufacture date and DRAM maker are left out", where[k], info.PartNumber)
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
	c.missingSPD(exposed, m.Modules[:smbiosCount], found)
}

// missingSPD says when the kernel exposed SPD EEPROMs for some modules but
// not all (#203). The likely cause: it registers them only at 0x50 + n
// for n below the firmware's slot count, and stops at as many modules as
// the firmware lists (drivers/i2c/i2c-smbus.c, i2c_register_spd), so a
// module elsewhere on the bus has no EEPROM device and its maker and
// manufacture date are missing. With the firmware's module list (--full)
// the modules that can have an SPD are counted: soldered memory has none.
// Without it, the SPD modules' total is compared with the usable memory,
// unless an SPD's size couldn't be decoded. No EEPROM at all says
// nothing: many platforms expose none.
func (c *collector) missingSPD(exposed int, firmware []report.MemoryModule, found []*spd.Info) {
	const why = "a likely cause: the kernel registers SPD EEPROMs only at 0x50 + slot index"
	switch {
	case exposed == 0:
	case len(firmware) > 0:
		socketed := 0
		for i := range firmware {
			if moduleMounting(&firmware[i]).Kind != smbios.MountOnboard {
				socketed++
			}
		}
		if exposed < socketed {
			c.warn("memory: the kernel exposed SPD for %d of the %d modules that can have one (%s); the others' manufacture date and DRAM maker are left out", exposed, socketed, why)
		}
	case len(found) == exposed:
		var total uint64
		for _, info := range found {
			if info.SizeBytes == 0 {
				return // a size the SPD didn't give can't be compared
			}
			total += info.SizeBytes
		}
		if total < c.r.Memory.TotalBytes {
			c.warn("memory: SPD modules total %s, less than the %s usable: some modules' SPD isn't exposed (%s), so they aren't listed; --full lists every module",
				gib(total), gib(c.r.Memory.TotalBytes), why)
		}
	}
}

// gib writes a byte count in GiB with one decimal.
func gib(n uint64) string { return strconv.FormatFloat(float64(n)/(1<<30), 'f', 1, 64) + " GiB" }

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
// systems with ECC memory and an EDAC driver. A module can have several
// EDAC entries (one per rank); their counts are added up.
func (c *collector) edac() {
	const base = "/sys/devices/system/edac/mc/"
	type counts struct{ ce, ue uint64 }
	found := map[int]*counts{}
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
			i, err := moduleByLabel(c.r.Memory.Modules, label)
			if err != nil {
				c.warn("memory: EDAC %s/%s (%q) %v; %d corrected, %d uncorrected errors", mc, dimm, label, err, ce, ue)
				continue
			}
			if found[i] == nil {
				found[i] = &counts{}
			}
			found[i].ce += ce
			found[i].ue += ue
		}
	}
	for i, n := range found {
		h := &report.Health{Status: report.StatusOK, Source: report.HealthFromEDAC}
		metric(h, report.MetricECCCorrected, float64(n.ce), true)
		metric(h, report.MetricECCUncorrected, float64(n.ue), true)
		switch {
		case n.ue > 0:
			h.Status = report.StatusFailing
			h.Reasons = append(h.Reasons, fmt.Sprintf("%d uncorrectable memory errors since boot: replace this module", n.ue))
		case n.ce > 0:
			h.Status = report.StatusWarning
			h.Reasons = append(h.Reasons, fmt.Sprintf("%d corrected memory errors since boot: the module may be failing", n.ce))
		}
		c.r.Memory.Modules[i].Health = h
	}
}

// moduleByLabel finds the module an EDAC DIMM label names. ghes_edac
// labels are "<bank locator> <locator>" ("P0 CHANNEL A DIMM 1"); others
// end with a BIOS name ("..._DIMM#0 DIMM_A1"). The label must end with the
// module's bank locator and locator, or failing that its locator alone, on
// word boundaries ("A1" doesn't match "A10"). Several boards repeat a
// locator in every channel, so more than one candidate is an error, never a
// guess: the result tells the user which module to replace.
func moduleByLabel(mods []report.MemoryModule, label string) (int, error) {
	l := words(label)
	if len(l) == 0 {
		return -1, errors.New("has no label")
	}
	endsWith := func(loc []string) bool {
		want := strings.Join(loc, "")
		if want == "" {
			return false
		}
		// Join the label's last k words, so "DIMM_A1" matches "DIMMA1"
		// without matching inside a word.
		for k := 1; k <= len(l); k++ {
			if strings.Join(l[len(l)-k:], "") == want {
				return true
			}
		}
		return false
	}
	for _, full := range []bool{true, false} {
		match := -1
		for i, m := range mods {
			loc := words(m.Locator)
			if full {
				if m.BankLocator == "" {
					continue
				}
				loc = append(words(m.BankLocator), loc...)
			}
			if !endsWith(loc) {
				continue
			}
			if match >= 0 {
				return -1, errors.New("matches several modules")
			}
			match = i
		}
		if match >= 0 {
			return match, nil
		}
	}
	return -1, errors.New("doesn't match a module")
}

// words splits s into lower-case alphanumeric runs.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}
