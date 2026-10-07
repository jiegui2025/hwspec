package collect

import (
	"strings"
	"testing"

	"github.com/jiegui2025/hwspec/internal/report"
)

// An amdgpu GPU's fw_version/ files become named components, in the
// group's (sorted) order; a hidden block has no file, and a zero one is
// left out; a value that isn't the kernel's form, or a file that can't be
// read, is a warning.
func TestAMDGPUFirmware(t *testing.T) {
	file, _ := fakeRoot(t)
	dir := "/sys/bus/pci/devices/0000:03:00.0/fw_version/"
	file(dir+"smc_fw_version", "0x00415b00\n")
	file(dir+"sos_fw_version", "0x00210c64\n")
	file(dir+"vcn_fw_version", "0x0110101b\n")
	file(dir+"vce_fw_version", "0x00000000\n") // zero: no firmware
	file(dir+"uevent", "")                     // not a version file
	col := &collector{r: &report.Report{}}
	g := &report.GPU{PCIAddress: "0000:03:00.0", Driver: &report.Driver{Name: "amdgpu"}}
	col.amdgpuFirmware(g)
	var got []string
	for _, fw := range g.FirmwareComponents {
		got = append(got, fw.Name+" "+fw.Version+" "+fw.Source)
	}
	if want := "smc 0x00415b00 amdgpu; sos 0x00210c64 amdgpu; vcn 0x0110101b amdgpu"; strings.Join(got, "; ") != want || len(col.r.Warnings) != 0 {
		t.Errorf("%q, warnings %q", got, col.r.Warnings)
	}

	file(dir+"mec_fw_version", "1.2\n")
	file(dir+"mec2_fw_version", "0x415b00\n")   // too short
	file(dir+"pfp_fw_version", "0x00415b00x\n") // trailing text
	file(dir+"me_fw_version/x", "")             // a directory: can't be read
	col = &collector{r: &report.Report{}}
	g.FirmwareComponents = nil
	col.amdgpuFirmware(g)
	w := strings.Join(col.r.Warnings, "\n")
	if len(g.FirmwareComponents) != 3 || !strings.Contains(w, `gpu 0000:03:00.0 fw_version/mec_fw_version: "1.2" isn't amdgpu's 0x%08x form`) ||
		!strings.Contains(w, "gpu 0000:03:00.0 fw_version/me_fw_version: ") || !strings.Contains(w, `mec2_fw_version: "0x415b00"`) ||
		!strings.Contains(w, `pfp_fw_version: "0x00415b00x"`) {
		t.Errorf("%+v, warnings %q", g.FirmwareComponents, w)
	}

	// No fw_version group, other drivers, no driver: nothing.
	for _, g := range []*report.GPU{
		{PCIAddress: "0000:04:00.0", Driver: &report.Driver{Name: "amdgpu"}},
		{PCIAddress: "0000:03:00.0", Driver: &report.Driver{Name: "radeon"}},
		{PCIAddress: "0000:03:00.0"},
	} {
		col := &collector{r: &report.Report{}}
		col.amdgpuFirmware(g)
		if g.FirmwareComponents != nil || len(col.r.Warnings) != 0 {
			t.Errorf("%s: %+v, warnings %q", g.PCIAddress, g.FirmwareComponents, col.r.Warnings)
		}
	}
}
