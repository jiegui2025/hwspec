package advisor

import (
	"fmt"
	"iter"
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
func driverAnswer(cands []report.ModuleCandidate, bl []report.BlacklistedModule) (driverCase, int, []int) {
	if len(cands) == 0 {
		return caseNone, -1, nil
	}
	for i, c := range cands {
		if c.Loaded { // a built-in one is loaded too
			return caseDidNotBind, i, nil
		}
	}
	var entries []int
	for i, c := range cands {
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
	for suffix, want := range map[string]driverCase{
		"-driver-did-not-bind": caseDidNotBind,
		"-driver-blacklisted":  caseBlacklisted,
		"-driver-not-loaded":   caseNotLoaded,
	} {
		register("pci"+suffix, check{
			run:       driverCheck(want, pciDriverless),
			needs:     []string{"pci[].module_candidates", "kernel.module_index", "kernel.module_blacklist"},
			available: candidatesKnown,
			provides:  []string{"module"},
			matches:   []string{"pci_class"},
			requires:  []string{"pci_class"},
			example:   driverExample("pci"+suffix, want),
		})
		register("usb"+suffix, check{
			run:       driverCheck(want, usbDriverless),
			needs:     []string{"usb[].interfaces", "kernel.module_index", "kernel.module_blacklist"},
			available: func(in *Input) bool { return candidatesKnown(in) && usbInterfacesRead(in.Report) },
			provides:  []string{"module"},
			matches:   []string{"usb_class"},
			requires:  []string{"usb_class"},
			example:   usbDriverExample("usb"+suffix, want),
		})
	}
	register("usb-without-driver", check{
		run:       usbWithoutDriver,
		needs:     []string{"usb[].interfaces"},
		available: func(in *Input) bool { return usbInterfacesRead(in.Report) },
		provides:  []string{"modalias"},
		matches:   []string{"usb_class"},
		requires:  []string{"usb_class"},
		example:   usbDriverExample("usb-without-driver", caseNone),
	})
}

// usbInterfacesRead says whether the capture lists USB interfaces: one
// from before them (#216) can't be evaluated per interface.
func usbInterfacesRead(r *report.Report) bool {
	return len(r.USB) == 0 || slices.ContainsFunc(r.USB, func(d report.USBDevice) bool { return d.Interfaces != nil })
}

// driverless is a device or interface without a driver, selected by a
// rule's class match: its reference, the evidence of the match, where its
// fields are, and its candidate modules.
type driverless struct {
	ref      *DeviceRef
	evidence []Evidence
	path     func(field string) string
	cands    []report.ModuleCandidate
	modalias string // a USB interface's, validated for a command, or ""
}

// pciDriverless yields the PCI devices of the rule's classes without a
// driver.
func pciDriverless(r *report.Report, match kb.Match) iter.Seq[driverless] {
	return func(yield func(driverless) bool) {
		for m := range pciMatches(r, match) {
			if m.dev.Driver != nil {
				continue
			}
			if !yield(driverless{ref: m.ref, evidence: m.evidence, path: m.path, cands: m.dev.ModuleCandidates}) {
				return
			}
		}
	}
}

// usbModalias is a USB interface's modalias as the kernel writes it
// (#7), checked before it goes into a command: a capture may be someone
// else's file.
var usbModalias = regexp.MustCompile(`^usb:v[0-9A-F]{4}p[0-9A-F]{4}d[0-9A-F]{4}dc[0-9A-F]{2}dsc[0-9A-F]{2}dp[0-9A-F]{2}ic[0-9A-F]{2}isc[0-9A-F]{2}ip[0-9A-F]{2}in[0-9A-F]{2}$`)

// usbDriverless yields the USB interfaces of the rule's classes (class,
// subclass, protocol prefixes) without a driver, named after their device.
func usbDriverless(r *report.Report, match kb.Match) iter.Seq[driverless] {
	return func(yield func(driverless) bool) {
		for i, d := range r.USB {
			name := d.Class
			if d.Identity != nil && d.Identity.Model != "" {
				name = d.Identity.Model
			}
			for j, in := range d.Interfaces {
				if in.Driver != nil || !hasAnyPrefix(strings.ToLower(in.ClassCode), match.USBClass) {
					continue
				}
				path := func(field string) string { return fmt.Sprintf("usb[%d].interfaces[%d].%s", i, j, field) }
				modalias := ""
				if usbModalias.MatchString(in.Modalias) {
					modalias = in.Modalias
				}
				if !yield(driverless{ref: &DeviceRef{Kind: "usb", Key: in.Name, Name: name},
					evidence: []Evidence{Present(path("class_code"), in.ClassCode)}, path: path, cands: in.ModuleCandidates, modalias: modalias}) {
					return
				}
			}
		}
	}
}

// usbWithoutDriver is pci-without-driver for USB interfaces: those no
// module claims, or whose candidates weren't read.
func usbWithoutDriver(in *Input, rule *kb.Rule) ([]hit, error) {
	if modulesMissing(in.Report) {
		return nil, nil
	}
	var hits []hit
	for d := range usbDriverless(in.Report, rule.Match) {
		if len(d.cands) > 0 {
			continue
		}
		h := hit{device: d.ref, evidence: append(d.evidence, Absent(d.path("driver")))}
		if d.cands != nil {
			h.evidence = append(h.evidence, Present(d.path("module_candidates"), d.cands))
		}
		if d.modalias != "" {
			h.vars = map[string]string{"modalias": d.modalias}
		}
		hits = append(hits, h)
	}
	return hits, nil
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

// driverCheck finds the devices or interfaces of the rule's classes whose
// answer is want: each gets exactly one of the driver checks' findings.
func driverCheck(want driverCase, source func(*report.Report, kb.Match) iter.Seq[driverless]) func(*Input, *kb.Rule) ([]hit, error) {
	return func(in *Input, rule *kb.Rule) ([]hit, error) {
		bl := in.Report.Kernel.ModuleBlacklist
		var hits []hit
		for m := range source(in.Report, rule.Match) {
			got, cand, entries := driverAnswer(m.cands, bl)
			if got != want {
				continue
			}
			c := m.cands[cand]
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

// usbDriverExample is a driverless HID interface (a keyboard's) whose
// candidate, usbhid, is in the example's case; for usb-without-driver,
// one no module claims.
func usbDriverExample(name string, want driverCase) func() (kb.Rule, *report.Report) {
	return func() (kb.Rule, *report.Report) {
		var cands []report.ModuleCandidate
		if want != caseNone {
			cands = []report.ModuleCandidate{{Module: "usbhid", Loaded: want == caseDidNotBind}}
		} else {
			cands = []report.ModuleCandidate{}
		}
		bl := []report.BlacklistedModule{}
		if want == caseBlacklisted {
			bl = append(bl, report.BlacklistedModule{Module: "usbhid", Kind: report.BlacklistKernel, Source: "cmdline"})
		}
		rule := kb.Rule{ID: "usb." + name, Check: name, Category: "needs-attention", Severity: "warning", Title: "Driver",
			Match: kb.Match{USBClass: []string{"03"}}, Src: []string{"kernel"}}
		switch want {
		case caseNone:
			rule.Actions = []kb.Action{{Text: "t", Commands: []string{"modprobe -R {modalias}"}}}
		case caseBlacklisted:
		default:
			rule.Actions = []kb.Action{{Text: "t", Commands: []string{"echo {module}"}}}
		}
		return rule, &report.Report{
			USB: []report.USBDevice{{Path: "1-5", VendorID: "046d", ProductID: "c31c", Class: "Human Interface Device",
				Identity: &report.Identity{Model: "Keyboard K120"},
				Interfaces: []report.USBInterface{{Name: "1-5:1.0", ClassCode: "030101",
					Modalias: "usb:v046DpC31Cd6400dc00dsc00dp00ic03isc01ip01in00", ModuleCandidates: cands}}}},
			Kernel: &report.Kernel{ModuleIndex: &report.ModuleIndex{Release: "7.2.9-1", Status: report.ModulesFound, Dir: "/lib/modules/7.2.9-1"},
				ModuleBlacklist: bl},
		}
	}
}
