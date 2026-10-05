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
