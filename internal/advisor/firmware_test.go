package advisor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jiegui2025/hwspec/internal/kb"
	"github.com/jiegui2025/hwspec/internal/report"
)

// failed is a root capture of distro os whose kernel log has the given
// firmware load failures, for an AX200 at 0000:02:00.0.
func failed(os report.OS, fs ...report.FirmwareFailure) *report.Report {
	return &report.Report{OS: os,
		PCI: []report.PCIDevice{{Address: "0000:02:00.0", VendorID: "8086", DeviceID: "2723", ClassCode: "028000", Class: "Network controller",
			Identity: &report.Identity{Model: "Wi-Fi 6 AX200"}, Driver: &report.Driver{Name: "iwlwifi"}}},
		Kernel: &report.Kernel{FirmwareFailures: fs}}
}

func firmwareFindings(a Advice) []Finding {
	var out []Finding
	for _, f := range a.Findings {
		if f.ID == "firmware.load-failed" {
			out = append(out, f)
		}
	}
	return out
}

var cachy = report.OS{ID: "cachyos", IDLike: "arch"}

// #7's acceptance for part 5, on the shipped rule: an Arch-like capture
// gets the package that ships the file; another distro gets no command.
func TestMissingFirmware(t *testing.T) {
	k := embedded(t)
	iwl := report.FirmwareFailure{Device: "0000:02:00.0", Driver: "iwlwifi", File: "iwlwifi-cc-a0-77.ucode", Error: -2}
	for _, c := range []struct {
		name string
		os   report.OS
		want string
	}{
		{"CachyOS (ID_LIKE arch)", cachy, "sudo pacman -S linux-firmware-intel"},
		{"Arch", report.OS{ID: "arch"}, "sudo pacman -S linux-firmware-intel"},
		{"Fedora", report.OS{ID: "fedora"}, ""},
	} {
		fs := firmwareFindings(Advise(Input{Report: failed(c.os, iwl), KB: k, Now: noon}))
		if len(fs) != 1 {
			t.Fatalf("%s: %d findings", c.name, len(fs))
		}
		f := fs[0]
		if got := commands(f); got != c.want || f.Device.Kind != "pci" || f.Device.Name != "Wi-Fi 6 AX200" {
			t.Errorf("%s: %q, device %+v", c.name, got, f.Device)
		}
		if f.Confidence != "upstream-doc" || strings.Contains(sourcesOf(f), "arch-linux-firmware-intel") != (c.want != "") {
			t.Errorf("%s: sources %s, confidence %s", c.name, sourcesOf(f), f.Confidence)
		}
	}
	for file, want := range map[string]string{
		"i915/kbl_guc_70.1.1.bin":                 "linux-firmware-intel",
		"intel/ibt-20-1-3.sfi":                    "linux-firmware-intel",
		"intel/sof-ipc4/tgl/sof-tgl.ri":           "sof-firmware", // the longer key wins
		"nvidia/ad102/gsp/booter_load-535.bin":    "linux-firmware-nvidia",
		"rtw88/rtw8822c_fw.bin":                   "linux-firmware-realtek",
		"amdgpu/navi10_sos.bin":                   "linux-firmware-amdgpu",
		"ath11k/WCN6855/hw2.0/amss.bin":           "linux-firmware-atheros",
		"unknown-vendor.bin":                      "",
		"iwlwifi-cc-a0-77.ucode/../../etc/passwd": "",
	} {
		f := iwl
		f.File = file
		a := Advise(Input{Report: failed(cachy, f), KB: k, Now: noon})
		fs := firmwareFindings(a)
		got := commands(fs[0])
		if want == "" && got != "" || want != "" && got != "sudo pacman -S "+want {
			t.Errorf("%s: %q, want %s", file, got, want)
		}
	}
}

// File names come from the kernel log, which carries hardware's text:
// one with shell metacharacters, a "..", or a leading "/" reaches no
// command, and gets a warning.
func TestFirmwareFileNeverReachesACommandUnchecked(t *testing.T) {
	rule := kb.Rule{ID: "firmware.load-failed", Check: "firmware-load-failed", Category: "needs-attention", Severity: "warning",
		Title: "Missing firmware", Src: []string{"kernel"},
		Data:    json.RawMessage(`{"packages": {"x/": {"arch": [{"value": "pkg", "src": "kernel"}]}}}`),
		Actions: []kb.Action{{Text: "t", Commands: []string{"ls /lib/firmware/{file}"}}}}
	for _, file := range []string{"x/a;id", "x/$(id)", "x/../../etc/shadow", "/etc/shadow", "x/a b", "x/`id`"} {
		a := Advise(Input{Report: failed(cachy, report.FirmwareFailure{Device: "1-14:1.0", Driver: "btusb", File: file, Error: -2}),
			KB: knowledge(rule), Now: noon})
		fs := firmwareFindings(a)
		if len(fs) != 1 || commands(fs[0]) != "" || !strings.Contains(strings.Join(a.Warnings, "\n"), "needs {file}") {
			t.Errorf("%q: %+v, warnings %q", file, fs, a.Warnings)
		}
		if fs[0].Device.Kind != "device" || fs[0].Device.Key != "1-14:1.0" || fs[0].Device.Name != "btusb" {
			t.Errorf("%q: device %+v", file, fs[0].Device)
		}
	}
}

func TestFirmwareRuleData(t *testing.T) {
	rule := func(data string) kb.Rule {
		return kb.Rule{ID: "f", Check: "firmware-load-failed", Category: "needs-attention", Severity: "warning", Title: "t",
			Data: json.RawMessage(data), Src: []string{"kernel"}}
	}
	for data, want := range map[string]string{
		`{"packages": {}}`: "packages: none",
		`{"packages": {"[": {"arch": [{"value": "p", "src": "kernel"}]}}}`:    `packages: "[" isn't a file glob`,
		`{"packages": {"": {"arch": [{"value": "p", "src": "kernel"}]}}}`:     `packages: "" isn't a file glob`,
		`{"packages": {"x/": {"Arch": [{"value": "p", "src": "kernel"}]}}}`:   `packages["x/"]: "Arch" isn't an os-release ID`,
		`{"packages": {"x/": {"arch": [{"value": "p;q", "src": "kernel"}]}}}`: `packages["x/"]["arch"]: "p;q" isn't a package name`,
		`{"package": {}}`: `unknown field "package"`,
	} {
		r := rule(data)
		if errs := ValidateRule(&r); len(errs) != 1 || !strings.Contains(errs[0].Error(), want) {
			t.Errorf("%s: %v, want %q", data, errs, want)
		}
	}
	// {package} only where the check provides it.
	r := noDriverRule()
	r.Actions = []kb.Action{{Text: "t", Commands: []string{"sudo pacman -S {package}"}}}
	if errs := ValidateRule(&r); len(errs) != 1 || !strings.Contains(errs[0].Error(), "uses {package}, which check \"pci-without-driver\" doesn't fill") {
		t.Errorf("%v", errs)
	}
	// A failure without its driver named, and a bad rule at run time.
	a := Advise(Input{Report: failed(cachy, report.FirmwareFailure{Device: "0000:02:00.0", File: "x/y", Error: -2}),
		KB: knowledge(rule(`{"packages": {"x/": {"arch": [{"value": "pkg", "src": "kernel"}]}}}`)), Now: noon})
	if len(a.Findings) != 1 || strings.Contains(evidence(a.Findings[0]), ".driver=") {
		t.Errorf("findings %+v", a.Findings)
	}
	if _, err := firmwareLoadFailed(&Input{Report: failed(cachy)}, &kb.Rule{Data: json.RawMessage(`{"packages": {}}`)}); err == nil {
		t.Error("bad data ran")
	}
}

// The package lookup: a "/" key covers its directory, any other key is a
// glob (never a bare prefix); the longest covering key wins, whatever the
// map's order; a file that failed validation gets no package, even from
// a "*" key.
func TestFirmwarePackageLookup(t *testing.T) {
	d, err := decodeFirmwareData(json.RawMessage(`{"packages": {
		"intel/": {"arch": [{"value": "short", "src": "kernel"}]},
		"intel/sof/": {"arch": [{"value": "long", "src": "kernel"}]},
		"ab": {"arch": [{"value": "glob", "src": "kernel"}]},
		"*": {"debian": [{"value": "any", "src": "kernel"}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	for range 20 { // map order differs between runs
		if pkg, _, _ := firmwarePackage(d, "intel/sof/x.ri", []string{"arch"}); pkg != "long" {
			t.Fatalf("longest key: %q", pkg)
		}
	}
	for file, want := range map[string]string{"intel/ibt.sfi": "short", "ab": "glob", "abc": "", "": ""} {
		if pkg, _, _ := firmwarePackage(d, file, []string{"arch"}); pkg != want {
			t.Errorf("%q: %q, want %q", file, pkg, want)
		}
	}
	if pkg, _, ok := firmwarePackage(d, "", []string{"debian"}); ok || pkg != "" {
		t.Errorf("an invalid file got %q from \"*\"", pkg)
	}
	if pkg, _, _ := firmwarePackage(d, "z.bin", []string{"fedora", "debian"}); pkg != "any" {
		t.Errorf("ID_LIKE fallback: %q", pkg)
	}
}

func sourcesOf(f Finding) string {
	var ids []string
	for _, s := range f.Sources {
		ids = append(ids, s.ID)
	}
	return strings.Join(ids, " ")
}
