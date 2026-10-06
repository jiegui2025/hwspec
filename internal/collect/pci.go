package collect

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jiegui2025/hwspec/internal/edid"
	"github.com/jiegui2025/hwspec/internal/report"
)

const pciDir = "/sys/bus/pci/devices/"

func (c *collector) pci() {
	c.r.PCI = []report.PCIDevice{}
	for _, addr := range list(pciDir) {
		d := pciDir + addr + "/"
		vid, did := hex4(readStr(d+"vendor")), hex4(readStr(d+"device"))
		svid, sdid := hex4(readStr(d+"subsystem_vendor")), hex4(readStr(d+"subsystem_device"))
		class := hex4(readStr(d + "class"))
		dev := report.PCIDevice{
			Address:     addr,
			VendorID:    vid,
			DeviceID:    did,
			SubVendorID: svid,
			SubDeviceID: sdid,
			ClassCode:   class,
			IOMMUGroup:  linkBase(d + "iommu_group"),
			Link:        pcieLink(d),
			Driver:      c.driverAt(pciDir + addr),
		}
		// The kernel exports a firmware label with "index" when it came
		// from SMBIOS type 41 (an onboard device), with "acpi_index" when
		// it came from an ACPI _DSM name (drivers/pci/pci-label.c).
		if dev.Label = readStr(d + "label"); dev.Label != "" {
			switch {
			case exists(d + "index"):
				dev.LabelSource = "smbios"
			case exists(d + "acpi_index"):
				dev.LabelSource = "acpi"
			}
		}
		if parent, ok := pciParent(addr); ok {
			dev.Parent = parent
		} else {
			c.warn("pci %s: parent unknown: its sysfs path isn't under a PCI bus", addr)
		}
		// Names are filled in by resolve; the revision is the device's own.
		if rev := hex4(readStr(d + "revision")); rev != "" {
			dev.Identity = &report.Identity{Revision: rev}
		}
		c.r.PCI = append(c.r.PCI, dev)
	}
	if len(c.r.PCI) == 0 {
		c.warn("pci: no devices under %s", pciDir)
	}
}

// pciAddress is a PCI device's sysfs name, domain:bus:device.function,
// and pciBus a root bus's, pci<domain>:<bus>. Domains have 4 hex digits,
// or more behind Intel VMD (10000:e0:06.0).
var (
	pciAddress = regexp.MustCompile(`^[0-9a-f]{4,}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$`)
	pciBus     = regexp.MustCompile(`^pci[0-9a-f]{4,}:[0-9a-f]{2}$`)
)

// pciParent is the PCI device a device sits behind, from its sysfs path:
// the bridge or root port above it; for a device on a root bus that a
// PCI device hosts (Intel VMD), that device; "" on a platform root bus.
// ok is false when the path doesn't resolve to a PCI bus at all.
func pciParent(addr string) (parent string, ok bool) {
	path := realPath(pciDir + addr)
	if path == "" {
		return "", false
	}
	up := filepath.Dir(path)
	switch base := filepath.Base(up); {
	case pciAddress.MatchString(base):
		return base, true
	case pciBus.MatchString(base):
		if host := filepath.Base(filepath.Dir(up)); pciAddress.MatchString(host) {
			return host, true
		}
		return "", true
	}
	return "", false
}

func pcieLink(dir string) *report.PCIeLink {
	speed := readStr(dir + "current_link_speed")
	if speed == "" || strings.HasPrefix(speed, "Unknown") {
		return nil
	}
	w, _ := readInt32(dir + "current_link_width")
	mw, _ := readInt32(dir + "max_link_width")
	return &report.PCIeLink{
		Speed:    speed,
		Width:    w,
		MaxSpeed: readStr(dir + "max_link_speed"),
		MaxWidth: mw,
	}
}

// gpus picks display controllers (PCI class 03) from the PCI list and adds
// DRM details: card name, VRAM (amdgpu) and output connectors. Names are
// filled in later by the resolve package.
func (c *collector) gpus() {
	c.r.GPUs = []report.GPU{}
	cards := drmCards()
	for _, dev := range c.r.PCI {
		if !strings.HasPrefix(dev.ClassCode, "03") {
			continue
		}
		g := report.GPU{
			PCIAddress: dev.Address,
			VendorID:   dev.VendorID,
			DeviceID:   dev.DeviceID,
			Driver:     dev.Driver,
			BootVGA:    readStr(pciDir+dev.Address+"/boot_vga") == "1",
			Link:       dev.Link,
			Outputs:    []string{},
			Firmware:   gpuFirmware(dev.Address),
		}
		if dev.Identity != nil {
			g.Identity = &report.Identity{Revision: dev.Identity.Revision}
		}
		g.VRAMBytes = readUint(pciDir + dev.Address + "/mem_info_vram_total") // amdgpu
		// Sorted, and the first card only: two cards on one device would
		// otherwise overwrite each other in map order.
		for _, card := range slices.Sorted(maps.Keys(cards)) {
			if cards[card] != dev.Address || g.DRMCard != "" {
				continue
			}
			g.DRMCard = card
			g.Clocks = c.gpuClocks(card, dev.Address)
			for _, conn := range list("/sys/class/drm") {
				if after, ok := strings.CutPrefix(conn, card+"-"); ok {
					g.Outputs = append(g.Outputs, after)
				}
			}
		}
		c.r.GPUs = append(c.r.GPUs, g)
	}
}

// gpuFirmware reads the video BIOS version where the driver exposes it
// without root: amdgpu in sysfs, the NVIDIA driver under /proc.
func gpuFirmware(addr string) *report.Firmware {
	if fw := firmwareVersion(readStr(pciDir+addr+"/vbios_version"), "vbios"); fw != nil {
		return fw
	}
	for line := range strings.SplitSeq(readStr("/proc/driver/nvidia/gpus/"+addr+"/information"), "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "Video BIOS" {
			return firmwareVersion(v, "vbios")
		}
	}
	return nil
}

// gpuClocks reads the graphics core's hardware clock range and its
// measured clock where the driver exposes them to users:
//   - i915: gt_RPn_freq_mhz and gt_RP0_freq_mhz, the hardware's lowest and
//     highest (read-only; gt_min/max_freq_mhz are adjustable limits), and
//     gt_act_freq_mhz, the measured clock (0 while the GPU idles in RC6;
//     gt_cur_freq_mhz is the requested one). On multi-GT parts the
//     card-level files cover all GTs.
//   - amdgpu: the levels in pp_dpm_sclk, the active one starred
//     (https://docs.kernel.org/gpu/amdgpu/thermal.html); a starred "S:"
//     line is the deep-sleep clock: the actual clock, not a level.
//
// Files a driver doesn't have give no clocks and no warning; unreadable or
// inconsistent ones give no clocks and a warning, never a guess.
func (c *collector) gpuClocks(card, addr string) *report.GPUClocks {
	if g, isI915 := c.i915Clocks("/sys/class/drm/"+card+"/", addr); isI915 {
		return g
	}
	return c.amdgpuClocks(addr)
}

// readMHz reads a whole number of MHz: present is false when the file
// doesn't exist. Errors name the file.
func readMHz(path string) (mhz int, present bool, err error) {
	s, err := readStrErr(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, true, err
	}
	if mhz, err = strconv.Atoi(s); err != nil {
		return 0, true, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return mhz, true, nil
}

func (c *collector) i915Clocks(base, addr string) (*report.GPUClocks, bool) {
	lo, hasLo, errLo := readMHz(base + "gt_RPn_freq_mhz")
	hi, hasHi, errHi := readMHz(base + "gt_RP0_freq_mhz")
	switch {
	case !hasLo && !hasHi:
		return nil, false
	case errLo != nil || errHi != nil:
		c.warn("gpu %s clocks: %v", addr, errors.Join(errLo, errHi))
		return nil, true
	case !hasLo:
		c.warn("gpu %s clocks: i915 has gt_RP0_freq_mhz but no gt_RPn_freq_mhz", addr)
		return nil, true
	case !hasHi:
		c.warn("gpu %s clocks: i915 has gt_RPn_freq_mhz but no gt_RP0_freq_mhz", addr)
		return nil, true
	case lo <= 0 || lo > hi:
		c.warn("gpu %s clocks: i915 reports %d–%d MHz, not a range", addr, lo, hi)
		return nil, true
	}
	g := &report.GPUClocks{MinFreqMHz: lo, MaxFreqMHz: hi, Source: "i915"}
	act, hasAct, err := readMHz(base + "gt_act_freq_mhz")
	switch {
	case err != nil:
		c.warn("gpu %s clocks: actual clock: %v", addr, err)
	case hasAct && act != 0 && (act < lo || act > hi):
		c.warn("gpu %s clocks: actual clock %d MHz is outside %d–%d MHz; left out", addr, act, lo, hi)
	case hasAct:
		g.ActualFreqMHz = &act
	}
	return g, true
}

// dpmLevel is one line of pp_dpm_sclk: "1: 1000Mhz *", or "S: 19Mhz *" in
// deep sleep.
var dpmLevel = regexp.MustCompile(`^(S|[0-9]+): ([0-9]+)[Mm][Hh][Zz]( \*)?$`)

func (c *collector) amdgpuClocks(addr string) *report.GPUClocks {
	text, err := readStrErr(pciDir + addr + "/pp_dpm_sclk")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		// e.g. EPERM while the GPU is runtime-suspended
		c.warn("gpu %s clocks: %v", addr, err)
		return nil
	}
	var levels []int
	var actual *int
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := dpmLevel.FindStringSubmatch(line)
		if m == nil {
			c.warn("gpu %s clocks: unexpected pp_dpm_sclk line %q", addr, line)
			return nil
		}
		mhz, err := strconv.Atoi(m[2]) // digits only, but may overflow
		if err != nil {
			c.warn("gpu %s clocks: pp_dpm_sclk line %q: %v", addr, line, err)
			return nil
		}
		if m[3] != "" {
			if actual != nil {
				c.warn("gpu %s clocks: pp_dpm_sclk marks two levels active", addr)
				return nil
			}
			actual = &mhz
		}
		if m[1] != "S" {
			levels = append(levels, mhz)
		}
	}
	if len(levels) == 0 || slices.Min(levels) <= 0 {
		c.warn("gpu %s clocks: pp_dpm_sclk lists no usable levels", addr)
		return nil
	}
	g := &report.GPUClocks{MinFreqMHz: slices.Min(levels), MaxFreqMHz: slices.Max(levels), Source: "amdgpu"}
	if actual != nil && *actual > g.MaxFreqMHz {
		c.warn("gpu %s clocks: active level %d MHz is above %d MHz; left out", addr, *actual, g.MaxFreqMHz)
	} else {
		g.ActualFreqMHz = actual
	}
	return g
}

// drmCards maps DRM card names (card0, card1) to their PCI addresses.
func drmCards() map[string]string {
	out := map[string]string{}
	for _, n := range list("/sys/class/drm") {
		if !strings.HasPrefix(n, "card") || strings.Contains(n, "-") {
			continue
		}
		if bus, addr := busOf("/sys/class/drm/" + n + "/device"); bus == "pci" {
			out[n] = addr
		}
	}
	return out
}

// displays decodes the EDID of every connected monitor.
func (c *collector) displays() {
	c.r.Displays = []report.Display{}
	for _, conn := range list("/sys/class/drm") {
		if !strings.Contains(conn, "-") || readStr("/sys/class/drm/"+conn+"/status") != "connected" {
			continue
		}
		disp := report.Display{Connector: conn}
		raw, err := readFile("/sys/class/drm/" + conn + "/edid")
		if err == nil && len(raw) > 0 {
			if e, err := edid.Parse(raw); err == nil {
				disp.ManufacturerID = e.ManufacturerID
				id := &report.Identity{
					Model:      e.Name,
					PartNumber: fmt.Sprintf("%04X", e.ProductCode),
					Serial:     e.SerialText,
				}
				if id.Serial == "" && e.SerialNumber != 0 && e.SerialNumber != 0x01010101 {
					id.Serial = strconv.FormatUint(uint64(e.SerialNumber), 10)
				}
				// Week 255 means the year is a model year, not a manufacture date.
				if e.Week == 255 {
					disp.ModelYear = e.Year
				} else {
					id.ManufactureDate, id.ManufactureDateSource = isoWeek(e.Year, e.Week), "edid"
				}
				disp.Identity = id
				disp.WidthMM, disp.HeightMM = e.WidthMM, e.HeightMM
				disp.DiagonalIn = e.DiagonalInches()
				disp.NativeWidth, disp.NativeHeight = e.NativeWidth, e.NativeHeight
				disp.NativeRefreshHz = e.NativeRefreshHz
				disp.EDIDVersion = e.Version
			} else {
				c.warn("display %s: %v", conn, err)
			}
		}
		c.r.Displays = append(c.r.Displays, disp)
	}
}
