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
