// Package advisor turns a capture into advice (ADR 0009). Advise is pure:
// the capture, the knowledge base and the live machine's maintenance state
// come in as values, findings go out. Every check is a Go function in this
// package, registered by name; knowledge-base rules name a check, supply
// its data and cite their sources.
package advisor

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jiegui2025/hwspec/internal/kb"
	"github.com/jiegui2025/hwspec/internal/report"
)

// Version is the advice document's format version.
const Version = 1

// Advice is the document `hwspec advise` prints. It is never part of a
// capture: it changes whenever the knowledge base does.
type Advice struct {
	AdviceVersion int    `json:"advice_version"`
	KBVersion     string `json:"kb_version"`
	// CaptureSHA256 identifies the advised content, not a file: SHA-256 of
	// the capture as this build reads it (fields it doesn't know are
	// dropped), after --redact, encoded as compact JSON. The JSON and YAML
	// of one capture agree; it isn't `sha256sum FILE`.
	CaptureSHA256 string `json:"capture_sha256"`
	// Live is true when the capture is of the machine running advise, so
	// its maintenance state could be used.
	Live     bool `json:"live"`
	Redacted bool `json:"redacted"`
	// RulesApplied and RulesSkipped count the knowledge base's rules: no
	// findings means nothing was found by the applied ones only.
	RulesApplied int       `json:"rules_applied"`
	RulesSkipped int       `json:"rules_skipped"`
	Warnings     []string  `json:"warnings"`
	Findings     []Finding `json:"findings"`
}

// Finding is one piece of advice about one device, or the whole machine. A
// finding is identified by its rule ID and its device's kind and key.
type Finding struct {
	ID         string        `json:"id"` // the rule's ID
	Category   kb.Category   `json:"category"`
	Severity   kb.Severity   `json:"severity"`
	Device     *DeviceRef    `json:"device,omitempty"`
	Title      string        `json:"title"`
	Detail     string        `json:"detail,omitempty"`
	Evidence   []Evidence    `json:"evidence,omitempty"`
	Actions    []Action      `json:"actions,omitempty"`
	Sources    []Source      `json:"sources"`
	Confidence kb.Confidence `json:"confidence"`
}

// DeviceRef names a device by the key its kind uses in captures: a PCI
// address, a USB bus-device, a disk's name.
type DeviceRef struct {
	Kind string `json:"kind"`
	Key  string `json:"key"`
	Name string `json:"name,omitempty"` // for people; matching never uses it
}

// Evidence is a capture value that triggered a finding, by its path in the
// capture's JSON (e.g. "pci[16].class_code"), or a field the capture
// doesn't have. It is written as exactly {path, value} or {path, absent:
// true}. Never an identity, serial, UUID, MAC or hostname field.
type Evidence struct {
	path   string
	value  any
	absent bool
}

// Present is evidence of a value the capture has.
func Present(path string, value any) Evidence { return Evidence{path: path, value: value} }

// Absent is evidence of a field the capture doesn't have.
func Absent(path string) Evidence { return Evidence{path: path, absent: true} }

// Path is where in the capture the evidence is.
func (e Evidence) Path() string { return e.path }

// Value is the value found, and whether there was one.
func (e Evidence) Value() (any, bool) { return e.value, !e.absent }

func (e Evidence) MarshalJSON() ([]byte, error) {
	if e.absent {
		return json.Marshal(struct {
			Path   string `json:"path"`
			Absent bool   `json:"absent"`
		}{e.path, true})
	}
	return json.Marshal(struct {
		Path  string `json:"path"`
		Value any    `json:"value"`
	}{e.path, e.value})
}

func (e *Evidence) UnmarshalJSON(data []byte) error {
	var v struct {
		Path   string `json:"path"`
		Value  any    `json:"value"`
		Absent bool   `json:"absent"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*e = Evidence{path: v.Path, value: v.Value, absent: v.Absent}
	return nil
}

// Action is what the user can do about a finding: what can go wrong, and
// how to go back. Commands are shown, never run.
type Action struct {
	Distro   string   `json:"distro,omitempty"`
	Text     string   `json:"text"`
	Commands []string `json:"commands,omitempty"`
	Risk     string   `json:"risk,omitempty"`
	Undo     string   `json:"undo,omitempty"`
}

// Source is a document a finding rests on, as the advice shows it.
type Source struct {
	ID         string        `json:"id"`
	URL        string        `json:"url"`
	Mirror     string        `json:"mirror,omitempty"`
	Title      string        `json:"title,omitempty"`
	Doc        string        `json:"doc,omitempty"`
	Edition    string        `json:"edition,omitempty"`
	Published  string        `json:"published,omitempty"`
	Retrieved  string        `json:"retrieved"`
	Licence    string        `json:"licence"`
	Confidence kb.Confidence `json:"confidence"`
	Quote      string        `json:"quote,omitempty"`
	Locator    string        `json:"locator,omitempty"`
}

// State is the live machine's maintenance record (ADR 0009): one entry per
// time a task was done. It is nil when advising a saved capture, or when
// the machine has no readable machine-id. #11 writes it.
type State struct {
	Format int    `json:"format"`
	Done   []Done `json:"done,omitempty"`
}

// Done is one maintenance task done on one day.
type Done struct {
	Task     string `json:"task"`
	Date     string `json:"date"` // YYYY-MM-DD
	Material string `json:"material,omitempty"`
	Note     string `json:"note,omitempty"`
}

// StateFormat is the version of State's file format.
const StateFormat = 1

// Input is everything advice is made from. Later inputs (#11's config,
// #10's firmware catalogue) are added as fields.
type Input struct {
	Report *report.Report
	KB     *kb.KB
	State  *State // nil unless Live
	Live   bool
	Now    time.Time
}

// A hit is what a check found about one device: Advise adds the rule's ID,
// words and sources.
type hit struct {
	device   *DeviceRef
	evidence []Evidence
	actions  []Action
	used     []string // sources the check used beyond its rule's, e.g. a claim's
	// vars are the values a rule's commands may name as {name}. A check
	// sets only values it has validated: they become part of a shell line.
	vars map[string]string
}

// A check applies one rule to a capture. An error means the rule's data is
// unusable.
type check struct {
	run func(in *Input, rule *kb.Rule) ([]hit, error)
	// needs names the capture fields the check reads, and available says
	// whether a capture has them: one from before a field existed, or one
	// whose collector couldn't read it, can't be evaluated, which is not
	// the same as "nothing found".
	needs     []string
	available func(r *report.Report) bool
	// provides names the {placeholders} the check can fill in a rule's
	// commands; tools/genkb refuses any other.
	provides []string
}

var checks = map[string]check{
	"pci-without-driver": {
		run:       pciWithoutDriver,
		needs:     []string{"pci[].class_code", "pci[].driver"},
		available: func(r *report.Report) bool { return r.PCI != nil },
		provides:  []string{"modalias"},
	},
}

// Checks lists the registered check names, for tools/genkb to validate
// rules against.
func Checks() []string {
	return slices.Sorted(maps.Keys(checks))
}

// Provides lists the placeholders a check fills in its rule's commands,
// for tools/genkb to validate rules against.
func Provides(name string) []string {
	return slices.Clone(checks[name].provides)
}

// Placeholders lists the {names} a command uses.
func Placeholders(command string) []string {
	var names []string
	for _, m := range placeholder.FindAllStringSubmatch(command, -1) {
		names = append(names, m[1])
	}
	return names
}

// Advise applies every rule in the knowledge base to the capture. Rules
// this build can't apply (skipped by kb.Parse, an unknown check from a
// newer knowledge base, a capture without the fields a check needs, or
// unusable data) become warnings, never findings. The capture's own
// warnings are carried over: what it couldn't read can hide findings.
func Advise(in Input) Advice {
	a := Advice{
		AdviceVersion: Version,
		KBVersion:     in.KB.Version,
		Live:          in.Live,
		Redacted:      in.Report.Redacted,
		Warnings:      []string{},
		Findings:      []Finding{},
	}
	if js, err := json.Marshal(in.Report); err == nil { // a Report always encodes
		sum := sha256.Sum256(js)
		a.CaptureSHA256 = hex.EncodeToString(sum[:])
	}
	for _, w := range in.Report.Warnings {
		a.Warnings = append(a.Warnings, "capture: "+w)
	}
	a.Warnings = append(a.Warnings, in.KB.Skipped...)
	a.RulesSkipped = in.KB.SkippedRules
	for i := range in.KB.Rules {
		rule := &in.KB.Rules[i]
		c, ok := checks[rule.Check]
		switch {
		case !ok:
			a.Warnings = append(a.Warnings, fmt.Sprintf("rule %s needs check %q, which this build of hwspec doesn't have; update hwspec", rule.ID, rule.Check))
			a.RulesSkipped++
			continue
		case c.available != nil && !c.available(in.Report):
			a.Warnings = append(a.Warnings, fmt.Sprintf("rule %s can't evaluate this capture: it needs %s, which the capture doesn't have", rule.ID, strings.Join(c.needs, ", ")))
			a.RulesSkipped++
			continue
		}
		hits, err := c.run(&in, rule)
		if err != nil {
			a.Warnings = append(a.Warnings, fmt.Sprintf("rule %s skipped: %v", rule.ID, err))
			a.RulesSkipped++
			continue
		}
		a.RulesApplied++
		seen := map[string]bool{} // one warning per rule for the same problem
		warn := func(w string) {
			if !seen[w] {
				seen[w] = true
				a.Warnings = append(a.Warnings, w)
			}
		}
		for _, h := range hits {
			a.Findings = append(a.Findings, finding(in.KB, rule, h, warn))
		}
	}
	slices.SortStableFunc(a.Findings, compareFindings)
	return a
}

func finding(k *kb.KB, rule *kb.Rule, h hit, warn func(string)) Finding {
	f := Finding{
		ID: rule.ID, Category: rule.Category, Severity: rule.Severity,
		Device: h.device, Title: rule.Title, Detail: rule.Detail, Evidence: h.evidence,
	}
	for _, act := range rule.Actions {
		filled, missing := fill(Action(act), h.vars)
		f.Actions = append(f.Actions, filled)
		for _, name := range missing {
			where := "the machine"
			if h.device != nil {
				where = h.device.Kind + " " + h.device.Key
			}
			warn(fmt.Sprintf("rule %s: a suggested command needs {%s}, which the capture doesn't give for %s; it's left out", rule.ID, name, where))
		}
	}
	f.Actions = append(f.Actions, h.actions...)
	used := slices.Compact(slices.Sorted(slices.Values(append(slices.Clone(rule.Src), h.used...))))
	for _, id := range used {
		s := k.Source(id)
		if s == nil {
			warn(fmt.Sprintf("rule %s cites source %q, which the knowledge base doesn't have", rule.ID, id))
			continue
		}
		f.Sources = append(f.Sources, Source{
			ID: s.ID, URL: s.URL, Mirror: s.Mirror, Title: s.Title, Doc: s.Doc, Edition: s.Edition, Published: s.Published, Retrieved: s.Retrieved,
			Licence: s.Licence, Confidence: s.Confidence, Quote: s.Quote, Locator: s.Locator,
		})
	}
	f.Confidence = k.Weakest(used)
	return f
}

var placeholder = regexp.MustCompile(`\{([a-z_]+)\}`)

// fill puts a hit's validated values into a rule's commands. A command
// naming a value the hit doesn't have is left out, and its name returned:
// capture text never reaches a shell line unless a check validated it.
func fill(a Action, vars map[string]string) (Action, []string) {
	var missing []string
	cmds := make([]string, 0, len(a.Commands))
	for _, c := range a.Commands {
		ok := true
		filled := placeholder.ReplaceAllStringFunc(c, func(m string) string {
			name := m[1 : len(m)-1]
			v, have := vars[name]
			if !have {
				ok = false
				missing = append(missing, name)
			}
			return v
		})
		if ok {
			cmds = append(cmds, filled)
		}
	}
	a.Commands = cmds
	return a, missing
}

// compareFindings orders the most severe first, then by category, rule and
// device, so the same inputs always give the same document. Unknown
// severities and categories sort last.
func compareFindings(x, y Finding) int {
	return cmp.Or(
		cmp.Compare(x.Severity.Rank(), y.Severity.Rank()),
		cmp.Compare(x.Category.Rank(), y.Category.Rank()),
		cmp.Compare(x.ID, y.ID),
		cmp.Compare(deviceKey(x.Device), deviceKey(y.Device)),
	)
}

func deviceKey(d *DeviceRef) string {
	if d == nil {
		return ""
	}
	return d.Kind + "\x00" + d.Key
}
