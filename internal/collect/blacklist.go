package collect

import (
	"maps"
	"slices"
	"strings"

	"github.com/jiegui2025/hwspec/internal/report"
)

// modprobeDirs are modprobe.d's directories in order of precedence (man 5
// modprobe.d, kmod): a file name found in one masks the same name in
// those after it.
var modprobeDirs = []string{"/etc/modprobe.d", "/run/modprobe.d", "/usr/local/lib/modprobe.d", "/usr/lib/modprobe.d", "/lib/modprobe.d"}

// moduleBlacklist records the modules kept from loading (#212):
//   - modprobe.d's *.conf files, read as kmod reads them (each file name
//     from the first directory that has it, the files in lexicographic
//     order of name, whatever their directory): "blacklist <module>"
//     (man 5 modprobe.d: its aliases are ignored), and "install <module>
//     <no-op>", which runs /bin/false or /bin/true instead of loading it;
//   - on the kernel command line, modprobe.blacklist= (man 8 modprobe),
//     a blacklist as modprobe.d's, and module_blacklist= ("Do not load a
//     comma-separated list of modules", kernel-parameters.txt), which the
//     kernel itself enforces (kernel/module/main.c, blacklisted()).
//
// Only those two keys of the command line are read, so its other values
// (root UUIDs, for one) never reach the capture.
func (c *collector) moduleBlacklist() []report.BlacklistedModule {
	winner := map[string]string{} // file name → its path
	for _, dir := range modprobeDirs {
		for _, name := range list(dir) {
			if _, masked := winner[name]; !masked && strings.HasSuffix(name, ".conf") {
				winner[name] = dir + "/" + name
			}
		}
	}
	out := []report.BlacklistedModule{}
	for _, name := range slices.Sorted(maps.Keys(winner)) {
		path := winner[name]
		b, err := readFile(path)
		if err != nil {
			c.warnRead("modprobe.d "+path, err)
			continue
		}
		for _, e := range parseBlacklist(string(b)) {
			e.Source = path
			out = append(out, e)
		}
	}
	cmdline, err := readStrErr("/proc/cmdline")
	if err != nil {
		c.warnRead("kernel command line (module blacklist)", err)
		return out
	}
	for f := range strings.FieldsSeq(cmdline) {
		key, list, _ := strings.Cut(f, "=")
		kind, ok := cmdlineBlacklists[key]
		if !ok {
			continue
		}
		for m := range strings.SplitSeq(list, ",") {
			if m != "" {
				out = append(out, report.BlacklistedModule{Module: moduleName(m), Kind: kind, Source: "cmdline"})
			}
		}
	}
	return out
}

// cmdlineBlacklists are the command-line keys that keep modules from
// loading, and how.
var cmdlineBlacklists = map[string]string{
	"modprobe.blacklist": report.BlacklistAlias,
	"module_blacklist":   report.BlacklistKernel,
}

// noOps are the install commands that load nothing: CIS benchmarks' way
// to disable a module ("install cramfs /bin/false").
var noOps = []string{"/bin/false", "/bin/true", "/usr/bin/false", "/usr/bin/true", "false", "true"}

// parseBlacklist returns the modules a modprobe.d file keeps from
// loading, without their source: one command per line, a trailing "\"
// joining the next line, blank lines and lines starting with "#" ignored
// (man 5 modprobe.d); names with "-" read as "_", as modprobe does. An
// install line counts only when its whole command is a no-op: any other
// command may load the module its own way.
func parseBlacklist(text string) []report.BlacklistedModule {
	var out []report.BlacklistedModule
	command := func(line string) {
		// A comment's first word is "#…", never a command.
		f := strings.Fields(line)
		switch {
		case len(f) >= 2 && f[0] == "blacklist":
			out = append(out, report.BlacklistedModule{Module: moduleName(f[1]), Kind: report.BlacklistAlias})
		case len(f) == 3 && f[0] == "install" && slices.Contains(noOps, f[2]):
			out = append(out, report.BlacklistedModule{Module: moduleName(f[1]), Kind: report.BlacklistInstall})
		}
	}
	var line strings.Builder
	for l := range strings.Lines(text) {
		l = strings.TrimRight(l, "\r\n")
		if cont, ok := strings.CutSuffix(l, `\`); ok {
			line.WriteString(cont + " ")
			continue
		}
		line.WriteString(l)
		command(line.String())
		line.Reset()
	}
	command(line.String()) // a "\" on the last line continues into nothing
	return out
}
