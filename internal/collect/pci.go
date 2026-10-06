package collect

import (
	"fmt"
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
		for card, addr := range cards {
			if addr != dev.Address {
				continue
			}
			g.DRMCard = card
			for _, conn := range list("/sys/class/drm") {
				if strings.HasPrefix(conn, card+"-") {
					g.Outputs = append(g.Outputs, strings.TrimPrefix(conn, card+"-"))
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
	for _, line := range strings.Split(readStr("/proc/driver/nvidia/gpus/"+addr+"/information"), "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "Video BIOS" {
			return firmwareVersion(v, "vbios")
		}
	}
	return nil
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
