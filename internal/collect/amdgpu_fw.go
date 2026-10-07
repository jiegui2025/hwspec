package collect

import (
	"regexp"
	"strings"

	"github.com/jiegui2025/hwspec/internal/report"
)

// amdgpuHex is a version as amdgpu prints it: FW_VERSION_ATTR in
// drivers/gpu/drm/amd/amdgpu/amdgpu_ucode.c, sysfs_emit(buf, "0x%08x\n").
var amdgpuHex = regexp.MustCompile(`^0x[0-9a-f]{8}$`)

// amdgpuFirmware reads an amdgpu GPU's per-block firmware versions (#205):
// the fw_version/ sysfs group, one 0444 file per block ("smc_fw_version",
// "sos_fw_version", …), readable without root. amdgpu hides a block whose
// version is 0 when it creates the group (amdgpu_ucode_sys_visible); one
// read as 0x00000000 later is left out too: it names no firmware.
func (c *collector) amdgpuFirmware(gpu *report.GPU) {
	if gpu.Driver == nil || gpu.Driver.Name != "amdgpu" {
		return
	}
	dir := pciDir + gpu.PCIAddress + "/fw_version/"
	for _, f := range list(dir) {
		name, ok := strings.CutSuffix(f, "_fw_version")
		if !ok {
			continue
		}
		v, err := readStrErr(dir + f)
		switch {
		case err != nil:
			c.warnRead("gpu "+gpu.PCIAddress+" fw_version/"+f, err)
		case !amdgpuHex.MatchString(v):
			c.warn("gpu %s fw_version/%s: %q isn't amdgpu's 0x%%08x form", gpu.PCIAddress, f, v)
		case v != "0x00000000":
			gpu.FirmwareComponents = append(gpu.FirmwareComponents, report.Firmware{Name: name, Version: v, Source: "amdgpu"})
		}
	}
}
