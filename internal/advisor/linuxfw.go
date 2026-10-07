package advisor

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jiegui2025/hwspec/internal/kb"
	"github.com/jiegui2025/hwspec/internal/report"
)

// Loaded firmware compared with linux-firmware's latest release (#225,
// ADR 0012). iwlwifi first: its version names the file it came from.

// iwlwifiVersion is iwlwifi's version of a firmware with a FW_VERSION
// record: "%u.%08x.%u %s", major, minor, local_comp and the file's name
// without "iwlwifi-" (drivers/net/wireless/intel/iwlwifi/iwl-drv.c), e.g.
// "77.8dbafb52.0 cc-a0-77.ucode". Older firmware's "%u.%u.%u.%u" form
// names no build and isn't compared.
var iwlwifiVersion = regexp.MustCompile(`^[0-9]+\.([0-9a-f]{8})\.[0-9]+ ([A-Za-z0-9._-]+\.ucode)$`)

// whenceBuild is WHENCE's Version for the same files: its first number
// isn't the kernel's (74 where the kernel says 77), so only the build, the
// eight hex digits, is compared. Builds aren't ordered: "differs" never
// means "older".
var whenceBuild = regexp.MustCompile(`^[0-9]+\.([0-9a-f]{8})\.[0-9]+$`)

const (
	sameBuild = iota
	otherBuild
)

// firmwareIndexMissing is the one warning when there is no index.
const firmwareIndexMissing = "no firmware index: run `hwspec firmware update` to compare firmware with linux-firmware's latest release"

func init() {
	for _, c := range []struct {
		name string
		want int
		rule kb.Rule
	}{
		{"linux-firmware-matches", sameBuild, kb.Rule{ID: "firmware.linux-firmware-matches", Title: "Firmware is linux-firmware's latest release"}},
		{"linux-firmware-differs", otherBuild, kb.Rule{ID: "firmware.linux-firmware-differs", Title: "Firmware differs from linux-firmware's latest release"}},
	} {
		register(c.name, check{
			run:         linuxFirmwareCheck(c.want),
			needs:       []string{"network[].firmware", "the firmware index"},
			available:   func(in *Input) bool { return in.LinuxFirmware != nil },
			unavailable: firmwareIndexMissing,
			example: func() (kb.Rule, *report.Report) {
				r := c.rule
				r.Check, r.Category, r.Severity, r.Src = c.name, "firmware", "info", []string{"kernel"}
				return r, &report.Report{Network: []report.NIC{{Name: "wlan0", Bus: "pci", BusAddress: "0000:02:00.0",
					Driver:   &report.Driver{Name: "iwlwifi"},
					Firmware: &report.Firmware{Version: "77.8dbafb52.0 cc-a0-77.ucode", Source: "ethtool"}}}}
			},
			exampleInput: func(in *Input) {
				build := map[int]string{sameBuild: "8dbafb52", otherBuild: "563a6e92"}[c.want]
				in.LinuxFirmware = &LinuxFirmware{Tag: "20260916", FetchedAt: time.Date(2026, 10, 7, 4, 0, 0, 0, time.UTC),
					Versions: map[string]string{"iwlwifi-cc-a0-77.ucode": "74." + build + ".0"}}
			},
		})
	}
}

// linuxFirmwareCheck finds the iwlwifi firmware whose build is (want
// sameBuild) or isn't (otherBuild) the one linux-firmware's release lists
// for the same file. A version that names no file, or a file WHENCE gives
// no build for, gets no finding either way.
func linuxFirmwareCheck(want int) func(*Input, *kb.Rule) ([]hit, error) {
	return func(in *Input, _ *kb.Rule) ([]hit, error) {
		lf := in.LinuxFirmware
		var hits []hit
		for i, n := range in.Report.Network {
			if n.Driver == nil || n.Driver.Name != "iwlwifi" || !n.Firmware.Known() {
				continue
			}
			m := iwlwifiVersion.FindStringSubmatch(n.Firmware.Version)
			if m == nil {
				continue
			}
			loaded, reduced := m[1], m[2]
			// The kernel drops an "iwlwifi-" prefix from the name it shows.
			file, listed := "iwlwifi-"+reduced, ""
			if v, ok := lf.Versions[file]; ok {
				listed = v
			} else if v, ok := lf.Versions[reduced]; ok {
				file, listed = reduced, v
			}
			w := whenceBuild.FindStringSubmatch(listed)
			if w == nil {
				continue
			}
			got := otherBuild
			if w[1] == loaded {
				got = sameBuild
			}
			if got != want {
				continue
			}
			release := fmt.Sprintf("linux-firmware %s (checked %s)", lf.Tag, lf.FetchedAt.UTC().Format("2006-01-02"))
			text := fmt.Sprintf("%s lists build %s for %s (Version %s): the loaded firmware is that build", release, w[1], file, listed)
			if got == otherBuild {
				text = fmt.Sprintf("%s lists build %s for %s (Version %s); the loaded firmware is build %s. Builds aren't ordered, so this doesn't say which is newer",
					release, w[1], file, listed, loaded)
			}
			at := fmt.Sprintf("network[%d].", i)
			hits = append(hits, hit{
				device:   nicDevice(n),
				evidence: []Evidence{Present(at+"driver.name", n.Driver.Name), Present(at+"firmware.version", n.Firmware.Version)},
				answers:  []Answer{{Topic: "linux-firmware", Known: true, Text: text}},
			})
		}
		return hits, nil
	}
}

// nicDevice names a network interface by its PCI address when it has one,
// so findings about the card line up, else by its name.
func nicDevice(n report.NIC) *DeviceRef {
	name := ""
	if n.Identity != nil {
		name = strings.TrimSpace(n.Identity.Vendor + " " + n.Identity.Model)
	}
	if n.Bus == "pci" && n.BusAddress != "" {
		return &DeviceRef{Kind: "pci", Key: n.BusAddress, Name: name}
	}
	return &DeviceRef{Kind: "network", Key: n.Name, Name: name}
}
