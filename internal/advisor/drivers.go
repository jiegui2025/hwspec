package advisor

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/jiegui2025/hwspec/internal/kb"
	"github.com/jiegui2025/hwspec/internal/report"
)

// Why a PCI device has no driver (#214, #7 part 4). The capture names the
// modules whose aliases claim a driverless device (pci[].module_candidates,
// #131) and the modules kept from loading (kernel.module_blacklist, #212);
// each device gets one answer, from the first that holds:
//
//  1. a candidate is loaded (or built in), yet the device is unbound: its
//     probe failed ("did not bind");
//  2. a candidate is neither loaded nor blacklisted: load it ("not
//     loaded"), even if another is blacklisted, as nouveau is on machines
//     that use NVIDIA's own driver (review of #219);
//  3. every candidate is blacklisted: the entries say how and where;
//  4. no candidate: no in-kernel driver (pci-without-driver, narrowed).
//
// When the running kernel's modules aren't installed (an upgrade without a
// reboot), none of these can be told: one machine-wide finding says a
// reboot is pending, and the device checks can't evaluate.

// driverCase is one device's answer.
type driverCase int

const (
	caseNone driverCase = iota // no candidate, or none read: pci-without-driver
	caseDidNotBind
	caseBlacklisted
	caseNotLoaded
)

// moduleName is a module name as the capture writes it, safe in a command.
var moduleNameRe = regexp.MustCompile(`^[a-z0-9_]+$`)

// driverAnswer classifies a driverless device: its case, the candidate it
// rests on (its index in module_candidates) and, when every candidate is
// blacklisted, the blacklist entries naming them.
func driverAnswer(d *report.PCIDevice, bl []report.BlacklistedModule) (driverCase, int, []int) {
	if len(d.ModuleCandidates) == 0 {
		return caseNone, -1, nil
	}
	for i, c := range d.ModuleCandidates {
		if c.Loaded { // a built-in one is loaded too
			return caseDidNotBind, i, nil
		}
	}
	var entries []int
	for i, c := range d.ModuleCandidates {
		named := len(entries)
		for j, b := range bl {
			if b.Module == c.Module {
				entries = append(entries, j)
			}
		}
		if len(entries) == named {
			return caseNotLoaded, i, nil // not blacklisted: load it
		}
	}
	return caseBlacklisted, 0, entries
}

// modulesMissing says whether the running kernel's modules aren't
// installed, though other kernels' are.
func modulesMissing(r *report.Report) bool {
	return r.Kernel != nil && r.Kernel.ModuleIndex != nil && r.Kernel.ModuleIndex.Status == report.ModulesOtherRelease
}

// candidatesKnown says whether the device checks can tell their cases: the
// module index was found, and the blacklist read.
func candidatesKnown(in *Input) bool {
	k := in.Report.Kernel
	return k != nil && k.ModuleIndex != nil && k.ModuleIndex.Status == report.ModulesFound && k.ModuleBlacklist != nil
}

func init() {
	register("kernel-modules-missing", check{
		run:   kernelModulesMissing,
		needs: []string{"kernel.module_index"},
		available: func(in *Input) bool {
			return in.Report.Kernel != nil && in.Report.Kernel.ModuleIndex != nil
		},
		example: func() (kb.Rule, *report.Report) {
			return kb.Rule{ID: "kernel.modules-missing", Check: "kernel-modules-missing", Category: "needs-attention", Severity: "warning",
					Title: "Reboot pending", Src: []string{"kernel"}},
				&report.Report{Kernel: &report.Kernel{ModuleIndex: &report.ModuleIndex{Release: "7.2.8-1", Status: report.ModulesOtherRelease,
					OtherReleases: []string{"7.2.9-1"}}}}
		},
	})
	for name, want := range map[string]driverCase{
		"pci-driver-did-not-bind": caseDidNotBind,
		"pci-driver-blacklisted":  caseBlacklisted,
		"pci-driver-not-loaded":   caseNotLoaded,
	} {
		register(name, check{
			run:       driverCheck(want),
			needs:     []string{"pci[].module_candidates", "kernel.module_index", "kernel.module_blacklist"},
			available: candidatesKnown,
			provides:  []string{"module"},
			matches:   []string{"pci_class"},
			requires:  []string{"pci_class"},
			example:   driverExample(name, want),
		})
	}
}

// kernelModulesMissing is the machine-wide finding: the running kernel's
// modules aren't installed, so no driver can be loaded until a reboot.
func kernelModulesMissing(in *Input, _ *kb.Rule) ([]hit, error) {
	if !modulesMissing(in.Report) {
		return nil, nil
	}
	idx := in.Report.Kernel.ModuleIndex
	return []hit{{evidence: []Evidence{
		Present("kernel.module_index.release", idx.Release),
		Present("kernel.module_index.status", idx.Status),
		Present("kernel.module_index.other_releases", idx.OtherReleases),
	}}}, nil
}

// driverCheck finds the devices of the rule's classes whose answer is
// want: each device gets exactly one of the device checks' findings.
func driverCheck(want driverCase) func(*Input, *kb.Rule) ([]hit, error) {
	return func(in *Input, rule *kb.Rule) ([]hit, error) {
		bl := in.Report.Kernel.ModuleBlacklist
		var hits []hit
		for m := range pciMatches(in.Report, rule.Match) {
			if m.dev.Driver != nil {
				continue
			}
			got, cand, entries := driverAnswer(m.dev, bl)
			if got != want {
				continue
			}
			c := m.dev.ModuleCandidates[cand]
			at := fmt.Sprintf("module_candidates[%d].", cand)
			h := hit{device: m.ref, evidence: append(m.evidence, Absent(m.path("driver")),
				Present(m.path(at+"module"), c.Module), Present(m.path(at+"loaded"), c.Loaded))}
			for _, j := range entries {
				at := fmt.Sprintf("kernel.module_blacklist[%d].", j)
				h.evidence = append(h.evidence, Present(at+"kind", bl[j].Kind), Present(at+"source", bl[j].Source))
			}
			if moduleNameRe.MatchString(c.Module) {
				h.vars = map[string]string{"module": c.Module}
			}
			hits = append(hits, h)
		}
		return hits, nil
	}
}

// driverExample is a driverless Wi-Fi card (8086:2723) whose candidate,
// iwlwifi, is in the example's case.
func driverExample(name string, want driverCase) func() (kb.Rule, *report.Report) {
	return func() (kb.Rule, *report.Report) {
		cand := report.ModuleCandidate{Module: "iwlwifi", Loaded: want == caseDidNotBind}
		bl := []report.BlacklistedModule{{Module: "pcspkr", Kind: report.BlacklistAlias, Source: "/etc/modprobe.d/x.conf"}}
		if want == caseBlacklisted {
			bl = append(bl, report.BlacklistedModule{Module: "iwlwifi", Kind: report.BlacklistAlias, Source: "/etc/modprobe.d/wifi.conf"})
		}
		rule := kb.Rule{ID: "pci." + name, Check: name, Category: "needs-attention", Severity: "warning", Title: "Driver",
			Match: kb.Match{PCIClass: []string{"02"}}, Src: []string{"kernel"}}
		if want != caseBlacklisted {
			rule.Actions = []kb.Action{{Text: "t", Commands: []string{"echo {module}"}}}
		}
		return rule, &report.Report{
			PCI: []report.PCIDevice{{Address: "0000:02:00.0", VendorID: "8086", DeviceID: "2723", ClassCode: "028000",
				Class: "Network controller", ModuleCandidates: []report.ModuleCandidate{cand}}},
			Kernel: &report.Kernel{ModuleIndex: &report.ModuleIndex{Release: "7.2.9-1", Status: report.ModulesFound, Dir: "/lib/modules/7.2.9-1"},
				ModuleBlacklist: bl},
		}
	}
}

// distroMatches says whether an action for distro applies to the
// capture's OS: no distro means every one; otherwise its os-release ID or
// one of its ID_LIKE words (CachyOS: ID=cachyos, ID_LIKE=arch).
func distroMatches(distro string, os report.OS) bool {
	if distro == "" {
		return true
	}
	return distro == os.ID || slices.Contains(strings.Fields(os.IDLike), distro)
}
