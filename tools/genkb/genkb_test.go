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
	if k.Version != "2026-10-06" || len(k.Rules) != 2 || k.Rules[0].ID != "a.first" || k.Rules[1].ID != "z.last" {
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
	if code, _, stderr := genkb(t, "-version", "2030-01-02", "-o", out, dir); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	data, _ = os.ReadFile(out)
	if k, _ := kb.Parse(data); k.Version != "2030-01-02" {
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
// change takes today's date. check compares content, not gzip bytes.
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
	if k, _ := kb.Parse(data); k.Version != "2026-10-15" {
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
	}
	for name, c := range cases {
		dir := writeFiles(t, c.files)
		code, _, stderr := genkb(t, "-o", filepath.Join(t.TempDir(), "out.gz"), dir)
		if code != 1 || !strings.Contains(stderr, c.want) {
			t.Errorf("%s: exit %d, %q; want %q", name, code, stderr, c.want)
		}
	}
}

func TestCheckNoticesAStaleFile(t *testing.T) {
	dir := writeFiles(t, map[string]string{"rules.yaml": ruleFile("a.rule")})
	out := filepath.Join(t.TempDir(), "advisor.json.gz")
	if code, _, stderr := genkb(t, "-o", out, dir); code != 0 {
		t.Fatal(stderr)
	}
	if code, stdout, _ := genkb(t, "check", out, dir); code != 0 || !strings.Contains(stdout, "1 rules, version 2026-10-06, up to date") {
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
