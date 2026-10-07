package collect

import (
	"strings"
	"syscall"
	"testing"

	"github.com/jiegui2025/hwspec/internal/report"
)

// Each part that can lack a firmware version says why, in its own words.
func TestFirmwareReasons(t *testing.T) {
	asMachine(t, "x86_64", 1000)
	if got := microcodeReason(); got != "the kernel's /proc/cpuinfo gives no microcode revision" {
		t.Errorf("x86: %q", got)
	}
	uname = func() (string, string) { return "", "aarch64" }
	if got := microcodeReason(); got != "the kernel doesn't report CPU microcode on aarch64" {
		t.Errorf("arm: %q", got)
	}
	if fw := firmwareOrUnknown(&report.Firmware{Version: "1", Source: "x"}, "why"); fw.Version != "1" || fw.Status != "" {
		t.Errorf("a known version replaced: %+v", fw)
	}
}

// The system's firmware: from DMI, or why not.
func TestSystemFirmwareIsExplained(t *testing.T) {
	file, _ := fakeRoot(t)
	c := &collector{r: &report.Report{}}
	c.dmi()
	if fw := c.r.System.Firmware; fw == nil || fw.Reason != "no DMI (SMBIOS) tables, where the kernel reports the system firmware version" {
		t.Errorf("no DMI: %+v", fw)
	}
	file(dmiDir+"sys_vendor", "HP\n")
	c = &collector{r: &report.Report{}}
	c.dmi()
	if fw := c.r.System.Firmware; fw == nil || fw.Status != report.FirmwareUnknown || fw.Reason != "the DMI tables give no BIOS version" {
		t.Errorf("DMI without bios_version: %+v", fw)
	}
}

// A USB device without a release number says so.
func TestUSBFirmwareIsExplained(t *testing.T) {
	file, _ := fakeRoot(t)
	file("/sys/bus/usb/devices/1-1/idVendor", "046d")
	file("/sys/bus/usb/devices/1-1/idProduct", "c52b")
	c := &collector{r: &report.Report{}}
	c.usb()
	if len(c.r.USB) != 1 || c.r.USB[0].Firmware.Known() || c.r.USB[0].Firmware.Reason != "bcdDevice can't be read" {
		t.Errorf("usb = %+v", c.r.USB)
	}
}

// A GPU's firmware reason follows its driver: none bound, a VBIOS version
// the driver gives or can't, the NVIDIA driver's information file, and
// Intel's GuC, HuC and DMC, which hwspec doesn't read yet.
func TestGPUFirmware(t *testing.T) {
	const addr = "0000:01:00.0"
	for _, c := range []struct {
		name   string
		driver string
		setup  func(file func(string, string))
		want   string
	}{
		{"no driver", "", nil, "unknown: no driver is bound"},
		{"amdgpu", "amdgpu", func(f func(string, string)) { f(pciDir+addr+"/vbios_version", "113-D4120100-100\n") }, "113-D4120100-100"},
		{"amdgpu, a placeholder", "amdgpu", func(f func(string, string)) { f(pciDir+addr+"/vbios_version", "N/A") }, "unknown: the driver reports no VBIOS version"},
		{"amdgpu, unreadable", "amdgpu", func(f func(string, string)) {
			f(pciDir+addr+"/vbios_version", "x")
			unreadable = map[string]error{pciDir + addr + "/vbios_version": syscall.EIO}
		}, "unknown: vbios_version can't be read: open "},
		{"nvidia", "nvidia", func(f func(string, string)) {
			f("/proc/driver/nvidia/gpus/"+addr+"/information", "Model: \t\t NVIDIA RTX\nVideo BIOS: \t 94.06.2f.00.9a\n")
		}, "94.06.2f.00.9a"},
		{"nvidia without the line", "nvidia", func(f func(string, string)) {
			f("/proc/driver/nvidia/gpus/"+addr+"/information", "Model: \t\t NVIDIA RTX\n")
		}, "unknown: the NVIDIA driver gives no Video BIOS version"},
		{"nvidia, unreadable", "nvidia", func(f func(string, string)) {
			f("/proc/driver/nvidia/gpus/"+addr+"/information", "x")
			unreadable = map[string]error{"/proc/driver/nvidia/gpus/" + addr + "/information": syscall.EIO}
		}, "unknown: the NVIDIA driver's information can't be read: open "},
		{"i915", "i915", nil, "unknown: an Intel GPU has no video BIOS version; its GuC and HuC are firmware components (--full)"},
		{"xe", "xe", nil, "unknown: an Intel GPU has no video BIOS version; its GuC and HuC are firmware components (--full)"},
		{"nouveau", "nouveau", nil, "unknown: the nouveau driver doesn't expose a firmware version"},
	} {
		t.Run(c.name, func(t *testing.T) {
			file, _ := fakeRoot(t)
			if c.setup != nil {
				c.setup(file)
			}
			d := report.PCIDevice{Address: addr}
			if c.driver != "" {
				d.Driver = &report.Driver{Name: c.driver}
			}
			got := ""
			switch fw := gpuFirmware(d); {
			case fw.Known():
				got = fw.Version
			default:
				got = "unknown: " + fw.Reason
			}
			if !strings.HasPrefix(got, c.want) {
				t.Errorf("%q, want %q", got, c.want)
			}
		})
	}
}

// Block devices without a device link (md RAID, zvols) aren't drives and
// get no firmware block; a drive that reports 0 says so.
func TestDiskFirmware(t *testing.T) {
	file, link := fakeRoot(t)
	asMachine(t, "x86_64", 1000)
	file("/sys/devices/virtual/block/md127/size", "2000")
	link("/sys/block/md127", "../devices/virtual/block/md127")
	dev := "/sys/devices/pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0"
	file(dev+"/block/sda/size", "2000")
	link("/sys/block/sda", "../devices/pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0/block/sda")
	link(dev+"/block/sda/device", "../..")
	file(dev+"/rev", "0")
	c := &collector{r: &report.Report{}}
	c.storage()
	got := map[string]*report.Firmware{}
	for _, d := range c.r.Storage {
		got[d.Name] = d.Firmware
	}
	if fw, ok := got["md127"]; !ok || fw != nil {
		t.Errorf("md127: listed %v, firmware %+v", ok, fw)
	}
	if fw := got["sda"]; fw == nil || fw.Reason != `the drive reports "0"` {
		t.Errorf("sda: %+v", fw)
	}
	if fw := diskFirmware(dev+"/block/sda", "mmc"); fw.Reason != "the kernel doesn't expose this drive's firmware revision" {
		t.Errorf("an eMMC without fwrev: %+v", fw)
	}
}

// Virtual (virtio, Xen) disks and eMMC/SD cards have no SMART, as root or not, so they
// don't ask for --full (#143); a SATA disk does.
func TestNoNeedsRootWarningForDisksWithoutSMART(t *testing.T) {
	for _, c := range []struct {
		name, target string
		warn         bool
	}{
		{"vda", "../devices/pci0000:00/0000:00:04.0/virtio1/block/vda", false},
		{"mmcblk0", "../devices/platform/sdhci/mmc_host/mmc0/mmc0:0001/block/mmcblk0", false},
		{"xvda", "../devices/vbd-51712/block/xvda", false},
		{"sda", "../devices/pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0/block/sda", true},
	} {
		file, link := fakeRoot(t)
		asMachine(t, "x86_64", 1000)
		dev := "/sys/" + strings.TrimPrefix(c.target, "../")
		file(dev+"/size", "2000")
		link("/sys/block/"+c.name, c.target)
		link(dev+"/device", "../..")
		col := &collector{r: &report.Report{}}
		col.storage()
		if got := hasWarning(col.r, "drive health (SMART): needs root"); got != c.warn {
			t.Errorf("%s: needs-root warning %v, want %v (%q)", c.name, got, c.warn, col.r.Warnings)
		}
	}
}

// An NVMe drive's unset temperature limits (0xFFFF K, 65261.85 °C) aren't
// limits (#143); real ones, and other kinds' limits, are kept.
func TestUnsetTemperatureLimits(t *testing.T) {
	file, link := fakeRoot(t)
	hw := "/sys/devices/pci0000:00/0000:00:1b.0/0000:01:00.0/nvme/nvme0/hwmon1"
	link("/sys/class/hwmon/hwmon1", "../../devices/pci0000:00/0000:00:1b.0/0000:01:00.0/nvme/nvme0/hwmon1")
	file(hw+"/name", "nvme")
	file(hw+"/temp1_input", "38850")
	file(hw+"/temp1_max", "84850")
	file(hw+"/temp1_crit", "84850")
	file(hw+"/temp2_input", "38850")
	file(hw+"/temp2_max", "65261850")
	file(hw+"/temp2_crit", "65261850")
	file(hw+"/temp3_input", "38850")
	file(hw+"/temp3_max", "65261849")
	file(hw+"/power1_input", "5000000")
	file(hw+"/power1_max", "65261850000") // 65261.85 kW is a power limit, if an odd one
	c := &collector{r: &report.Report{}}
	c.sensors()
	if len(c.r.Sensors) != 1 {
		t.Fatalf("sensors %+v", c.r.Sensors)
	}
	got := map[string]report.SensorReading{}
	for _, r := range c.r.Sensors[0].Readings {
		got[r.Label] = r
	}
	if r := got["temp1"]; r.Max != 84.85 || r.Crit != 84.85 {
		t.Errorf("set limits: %+v", r)
	}
	if r := got["temp2"]; r.Max != 0 || r.Crit != 0 {
		t.Errorf("unset limits: %+v", r)
	}
	if r := got["temp3"]; r.Max != 65261.849 {
		t.Errorf("just below the unset value: %+v", r)
	}
	if r := got["power1"]; r.Max != 65261.85 {
		t.Errorf("not a temperature: %+v", r)
	}
}

// A monitor whose EDID fails its checksum keeps only the vendor and
// product block, with a warning (#143).
func TestMonitorWithACorruptEDID(t *testing.T) {
	file, _ := fakeRoot(t)
	b := make([]byte, 128)
	copy(b, []byte{0, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0})
	b[8], b[9] = 0x10, 0xAC // DEL
	b[10], b[11] = 0x98, 0xA1
	b[16], b[17], b[18], b[19], b[21], b[22] = 38, 30, 1, 4, 60, 34
	b[127] = 1 // the checksum doesn't add up
	file("/sys/class/drm/card0-DP-1/status", "connected")
	file("/sys/class/drm/card0-DP-1/edid", string(b))
	c := &collector{r: &report.Report{}}
	c.displays()
	d := c.r.Displays[0]
	if d.ManufacturerID != "DEL" || d.Identity == nil || d.Identity.PartNumber != "A198" || d.Identity.ManufactureDate != "2020-W38" ||
		d.WidthMM != 0 || d.DiagonalIn != 0 || !hasWarning(c.r, "display card0-DP-1: edid: checksum mismatch") {
		t.Errorf("display %+v (identity %+v), warnings %q", d, d.Identity, c.r.Warnings)
	}
}

// Any EDID it can't read is said, not dropped silently (#143): here a
// manufacturer ID with a letter outside A-Z.
func TestMonitorWithAnUnreadableEDID(t *testing.T) {
	file, _ := fakeRoot(t)
	b := make([]byte, 128)
	copy(b, []byte{0, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0})
	b[8], b[9] = 0x7C, 0x21 // a first letter of 31
	var sum byte
	for _, v := range b[:127] {
		sum += v
	}
	b[127] = -sum
	file("/sys/class/drm/card0-DP-1/status", "connected")
	file("/sys/class/drm/card0-DP-1/edid", string(b))
	c := &collector{r: &report.Report{}}
	c.displays()
	if d := c.r.Displays[0]; d.ManufacturerID != "" || d.Identity != nil ||
		!hasWarning(c.r, "display card0-DP-1: edid: manufacturer ID 0x7c21 isn't three letters") {
		t.Errorf("display %+v, warnings %q", d, c.r.Warnings)
	}
}
