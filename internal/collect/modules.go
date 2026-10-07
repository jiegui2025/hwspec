package collect

import (
	"bufio"
	"bytes"
	"path"
	"slices"
	"strings"

	"github.com/jiegui2025/hwspec/internal/report"
)

// moduleAlias is one alias of a kernel module: a glob over modaliases
// ("pci:v00008086d0000A36Fsv*sd*bc05sc00i*") and the module it loads.
type moduleAlias struct {
	pattern, module string
	builtin         bool
}

// moduleName normalises a module name the way modprobe does: "there is
// no difference between _ and - in module names" (modprobe(8)).
func moduleName(s string) string { return strings.ReplaceAll(s, "-", "_") }

// parseModulesAlias reads modules.alias: "alias <glob> <module>" lines,
// with # comments.
func parseModulesAlias(b []byte) []moduleAlias {
	var out []moduleAlias
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 3 && f[0] == "alias" {
			out = append(out, moduleAlias{pattern: f[1], module: moduleName(f[2])})
		}
	}
	return out
}

// parseBuiltinModinfo reads modules.builtin.modinfo: NUL-separated
// "<module>.<key>=<value>" entries for the modules built into the kernel,
// of which the "alias" ones are kept.
func parseBuiltinModinfo(b []byte) []moduleAlias {
	var out []moduleAlias
	for entry := range bytes.SplitSeq(b, []byte{0}) {
		k, v, ok := strings.Cut(string(entry), "=")
		module, key, dot := strings.Cut(k, ".")
		if ok && dot && key == "alias" && module != "" && v != "" {
			out = append(out, moduleAlias{pattern: v, module: moduleName(module), builtin: true})
		}
	}
	return out
}

// moduleCandidates returns the modules with an alias matching modalias,
// sorted, each once (built in when any of its matching aliases is). The
// kernel's globs are fnmatch(3) patterns over strings without "/", which
// path.Match reads the same way; a malformed one matches nothing.
func moduleCandidates(aliases []moduleAlias, modalias string) []report.ModuleCandidate {
	out := []report.ModuleCandidate{}
	for _, a := range aliases {
		if ok, _ := path.Match(a.pattern, modalias); !ok { // a malformed glob is false
			continue
		}
		i := slices.IndexFunc(out, func(c report.ModuleCandidate) bool { return c.Module == a.module })
		if i < 0 {
			out = append(out, report.ModuleCandidate{Module: a.module})
			i = len(out) - 1
		}
		out[i].Builtin = out[i].Builtin || a.builtin
	}
	slices.SortFunc(out, func(a, b report.ModuleCandidate) int { return strings.Compare(a.Module, b.Module) })
	return out
}

// moduleTrees are where distributions install kernel modules: /lib (or
// /usr/lib behind it), and NixOS's booted system, which has no /lib.
var moduleTrees = []string{"/lib/modules", "/usr/lib/modules", "/run/booted-system/kernel-modules/lib/modules"}

// moduleIndex finds the running kernel's module index in the module
// trees, or what they hold instead.
func moduleIndex(release string) *report.ModuleIndex {
	idx := &report.ModuleIndex{Release: release, Status: report.ModulesNone}
	for _, tree := range moduleTrees {
		if exists(tree + "/" + release + "/modules.alias") {
			idx.Status, idx.Dir = report.ModulesFound, tree+"/"+release
			return idx
		}
	}
	for _, tree := range moduleTrees {
		for _, other := range list(tree) {
			if exists(tree+"/"+other+"/modules.alias") && !slices.Contains(idx.OtherReleases, other) {
				idx.OtherReleases = append(idx.OtherReleases, other)
			}
		}
	}
	if len(idx.OtherReleases) > 0 {
		idx.Status = report.ModulesOtherRelease
		slices.Sort(idx.OtherReleases)
	}
	return idx
}

// kernelModules records where the running kernel's module index is (or
// what's there instead), and for each PCI device without a driver, the
// modules that claim it (#7).
func (c *collector) kernelModules() {
	release := c.r.OS.Kernel
	if release == "" {
		return // osInfo already warned
	}
	idx := moduleIndex(release)
	c.r.Kernel = &report.Kernel{ModuleIndex: idx}
	var driverless []int
	for i, d := range c.r.PCI {
		if d.Driver == nil {
			driverless = append(driverless, i)
		}
	}
	if len(driverless) == 0 {
		return
	}
	switch {
	case idx.Status == report.ModulesOtherRelease && c.r.OS.Virtualization != "container":
		c.warn("kernel modules: only other kernels' modules are installed (%s), not the running %s's: a kernel upgrade without a reboot? Devices without a driver can't get their candidate modules",
			strings.Join(idx.OtherReleases, ", "), release)
		return
	case idx.Status != report.ModulesFound:
		return // no index for this kernel: candidates stay unknown
	}
	aliases, ok := c.moduleAliases()
	if !ok {
		return
	}
	for _, i := range driverless {
		d := &c.r.PCI[i]
		modalias := readStr(pciDir + d.Address + "/modalias")
		if modalias == "" {
			continue // nothing to match: candidates stay unknown
		}
		d.ModuleCandidates = loadedCandidates(aliases, modalias)
	}
}

// moduleAliases reads the running kernel's module aliases once: those of
// its modules and of its built-in ones. ok is false when there is no index
// for this kernel, or it can't be read (a warning, once).
func (c *collector) moduleAliases() ([]moduleAlias, bool) {
	if c.aliases != nil {
		return *c.aliases, len(*c.aliases) > 0
	}
	c.aliases = &[]moduleAlias{}
	idx := c.r.Kernel.ModuleIndex
	if idx.Status != report.ModulesFound {
		return nil, false
	}
	b, err := readFile(idx.Dir + "/modules.alias")
	if err != nil {
		c.warn("kernel modules: %v", err)
		return nil, false
	}
	aliases := parseModulesAlias(b)
	// Kernels before 5.2 have no modules.builtin.modinfo: no built-in
	// candidates, then.
	if b, err := readFile(idx.Dir + "/modules.builtin.modinfo"); err == nil {
		aliases = append(aliases, parseBuiltinModinfo(b)...)
	}
	*c.aliases = aliases
	return aliases, len(aliases) > 0
}

// loadedCandidates are the modules claiming a modalias, each with whether
// it's loaded.
func loadedCandidates(aliases []moduleAlias, modalias string) []report.ModuleCandidate {
	cands := moduleCandidates(aliases, modalias)
	for j := range cands {
		cands[j].Loaded = cands[j].Builtin || readStr("/sys/module/"+cands[j].Module+"/initstate") == "live"
	}
	return cands
}

// usbCandidates gives each USB interface without a driver the modules
// that claim it (#216), as kernelModules does for PCI devices. Without an
// index for the running kernel, they stay unknown; kernelModules has
// already said why when it matters.
func (c *collector) usbCandidates() {
	if c.r.Kernel == nil || c.r.Kernel.ModuleIndex == nil {
		return
	}
	for i := range c.r.USB {
		for j := range c.r.USB[i].Interfaces {
			in := &c.r.USB[i].Interfaces[j]
			if in.Driver != nil || in.Modalias == "" {
				continue
			}
			aliases, ok := c.moduleAliases()
			if !ok {
				return
			}
			in.ModuleCandidates = loadedCandidates(aliases, in.Modalias)
		}
	}
}
