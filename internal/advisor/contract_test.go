package advisor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/jiegui2025/hwspec/internal/kb"
	"github.com/jiegui2025/hwspec/internal/report"
)

// firmwareData is what the test check below reads from a rule's data.
type firmwareData struct {
	Latest claims[string] `json:"latest"`
}

// withTestCheck registers a check that takes data and no match keys, the
// shape #10's and #128's checks will have, for the length of one test.
func withTestCheck(t *testing.T) {
	t.Helper()
	register("firmware-test", check{
		run: func(in *Input, rule *kb.Rule) ([]hit, error) {
			d, err := decodeData[firmwareData](rule.Data)
			if err != nil {
				return nil, err
			}
			v, src := d.Latest.first()
			return []hit{{evidence: []Evidence{Present("system.firmware.version", in.Report.System.Firmware.Version)}, vars: map[string]string{"latest": v}, used: []string{src}}}, nil
		},
		provides: []string{"latest"},
		data:     func(raw json.RawMessage) error { _, err := decodeData[firmwareData](raw); return err },
	})
	t.Cleanup(func() { delete(checks, "firmware-test") })
}

func firmwareRule(data string) kb.Rule {
	return kb.Rule{ID: "fw.newer", Check: "firmware-test", Category: "firmware", Severity: "info", Title: "Newer firmware",
		Data: json.RawMessage(data), Actions: []kb.Action{{Text: "Update", Commands: []string{"echo {latest}"}}}, Src: []string{"kernel"}}
}

// A rule must fit its check: genkb refuses one that doesn't, and Advise
// skips it (a newer knowledge base may have it), never applying it more
// broadly than written or with part of its data ignored.
func TestRulesMustFitTheirCheck(t *testing.T) {
	withTestCheck(t)
	noMatch := noDriverRule()
	noMatch.ID, noMatch.Match = "pci.everything", kb.Match{}
	pciData := noDriverRule()
	pciData.ID, pciData.Data = "pci.data", json.RawMessage(`{"devicez": [{"value": "x", "src": "kernel"}]}`)
	fwMatch := firmwareRule(`{"latest": [{"value": "1.2", "src": "kernel"}]}`)
	fwMatch.ID, fwMatch.Match = "fw.match", kb.Match{PCIClass: []string{"02"}}
	fwKey := firmwareRule(`{"latestt": [{"value": "1.2", "src": "kernel"}]}`)
	fwKey.ID = "fw.key"
	fwNone := firmwareRule("")
	fwNone.ID = "fw.none"
	badCommand := noDriverRule()
	badCommand.ID, badCommand.Actions = "pci.command", []kb.Action{{Text: "t", Commands: []string{"modprobe {modallias}"}}}
	unknown := noDriverRule()
	unknown.ID, unknown.Check = "pci.unknown", "magic"
	cases := []struct {
		rule kb.Rule
		want string
	}{
		{noMatch, `match.pci_class is empty: check "pci-without-driver" needs it`},
		{pciData, `check "pci-without-driver" takes no data`},
		{fwMatch, `check "firmware-test" doesn't use match.pci_class (it uses: )`},
		{fwKey, `data: json: unknown field "latestt"`},
		{fwNone, "data: no data"},
		{badCommand, `command "modprobe {modallias}" uses {modallias}, which check "pci-without-driver" doesn't fill (it fills: modalias)`},
		{unknown, `unknown check "magic" (have firmware-test, memory-below-minimum, memory-upgrade, pci-without-driver)`},
	}
	for _, c := range cases {
		errs := ValidateRule(&c.rule)
		if len(errs) != 1 || errs[0].Error() != c.want {
			t.Errorf("%s: %v, want %q", c.rule.ID, errs, c.want)
		}
	}
	good := firmwareRule(`{"latest": [{"value": "1.2", "src": "forum"}]}`)
	if errs := ValidateRule(&good); errs != nil {
		t.Errorf("a fitting rule: %v", errs)
	}
	r := machine()
	r.System.Firmware = &report.Firmware{Version: "1.1"}
	k := knowledge(noMatch, pciData, fwMatch, fwKey, good)
	a := Advise(Input{Report: r, KB: k, Now: noon})
	w := strings.Join(a.Warnings, "\n")
	for _, c := range cases[:4] {
		if want := "rule " + c.rule.ID + " skipped: " + c.want; !strings.Contains(w, want) {
			t.Errorf("missing %q in %q", want, a.Warnings)
		}
	}
	if a.RulesApplied != 1 || a.RulesSkipped != 4 || len(a.Findings) != 1 {
		t.Fatalf("applied %d, skipped %d, findings %+v", a.RulesApplied, a.RulesSkipped, a.Findings)
	}
	f := a.Findings[0]
	if f.Actions[0].Commands[0] != "echo 1.2" || len(f.Sources) != 2 || f.Confidence != "community" {
		t.Errorf("the data's claim isn't used and cited: %+v", f)
	}
	// Several problems at once stay on one line.
	both := noDriverRule()
	both.ID, both.Match, both.Data = "pci.both", kb.Match{}, json.RawMessage(`{"x": [{"value": 1, "src": "kernel"}]}`)
	a = Advise(Input{Report: machine(), KB: knowledge(both), Now: noon})
	if len(a.Warnings) != 1 || !strings.Contains(a.Warnings[0], "needs it; check") {
		t.Errorf("warnings %q", a.Warnings)
	}
}

func TestChecksAreRegisteredOnce(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a second check of one name was registered")
		}
	}()
	register("pci-without-driver", check{})
}

func TestTypedClaims(t *testing.T) {
	var none claims[int]
	if v, src := none.first(); v != 0 || src != "" {
		t.Errorf("no claims: %v %q", v, src)
	}
	d, err := decodeData[firmwareData](json.RawMessage(`{"latest": [{"value": "2.0", "src": "a"}, {"value": "1.9", "src": "b"}]}`))
	if v, src := d.Latest.first(); err != nil || v != "2.0" || src != "a" {
		t.Errorf("first claim: %q %q %v", v, src, err)
	}
	if _, err := decodeData[firmwareData](json.RawMessage(`{"latest": [{"value": 2, "src": "a"}]}`)); err == nil {
		t.Error("a number decoded as a version string")
	}
}

// Every registered check comes with an example rule and capture on which
// it finds something; the tests below run on all of them, so a new check
// is covered without assuming it reads PCI devices.
func tryExample(name string) (Advice, error) {
	c := checks[name]
	if c.example == nil {
		return Advice{}, fmt.Errorf("check %s has no example", name)
	}
	rule, r := c.example()
	if rule.Check != name {
		return Advice{}, fmt.Errorf("check %s: its example rule is for %q", name, rule.Check)
	}
	if errs := ValidateRule(&rule); errs != nil {
		return Advice{}, fmt.Errorf("check %s: its example rule doesn't fit it: %v", name, errs)
	}
	k := exampleKnowledge(name, rule)
	if err := k.Validate(); err != nil {
		return Advice{}, fmt.Errorf("check %s: its example rule isn't valid: %w", name, err)
	}
	a := Advise(Input{Report: r, KB: k, Now: noon})
	if len(a.Findings) == 0 || len(a.Warnings) != 0 {
		return Advice{}, fmt.Errorf("check %s: its example gives findings %+v, warnings %q", name, a.Findings, a.Warnings)
	}
	return a, nil
}

// exampleKnowledge is the test sources with a check's example rule and
// the example's own sources and model entries.
func exampleKnowledge(name string, rule kb.Rule) *kb.KB {
	k := knowledge(rule)
	if data := checks[name].exampleData; data != nil {
		srcs, models := data()
		k.Sources = append(k.Sources, srcs...)
		slices.SortFunc(k.Sources, func(a, b kb.Source) int { return strings.Compare(a.ID, b.ID) })
		k.Models = models
	}
	return k
}

func examples(t *testing.T) map[string]Advice {
	t.Helper()
	out := map[string]Advice{}
	for _, name := range Checks() {
		a, err := tryExample(name)
		if err != nil {
			t.Error(err)
			continue
		}
		out[name] = a
	}
	return out
}

func TestEveryCheckHasAWorkingExample(t *testing.T) {
	if got := examples(t); len(got) != len(checks) {
		t.Errorf("%d examples for %d checks", len(got), len(checks))
	}
	withTestCheck(t) // its rule needs data: an example without it gives nothing
	old := checks["firmware-test"]
	for want, change := range map[string]func(c *check){
		"has no example": func(c *check) {},
		"is for \"other\"": func(c *check) {
			c.example = func() (kb.Rule, *report.Report) { r := firmwareRule(""); r.Check = "other"; return r, &report.Report{} }
		},
		"doesn't fit it": func(c *check) {
			c.example = func() (kb.Rule, *report.Report) { return firmwareRule(""), &report.Report{} }
		},
		"isn't valid": func(c *check) {
			c.example = func() (kb.Rule, *report.Report) {
				r := firmwareRule(`{"latest": [{"value": "2", "src": "kernel"}]}`)
				r.Title = ""
				return r, &report.Report{System: report.System{Firmware: &report.Firmware{Version: "1"}}}
			}
		},
		"gives findings": func(c *check) {
			c.example = func() (kb.Rule, *report.Report) {
				r := firmwareRule(`{"latest": [{"value": "2", "src": "kernel"}]}`)
				r.Actions = []kb.Action{{Text: "t", Commands: []string{"x {latest}"}}}
				return r, &report.Report{Warnings: []string{"w"}, System: report.System{Firmware: &report.Firmware{Version: "1"}}}
			}
		},
	} {
		c := old
		change(&c)
		checks["firmware-test"] = c
		if _, err := tryExample("firmware-test"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", want, err)
		}
	}
}

// Every finding's evidence path resolves to the value it cites in the
// capture's JSON, or names a field the capture doesn't have.
func TestEvidencePathsResolve(t *testing.T) {
	for _, name := range Checks() {
		rule, r := checks[name].example()
		js, _ := json.Marshal(r)
		var doc any
		if err := json.Unmarshal(js, &doc); err != nil {
			t.Fatal(err)
		}
		a := Advise(Input{Report: r, KB: exampleKnowledge(name, rule), Now: noon})
		for _, f := range a.Findings {
			for _, e := range f.Evidence {
				got, found := resolve(doc, e.Path())
				want, present := e.Value()
				wantJSON, _ := json.Marshal(want)
				gotJSON, _ := json.Marshal(got)
				if found != present || present && !bytes.Equal(gotJSON, wantJSON) {
					t.Errorf("%s: evidence %s = %s (present %v), the capture has %s (present %v)", name, e.Path(), wantJSON, present, gotJSON, found)
				}
			}
		}
	}
}

var segment = regexp.MustCompile(`^([a-z_]+)(?:\[(\d+)\])?$`)

// resolve follows a path such as pci[1].class_code through a decoded
// capture; found is false when a field on the way isn't there.
func resolve(doc any, path string) (any, bool) {
	cur := doc
	for part := range strings.SplitSeq(path, ".") {
		m := segment.FindStringSubmatch(part)
		obj, ok := cur.(map[string]any)
		if m == nil || !ok {
			return nil, false
		}
		if cur, ok = obj[m[1]]; !ok || cur == nil {
			return nil, false
		}
		if m[2] != "" {
			i, _ := strconv.Atoi(m[2])
			list, ok := cur.([]any)
			if !ok || i >= len(list) {
				return nil, false
			}
			cur = list[i]
		}
	}
	return cur, true
}

func TestResolve(t *testing.T) {
	doc := map[string]any{"pci": []any{map[string]any{"class_code": "020000", "driver": nil}}, "bios": map[string]any{"version": "1"}}
	for path, want := range map[string]any{"pci[0].class_code": "020000", "bios.version": "1"} {
		if got, ok := resolve(doc, path); !ok || got != want {
			t.Errorf("%s = %v %v", path, got, ok)
		}
	}
	for _, path := range []string{"pci[0].driver", "pci[1].class_code", "bios[0]", "pci.x", "PCI", "gpu.version", "bios.version.x"} {
		if got, ok := resolve(doc, path); ok {
			t.Errorf("%s resolved to %v", path, got)
		}
	}
}

// Evidence never copies an identifier (ADR 0009), whatever the check: each
// example capture gets a serial in every identity block, and a hostname.
func TestEvidenceNeverCopiesIdentifiers(t *testing.T) {
	for _, name := range Checks() {
		rule, r := checks[name].example()
		r.Hostname = "host"
		fillIdentities(reflect.ValueOf(r).Elem())
		a := Advise(Input{Report: r, KB: exampleKnowledge(name, rule), Now: noon})
		if len(a.Findings) == 0 {
			t.Errorf("%s: no findings to look at", name)
		}
		for _, f := range a.Findings {
			for _, e := range f.Evidence {
				p := strings.ToLower(e.Path())
				for _, banned := range []string{"identity", "serial", "uuid", "mac", "hostname", "asset"} {
					if strings.Contains(p, banned) {
						t.Errorf("%s: evidence %s copies an identifier", name, e.Path())
					}
				}
				if v, _ := e.Value(); v == "SERIAL" || v == "host" {
					t.Errorf("%s: evidence %s = %v", name, e.Path(), v)
				}
			}
		}
	}
}

// fillIdentities gives every identity block reachable from v a serial.
func fillIdentities(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() && v.Type().Elem() == reflect.TypeFor[report.Identity]() && v.CanSet() {
			v.Set(reflect.ValueOf(&report.Identity{Model: "m"}))
		}
		if !v.IsNil() {
			fillIdentities(v.Elem())
		}
	case reflect.Struct:
		if id, ok := reflect.TypeAssert[*report.Identity](v.Addr()); ok {
			id.Serial = "SERIAL"
			return
		}
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				fillIdentities(v.Field(i))
			}
		}
	case reflect.Slice:
		for i := range v.Len() {
			fillIdentities(v.Index(i))
		}
	}
}

// Whatever a check copies from a hostile capture into advice, the text
// output carries no control characters: every string in each example
// capture gets an escape sequence and a bidi override.
func TestEveryCheckIsTerminalSafe(t *testing.T) {
	for _, name := range Checks() {
		rule, r := checks[name].example()
		poison(reflect.ValueOf(r).Elem())
		a := Advise(Input{Report: r, KB: exampleKnowledge(name, rule), Now: noon})
		var buf bytes.Buffer
		if err := WriteText(&buf, a); err != nil {
			t.Fatal(err)
		}
		if i := strings.IndexFunc(buf.String(), func(c rune) bool { return c != '\n' && (unicode.IsControl(c) || unicode.Is(unicode.Cf, c)) }); i >= 0 {
			t.Errorf("%s: a control character reaches the terminal: %q", name, buf.String()[max(0, i-20):i+5])
		}
	}
}

// poison appends an escape sequence and a bidi override to every string
// reachable from v.
func poison(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			poison(v.Elem())
		}
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				poison(v.Field(i))
			}
		}
	case reflect.Slice:
		for i := range v.Len() {
			poison(v.Index(i))
		}
	case reflect.String:
		if v.CanSet() {
			v.SetString(v.String() + "\x1b[2J\u202e")
		}
	}
}

// Each example's commands use only placeholders the check fills, and the
// check fills them for the example's findings.
func TestEveryExampleFillsItsCommands(t *testing.T) {
	for name, a := range examples(t) {
		rule, _ := checks[name].example()
		for _, f := range a.Findings {
			n := 0
			for _, act := range rule.Actions {
				n += len(act.Commands)
			}
			got := 0
			for _, act := range f.Actions {
				got += len(act.Commands)
				for _, c := range act.Commands {
					if strings.ContainsAny(c, "{}") {
						t.Errorf("%s: command %q left unfilled", name, c)
					}
				}
			}
			if got != n {
				t.Errorf("%s: %d of %d commands kept", name, got, n)
			}
		}
		if !slices.ContainsFunc(rule.Actions, func(a kb.Action) bool { return len(a.Commands) > 0 }) {
			t.Logf("%s: its example has no command to fill", name)
		}
	}
}

// The PCI helper selects by class when the rule names classes, with the
// class code as evidence, and every device otherwise; a caller may stop.
func TestPCIMatches(t *testing.T) {
	r := machine()
	n := 0
	for m := range pciMatches(r, kb.Match{}) {
		if len(m.evidence) != 0 || m.ref.Key != r.PCI[m.index].Address {
			t.Errorf("no classes: %+v", m)
		}
		n++
	}
	if n != len(r.PCI) {
		t.Errorf("%d of %d devices without a class match", n, len(r.PCI))
	}
	for m := range pciMatches(r, kb.Match{PCIClass: []string{"0403"}}) {
		if m.ref.Name != "Audio device" || m.evidence[0].Path() != "pci[3].class_code" {
			t.Errorf("audio: %+v", m)
		}
		break
	}
	n = 0
	for range pciMatches(r, kb.Match{PCIClass: []string{"02"}}) {
		n++
		break
	}
	if n != 1 {
		t.Errorf("stopping after one went on: %d", n)
	}
}

// A check failing on a rule's data at run time skips the rule.
func TestARunErrorSkipsTheRule(t *testing.T) {
	withRun(t, func(*Input, *kb.Rule) ([]hit, error) { return nil, errors.New("boom") })
	a := Advise(Input{Report: machine(), KB: knowledge(noDriverRule()), Now: noon})
	if a.RulesSkipped != 1 || a.RulesApplied != 0 || len(a.Warnings) != 1 || a.Warnings[0] != "rule pci.no-driver skipped: boom" {
		t.Errorf("skipped %d, applied %d, warnings %q", a.RulesSkipped, a.RulesApplied, a.Warnings)
	}
}

// A check declares only match keys kb.Match has, and requires only keys
// it honours.
func TestDeclaredMatchKeysExist(t *testing.T) {
	var all []string
	mt := reflect.TypeFor[kb.Match]()
	for f := range mt.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		all = append(all, name)
	}
	for _, name := range Checks() {
		c := checks[name]
		for _, k := range c.matches {
			if !slices.Contains(all, k) {
				t.Errorf("check %s honours match.%s, which kb.Match doesn't have (%s)", name, k, all)
			}
		}
		for _, k := range c.requires {
			if !slices.Contains(c.matches, k) {
				t.Errorf("check %s requires match.%s without honouring it", name, k)
			}
		}
	}
}
