package advisor

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jiegui2025/hwspec/internal/kb"
	"github.com/jiegui2025/hwspec/internal/report"
)

func linuxFirmwareKB() *kb.KB {
	matches, _ := checks["linux-firmware-matches"].example()
	differs, _ := checks["linux-firmware-differs"].example()
	return knowledge(matches, differs)
}

func iwlNIC(name, version string) report.NIC {
	return report.NIC{Name: name, Bus: "pci", BusAddress: "0000:02:00.0", Identity: &report.Identity{Vendor: "Intel", Model: "Wi-Fi 6 AX200"},
		Driver: &report.Driver{Name: "iwlwifi"}, Firmware: &report.Firmware{Version: version, Source: "ethtool"}}
}

// The reference machine's AX200 (loaded 77.8dbafb52.0 cc-a0-77.ucode)
// against tag 20260916's WHENCE (74.8dbafb52.0): the same build. Against
// master's (74.563a6e92.0): differs, and says builds aren't ordered.
func TestLinuxFirmwareComparesTheBuild(t *testing.T) {
	r := &report.Report{Network: []report.NIC{iwlNIC("wlan0", "77.8dbafb52.0 cc-a0-77.ucode")}}
	checked := time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name, whence, rule, text string
	}{
		{"tag 20260916", "74.8dbafb52.0", "firmware.linux-firmware-matches",
			"linux-firmware 20260916 (checked 2026-10-07) lists build 8dbafb52 for iwlwifi-cc-a0-77.ucode (Version 74.8dbafb52.0): the loaded firmware is that build"},
		{"master", "74.563a6e92.0", "firmware.linux-firmware-differs",
			"linux-firmware 20260916 (checked 2026-10-07) lists build 563a6e92 for iwlwifi-cc-a0-77.ucode (Version 74.563a6e92.0); the loaded firmware is build 8dbafb52. Builds aren't ordered, so this doesn't say which is newer"},
	} {
		lf := &LinuxFirmware{Tag: "20260916", FetchedAt: checked, Versions: map[string]string{"iwlwifi-cc-a0-77.ucode": c.whence}}
		a := Advise(Input{Report: r, KB: linuxFirmwareKB(), Now: noon, LinuxFirmware: lf})
		if len(a.Findings) != 1 || len(a.Warnings) != 0 {
			t.Fatalf("%s: findings %+v, warnings %q", c.name, a.Findings, a.Warnings)
		}
		f := a.Findings[0]
		if f.ID != c.rule || f.Category != "firmware" || len(f.Answers) != 1 || f.Answers[0].Text != c.text ||
			*f.Device != (DeviceRef{Kind: "pci", Key: "0000:02:00.0", Name: "Intel Wi-Fi 6 AX200"}) {
			t.Errorf("%s: %+v (answers %+v, device %+v)", c.name, f, f.Answers, f.Device)
		}
	}
}

// Only a version that names its file is compared, with a build WHENCE
// gives for that file: anything else gets no finding, not a guess.
func TestLinuxFirmwareNeedsANamedFileAndABuild(t *testing.T) {
	lf := &LinuxFirmware{Tag: "20260916", Versions: map[string]string{
		"iwlwifi-cc-a0-77.ucode": "74.8dbafb52.0", "iwlwifi-ty-a0-gf-a0-83.ucode": "83.e8f84e98.0",
		"iwlwifi-so-a0-gf-a0-89.ucode": "no build here", "iwlwifi-7265D-29.ucode": "29.0000abcd.0", "odd-1.ucode": "1.0000abcd.0",
	}}
	other := iwlNIC("eth0", "77.8dbafb52.0 cc-a0-77.ucode")
	other.Driver.Name = "e1000e"
	unknown := iwlNIC("wlan3", "77.8dbafb52.0 cc-a0-77.ucode")
	unknown.Firmware = report.UnknownFirmware("ethtool gave none")
	nodriver := iwlNIC("wlan4", "77.8dbafb52.0 cc-a0-77.ucode")
	nodriver.Driver = nil
	nofw := iwlNIC("wlan8", "")
	nofw.Firmware = nil
	r := &report.Report{Network: []report.NIC{
		other, unknown, nodriver, nofw,
		iwlNIC("wlan0", "36.ca7b901d.0 8000C-36.ucode"),       // its file isn't in WHENCE's versions
		iwlNIC("wlan1", "29.1044073957.0 7265D-29.ucode"),     // not eight hex digits
		iwlNIC("wlan2", "17.3.0.5 8000C-17.ucode"),            // the older four-number form
		iwlNIC("wlan5", "89.12345678.0 so-a0-gf-a0-89.ucode"), // WHENCE's Version has no build
		iwlNIC("wlan6", "83.e8f84e98.0 ty-a0-gf-a0-83.ucode"),
		iwlNIC("wlan7", "1.0000abcd.0 odd-1.ucode"), // a file not named iwlwifi-*
	}}
	a := Advise(Input{Report: r, KB: linuxFirmwareKB(), Now: noon, LinuxFirmware: lf})
	var got []string
	for _, f := range a.Findings {
		got = append(got, f.ID+" "+strings.Fields(f.Answers[0].Text)[8])
	}
	slices.Sort(got)
	want := []string{"firmware.linux-firmware-matches iwlwifi-ty-a0-gf-a0-83.ucode", "firmware.linux-firmware-matches odd-1.ucode"}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("findings %q, want %q", got, want)
	}
}

// Without the index both rules are skipped with one warning naming the
// command that fetches it; a NIC off PCI is named by its interface.
func TestLinuxFirmwareWithoutTheIndex(t *testing.T) {
	r := &report.Report{Network: []report.NIC{iwlNIC("wlan0", "77.8dbafb52.0 cc-a0-77.ucode")}}
	a := Advise(Input{Report: r, KB: linuxFirmwareKB(), Now: noon})
	if len(a.Findings) != 0 || !slices.Equal(a.Warnings, []string{firmwareIndexMissing}) || a.RulesSkipped != 2 || !strings.Contains(a.Warnings[0], "hwspec firmware update") {
		t.Errorf("findings %+v, warnings %q, skipped %d", a.Findings, a.Warnings, a.RulesSkipped)
	}
	usb := iwlNIC("wlan1", "")
	usb.Bus, usb.BusAddress, usb.Identity = "usb", "1-2", nil
	if got := nicDevice(usb); *got != (DeviceRef{Kind: "network", Key: "wlan1"}) {
		t.Errorf("a USB adapter: %+v", got)
	}
}
