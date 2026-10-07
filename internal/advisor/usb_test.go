package advisor

import (
	"strings"
	"testing"

	"github.com/jiegui2025/hwspec/internal/kb"
	"github.com/jiegui2025/hwspec/internal/report"
)

// keyboard is a capture with one USB keyboard whose boot-keyboard
// interface (030101) has no driver, with the given candidates.
func keyboard(cands []report.ModuleCandidate, modalias string, bl ...report.BlacklistedModule) *report.Report {
	if bl == nil {
		bl = []report.BlacklistedModule{}
	}
	return &report.Report{OS: report.OS{ID: "cachyos", IDLike: "arch"},
		USB: []report.USBDevice{{Path: "1-5", VendorID: "046d", ProductID: "c31c", Identity: &report.Identity{Model: "Keyboard K120"},
			Interfaces: []report.USBInterface{
				{Name: "1-5:1.0", ClassCode: "030101", Modalias: modalias, ModuleCandidates: cands},
				{Name: "1-5:1.1", ClassCode: "ff0000", Modalias: "usb:v046DpC31Cd6400dc00dsc00dp00icFFisc00ip00in01"}, // vendor-specific: not flagged
			}}},
		Kernel: &report.Kernel{ModuleIndex: &report.ModuleIndex{Release: "7.2.9-1-cachyos", Status: report.ModulesFound, Dir: "/lib/modules/7.2.9-1-cachyos"},
			ModuleBlacklist: bl}}
}

const k120 = "usb:v046DpC31Cd6400dc00dsc00dp00ic03isc01ip01in00"

func usbFinding(t *testing.T, a Advice) Finding {
	t.Helper()
	var got []Finding
	for _, f := range a.Findings {
		if f.Device != nil && f.Device.Kind == "usb" {
			got = append(got, f)
		}
	}
	if len(got) != 1 {
		t.Fatalf("%d USB findings: %+v (warnings %q)", len(got), got, a.Warnings)
	}
	return got[0]
}

// #7's acceptance for part 6, on the shipped rules.
func TestUSBInterfacesWithoutADriver(t *testing.T) {
	k := embedded(t)
	t.Run("not loaded", func(t *testing.T) {
		f := usbFinding(t, Advise(Input{Report: keyboard([]report.ModuleCandidate{{Module: "usbhid"}}, k120), KB: k, Now: noon}))
		if f.ID != "usb.driver-not-loaded" || commands(f) != "sudo modprobe usbhid" || f.Device.Key != "1-5:1.0" || f.Device.Name != "Keyboard K120" {
			t.Errorf("%s: %q, device %+v", f.ID, commands(f), f.Device)
		}
		if ev := evidence(f); !strings.Contains(ev, "usb[0].interfaces[0].class_code=030101") || !strings.Contains(ev, "usb[0].interfaces[0].module_candidates[0].module=usbhid") {
			t.Errorf("evidence %s", ev)
		}
	})
	t.Run("blacklisted", func(t *testing.T) {
		bl := report.BlacklistedModule{Module: "usbhid", Kind: report.BlacklistKernel, Source: "cmdline"}
		if f := usbFinding(t, Advise(Input{Report: keyboard([]report.ModuleCandidate{{Module: "usbhid"}}, k120, bl), KB: k, Now: noon})); f.ID != "usb.driver-blacklisted" || commands(f) != "" {
			t.Errorf("%s: %q", f.ID, commands(f))
		}
	})
	t.Run("didn't bind", func(t *testing.T) {
		f := usbFinding(t, Advise(Input{Report: keyboard([]report.ModuleCandidate{{Module: "usbhid", Builtin: true, Loaded: true}}, k120), KB: k, Now: noon}))
		if f.ID != "usb.driver-did-not-bind" || commands(f) != "journalctl -k -b --grep usbhid" {
			t.Errorf("%s: %q", f.ID, commands(f))
		}
	})
	t.Run("no candidate", func(t *testing.T) {
		f := usbFinding(t, Advise(Input{Report: keyboard([]report.ModuleCandidate{}, k120), KB: k, Now: noon}))
		if f.ID != "usb.no-driver" || commands(f) != "modprobe -R "+k120 || !strings.Contains(evidence(f), "usb[0].interfaces[0].module_candidates=?") {
			t.Errorf("%s: %q, evidence %s", f.ID, commands(f), evidence(f))
		}
	})
	t.Run("a modalias that isn't the kernel's form reaches no command", func(t *testing.T) {
		for _, bad := range []string{k120 + ";id", "usb:v046DpC31C$(id)", strings.ToLower(k120), ""} {
			a := Advise(Input{Report: keyboard([]report.ModuleCandidate{}, bad), KB: k, Now: noon})
			if f := usbFinding(t, a); commands(f) != "" || !strings.Contains(strings.Join(a.Warnings, "\n"), "needs {modalias}") {
				t.Errorf("%q: %q, warnings %q", bad, commands(f), a.Warnings)
			}
		}
	})
	t.Run("a bound interface, and modules missing", func(t *testing.T) {
		r := keyboard([]report.ModuleCandidate{}, k120)
		r.USB[0].Interfaces[0].Driver = &report.Driver{Name: "usbhid"}
		for _, f := range Advise(Input{Report: r, KB: k, Now: noon}).Findings {
			if f.Device != nil {
				t.Errorf("%+v", f)
			}
		}
		r = keyboard(nil, k120)
		r.Kernel.ModuleIndex = &report.ModuleIndex{Release: "7.2.8", Status: report.ModulesOtherRelease, OtherReleases: []string{"7.2.9"}}
		for _, f := range Advise(Input{Report: r, KB: k, Now: noon}).Findings {
			if f.Device != nil {
				t.Errorf("modules missing: %+v", f)
			}
		}
	})
}

// A capture from before USB interfaces were read can't be evaluated per
// interface; one with no USB devices at all can.
func TestUSBRulesOnOlderCaptures(t *testing.T) {
	k := embedded(t)
	r := keyboard(nil, k120)
	r.USB[0].Interfaces = nil
	w := strings.Join(Advise(Input{Report: r, KB: k, Now: noon}).Warnings, "\n")
	for _, rule := range []string{"usb.no-driver", "usb.driver-not-loaded"} {
		if !strings.Contains(w, "rule "+rule+" can't evaluate this capture") {
			t.Errorf("%s: %q", rule, w)
		}
	}
	r.USB = nil
	w = strings.Join(Advise(Input{Report: r, KB: k, Now: noon}).Warnings, "\n")
	if strings.Contains(w, "rule usb.") {
		t.Errorf("no USB devices: %q", w)
	}
}

// The iterators stop when their caller does.
func TestDriverlessIteratorsStop(t *testing.T) {
	r := keyboard([]report.ModuleCandidate{}, k120)
	r.USB = append(r.USB, r.USB[0])
	r.PCI = []report.PCIDevice{{Address: "0000:02:00.0", ClassCode: "028000"}, {Address: "0000:03:00.0", ClassCode: "028000"}}
	for name, seq := range map[string]func(func(driverless) bool){
		"pci": pciDriverless(r, kb.Match{PCIClass: []string{"02"}}),
		"usb": usbDriverless(r, kb.Match{USBClass: []string{"03"}}),
	} {
		n := 0
		for range seq {
			n++
			break
		}
		if n != 1 {
			t.Errorf("%s: %d", name, n)
		}
	}
}
