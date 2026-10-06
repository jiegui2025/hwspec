package collect

import (
	"strings"
	"testing"

	"github.com/jiegui2025/hwspec/internal/report"
)

// machineFacts lists, per recorded machine, what each feature must find
// there: facts checked against the real hardware, not read back from the
// capture.
var machineFacts = map[string]func(t *testing.T, r *report.Report){
	// HP EliteDesk 800 G5 Desktop Mini: i5-9500T, one 16 GiB Avant DDR4
	// SO-DIMM, Samsung PM981 NVMe, Intel I219 Ethernet and 9560 Wi-Fi, Dell
	// S2721QS on DisplayPort. Recorded without root.
	"hp-elitedesk-800-g5-mini": func(t *testing.T, r *report.Report) {
		want := map[string]bool{
			// System identity and firmware from DMI.
			"system model":  r.System.Identity != nil && r.System.Identity.Model == "HP EliteDesk 800 G5 Desktop Mini",
			"system SKU":    r.System.Identity != nil && r.System.Identity.PartNumber == "7LL88UT#ABA",
			"BIOS from DMI": r.System.Firmware != nil && r.System.Firmware.Source == "dmi",
			"not root":      !r.Privileged,
			// CPU: model, topology, microcode, built-in scaling driver.
			"cpu model":         r.CPU.Identity != nil && r.CPU.Identity.Model == "Intel(R) Core(TM) i5-9500T CPU @ 2.20GHz",
			"cpu 6 cores":       r.CPU.Cores == 6 && r.CPU.Threads == 6,
			"microcode":         r.CPU.Firmware != nil && r.CPU.Firmware.Source == "microcode",
			"intel_pstate":      r.CPU.Driver != nil && r.CPU.Driver.Name == "intel_pstate" && r.CPU.Driver.Builtin,
			"throttle counters": r.CPU.Health != nil && r.CPU.Health.Source == "thermal_throttle",
			// Memory: without root there is no SMBIOS, so the SPD module
			// stands on its own.
			"one SPD module":     len(r.Memory.Modules) == 1 && r.Memory.Modules[0].Locator == "SPD 7-0050",
			"module 16 GiB":      len(r.Memory.Modules) == 1 && r.Memory.Modules[0].SizeBytes == 16<<30,
			"module DDR4 SODIMM": len(r.Memory.Modules) == 1 && r.Memory.Modules[0].Type == "DDR4" && r.Memory.Modules[0].FormFactor == "SODIMM",
			"module part":        len(r.Memory.Modules) == 1 && r.Memory.Modules[0].Identity.PartNumber == "J642GU44J2320NL",
		}
		// GPU: the UHD 630's hardware clock range, as the machine's own
		// read-only gt_RPn/RP0_freq_mhz gave it (350, 1100) when read
		// directly on 2026-10-06; not the adjustable gt_min/max limits.
		if g := find(r.GPUs, func(g report.GPU) bool { return g.PCIAddress == "0000:00:02.0" }); g != nil {
			want["gpu clock range"] = g.Clocks != nil && g.Clocks.Source == "i915" && g.Clocks.MinFreqMHz == 350 && g.Clocks.MaxFreqMHz == 1100 && g.Clocks.ActualFreqMHz != nil
		} else {
			want["gpu found"] = false
		}
		// Platform firmware the kernel shows a normal user: the CSME and EC
		// versions and the TPM's spec version, as read on the machine
		// (/sys/class/mei/mei0/fw_ver "0:12.0.45.1509",
		// ec_firmware_release "8.9", tpm_version_major "2"; 2026-10-06).
		want["me firmware"] = r.System.MEFirmware != nil && r.System.MEFirmware.Version == "12.0.45.1509" && r.System.MEFirmware.Source == "mei"
		want["ec firmware"] = r.System.ECFirmware != nil && r.System.ECFirmware.Version == "8.9" && r.System.ECFirmware.Source == "dmi"
		want["tpm 2"] = r.TPM != nil && r.TPM.SpecVersionMajor == 2
		// Mounting without root: the kernel's labels for the IGD and the
		// LAN come from SMBIOS type 41 (index files present), so both are
		// onboard; the Wi-Fi and NVMe need the root-only slot table. A
		// root capture of the same machine is TestTheReferenceMachinesMountings.
		pciDev := func(addr string) *report.PCIDevice {
			return find(r.PCI, func(d report.PCIDevice) bool { return d.Address == addr })
		}
		mount := func(addr string) *report.Mounting {
			if d := pciDev(addr); d != nil && d.Mounting != nil {
				return d.Mounting
			}
			return &report.Mounting{Kind: "none"}
		}
		const needsRoot = "the firmware's slot table needs root (run with --full)"
		for _, addr := range []string{"0000:00:02.0", "0000:00:1f.6"} {
			d := pciDev(addr)
			want["label from smbios "+addr] = d != nil && d.LabelSource == "smbios" && strings.HasPrefix(d.Label, "Onboard ")
			want["onboard "+addr] = mount(addr).Kind == "onboard" && mount(addr).Confidence == "high"
		}
		want["wi-fi needs root"] = mount("0000:02:00.0").Kind == "unknown" && mount("0000:02:00.0").Reason == needsRoot
		want["nvme needs root"] = mount("0000:01:00.0").Kind == "unknown" && mount("0000:01:00.0").Reason == needsRoot
		want["sodimm in a slot, from its SPD"] = len(r.Memory.Modules) == 1 && r.Memory.Modules[0].Mounting != nil &&
			r.Memory.Modules[0].Mounting.Kind == "slot" && len(r.Memory.Modules[0].Mounting.Evidence) == 1 &&
			r.Memory.Modules[0].Mounting.Evidence[0] == "the module's SPD gives form factor SODIMM"
		// PCI topology: the NVMe and the Wi-Fi card sit behind root ports
		// 00:1b.0 and 00:1c.0 (readlink -f on the machine, 2026-10-06);
		// the iGPU is on the root bus.
		parent := func(addr string) string {
			if d := find(r.PCI, func(d report.PCIDevice) bool { return d.Address == addr }); d != nil {
				return d.Parent
			}
			return "missing"
		}
		want["nvme behind 00:1b.0"] = parent("0000:01:00.0") == "0000:00:1b.0"
		want["wi-fi behind 00:1c.0"] = parent("0000:02:00.0") == "0000:00:1c.0"
		want["igpu on the root bus"] = parent("0000:00:02.0") == ""
		// Disk: model, firmware and the controller's driver.
		if d := find(r.Storage, func(d report.Disk) bool { return d.Name == "nvme0n1" }); d != nil {
			want["nvme model"] = d.Identity != nil && d.Identity.Model == "SAMSUNG MZVLB256HAHQ-000L7"
			want["nvme firmware"] = d.Firmware != nil && d.Firmware.Version == "1L2QEXD7" && d.Firmware.Source == "nvme"
			want["nvme driver is the controller's"] = d.Driver != nil && d.Driver.Name == "nvme" && d.Driver.Module == "nvme"
		} else {
			want["nvme0n1 found"] = false
		}
		// Display: EDID identity and manufacture week.
		want["display made 2020-W38"] = len(r.Displays) == 1 && r.Displays[0].Identity != nil &&
			r.Displays[0].Identity.Model == "DELL S2721QS" && r.Displays[0].Identity.ManufactureDate == "2020-W38"
		// Network: firmware via ethtool, driver, health from counters.
		for name, fw := range map[string]string{"eno1": "0.5-4", "wlan0": "77.8dbafb52.0 cc-a0-77.ucode"} {
			n := find(r.Network, func(n report.NIC) bool { return n.Name == name })
			want[name+" firmware"] = n != nil && n.Firmware != nil && n.Firmware.Version == fw && n.Firmware.Source == "ethtool"
			want[name+" health"] = n != nil && n.Health != nil && n.Health.Status == report.StatusOK
		}
		// The Wi-Fi card is told apart by its (empty) wireless/ directory.
		wlan := find(r.Network, func(n report.NIC) bool { return n.Name == "wlan0" })
		want["wlan0 is wireless"] = wlan != nil && wlan.Type == "wireless"
		want["eno1 is ethernet"] = find(r.Network, func(n report.NIC) bool { return n.Name == "eno1" && n.Type == "ethernet" }) != nil
		// Root-only DMI files are replayed as root-only.
		want["dmi serials need root"] = len(r.Warnings) > 0 && strings.Contains(strings.Join(r.Warnings, "\n"), "dmi product_serial")
		// Bluetooth: management answer and the USB driver.
		want["bluetooth 5.2 on btusb"] = len(r.Bluetooth) == 1 && r.Bluetooth[0].Version == "5.2" &&
			r.Bluetooth[0].Driver != nil && r.Bluetooth[0].Driver.Name == "btusb"
		// Built-in drivers are recognised even when they link to a module
		// directory (ahci, xhci_hcd).
		builtin := map[string]bool{}
		for _, p := range r.PCI {
			if p.Driver != nil && p.Driver.Builtin {
				builtin[p.Driver.Name] = true
			}
		}
		want["ahci built in"] = builtin["ahci"]
		want["xhci_hcd built in"] = builtin["xhci_hcd"]
		// Audio: the HDA codecs.
		hda := find(r.Audio, func(a report.SoundCard) bool { return a.Name == "HDA Intel PCH" })
		want["HDA codecs"] = hda != nil && len(hda.Codecs) == 2

		for fact, ok := range want {
			if !ok {
				t.Errorf("fact not met: %s", fact)
			}
		}
	},
}

func find[T any](items []T, match func(T) bool) *T {
	for i := range items {
		if match(items[i]) {
			return &items[i]
		}
	}
	return nil
}
