package collect

import (
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/jiegui2025/hwspec/internal/report"
)

// modules.alias lines as the kernel's depmod writes them (from the
// reference machine's 7.2.9 index), plus malformed ones that are skipped.
const aliasFile = `# Aliases extracted from modules themselves.
alias pci:v00008086d00002723sv*sd*bc*sc*i* iwlwifi
alias pci:v00008086d0000A348sv*sd*bc*sc*i* snd_hda_intel
alias pci:v00008086d*sv*sd*bc04sc03i00* snd-hda-intel
alias pci:v*d*sv*sd*bc01sc08i02* nvme
alias pci:v0000103Cd00003[2-4]0Bsv*sd*bc*sc*i* hpsa
alias pci:v00008086d0000A3?8sv*sd*bc*sc*i* wildcard_one
alias pci:v00008086d0000[ sv*sd*bc*sc*i* broken_glob
alias pci:v00008086d0000A348sv*sd*bc*sc*i* a_first
alias too few
blacklist something else
`

func TestModulesAliasParsing(t *testing.T) {
	got := parseModulesAlias([]byte(aliasFile))
	if len(got) != 8 || got[2].module != "snd_hda_intel" || got[0].builtin {
		t.Errorf("parsed %+v", got)
	}
	// modules.builtin.modinfo: NUL-separated entries of every key.
	modinfo := "ahci.alias=pci:v*d*sv*sd*bc01sc06i01*\x00ahci.license=GPL\x00xhci-pci.alias=pci:v*d*sv*sd*bc0Csc03i30*\x00" +
		"broken\x00.alias=x\x00nokey=y\x00ext4.alias=\x00"
	b := parseBuiltinModinfo([]byte(modinfo))
	if len(b) != 2 || b[0].module != "ahci" || b[1].module != "xhci_pci" || !b[1].builtin {
		t.Errorf("builtin %+v", b)
	}
}

// Globs match as the kernel's fnmatch does: *, ? and [ranges]; names
// with - and _ are one module; a module listed by several aliases, or
// both built in and as a file, comes once.
func TestModuleCandidates(t *testing.T) {
	// The built-in alias first: a module is built in whichever alias says so.
	aliases := append([]moduleAlias{{pattern: "pci:v00008086d0000A348sv*sd*bc*sc*i*", module: "snd_hda_intel", builtin: true}},
		parseModulesAlias([]byte(aliasFile))...)
	for modalias, want := range map[string][]report.ModuleCandidate{
		"pci:v00008086d0000A348sv0000103Csd00008595bc04sc03i00": {{Module: "a_first"}, {Module: "snd_hda_intel", Builtin: true}, {Module: "wildcard_one"}},
		"pci:v00008086d00002723sv00008086sd00000084bc02sc80i00": {{Module: "iwlwifi"}},
		"pci:v0000144Dd0000A808sv0000144Dsd0000A801bc01sc08i02": {{Module: "nvme"}},
		"pci:v0000103Cd0000330Bsv0000103Csd00003351bc01sc04i00": {{Module: "hpsa"}},
		"pci:v0000103Cd0000350Bsv0000103Csd00003351bc01sc04i00": {},
		"pci:v00008086d0000A36Fsv0000103Csd00008595bc05sc00i00": {},
	} {
		if got := moduleCandidates(aliases, modalias); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %+v, want %+v", modalias, got, want)
		}
	}
}

// pciForModules adds a PCI device to a fake root, with its modalias.
func pciForModules(file, link func(string, string), addr, modalias string, driver bool) {
	d := "/sys/devices/pci0000:00/" + addr
	file(d+"/vendor", "0x8086")
	file(d+"/device", "0x2723")
	file(d+"/class", "0x028000")
	file(d+"/modalias", modalias)
	link(pciDir+addr, "../../../devices/pci0000:00/"+addr)
	if driver {
		file("/sys/bus/pci/drivers/iwlwifi/module/initstate", "live")
		link(d+"/driver", "../../../bus/pci/drivers/iwlwifi")
	}
}

// The running kernel's module index: present or not, and for each PCI
// device without a driver, the modules that claim it and whether they're
// loaded; a device with a driver gets no candidates.
func TestKernelModulesScenarios(t *testing.T) {
	const wifi = "pci:v00008086d00002723sv00008086sd00000084bc02sc80i00"
	const hda = "pci:v00008086d0000A348sv0000103Csd00008595bc04sc03i00"
	found := func(dir string) *report.ModuleIndex {
		return &report.ModuleIndex{Release: "7.2.9", Status: report.ModulesFound, Dir: dir}
	}
	for _, c := range []struct {
		name      string
		setup     func(file, link func(string, string))
		release   string
		container bool
		index     *report.ModuleIndex
		candidate map[string][]report.ModuleCandidate // by address; absent: no field
		warn      string
	}{
		{"candidates, one loaded, one built in", func(file, link func(string, string)) {
			file("/lib/modules/7.2.9/modules.alias", aliasFile)
			file("/lib/modules/7.2.9/modules.builtin.modinfo", "snd-hda-intel.alias=pci:v00008086d0000A348sv*sd*bc*sc*i*\x00")
			file("/sys/module/iwlwifi/initstate", "live")
			file("/sys/module/a_first/initstate", "coming") // still loading: not yet usable
			pciForModules(file, link, "0000:02:00.0", wifi, false)
			pciForModules(file, link, "0000:00:1f.3", hda, false)
			pciForModules(file, link, "0000:03:00.0", wifi, true)
		}, "7.2.9", false, found("/lib/modules/7.2.9"), map[string][]report.ModuleCandidate{
			"0000:02:00.0": {{Module: "iwlwifi", Loaded: true}},
			"0000:00:1f.3": {{Module: "a_first"}, {Module: "snd_hda_intel", Builtin: true, Loaded: true}, {Module: "wildcard_one"}},
		}, ""},
		{"no candidate, no builtin file", func(file, link func(string, string)) {
			file("/lib/modules/7.2.9/modules.alias", aliasFile)
			pciForModules(file, link, "0000:00:14.2", "pci:v00008086d0000A36Fsv0000103Csd00008595bc05sc00i00", false)
		}, "7.2.9", false, found("/lib/modules/7.2.9"), map[string][]report.ModuleCandidate{"0000:00:14.2": {}}, ""},
		{"/usr/lib only", func(file, link func(string, string)) {
			file("/usr/lib/modules/7.2.9/modules.alias", aliasFile)
			pciForModules(file, link, "0000:02:00.0", wifi, false)
		}, "7.2.9", false, found("/usr/lib/modules/7.2.9"), map[string][]report.ModuleCandidate{"0000:02:00.0": {{Module: "iwlwifi"}}}, ""},
		{"NixOS", func(file, link func(string, string)) {
			file("/run/booted-system/kernel-modules/lib/modules/7.2.9/modules.alias", aliasFile)
			pciForModules(file, link, "0000:02:00.0", wifi, false)
		}, "7.2.9", false, found("/run/booted-system/kernel-modules/lib/modules/7.2.9"), map[string][]report.ModuleCandidate{"0000:02:00.0": {{Module: "iwlwifi"}}}, ""},
		{"no modalias", func(file, link func(string, string)) {
			file("/lib/modules/7.2.9/modules.alias", aliasFile)
			pciForModules(file, link, "0000:02:00.0", "", false)
		}, "7.2.9", false, found("/lib/modules/7.2.9"), nil, ""},
		{"only other releases", func(file, link func(string, string)) {
			file("/lib/modules/7.2.8/modules.alias", aliasFile)
			file("/usr/lib/modules/7.2.8/modules.alias", aliasFile) // the same tree through /usr
			file("/usr/lib/modules/6.18.55-lts/modules.alias", aliasFile)
			file("/lib/modules/build-only/README", "no index here")
			pciForModules(file, link, "0000:02:00.0", wifi, false)
		}, "7.2.9", false, &report.ModuleIndex{Release: "7.2.9", Status: report.ModulesOtherRelease, OtherReleases: []string{"6.18.55-lts", "7.2.8"}}, nil,
			"kernel modules: only other kernels' modules are installed (6.18.55-lts, 7.2.8), not the running 7.2.9's"},
		{"only other releases, in a container", func(file, link func(string, string)) {
			file("/lib/modules/7.2.8/modules.alias", aliasFile)
			pciForModules(file, link, "0000:02:00.0", wifi, false)
		}, "7.2.9", true, &report.ModuleIndex{Release: "7.2.9", Status: report.ModulesOtherRelease, OtherReleases: []string{"7.2.8"}}, nil, ""},
		{"only other releases, nothing driverless", func(file, link func(string, string)) {
			file("/lib/modules/7.2.8/modules.alias", aliasFile)
			pciForModules(file, link, "0000:03:00.0", wifi, true)
		}, "7.2.9", false, &report.ModuleIndex{Release: "7.2.9", Status: report.ModulesOtherRelease, OtherReleases: []string{"7.2.8"}}, nil, ""},
		{"no module tree (a container, a kernel without modules)", func(file, link func(string, string)) {
			pciForModules(file, link, "0000:02:00.0", wifi, false)
		}, "7.2.9", false, &report.ModuleIndex{Release: "7.2.9", Status: report.ModulesNone}, nil, ""},
		{"unreadable index", func(file, link func(string, string)) {
			file("/lib/modules/7.2.9/modules.alias", aliasFile)
			unreadable = map[string]error{"/lib/modules/7.2.9/modules.alias": syscall.EACCES}
			pciForModules(file, link, "0000:02:00.0", wifi, false)
		}, "7.2.9", false, found("/lib/modules/7.2.9"), nil, "kernel modules: open "},
		{"no kernel release", func(file, link func(string, string)) {
			pciForModules(file, link, "0000:02:00.0", wifi, false)
		}, "", false, nil, nil, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			file, link := fakeRoot(t)
			c.setup(file, link)
			col := &collector{r: &report.Report{OS: report.OS{Kernel: c.release}}}
			if c.container {
				col.r.OS.Virtualization = "container"
			}
			col.pci()
			col.kernelModules()
			var idx *report.ModuleIndex
			if col.r.Kernel != nil {
				idx = col.r.Kernel.ModuleIndex
			}
			if !reflect.DeepEqual(idx, c.index) {
				t.Errorf("module index %+v, want %+v", idx, c.index)
			}
			got := map[string][]report.ModuleCandidate{}
			for _, d := range col.r.PCI {
				if d.ModuleCandidates != nil {
					got[d.Address] = d.ModuleCandidates
				}
			}
			if len(got) == 0 {
				got = nil
			}
			if !reflect.DeepEqual(got, c.candidate) {
				t.Errorf("candidates %+v, want %+v", got, c.candidate)
			}
			w := strings.Join(col.r.Warnings, "\n")
			if c.warn == "" && w != "" || c.warn != "" && !strings.Contains(w, c.warn) {
				t.Errorf("warnings %q, want %q", w, c.warn)
			}
		})
	}
}
