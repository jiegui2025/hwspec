package collect

import (
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/jiegui2025/hwspec/internal/report"
	"github.com/jiegui2025/hwspec/internal/tpm"
)

// platformFirmware records the Intel Management Engine's firmware and the
// TPM's specification version: both readable without root.
func (c *collector) platformFirmware() {
	c.r.System.MEFirmware = c.meFirmware()
	c.r.System.ESRT = c.esrt()
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
		return &report.TPM{SpecVersionMajor: major, Firmware: c.tpmFirmware(dev, major)}
	}
	return nil
}

// tpmFirmware reads a TPM's manufacturer and firmware version. A TPM 2.0
// is asked under --full (#122: one TPM2_GetCapability through the kernel's
// resource manager, /dev/tpmrm0); otherwise, or if it doesn't answer,
// udev's tpm2_id (systemd) record of the same answer is read, which
// everyone can: ID_TPM2_MODALIAS "…:mfIFX:…:fw7.85.1166080:". Both write
// the version alike (tpm.Info.FirmwareVersion). For a TPM 1.2, the
// kernel's caps file: "Firmware version: 6.40".
func (c *collector) tpmFirmware(dev string, major int) *report.Firmware {
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
	rm := strings.Replace(dev, "tpm", "tpmrm", 1)
	if c.privileged {
		info, err := queryTPM("/dev/" + rm)
		if err == nil {
			return &report.Firmware{Vendor: info.Manufacturer, Version: info.FirmwareVersion(), Source: "tpm"}
		}
		c.warn("tpm: asking /dev/%s for its firmware version: %v", rm, err)
	}
	props := udevRecord("c", "/sys/class/tpmrm/"+rm)
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
	switch {
	case fw == nil && c.privileged:
		return report.UnknownFirmware("the TPM didn't give it, and udev didn't record it")
	case fw == nil:
		return report.UnknownFirmware("udev didn't record it; asking the TPM needs --full")
	}
	fw.Vendor = vendor
	return fw
}

// queryTPM asks a TPM 2.0 for its manufacturer and firmware version.
func queryTPM(node string) (tpm.Info, error) {
	reply, err := tpmTransmit(node, tpm.Command())
	if err != nil {
		return tpm.Info{}, err
	}
	return tpm.Parse(reply)
}

// tpmTransmit sends one command to a TPM device and returns its reply: a
// seam, as the device needs root and a TPM. The kernel's TPM driver bounds
// how long the read waits.
var tpmTransmit = func(node string, cmd []byte) ([]byte, error) {
	f, err := os.OpenFile(node, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Write(cmd); err != nil {
		return nil, err
	}
	reply := make([]byte, 4096) // the kernel's TPM_BUFSIZE (include/linux/tpm_command.h): no reply is longer
	n, err := f.Read(reply)
	if err != nil {
		return nil, err
	}
	return reply[:n], nil
}
