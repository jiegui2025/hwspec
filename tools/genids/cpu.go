package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/jiegui2025/hwspec/internal/ids"
)

// cpuEntry is one generated line: "intel:6:9e[:10]<TAB>Codename<TAB>Microarchitecture".
type cpuEntry struct{ codename, uarch string }

var (
	intelDefine = regexp.MustCompile(`^#define\s+INTEL_([A-Z0-9_]+)\s+IFM\((\d+),\s*0x([0-9A-Fa-f]+)\)\s*(?:/\*\s*(.*?)\s*\*/)?`)
	amdFamily   = regexp.MustCompile(`^\s*case 0x([0-9a-f]+):\s*$`)
	amdRange    = regexp.MustCompile(`^\s*case 0x([0-9a-f]+)(?:\s*\.\.\.\s*0x([0-9a-f]+))?:`)
	amdZen      = regexp.MustCompile(`X86_FEATURE_ZEN(\d)`)
)

// Suffixes on kernel macro names that name a server/embedded variant worth
// keeping ("Ice Lake-X"); others (L, G, P, H, ...) are dropped.
var keepSuffix = map[string]bool{"X": true, "D": true, "EP": true, "EX": true}

// sourcePrefixes are the kinds of source a curated line may cite (its 4th
// field): every name must be checkable by a reviewer. The vendor and url
// kinds are links, so they must be https URLs.
var sourcePrefixes = []string{"kernel:", "instlat:", "amd:", "intel:", "url:"}

func checkSource(src string) error {
	kind, rest, _ := strings.Cut(src, ":")
	if !slices.Contains(sourcePrefixes, kind+":") || rest == "" {
		return fmt.Errorf("source %q has none of the prefixes %v", src, sourcePrefixes)
	}
	if (kind == "amd" || kind == "intel" || kind == "url") && !strings.HasPrefix(rest, "https://") {
		return fmt.Errorf("source %q: %s: must be followed by an https:// URL", src, kind)
	}
	return nil
}

// cpu builds cpu.ids from the kernel's intel-family.h and amd.c plus the
// curated file, which wins. Curated lines that change nothing are reported
// to warn: kernel updates make lines redundant, which mustn't fail a build.
func cpu(intelPath, amdPath, curatedPath string, warn io.Writer) ([]byte, error) {
	out := map[string]cpuEntry{}

	intel, err := os.ReadFile(intelPath)
	if err != nil {
		return nil, err
	}
	uarchOf := map[string]string{} // base name -> core name, for defines without a comment
	n := 0
	for line := range strings.SplitSeq(string(intel), "\n") {
		m := intelDefine.FindStringSubmatch(line)
		if m == nil || strings.HasSuffix(m[1], "_START") || strings.HasSuffix(m[1], "_LAST") || m[1] == "ANY" {
			continue
		}
		macro, family, model, comment := m[1], m[2], strings.ToLower(m[3]), m[4]
		var e cpuEntry
		if rest, isAtom := strings.CutPrefix(macro, "ATOM_"); isAtom {
			// ATOM_GOLDMONT /* Apollo Lake */: core name in the macro,
			// product codename in the comment.
			e = cpuEntry{codename: tidy(comment), uarch: pretty(strings.ReplaceAll(rest, "_", " "))}
		} else if legacy(macro) {
			// P4_PRESCOTT, CORE2_MEROM, PENTIUM_III_TUALATIN...: the whole
			// macro is the name; comments are notes, not core names.
			e = cpuEntry{codename: pretty(strings.ReplaceAll(macro, "_", " "))}
			e.uarch = e.codename
		} else {
			base, suffix := splitSuffix(macro)
			e.codename = pretty(base)
			if keepSuffix[suffix] {
				e.codename += "-" + suffix
			}
			// Since Skylake the kernel comments name the core ("Sunny
			// Cove"); older ones name products ("Auburndale / Havendale").
			switch {
			case strings.Contains(comment, "Cove") || strings.Contains(comment, "mont") || comment == "Sky Lake":
				uarchOf[base] = tidy(comment)
			case comment != "":
				e.codename = tidy(comment)
			}
			e.uarch = uarchOf[base]
			if e.uarch == "" {
				e.uarch = pretty(base) // older cores: codename and core share a name
			}
		}
		fam, _ := strconv.Atoi(family)
		out[fmt.Sprintf("intel:%x:%s", fam, model)] = e
		n++
	}
	if n < 50 {
		return nil, fmt.Errorf("only %d Intel models parsed; intel-family.h format changed?", n)
	}

	amd, err := os.ReadFile(amdPath)
	if err != nil {
		return nil, err
	}
	src := string(amd)
	start := strings.Index(src, "Figure out Zen generations")
	if start < 0 {
		return nil, fmt.Errorf("amd.c: Zen generation table not found")
	}
	family := ""
	var pending [][2]int
	zen := 0
	lines := strings.Split(src[start:], "\n")
	// A "case 0x19:" opens a family only when the next statement is the
	// model switch; any other single case is a model.
	opensModelSwitch := func(i int) bool {
		for _, next := range lines[i+1:] {
			t := strings.TrimSpace(next)
			if t == "" || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "//") {
				continue // blank lines and comments
			}
			return strings.HasPrefix(t, "switch (c->x86_model)")
		}
		return false
	}
	for i, line := range lines {
		if strings.Contains(line, "switch (c->x86_model)") {
			continue
		}
		if m := amdZen.FindStringSubmatch(line); m != nil {
			gen := "Zen"
			if m[1] != "1" {
				gen += " " + m[1]
			}
			for _, r := range pending {
				for model := r[0]; model <= r[1]; model++ {
					key := fmt.Sprintf("amd:%s:%02x", family, model)
					// A misparsed family would land on another family's
					// models; never let that happen silently.
					if prev, dup := out[key]; dup && prev.uarch != gen {
						return nil, fmt.Errorf("amd.c: %s parsed as both %s and %s; parser out of step with the kernel source", key, prev.uarch, gen)
					}
					out[key] = cpuEntry{uarch: gen}
				}
			}
			pending = nil
			zen++
			continue
		}
		if m := amdRange.FindStringSubmatch(line); m != nil {
			lo, _ := strconv.ParseInt(m[1], 16, 32)
			hi := lo
			if m[2] != "" {
				hi, _ = strconv.ParseInt(m[2], 16, 32)
			}
			if m[2] == "" && amdFamily.MatchString(line) && opensModelSwitch(i) {
				if len(pending) > 0 {
					return nil, fmt.Errorf("amd.c: model cases without a Zen generation before family 0x%s", m[1])
				}
				family = strings.TrimLeft(m[1], "0")
				continue
			}
			pending = append(pending, [2]int{int(lo), int(hi)})
		}
		if strings.Contains(line, "default:") && family != "" && strings.HasPrefix(strings.TrimSpace(line), "default:") && pending == nil && zen > 0 && strings.Count(line, "\t") == 1 {
			break // end of the family switch
		}
	}
	if zen < 4 {
		return nil, fmt.Errorf("amd.c: only %d Zen generations parsed", zen)
	}

	curated, err := os.ReadFile(curatedPath)
	if err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(bytes.NewReader(curated))
	for ln := 1; sc.Scan(); ln++ {
		line := sc.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		f := strings.Fields(parts[0])
		if len(parts) < 2 || len(f) < 3 {
			return nil, fmt.Errorf("%s:%d: malformed", curatedPath, ln)
		}
		if len(parts) < 4 || strings.TrimSpace(parts[3]) == "" {
			return nil, fmt.Errorf("%s:%d: no source (4th field)", curatedPath, ln)
		}
		if len(parts) > 4 {
			return nil, fmt.Errorf("%s:%d: %d tab-separated fields, want 4 (separate sources with spaces)", curatedPath, ln, len(parts))
		}
		for src := range strings.FieldsSeq(parts[3]) {
			if err := checkSource(src); err != nil {
				return nil, fmt.Errorf("%s:%d: %w", curatedPath, ln, err)
			}
		}
		var keys []string
		base := fmt.Sprintf("%s:%s:%s", f[0], strings.ToLower(f[1]), strings.ToLower(f[2]))
		if len(f) == 4 {
			lo, hi, isRange := strings.Cut(f[3], "-")
			if !isRange {
				hi = lo
			}
			a, err1 := strconv.Atoi(lo)
			b, err2 := strconv.Atoi(hi)
			if err1 != nil || err2 != nil || b < a {
				return nil, fmt.Errorf("%s:%d: bad stepping range %q", curatedPath, ln, f[3])
			}
			for s := a; s <= b; s++ {
				keys = append(keys, fmt.Sprintf("%s:%d", base, s))
			}
		} else {
			keys = []string{base}
		}
		changed := false
		for _, k := range keys {
			e := out[k]
			if e == (cpuEntry{}) {
				e = out[base] // a stepping entry starts from the model's
			}
			before := e // what a lookup finds without this line
			if c := strings.TrimSpace(parts[1]); c != "" {
				e.codename = c
			}
			if strings.TrimSpace(parts[2]) != "" {
				e.uarch = strings.TrimSpace(parts[2])
			}
			out[k] = e
			changed = changed || e != before
		}
		if !changed {
			// A GitHub Actions annotation when the weekly workflow runs it.
			fmt.Fprintf(warn, "::warning file=%[1]s,line=%[2]d::%[1]s:%[2]d: the kernel or an earlier line already says this; remove it\n", curatedPath, ln)
		}
	}

	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# CPU codenames: Linux kernel intel-family.h and amd.c, plus hwspec's curated list\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s\t%s\t%s\n", k, ids.CleanName(out[k].codename), ids.CleanName(out[k].uarch))
	}
	return []byte(b.String()), nil
}

// tidy fixes kernel comment spellings to match Intel's ("Sky Lake" ->
// "Skylake", "Alderlake N" -> "Alder Lake-N").
func tidy(s string) string {
	s = strings.ReplaceAll(s, "Sky Lake", "Skylake")
	s = regexp.MustCompile(`\b([A-Z][a-z]+)lake\b`).ReplaceAllStringFunc(s, func(w string) string {
		if w == "Skylake" {
			return w
		}
		return strings.TrimSuffix(w, "lake") + " Lake"
	})
	return regexp.MustCompile(`( Lake) ([A-Z])\b`).ReplaceAllString(s, "$1-$2")
}

func splitSuffix(macro string) (base, suffix string) {
	before, after, ok := strings.Cut(macro, "_")
	if !ok {
		return macro, ""
	}
	return before, after
}

// pretty turns kernel macro words into marketing spelling:
// KABYLAKE -> Kaby Lake, SAPPHIRERAPIDS -> Sapphire Rapids, HASWELL -> Haswell.
func pretty(s string) string {
	exceptions := map[string]string{"SKYLAKE": "Skylake", "LAKEFIELD": "Lakefield", "CORE2": "Core 2", "P4": "Pentium 4", "PENTIUM": "Pentium"}
	var words []string
	for w := range strings.FieldsSeq(s) {
		if e, ok := exceptions[w]; ok {
			words = append(words, e)
			continue
		}
		split := false
		for _, suffix := range []string{"LAKE", "RAPIDS", "BRIDGE", "FOREST", "RIDGE", "MONT"} {
			if strings.HasSuffix(w, suffix) && len(w) > len(suffix) && suffix != "MONT" {
				words = append(words, title(strings.TrimSuffix(w, suffix)), title(suffix))
				split = true
				break
			}
		}
		if !split {
			words = append(words, title(w))
		}
	}
	return strings.Join(words, " ")
}

func legacy(macro string) bool {
	for _, p := range []string{"P4_", "PENTIUM_", "CORE_", "CORE2_", "QUARK_", "XEON_PHI_"} {
		if strings.HasPrefix(macro, p) {
			return true
		}
	}
	return false
}

func title(w string) string {
	switch w {
	case "", "II", "III", "MMX", "2M", "X1000", "KNL", "KNM", "PHI", "PRO", "M", "L":
		if w == "PHI" {
			return "Phi"
		}
		if w == "PRO" {
			return "Pro"
		}
		return w
	}
	return w[:1] + strings.ToLower(w[1:])
}

// btcompany converts the Bluetooth SIG company_identifiers.yaml into
// "XXXX<TAB>Name" lines.
func btcompany(src []byte) ([]byte, error) {
	var doc struct {
		Companies []struct {
			Value int    `yaml:"value"`
			Name  string `yaml:"name"`
		} `yaml:"company_identifiers"`
	}
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, err
	}
	sort.Slice(doc.Companies, func(i, j int) bool { return doc.Companies[i].Value < doc.Companies[j].Value })
	var b strings.Builder
	b.WriteString("# Bluetooth SIG company identifiers\n")
	for _, c := range doc.Companies {
		fmt.Fprintf(&b, "%04X\t%s\n", c.Value, ids.CleanName(c.Name))
	}
	return []byte(b.String()), nil
}
