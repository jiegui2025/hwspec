package advisor

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/jiegui2025/hwspec/internal/kb"
	"github.com/jiegui2025/hwspec/internal/report"
)

func vendorKB(group string) *kb.KB {
	rule, _ := checks["vendor-firmware"].example()
	k := exampleKnowledge("vendor-firmware", rule)
	if group != "" {
		k.Models[0].Data["vendor_firmware"] = json.RawMessage(group)
	}
	return k
}

func hpBIOS(version string) *report.Report {
	return &report.Report{System: report.System{Identity: &report.Identity{Vendor: "HP", Model: "HP EliteDesk 800 G5 Desktop Mini"},
		Firmware: &report.Firmware{Vendor: "HP", Version: version, Source: "dmi"}}}
}

// #227's acceptance: BIOS R21 says "check the page", with HP's page (the
// source) and the date it was read, whichever side is newer.
func TestVendorFirmwareSaysCheckThePage(t *testing.T) {
	for installed, want := range map[string]string{
		"R21 Ver. 02.27.00": "this machine runs R21 Ver. 02.27.00, the same",
		"R21 Ver. 02.26.00": "this machine runs R21 Ver. 02.26.00, older: the page has the update",
		"R21 Ver. 02.28.00": "this machine runs R21 Ver. 02.28.00, newer than that",
	} {
		a := Advise(Input{Report: hpBIOS(installed), KB: vendorKB(""), Now: noon})
		if len(a.Findings) != 1 || len(a.Warnings) != 0 {
			t.Fatalf("%s: %+v, warnings %q", installed, a.Findings, a.Warnings)
		}
		f := a.Findings[0]
		text := f.Answers[0].Text
		if !strings.HasPrefix(text, "example-notes lists R21 02.27.00 (2026-08-11) as the latest, as of 2026-10-07; "+want+". When the page was read the vendor published it only on its own site, not on LVFS") ||
			!strings.HasSuffix(text, "check the page") || len(f.Sources) != 2 || f.Sources[1].URL != "https://example.com/sp1.html" ||
			f.Device.Name != "R21 BIOS" || f.Answers[0].Claims[0].Published != "2026-08-11" {
			t.Errorf("%s: %q, sources %+v", installed, text, f.Sources)
		}
	}
	// Another vendor's BIOS has no comparator yet: said, not guessed.
	r := hpBIOS("1.2.3")
	r.System.Identity.Vendor = "Dell Inc."
	k := vendorKB("")
	k.Models[0].Match.SysVendor = "Dell Inc."
	a := Advise(Input{Report: r, KB: k, Now: noon})
	if len(a.Findings) != 1 || !strings.Contains(a.Findings[0].Answers[0].Text, `no BIOS version comparator for vendor "Dell Inc."`) {
		t.Errorf("another vendor: %+v", a.Findings)
	}
}

// Several sources: each answered, each cited.
func TestVendorFirmwareEachSource(t *testing.T) {
	k := vendorKB(`{"system_bios": {"family": [{"value": "R21", "src": "capture"}],
		"latest": [{"value": {"version": "02.27.00", "date": "2026-08-11"}, "src": "example-notes"},
		           {"value": {"version": "02.26.00", "date": "2026-06-01"}, "src": "capture"}]}}`)
	a := Advise(Input{Report: hpBIOS("R21 Ver. 02.26.00"), KB: k, Now: noon})
	if len(a.Findings) != 1 || len(a.Findings[0].Answers) != 2 || !strings.Contains(a.Findings[0].Answers[1].Text, "R21 Ver. 02.26.00, the same") ||
		len(a.Findings[0].Sources) != 2 || a.Findings[0].Sources[1].ID != "example-notes" {
		t.Errorf("%+v", a.Findings)
	}
}

// No finding without the model's entry or its group; a group this build
// can't read skips the rule with a warning; an unknown BIOS version can't
// be evaluated.
func TestVendorFirmwareWhenItCantSay(t *testing.T) {
	r := hpBIOS("R21 Ver. 02.27.00")
	r.System.Identity.Model = "HP Something Else"
	if a := Advise(Input{Report: r, KB: vendorKB(""), Now: noon}); len(a.Findings) != 0 || len(a.Warnings) != 0 {
		t.Errorf("another model: %+v %q", a.Findings, a.Warnings)
	}
	k := vendorKB("")
	delete(k.Models[0].Data, "vendor_firmware")
	if a := Advise(Input{Report: hpBIOS("R21 Ver. 02.27.00"), KB: k, Now: noon}); len(a.Findings) != 0 || len(a.Warnings) != 0 {
		t.Errorf("no group: %+v %q", a.Findings, a.Warnings)
	}
	for group, want := range map[string]string{
		`{"system_bios": {"family": [{"value": "R21", "src": "x"}], "latest": [{"value": {"version": "2.27", "date": "2026-08-11"}, "src": "x"}]}}`:                 `version "2.27" isn't three numbers`,
		`{"system_bios": {"family": [{"value": "R21", "src": "x"}]}}`:                                                                                               "needs a family and the latest release",
		`{"system_bios": {"family": [{"value": "R21", "src": "x"}], "latest": [{"value": {"version": "02.27.00", "date": "2026-08-11", "url": "u"}, "src": "x"}]}}`: `unknown field "url"`,
		`{"bios": {}}`: `unknown field "bios"`,
		`{"system_bios": {"family": [{"value": "r21", "src": "x"}], "latest": [{"value": {"version": "02.27.00", "date": "2026-08-11"}, "src": "x"}]}}`: `"r21" isn't a family such as R21`,
	} {
		a := Advise(Input{Report: hpBIOS("R21 Ver. 02.27.00"), KB: vendorKB(group), Now: noon})
		if len(a.Findings) != 0 || len(a.Warnings) != 1 || !strings.Contains(a.Warnings[0], "rule firmware.vendor-only skipped: this build can't read") || !strings.Contains(a.Warnings[0], want) {
			t.Errorf("%s: %q", group, a.Warnings)
		}
		if errs := ValidateModel(&vendorKB(group).Models[0], nil); len(errs) != 1 || !strings.Contains(errs[0].Error(), "data.vendor_firmware: ") {
			t.Errorf("ValidateModel %s: %v", group, errs)
		}
	}
	r = hpBIOS("")
	r.System.Firmware = report.UnknownFirmware("x")
	if a := Advise(Input{Report: r, KB: vendorKB(""), Now: noon}); len(a.Findings) != 0 || !strings.Contains(strings.Join(a.Warnings, ""), "needs system.firmware.version") {
		t.Errorf("unknown BIOS version: %+v %q", a.Findings, a.Warnings)
	}
}

// Round 1 of #236's review. I1: when LVFS carries the machine's system
// firmware (an ESRT system entry it has a component for), its finding
// speaks for it, and this one stands down. Minors: another family's BIOS
// gets no page meant for R21; every family claim counts.
func TestVendorFirmwareRound1(t *testing.T) {
	const class = "b1413ca8-c3de-4754-9e3c-2a719d79cdbc"
	withESRT := func(kind string) *report.Report {
		r := hpBIOS("R21 Ver. 02.26.00")
		r.System.ESRT = &report.ESRT{Entries: []report.ESRTEntry{{FWClass: strings.ToUpper(class), FWType: kind, FWVersion: 1}}}
		return r
	}
	lv := &LVFS{Components: map[string][]LVFSComponent{class: {{ID: "com.hp.r21", Releases: []LVFSRelease{{Version: "1"}}}}}}
	if a := Advise(Input{Report: withESRT("system"), KB: vendorKB(""), Now: noon, LVFS: lv}); len(a.Findings) != 0 {
		t.Errorf("LVFS carries the system firmware: %+v", a.Findings)
	}
	for name, in := range map[string]Input{
		"a device capsule":    {Report: withESRT("device"), LVFS: lv},
		"no component for it": {Report: withESRT("system"), LVFS: &LVFS{Components: map[string][]LVFSComponent{}}},
		"no LVFS catalogue":   {Report: withESRT("system")},
		"no ESRT":             {Report: hpBIOS("R21 Ver. 02.26.00"), LVFS: lv},
	} {
		in.KB, in.Now = vendorKB(""), noon
		if a := Advise(in); len(a.Findings) != 1 {
			t.Errorf("%s: %+v", name, a.Findings)
		}
	}

	// Another family: no finding, rather than R21's page for a Q21 BIOS.
	if a := Advise(Input{Report: hpBIOS("Q21 Ver. 01.00.00"), KB: vendorKB(""), Now: noon}); len(a.Findings) != 0 || len(a.Warnings) != 0 {
		t.Errorf("another family: %+v %q", a.Findings, a.Warnings)
	}
	// Two families claimed: the machine's is used, with its source.
	two := vendorKB(`{"system_bios": {"family": [{"value": "R21", "src": "capture"}, {"value": "Q21", "src": "family-notes"}],
		"latest": [{"value": {"version": "01.02.00", "date": "2026-08-11"}, "src": "example-notes"}]}}`)
	two.Sources = append(two.Sources, kb.Source{ID: "family-notes", URL: "https://example.com/q21.html", Retrieved: "2026-10-07",
		Licence: "proprietary", Confidence: "oem-doc", LinkOnly: true, Locator: "TITLE"})
	slices.SortFunc(two.Sources, func(a, b kb.Source) int { return strings.Compare(a.ID, b.ID) })
	a := Advise(Input{Report: hpBIOS("Q21 Ver. 01.02.00"), KB: two, Now: noon})
	if len(a.Findings) != 1 || a.Findings[0].Device.Name != "Q21 BIOS" || len(a.Findings[0].Sources) != 3 ||
		!slices.ContainsFunc(a.Findings[0].Sources, func(s Source) bool { return s.ID == "family-notes" }) ||
		!strings.HasPrefix(a.Findings[0].Answers[0].Text, "example-notes lists Q21 01.02.00 (2026-08-11) as the latest, as of 2026-10-07; this machine runs Q21 Ver. 01.02.00, the same") {
		t.Errorf("two families: %+v", a.Findings)
	}
}

// What genkb refuses in a vendor_firmware group: each form, a missing
// family, and a release dated after its page was read; with a bad memory
// group too, both errors.
func TestValidateVendorFirmware(t *testing.T) {
	k := vendorKB("")
	for group, want := range map[string]string{
		`{"system_bios": {"family": [{"value": "R21X", "src": "x"}], "latest": [{"value": {"version": "02.27.00", "date": "2026-08-11"}, "src": "x"}]}}`:                              `"R21X" isn't a family`,
		`{"system_bios": {"family": [{"value": "R21", "src": "x"}, {"value": "q21", "src": "x"}], "latest": [{"value": {"version": "02.27.00", "date": "2026-08-11"}, "src": "x"}]}}`: `"q21" isn't a family`,
		`{"system_bios": {"family": [{"value": "R21", "src": "x"}], "latest": [{"value": {"version": "02.27.00a", "date": "2026-08-11"}, "src": "x"}]}}`:                              `version "02.27.00a" isn't three numbers`,
		`{"system_bios": {"family": [{"value": "R21", "src": "x"}], "latest": [{"value": {"version": "02.27.00", "date": "11/08/2026"}, "src": "x"}]}}`:                               `needs its release date as YYYY-MM-DD (got "11/08/2026")`,
		`{"system_bios": {"latest": [{"value": {"version": "02.27.00", "date": "2026-08-11"}, "src": "x"}]}}`:                                                                         "needs a family and the latest release",
		`{"system_bios": {"family": [{"value": "R21", "src": "example-notes"}], "latest": [{"value": {"version": "02.27.00", "date": "2099-01-01"}, "src": "example-notes"}]}}`:       "dated 2099-01-01, after its source example-notes was read (2026-10-07)",
	} {
		m := k.Models[0]
		m.Data = map[string]json.RawMessage{"vendor_firmware": json.RawMessage(group)}
		if errs := ValidateModel(&m, k.Source); len(errs) != 1 || !strings.Contains(errs[0].Error(), want) {
			t.Errorf("%s: %v, want %q", group, errs, want)
		}
	}
	m := k.Models[0]
	m.Data = map[string]json.RawMessage{"vendor_firmware": json.RawMessage(`{"bios": {}}`), "memory": json.RawMessage(`{"slots": [{"value": 0, "src": "x"}]}`)}
	if errs := ValidateModel(&m, k.Source); len(errs) != 2 {
		t.Errorf("both groups bad: %v", errs)
	}
}
