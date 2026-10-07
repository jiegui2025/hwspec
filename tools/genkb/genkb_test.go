package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiegui2025/hwspec/internal/kb"
)

var noon = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func genkb(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(args, noon, &out, &errOut)
	return code, out.String(), errOut.String()
}

// rule is a file with one source and one rule; %s is the rule's ID.
const rule = `sources:
  kernel-%s:
    url: https://docs.kernel.org/x.html
    retrieved: 2026-10-06
    licence: GPL-2.0-only
    confidence: upstream-doc
    quote: words
rules:
  - id: %s
    check: pci-without-driver
    category: needs-attention
    severity: warning
    title: No driver
    match: {pci_class: ["02"]}
    src: [kernel-%s]
`

func ruleFile(id string) string { return strings.ReplaceAll(rule, "%s", id) }

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The knowledge base built into hwspec must be exactly what kb/ says.
func TestTheEmbeddedKnowledgeBaseIsUpToDate(t *testing.T) {
	if code, stdout, stderr := genkb(t, "check", "../../internal/kb/data/advisor-v1.json.gz", "../../kb"); code != 0 {
		t.Fatalf("exit %d: %s%s", code, stdout, stderr)
	}
}

func TestRulesFromEveryFileAreSortedAndDated(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"rules/b.yaml":  ruleFile("z.last"),
		"models/a.yaml": ruleFile("a.first"),
		"empty.yaml":    "",
		"README.md":     "not rules",
	})
	out := filepath.Join(t.TempDir(), "advisor.json.gz")
	if code, _, stderr := genkb(t, "-o", out, dir); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	data, _ := os.ReadFile(out)
	k, err := kb.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if k.Version != "2026-10-06T12:00:00Z" || len(k.Rules) != 2 || k.Rules[0].ID != "a.first" || k.Rules[1].ID != "z.last" {
		t.Errorf("version %q, rules %+v", k.Version, k.Rules)
	}
	// Sources are keyed by ID in YAML, sorted by ID in the compiled file.
	if len(k.Sources) != 2 || k.Sources[0].ID != "kernel-a.first" || k.Sources[1].ID != "kernel-z.last" {
		t.Errorf("sources %+v", k.Sources)
	}
	// An unquoted YAML date is a timestamp; it's stored as YYYY-MM-DD.
	if got := k.Sources[0].Retrieved; got != "2026-10-06" {
		t.Errorf("retrieved = %q", got)
	}
	if code, _, stderr := genkb(t, "-version", "2030-01-02T00:00:00Z", "-o", out, dir); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	data, _ = os.ReadFile(out)
	if k, _ := kb.Parse(data); k.Version != "2030-01-02T00:00:00Z" {
		t.Errorf("-version ignored: %q", k.Version)
	}
}

// YAML values reach the knowledge base as written: a timestamp stays text,
// numbers keep their digits, an alias resolves.
func TestValuesSurviveAsWritten(t *testing.T) {
	r := ruleFile("a.rule")
	r = strings.Replace(r, "title: No driver", "title: &t No driver", 1)
	r = strings.Replace(r, "    quote: words", "    link_only: True\n    locator: p. 2", 1)
	r = strings.Replace(r, "    src: [kernel-a.rule]", "    detail: *t\n    src: [kernel-a.rule]\n    actions: [{text: 2026-10-06T12:00:00Z, commands: [\"1.50\", \"x\"]}]", 1)
	dir := writeFiles(t, map[string]string{"r.yaml": r})
	out := filepath.Join(t.TempDir(), "out.gz")
	if code, _, stderr := genkb(t, "-o", out, dir); code != 0 {
		t.Fatal(stderr)
	}
	data, _ := os.ReadFile(out)
	k, err := kb.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := k.Rules[0]; got.Detail != "No driver" || got.Actions[0].Text != "2026-10-06T12:00:00Z" || got.Actions[0].Commands[0] != "1.50" {
		t.Errorf("rule %+v", got)
	}
	if !k.Sources[0].LinkOnly { // YAML's True is true
		t.Errorf("source %+v", k.Sources[0])
	}
}

// Rebuilding unchanged sources changes no bytes, whatever the date; a
// change takes the time it was built. check compares content, not gzip bytes.
func TestRebuildsKeepTheVersionUntilSomethingChanges(t *testing.T) {
	dir := writeFiles(t, map[string]string{"r.yaml": ruleFile("a.rule")})
	out := filepath.Join(t.TempDir(), "out.gz")
	var other bytes.Buffer
	if code := run([]string{"-o", out, dir}, noon, &other, &other); code != 0 {
		t.Fatal(other.String())
	}
	first, _ := os.ReadFile(out)
	if code := run([]string{"-o", out, dir}, noon.AddDate(0, 0, 9), &other, &other); code != 0 {
		t.Fatal(other.String())
	}
	if again, _ := os.ReadFile(out); !bytes.Equal(first, again) {
		t.Error("an unchanged rebuild changed the file")
	}
	os.WriteFile(filepath.Join(dir, "s.yaml"), []byte(ruleFile("b.rule")), 0o644)
	run([]string{"-o", out, dir}, noon.AddDate(0, 0, 9), &other, &other)
	data, _ := os.ReadFile(out)
	if k, _ := kb.Parse(data); k.Version != "2026-10-15T12:00:00Z" {
		t.Errorf("changed rules kept version %q", k.Version)
	}
	// The same content, compressed differently (as another Go release
	// might): still up to date.
	k, _ := kb.Parse(data)
	js, _ := json.Marshal(k)
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	zw.Write(js)
	zw.Close()
	os.WriteFile(out, buf.Bytes(), 0o644)
	if code, stdout, stderr := genkb(t, "check", out, dir); code != 0 {
		t.Errorf("recompressed: exit %d %s%s", code, stdout, stderr)
	}
}

// Every rule needs its evidence, and only what this build can apply.
func TestInvalidRulesAreRefused(t *testing.T) {
	r := ruleFile("a.rule")
	cases := map[string]struct {
		files map[string]string
		want  string
	}{
		"uncited":        {map[string]string{"r.yaml": strings.Replace(r, "    src: [kernel-a.rule]\n", "", 1)}, "no src"},
		"unknown src":    {map[string]string{"r.yaml": strings.Replace(r, "src: [kernel-a.rule]", "src: [blog]", 1)}, `src "blog" isn't in sources`},
		"no licence":     {map[string]string{"r.yaml": strings.Replace(r, "    licence: GPL-2.0-only\n", "", 1)}, "no licence"},
		"unknown check":  {map[string]string{"r.yaml": strings.Replace(r, "pci-without-driver", "magic", 1)}, `unknown check "magic"`},
		"unknown field":  {map[string]string{"r.yaml": r + "colour: red\n"}, "unknown field"},
		"a bare list":    {map[string]string{"r.yaml": "- id: a.rule\n"}, "want sources and rules"},
		"two documents":  {map[string]string{"r.yaml": r + "---\nrules: [{id: x}]\n"}, "more than one YAML document"},
		".yml":           {map[string]string{"r.yaml": r, "s.yml": r}, "name knowledge-base files .yaml"},
		"no rules":       {map[string]string{"r.yaml": "sources: {}\n"}, "no rules"},
		"octal":          {map[string]string{"r.yaml": strings.Replace(r, `pci_class: ["02"]`, "pci_class: [0403]", 1)}, "0403 isn't a plain number; quote it"},
		"null":           {map[string]string{"r.yaml": strings.Replace(r, "title: No driver", "title: ~", 1)}, "a null value"},
		"repeated key":   {map[string]string{"r.yaml": strings.Replace(r, "    severity: warning", "    severity: warning\n    severity: info", 1)}, `"severity" appears twice`},
		"repeated rules": {map[string]string{"r.yaml": r + "rules: []\n"}, `"rules" appears twice`},
		"unknown placeholder": {map[string]string{"r.yaml": strings.Replace(r, "    src: [kernel-a.rule]", "    actions: [{text: t, commands: [\"modprobe -R {modallias}\"]}]\n    src: [kernel-a.rule]", 1)},
			`uses {modallias}, which check "pci-without-driver" doesn't fill (it fills: modalias)`},
		"not a boolean":  {map[string]string{"r.yaml": strings.Replace(r, "    quote: words", "    quote: words\n    link_only: !!bool maybe", 1)}, "invalid syntax"},
		"bidi title":     {map[string]string{"r.yaml": strings.Replace(r, "title: No driver", "title: \"No \\u202e driver\"", 1)}, "title has control or formatting characters"},
		"not YAML":       {map[string]string{"r.yaml": "- ["}, "yaml"},
		"duplicate rule": {map[string]string{"r.yaml": r, "s.yaml": strings.Replace(r, "kernel-a.rule:", "kernel-other:", 1)}, "duplicate id"},
		"source twice":   {map[string]string{"r.yaml": r, "s.yaml": strings.Replace(r, "id: a.rule", "id: b.rule", 1)}, `source "kernel-a.rule" is defined twice`},
		"source id":      {map[string]string{"r.yaml": strings.Replace(r, "    url:", "    id: x\n    url:", 1)}, "its id is its key"},
		"category":       {map[string]string{"r.yaml": strings.Replace(r, "needs-attention", "chores", 1)}, `category "chores"`},
		// #146: what only the check knows.
		"no match": {map[string]string{"r.yaml": strings.Replace(r, `    match: {pci_class: ["02"]}`+"\n", "", 1)},
			`"a.rule": match.pci_class is empty: check "pci-without-driver" needs it`},
		"no check": {map[string]string{"r.yaml": strings.Replace(r, "    check: pci-without-driver\n", "", 1)}, `rule "a.rule": no check`},
		"data the check doesn't take": {map[string]string{"r.yaml": strings.Replace(r, "    src: [kernel-a.rule]", "    data: {devicez: [{value: x, src: kernel-a.rule}]}\n    src: [kernel-a.rule]", 1)},
			`"a.rule": check "pci-without-driver" takes no data`},
	}
	for name, c := range cases {
		dir := writeFiles(t, c.files)
		code, _, stderr := genkb(t, "-o", filepath.Join(t.TempDir(), "out.gz"), dir)
		if code != 1 || !strings.Contains(stderr, c.want) {
			t.Errorf("%s: exit %d, %q; want %q", name, code, stderr, c.want)
		}
		if strings.Contains(stderr, `unknown check ""`) {
			t.Errorf("%s: a rule without a check reported twice: %q", name, stderr)
		}
	}
}

func TestCheckNoticesAStaleFile(t *testing.T) {
	dir := writeFiles(t, map[string]string{"rules.yaml": ruleFile("a.rule")})
	out := filepath.Join(t.TempDir(), "advisor.json.gz")
	if code, _, stderr := genkb(t, "-o", out, dir); code != 0 {
		t.Fatal(stderr)
	}
	if code, stdout, _ := genkb(t, "check", out, dir); code != 0 || !strings.Contains(stdout, "1 rules, version 2026-10-06T12:00:00Z, up to date") {
		t.Errorf("fresh file: exit %d, %q", code, stdout)
	}
	os.WriteFile(filepath.Join(dir, "more.yaml"), []byte(ruleFile("b.rule")), 0o644)
	if code, _, stderr := genkb(t, "check", out, dir); code != 1 || !strings.Contains(stderr, "out of date") {
		t.Errorf("stale file: exit %d, %q", code, stderr)
	}
	os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte("- ["), 0o644)
	if code, _, _ := genkb(t, "check", out, dir); code != 1 {
		t.Error("broken sources pass the check")
	}
	if code, _, stderr := genkb(t, "check", filepath.Join(dir, "rules.yaml"), dir); code != 1 || !strings.Contains(stderr, "knowledge base") {
		t.Errorf("not a compiled file: exit %d, %q", code, stderr)
	}
	if code, _, _ := genkb(t, "check", filepath.Join(dir, "missing.gz"), dir); code != 1 {
		t.Error("missing file passes the check")
	}
}

func TestUsageAndUnwritableOutput(t *testing.T) {
	if code, _, stderr := genkb(t, "compile"); code != 2 || !strings.Contains(stderr, "usage:") {
		t.Errorf("bad arguments: exit %d, %q", code, stderr)
	}
	dir := writeFiles(t, map[string]string{"rules.yaml": ruleFile("a.rule")})
	if code, _, _ := genkb(t, "-o", filepath.Join(dir, "no/such/dir/out.gz"), dir); code != 1 {
		t.Error("unwritable output reported success")
	}
	if code, _, _ := genkb(t, "-o", filepath.Join(t.TempDir(), "out.gz"), filepath.Join(dir, "missing")); code != 1 {
		t.Error("missing source directory reported success")
	}
}

// model is a file with the reference model's entry (#25) and its source;
// %s is the entry's ID.
const model = `sources:
  hp-ds-%s:
    url: https://h20195.www2.hp.com/v2/GetDocument.aspx?docname=4AA7-5436EEAP
    doc: 4AA7-5436EEAP
    published: "2019-12"
    retrieved: 2026-10-06
    licence: "HP: no reuse licence"
    confidence: oem-doc
    link_only: true
    locator: Memory
models:
  %s:
    match: {sys_vendor: HP, product_name: "HP EliteDesk 800 G5 Desktop Mini", board_name: "8595"}
    data:
      memory:
        max_total_gb: [{value: 64, src: hp-ds-%s}]
      chipset: [{value: Q370, src: hp-ds-%s}]
devices:
  %s.gpu:
    match: {bus: pci, id: "8086:3e92"}
    data:
      display_outputs: [{value: [{type: dp, max_width: 4096}], src: hp-ds-%s}]
cpus:
  %s.cpu:
    match: {vendor: intel, processor: i5-9500T}
    data:
      memory_max_gb: [{value: 128, src: hp-ds-%s}]
allowlists:
  %s.policy:
    match: {sys_vendor: HP, family: "103C_53307F HP EliteDesk", bios_version: {from: "R21 Ver. 02.00.00"}}
    data:
      restricted: [{value: false, src: hp-ds-%s}]
`

func modelFile(id string) string { return strings.ReplaceAll(model, "%s", id) }

// The data sections compile from YAML, keyed by ID like sources, and read
// back; genkb refuses what #25 says it must.
func TestSectionsCompile(t *testing.T) {
	dir := writeFiles(t, map[string]string{"rules.yaml": ruleFile("a.rule"), "models/hp.yaml": modelFile("hp.mini")})
	out := filepath.Join(t.TempDir(), "out.gz")
	if code, _, stderr := genkb(t, "-o", out, dir); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	k, err := kb.Parse(data)
	if err != nil || len(k.Skipped) != 0 {
		t.Fatalf("%v, skipped %q", err, k.Skipped)
	}
	if len(k.Models) != 1 || k.Models[0].ID != "hp.mini" || k.Models[0].Match.BoardName != "8595" ||
		len(k.Devices) != 1 || k.Devices[0].ID != "hp.mini.gpu" || len(k.CPUs) != 1 || len(k.Allowlists) != 1 ||
		!k.Confirmed(k.Allowlists[0].Data) {
		t.Errorf("sections: %+v %+v %+v %+v", k.Models, k.Devices, k.CPUs, k.Allowlists)
	}
	m := modelFile("hp.mini")
	for name, c := range map[string]struct {
		files map[string]string
		want  string
	}{
		"no src": {map[string]string{"m.yaml": strings.Replace(m, "chipset: [{value: Q370, src: hp-ds-hp.mini}]", "chipset: Q370", 1)},
			"chipset: a value without a source"},
		"unknown src": {map[string]string{"m.yaml": strings.Replace(m, "max_total_gb: [{value: 64, src: hp-ds-hp.mini}]", "max_total_gb: [{value: 64, src: forum}]", 1)},
			`src "forum" isn't in sources`},
		"empty claim list": {map[string]string{"m.yaml": strings.Replace(m, "chipset: [{value: Q370, src: hp-ds-hp.mini}]", "chipset: []", 1)},
			"chipset: empty claim list"},
		"the same DMI key twice": {map[string]string{"m.yaml": m, "n.yaml": strings.Replace(strings.Replace(modelFile("hp.other"), "8086:3e92", "8086:3e91", 1), "i5-9500T", "i5-9500", 1)},
			`models entry "hp.other": matches what "hp.mini" matches`},
		"an unparseable BIOS range": {map[string]string{"m.yaml": strings.Replace(m, `from: "R21 Ver. 02.00.00"`, `from: "2.0"`, 1)},
			`"2.0" isn't an HP BIOS version`},
		"an unknown field": {map[string]string{"m.yaml": strings.Replace(m, "    match: {bus: pci", "    colour: red\n    match: {bus: pci", 1)},
			`unknown field "colour"`},
		"an unknown group": {map[string]string{"m.yaml": strings.Replace(m, "      chipset:", "      colour: [{value: red, src: hp-ds-hp.mini}]\n      chipset:", 1)},
			`unknown group "colour"`},
		"an id field": {map[string]string{"m.yaml": strings.Replace(m, "  hp.mini:\n", "  hp.mini:\n    id: hp.mini\n", 1)},
			`models entry "hp.mini": its id is its key; drop the id field`},
		"defined twice": {map[string]string{"m.yaml": m, "n.yaml": strings.Replace(m, "hp-ds-hp.mini:", "hp-ds-other:", 1)},
			`models entry "hp.mini" is defined twice`},
		// The memory checks read the memory group strictly (#107).
		"a misspelt memory leaf": {map[string]string{"m.yaml": strings.Replace(m, "max_total_gb:", "max_totl_gb:", 1)},
			`models entry "hp.mini": data.memory: json: unknown field "max_totl_gb"`},
		"an unknown memory rule": {map[string]string{"m.yaml": strings.Replace(m, "        max_total_gb:", "        population: [{value: [any-order], src: hp-ds-hp.mini}]\n        max_total_gb:", 1)},
			`data.memory: population: "any-order" isn't one of`},
	} {
		dir := writeFiles(t, c.files)
		code, _, stderr := genkb(t, "-o", filepath.Join(t.TempDir(), "out.gz"), dir)
		if code != 1 || !strings.Contains(stderr, c.want) {
			t.Errorf("%s: exit %d, %q; want %q", name, code, stderr, c.want)
		}
	}
}

// Entries from several files come out sorted by ID, whichever file each
// is in.
func TestSectionEntriesAreSortedAcrossFiles(t *testing.T) {
	// Each file keeps its source and model; the other sections would repeat.
	first, _, _ := strings.Cut(strings.Replace(modelFile("z.model"), `board_name: "8595"`, `board_name: "9999"`, 1), "devices:")
	second, _, _ := strings.Cut(modelFile("a.model"), "devices:")
	dir := writeFiles(t, map[string]string{"r.yaml": ruleFile("a.rule"), "a.yaml": first, "b.yaml": second})
	out := filepath.Join(t.TempDir(), "out.gz")
	if code, _, stderr := genkb(t, "-o", out, dir); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	data, _ := os.ReadFile(out)
	k, err := kb.Parse(data)
	if err != nil || len(k.Models) != 2 || k.Models[0].ID != "a.model" || k.Models[1].ID != "z.model" {
		t.Errorf("%v: %+v", err, k.Models)
	}
}

// later: a compiled knowledge base whose content changed since the base
// copy must carry a later version; unchanged content, or no base copy, is
// fine; an earlier or equal version with new content isn't.
func TestLaterVersionsForChangedContent(t *testing.T) {
	dir := writeFiles(t, map[string]string{"r.yaml": ruleFile("a.rule")})
	tmp := t.TempDir()
	base, file := filepath.Join(tmp, "base.gz"), filepath.Join(tmp, "file.gz")
	build := func(out, version string) {
		t.Helper()
		if code, _, stderr := genkb(t, "-version", version, "-o", out, dir); code != 0 {
			t.Fatal(stderr)
		}
	}
	build(base, "2026-10-06T10:00:00Z")
	build(file, "2026-10-06T10:00:00Z")
	if code, stdout, stderr := genkb(t, "later", base, file); code != 0 || !strings.Contains(stdout, "content unchanged") {
		t.Errorf("unchanged: exit %d %s%s", code, stdout, stderr)
	}
	if code, stdout, _ := genkb(t, "later", filepath.Join(tmp, "none.gz"), file); code != 0 || !strings.Contains(stdout, "no base copy") {
		t.Errorf("no base: exit %d %s", code, stdout)
	}
	os.WriteFile(filepath.Join(dir, "s.yaml"), []byte(ruleFile("b.rule")), 0o644)
	for version, ok := range map[string]bool{"2026-10-06T10:00:00Z": false, "2026-10-06T09:59:59Z": false, "2026-10-06T10:00:01Z": true} {
		build(file, version)
		code, stdout, stderr := genkb(t, "later", base, file)
		if (code == 0) != ok || ok && !strings.Contains(stdout, "version later") || !ok && !strings.Contains(stderr, "isn't later than 2026-10-06T10:00:00Z") {
			t.Errorf("%s: exit %d %s%s", version, code, stdout, stderr)
		}
	}
	write := func(path, content string) { os.WriteFile(path, []byte(content), 0o644) }
	write(filepath.Join(tmp, "broken.gz"), "x")
	if code, stdout, _ := genkb(t, "later", filepath.Join(tmp, "broken.gz"), file); code != 0 || !strings.Contains(stdout, "the base copy isn't readable by this build") {
		t.Errorf("a base of an older format: exit %d %s", code, stdout)
	}
	if code, _, stderr := genkb(t, "later", base, filepath.Join(tmp, "broken.gz")); code != 1 || !strings.Contains(stderr, "broken.gz") {
		t.Errorf("unreadable file: exit %d %s", code, stderr)
	}
	if code, _, _ := genkb(t, "later", base, filepath.Join(tmp, "missing.gz")); code != 1 {
		t.Error("a missing file passed")
	}
	if code, _, _ := genkb(t, "later", dir, file); code != 1 {
		t.Error("a directory as base passed")
	}
}
