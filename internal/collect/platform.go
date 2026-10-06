package collect

import (
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/jiegui2025/hwspec/internal/report"
)

// platformFirmware records the Intel Management Engine's firmware and the
// TPM's specification version: both readable without root.
func (c *collector) platformFirmware() {
	c.r.System.MEFirmware = c.meFirmware()
	c.r.TPM = c.tpm()
}

// meVersion is one fw_ver block: "<platform>:<major>.<minor>.<hotfix>.<build>"
// (ABI sysfs-class-mei; drivers/misc/mei/main.c fw_ver_show). The kernel
// prints three blocks, the ME's code, recovery and FITC versions in that
// order (coreboot intelblocks/cse.h, struct me_fw_ver_resp), and zeros
// when it got no version.
var meVersion = regexp.MustCompile(`^[0-9]+:([0-9]+)\.([0-9]+)\.([0-9]+)\.([0-9]+)$`)

// meFirmware reads the platform ME's running (code) firmware version.
// Other mei devices (a discrete GPU's GSC, a visual sensing controller)
// have their own firmware and are skipped: only kind "mei" counts, or,
// on kernels before 5.8 without kind, a device whose parent is an Intel
// PCI communication controller (class 0780xx).
func (c *collector) meFirmware() *report.Firmware {
	for _, dev := range list("/sys/class/mei") {
		d := "/sys/class/mei/" + dev + "/"
		if kind, err := readStrErr(d + "kind"); err == nil {
			if kind != "mei" {
				continue
			}
		} else if !strings.HasPrefix(readStr(d+"device/class"), "0x0780") || readStr(d+"device/vendor") != "0x8086" {
			continue
		}
		text, err := readStrErr(d + "fw_ver")
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			c.warn("me firmware: %v", err)
			continue
		}
		first, _, _ := strings.Cut(text, "\n")
		m := meVersion.FindStringSubmatch(strings.TrimSpace(first))
		if m == nil {
			c.warn("me firmware: unexpected fw_ver %q", first)
			continue
		}
		if m[1]+m[2]+m[3]+m[4] == "0000" {
			continue // the kernel got no version from this device
		}
		return &report.Firmware{Vendor: "Intel", Version: strings.Join(m[1:], "."), Source: "mei"}
	}
	return nil
}

// tpm reads the TCG specification major version of the first TPM that
// reports one; like the ME, a device that can't be read is skipped.
func (c *collector) tpm() *report.TPM {
	for _, dev := range list("/sys/class/tpm") {
		text, err := readStrErr("/sys/class/tpm/" + dev + "/tpm_version_major")
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			c.warn("tpm: %v", err)
			continue
		}
		major, err := strconv.Atoi(text)
		if err != nil || major < 1 {
			c.warn("tpm: unexpected tpm_version_major %q", text)
			continue
		}
		return &report.TPM{SpecVersionMajor: major}
	}
	return nil
}
