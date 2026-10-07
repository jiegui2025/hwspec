package advisor

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/jiegui2025/hwspec/internal/kb"
	"github.com/jiegui2025/hwspec/internal/report"
)

// Missing firmware (#215, #7 part 5): each firmware load failure the
// kernel logged (kernel.firmware_failures, #213, --full), with the package
// that ships the file on the capture's distro. Packages come from the
// rule's data, keyed by the file the kernel asked for, then by distro:
// a key ending in "/" covers everything under that directory (nvidia/…
// is several levels deep), any other is a path.Match glob
// ("iwlwifi-*.ucode").

// fwFailureData is firmware.load-failed's data.
type fwFailureData struct {
	Packages map[string]map[string]claims[string] `json:"packages"`
}

var (
	// firmwareFile is a file the kernel asked for, safe in a command: a
	// relative path of plain characters, no "..".
	firmwareFile = regexp.MustCompile(`^[A-Za-z0-9._+-][A-Za-z0-9._+/-]*$`)
	// packageName is a distro package name, safe in a command.
	packageName = regexp.MustCompile(`^[a-z0-9][a-z0-9+._-]*$`)
	distroName  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
)

func decodeFirmwareData(raw json.RawMessage) (fwFailureData, error) {
	d, err := decodeData[fwFailureData](raw)
	if err != nil {
		return d, err
	}
	if len(d.Packages) == 0 {
		return d, errors.New("packages: none")
	}
	var errs []error
	for _, file := range slices.Sorted(maps.Keys(d.Packages)) {
		if _, err := path.Match(file, ""); err != nil || file == "" {
			errs = append(errs, fmt.Errorf("packages: %q isn't a file glob", file))
		}
		for _, distro := range slices.Sorted(maps.Keys(d.Packages[file])) {
			if !distroName.MatchString(distro) {
				errs = append(errs, fmt.Errorf("packages[%q]: %q isn't an os-release ID", file, distro))
			}
			for _, c := range d.Packages[file][distro] {
				if !packageName.MatchString(c.Value) {
					errs = append(errs, fmt.Errorf("packages[%q][%q]: %q isn't a package name", file, distro, c.Value))
				}
			}
		}
	}
	return d, errors.Join(errs...)
}

func init() {
	register("firmware-load-failed", check{
		run:   firmwareLoadFailed,
		needs: []string{"kernel.firmware_failures"},
		available: func(in *Input) bool {
			return in.Report.Kernel != nil && in.Report.Kernel.FirmwareFailures != nil
		},
		provides: []string{"file", "package"},
		data:     func(raw json.RawMessage) error { _, err := decodeFirmwareData(raw); return err },
		example: func() (kb.Rule, *report.Report) {
			return kb.Rule{ID: "firmware.load-failed", Check: "firmware-load-failed", Category: "needs-attention", Severity: "warning",
					Title: "Missing firmware", Src: []string{"kernel"},
					Data:    json.RawMessage(`{"packages": {"iwlwifi-*.ucode": {"arch": [{"value": "linux-firmware-intel", "src": "kernel"}]}}}`),
					Actions: []kb.Action{{Distro: "arch", Text: "Install it", Commands: []string{"sudo pacman -S {package}", "ls /lib/firmware/{file}"}}}},
				&report.Report{OS: report.OS{ID: "cachyos", IDLike: "arch"},
					PCI: []report.PCIDevice{{Address: "0000:02:00.0", VendorID: "8086", DeviceID: "2723", ClassCode: "028000", Class: "Network controller"}},
					Kernel: &report.Kernel{FirmwareFailures: []report.FirmwareFailure{
						{Device: "0000:02:00.0", Driver: "iwlwifi", File: "iwlwifi-cc-a0-77.ucode", Error: -2}}}}
		},
	})
}

// firmwareLoadFailed gives each logged failure a finding, with the file
// and, when the rule's data names one for the capture's distro, the
// package that ships it.
func firmwareLoadFailed(in *Input, rule *kb.Rule) ([]hit, error) {
	d, err := decodeFirmwareData(rule.Data)
	if err != nil {
		return nil, err
	}
	r := in.Report
	distros := append([]string{r.OS.ID}, strings.Fields(r.OS.IDLike)...)
	var hits []hit
	for i, f := range r.Kernel.FirmwareFailures {
		at := fmt.Sprintf("kernel.firmware_failures[%d].", i)
		h := hit{device: firmwareDevice(r, f), evidence: []Evidence{Present(at+"file", f.File), Present(at+"error", f.Error)}}
		if f.Driver != "" {
			h.evidence = append(h.evidence, Present(at+"driver", f.Driver))
		}
		h.vars = map[string]string{}
		if firmwareFile.MatchString(f.File) && !slices.Contains(strings.Split(f.File, "/"), "..") {
			h.vars["file"] = f.File
		}
		if pkg, src, ok := firmwarePackage(d, h.vars["file"], distros); ok {
			h.vars["package"] = pkg
			h.used = []string{src}
		}
		hits = append(hits, h)
	}
	return hits, nil
}

// firmwarePackage finds the package that ships file on the first of
// distros the data has a row for: the capture's ID, then its ID_LIKE
// words. The longest key that covers the file is used: a subdirectory
// another package ships (intel/sof/) beats its parent (intel/).
func firmwarePackage(d fwFailureData, file string, distros []string) (pkg, src string, ok bool) {
	best := ""
	for key := range d.Packages {
		covered := strings.HasSuffix(key, "/") && strings.HasPrefix(file, key)
		if m, _ := path.Match(key, file); (covered || m) && len(key) > len(best) {
			best = key
		}
	}
	if best == "" || file == "" {
		return "", "", false
	}
	for _, distro := range distros {
		if cs := d.Packages[best][distro]; len(cs) > 0 {
			pkg, src := cs.first()
			return pkg, src, true
		}
	}
	return "", "", false
}

// firmwareDevice names the device a failure is for: a PCI device by its
// address when the capture lists it, else as the kernel named it.
func firmwareDevice(r *report.Report, f report.FirmwareFailure) *DeviceRef {
	for _, d := range r.PCI {
		if d.Address == f.Device {
			name := d.Class
			if d.Identity != nil && d.Identity.Model != "" {
				name = d.Identity.Model
			}
			return &DeviceRef{Kind: "pci", Key: d.Address, Name: name}
		}
	}
	return &DeviceRef{Kind: "device", Key: f.Device, Name: f.Driver}
}
