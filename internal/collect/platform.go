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
// PCI communication controller (class 0780xx). A platform ME that gives
// no version gets an unknown block saying why; no ME, no block.
func (c *collector) meFirmware() *report.Firmware {
	why := ""
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
			why = "the kernel gives no fw_ver for the Management Engine"
			continue
		}
		if err != nil {
			c.warn("me firmware: %v", err)
			why = "fw_ver can't be read: " + err.Error()
			continue
		}
		first, _, _ := strings.Cut(text, "\n")
		m := meVersion.FindStringSubmatch(strings.TrimSpace(first))
		if m == nil {
			c.warn("me firmware: unexpected fw_ver %q", first)
			why = "fw_ver isn't in the kernel's format"
			continue
		}
		if m[1]+m[2]+m[3]+m[4] == "0000" {
			why = "the kernel got no version from the Management Engine"
			continue
		}
		return &report.Firmware{Vendor: "Intel", Version: strings.Join(m[1:], "."), Source: "mei"}
	}
	if why != "" {
		return report.UnknownFirmware(why)
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
		return &report.TPM{SpecVersionMajor: major, Firmware: tpmFirmware(dev, major)}
	}
	return nil
}

// tpmFirmware reads a TPM's firmware version where the system already
// recorded it for everyone to read: for TPM 2.0, udev's tpm2_id (systemd)
// in the resource manager device's record, ID_TPM2_MODALIAS "…:mfIFX:…:
// fw7.85.1166080:", as it gives it; for TPM 1.2, the kernel's caps file,
// "Firmware version: 6.40". hwspec doesn't query the TPM itself yet
// (#122).
func tpmFirmware(dev string, major int) *report.Firmware {
	if major == 1 {
		caps, err := readStrErr("/sys/class/tpm/" + dev + "/caps")
		if err != nil {
			return report.UnknownFirmware("the TPM 1.2 caps file can't be read")
		}
		for line := range strings.Lines(caps) {
			if v, ok := strings.CutPrefix(line, "Firmware version:"); ok {
				if fw := firmwareVersion(v, "caps"); fw != nil {
					return fw
				}
			}
		}
		return report.UnknownFirmware("the TPM 1.2 caps file gives no firmware version")
	}
	props := udevRecord("c", "/sys/class/tpmrm/"+strings.Replace(dev, "tpm", "tpmrm", 1))
	var fw *report.Firmware
	vendor := ""
	for f := range strings.SplitSeq(props["ID_TPM2_MODALIAS"], ":") {
		if v, ok := strings.CutPrefix(f, "fw"); ok {
			fw = firmwareVersion(v, "udev")
		}
		if v, ok := strings.CutPrefix(f, "mf"); ok {
			vendor = v
		}
	}
	if fw == nil {
		return report.UnknownFirmware("udev didn't record it, and hwspec doesn't query the TPM yet")
	}
	fw.Vendor = vendor
	return fw
}
