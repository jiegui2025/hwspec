package advisor

import (
	"slices"
	"strings"
	"testing"

	"github.com/jiegui2025/hwspec/internal/report"
)

// wifi is #7's synthetic device: a driverless Intel AX200 (8086:2723,
// class 028000) on a CachyOS capture whose module index was found, with
// the candidates and blacklist given.
func wifi(cands []report.ModuleCandidate, bl ...report.BlacklistedModule) *report.Report {
	if bl == nil {
		bl = []report.BlacklistedModule{}
	}
	return &report.Report{
		OS: report.OS{ID: "cachyos", IDLike: "arch"},
		PCI: []report.PCIDevice{{Address: "0000:02:00.0", VendorID: "8086", DeviceID: "2723", SubVendorID: "8086", SubDeviceID: "0084",
			ClassCode: "028000", Class: "Network controller", ModuleCandidates: cands}},
		Kernel: &report.Kernel{ModuleIndex: &report.ModuleIndex{Release: "7.2.9-1-cachyos", Status: report.ModulesFound, Dir: "/lib/modules/7.2.9-1-cachyos"},
			ModuleBlacklist: bl},
	}
}

// only returns the advice's one finding for the device, failing unless
// there is exactly one.
func only(t *testing.T, a Advice) Finding {
	t.Helper()
	var got []Finding
	for _, f := range a.Findings {
		if f.Device != nil && f.Device.Key == "0000:02:00.0" {
			got = append(got, f)
		}
	}
	if len(got) != 1 {
		t.Fatalf("%d findings for the device: %+v (warnings %q)", len(got), got, a.Warnings)
	}
	return got[0]
}

func commands(f Finding) string {
	var out []string
	for _, a := range f.Actions {
		out = append(out, a.Commands...)
	}
	return strings.Join(out, "; ")
}

func evidence(f Finding) string {
	var out []string
	for _, e := range f.Evidence {
		v, ok := e.Value()
		if !ok {
			v = "absent"
		}
		out = append(out, e.Path()+"="+strings.TrimSpace(strings.ReplaceAll(fmtAny(v), "\n", " ")))
	}
	return strings.Join(out, " ")
}

func fmtAny(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	}
	return "?"
}

// #7's acceptance criteria for part 4, on the shipped rules.
func TestWhyADeviceHasNoDriver(t *testing.T) {
	k := embedded(t)
	iwl := []report.ModuleCandidate{{Module: "iwlwifi"}}
	t.Run("not loaded", func(t *testing.T) {
		f := only(t, Advise(Input{Report: wifi(iwl), KB: k, Now: noon}))
		if f.ID != "pci.driver-not-loaded" || commands(f) != "sudo modprobe iwlwifi" || f.Actions[0].Undo != "sudo modprobe -r {module}" && f.Actions[0].Undo != "sudo modprobe -r iwlwifi" {
			t.Errorf("%s: %q, undo %q", f.ID, commands(f), f.Actions[0].Undo)
		}
	})
	t.Run("blacklisted in modprobe.d", func(t *testing.T) {
		bl := report.BlacklistedModule{Module: "iwlwifi", Kind: report.BlacklistAlias, Source: "/etc/modprobe.d/x.conf"}
		f := only(t, Advise(Input{Report: wifi(iwl, bl), KB: k, Now: noon}))
		if f.ID != "pci.driver-blacklisted" || commands(f) != "" || !strings.Contains(evidence(f), "kernel.module_blacklist[0].source=/etc/modprobe.d/x.conf") {
			t.Errorf("%s: %q, evidence %s", f.ID, commands(f), evidence(f))
		}
	})
	t.Run("blacklisted on the command line", func(t *testing.T) {
		bl := report.BlacklistedModule{Module: "iwlwifi", Kind: report.BlacklistKernel, Source: "cmdline"}
		f := only(t, Advise(Input{Report: wifi(iwl, bl), KB: k, Now: noon}))
		if f.ID != "pci.driver-blacklisted" || !strings.Contains(evidence(f), "kernel.module_blacklist[0].kind=kernel kernel.module_blacklist[0].source=cmdline") {
			t.Errorf("%s: evidence %s", f.ID, evidence(f))
		}
	})
	t.Run("loaded, didn't bind", func(t *testing.T) {
		f := only(t, Advise(Input{Report: wifi([]report.ModuleCandidate{{Module: "iwlwifi", Loaded: true}}), KB: k, Now: noon}))
		if f.ID != "pci.driver-did-not-bind" || commands(f) != "journalctl -k -b --grep iwlwifi" {
			t.Errorf("%s: %q", f.ID, commands(f))
		}
	})
	t.Run("built in, didn't bind", func(t *testing.T) {
		f := only(t, Advise(Input{Report: wifi([]report.ModuleCandidate{{Module: "iwlwifi", Builtin: true, Loaded: true}}), KB: k, Now: noon}))
		if f.ID != "pci.driver-did-not-bind" {
			t.Errorf("%s", f.ID)
		}
	})
	t.Run("no candidate", func(t *testing.T) {
		f := only(t, Advise(Input{Report: wifi([]report.ModuleCandidate{}), KB: k, Now: noon}))
		if f.ID != "pci.no-driver" || !strings.Contains(evidence(f), "pci[0].module_candidates=?") {
			t.Errorf("%s: evidence %s", f.ID, evidence(f))
		}
	})
	t.Run("NVIDIA's own driver not loaded, nouveau blacklisted", func(t *testing.T) {
		bl := report.BlacklistedModule{Module: "nouveau", Kind: report.BlacklistAlias, Source: "/usr/lib/modprobe.d/nvidia.conf"}
		f := only(t, Advise(Input{Report: wifi([]report.ModuleCandidate{{Module: "nouveau"}, {Module: "nvidia"}}, bl), KB: k, Now: noon}))
		if f.ID != "pci.driver-not-loaded" || commands(f) != "sudo modprobe nvidia" {
			t.Errorf("%s: %q", f.ID, commands(f))
		}
	})
	t.Run("every candidate blacklisted: all entries shown", func(t *testing.T) {
		bl := []report.BlacklistedModule{
			{Module: "nouveau", Kind: report.BlacklistAlias, Source: "/etc/modprobe.d/a.conf"},
			{Module: "pcspkr", Kind: report.BlacklistAlias, Source: "/etc/modprobe.d/b.conf"},
			{Module: "nvidia", Kind: report.BlacklistKernel, Source: "cmdline"},
		}
		f := only(t, Advise(Input{Report: wifi([]report.ModuleCandidate{{Module: "nouveau"}, {Module: "nvidia"}}, bl...), KB: k, Now: noon}))
		ev := evidence(f)
		if f.ID != "pci.driver-blacklisted" || !strings.Contains(ev, "kernel.module_blacklist[0].source=/etc/modprobe.d/a.conf") ||
			!strings.Contains(ev, "kernel.module_blacklist[2].kind=kernel") || strings.Contains(ev, "module_blacklist[1]") {
			t.Errorf("%s: evidence %s", f.ID, ev)
		}
	})
	t.Run("a loaded candidate wins over a blacklisted one", func(t *testing.T) {
		bl := report.BlacklistedModule{Module: "a", Kind: report.BlacklistAlias, Source: "/etc/modprobe.d/x.conf"}
		f := only(t, Advise(Input{Report: wifi([]report.ModuleCandidate{{Module: "a"}, {Module: "b", Loaded: true}}, bl), KB: k, Now: noon}))
		if f.ID != "pci.driver-did-not-bind" || commands(f) != "journalctl -k -b --grep b" {
			t.Errorf("%s: %q", f.ID, commands(f))
		}
	})
	t.Run("two candidates: the first is named", func(t *testing.T) {
		f := only(t, Advise(Input{Report: wifi([]report.ModuleCandidate{{Module: "iwlwifi"}, {Module: "iwlmvm"}}), KB: k, Now: noon}))
		if commands(f) != "sudo modprobe iwlwifi" {
			t.Errorf("%q", commands(f))
		}
	})
	t.Run("a device with a driver isn't asked about", func(t *testing.T) {
		r := wifi([]report.ModuleCandidate{{Module: "iwlwifi"}})
		r.PCI[0].Driver = &report.Driver{Name: "iwlwifi"}
		for _, f := range Advise(Input{Report: r, KB: k, Now: noon}).Findings {
			if f.Device != nil {
				t.Errorf("%+v", f)
			}
		}
	})
	t.Run("shell metacharacters in a module name", func(t *testing.T) {
		a := Advise(Input{Report: wifi([]report.ModuleCandidate{{Module: "iwl;id"}}), KB: k, Now: noon})
		f := only(t, a)
		if f.ID != "pci.driver-not-loaded" || commands(f) != "" || !strings.Contains(strings.Join(a.Warnings, "\n"), "needs {module}") {
			t.Errorf("%s: %q, warnings %q", f.ID, commands(f), a.Warnings)
		}
	})
}

// The running kernel's modules gone (an upgrade without a reboot): one
// machine-wide finding, no device findings, and the device checks can't
// evaluate.
func TestModulesMissingMeansRebootPending(t *testing.T) {
	r := wifi(nil)
	r.Kernel.ModuleIndex = &report.ModuleIndex{Release: "7.2.8-1-cachyos", Status: report.ModulesOtherRelease, OtherReleases: []string{"7.2.9-1-cachyos"}}
	r.PCI = append(r.PCI, report.PCIDevice{Address: "0000:03:00.0", VendorID: "10ec", DeviceID: "8168", ClassCode: "020000"})
	a := Advise(Input{Report: r, KB: embedded(t), Now: noon})
	a.Findings = slices.DeleteFunc(a.Findings, func(f Finding) bool { return f.Category != "needs-attention" })
	if len(a.Findings) != 1 || a.Findings[0].ID != "kernel.modules-missing" || a.Findings[0].Device != nil {
		t.Fatalf("findings %+v", a.Findings)
	}
	w := strings.Join(a.Warnings, "\n")
	for _, rule := range []string{"pci.driver-not-loaded", "pci.driver-blacklisted", "pci.driver-did-not-bind"} {
		if !strings.Contains(w, "rule "+rule+" can't evaluate this capture") {
			t.Errorf("%s: warnings %q", rule, a.Warnings)
		}
	}
	if !strings.Contains(evidence(a.Findings[0]), "kernel.module_index.status=other_release") {
		t.Errorf("evidence %s", evidence(a.Findings[0]))
	}
}

// No module tree at all (a container without the host's, a kernel built
// without modules) isn't a pending reboot: no machine-wide finding, and
// the driverless device gets today's finding.
func TestNoModuleTreeIsNotARebootPending(t *testing.T) {
	r := wifi(nil)
	r.Kernel.ModuleIndex = &report.ModuleIndex{Release: "7.2.9-1-cachyos", Status: report.ModulesNone}
	a := Advise(Input{Report: r, KB: embedded(t), Now: noon})
	for _, f := range a.Findings {
		if f.ID == "kernel.modules-missing" {
			t.Errorf("%+v", f)
		}
	}
	if f := only(t, a); f.ID != "pci.no-driver" {
		t.Errorf("%s", f.ID)
	}
}

// A capture from before candidates and blacklists were read still gets
// pci.no-driver for its driverless devices; the new device rules say they
// can't evaluate it.
func TestOlderCapturesKeepTodaysFinding(t *testing.T) {
	r := wifi(nil)
	r.Kernel = nil
	a := Advise(Input{Report: r, KB: embedded(t), Now: noon})
	if f := only(t, a); f.ID != "pci.no-driver" {
		t.Errorf("%s", f.ID)
	}
	if !strings.Contains(strings.Join(a.Warnings, "\n"), "rule pci.driver-not-loaded can't evaluate this capture") {
		t.Errorf("warnings %q", a.Warnings)
	}
	// Candidates read, the blacklist not (between #131 and #212): the
	// device can't be told, so no finding rather than a wrong one.
	r = wifi([]report.ModuleCandidate{{Module: "iwlwifi"}})
	r.Kernel.ModuleBlacklist = nil
	a = Advise(Input{Report: r, KB: embedded(t), Now: noon})
	for _, f := range a.Findings {
		if f.Device != nil {
			t.Errorf("a finding without the blacklist: %+v", f)
		}
	}
}

// An action for another distro is left out; one for the capture's ID or
// an ID_LIKE word is kept, as is one for every distro.
func TestActionsFollowTheCapturesDistro(t *testing.T) {
	for _, c := range []struct {
		distro string
		os     report.OS
		kept   bool
	}{
		{"", report.OS{}, true},
		{"arch", report.OS{ID: "arch"}, true},
		{"arch", report.OS{ID: "cachyos", IDLike: "arch"}, true},
		{"debian", report.OS{ID: "ubuntu", IDLike: "debian"}, true},
		{"arch", report.OS{ID: "fedora"}, false},
		{"arch", report.OS{}, false},
		{"arch", report.OS{ID: "archer", IDLike: "archlinux"}, false},
	} {
		if got := distroMatches(c.distro, c.os); got != c.kept {
			t.Errorf("%q on %+v: %v", c.distro, c.os, got)
		}
	}
	rule := noDriverRule()
	rule.Actions = append(rule.Actions, rule.Actions[0])
	rule.Actions[1].Distro, rule.Actions[1].Text = "fedora", "dnf way"
	a := Advise(Input{Report: machine(), KB: knowledge(rule), Now: noon})
	for _, f := range a.Findings {
		if len(f.Actions) != 1 || f.Actions[0].Text != "Check the wiki" {
			t.Errorf("%s: actions %+v", f.Device.Key, f.Actions)
		}
	}
}
