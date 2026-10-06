// Package kb is the advisor's knowledge base (ADR 0009): a registry of
// sources, and rules that name a check in internal/advisor, supply its data
// and cite the sources they rest on. tools/genkb compiles kb/**/*.yaml into
// advisor-v1.json.gz; this package parses and validates it. It is pure:
// bytes in, values out.
package kb

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Format is the version of the compiled file's structure, also in its name
// (FileName): a new format is a new file next to the old one in the signed
// bundle (#81), so older binaries keep reading theirs.
const Format = 1

// FileName is the compiled file's name, here and in the signed bundle.
const FileName = "advisor-v1.json.gz"

// Category, Severity and Confidence are open sets: a newer knowledge base
// may use values this build doesn't know. Parse skips a rule or source with
// an unknown value; where one still reaches a comparison, it ranks last
// (least severe, weakest), never first.
type (
	Category   string
	Severity   string
	Confidence string
)

// The known values, in rank order: most severe and strongest first.
var (
	Categories  = []Category{"needs-attention", "performance", "upgrade", "firmware", "maintenance"}
	Severities  = []Severity{"critical", "warning", "info"}
	Confidences = []Confidence{"oem-doc", "upstream-doc", "measured", "community"}
)

func rank[T comparable](known []T, v T) int {
	if i := slices.Index(known, v); i >= 0 {
		return i
	}
	return len(known)
}

// Rank orders categories as listed; unknown ones last.
func (c Category) Rank() int { return rank(Categories, c) }

// Rank orders severities, most severe first; unknown ones last.
func (s Severity) Rank() int { return rank(Severities, s) }

// Rank orders confidences, strongest first; unknown ones last (weakest).
func (c Confidence) Rank() int { return rank(Confidences, c) }

func known[T comparable](all []T, v T) bool { return slices.Contains(all, v) }

func names[T ~string](all []T) string {
	var out []string
	for _, v := range all {
		out = append(out, string(v))
	}
	return strings.Join(out, ", ")
}

// KB is a compiled knowledge base.
type KB struct {
	Format  int      `json:"format"`
	Version string   `json:"version"` // YYYY-MM-DD: the date the content last changed (ADR 0009)
	Sources []Source `json:"sources"` // sorted by ID
	Rules   []Rule   `json:"rules"`   // sorted by ID
	// Skipped says which sections, rules and sources Parse left out, and why: a
	// newer knowledge base can use fields and values this build doesn't
	// know.
	Skipped []string `json:"-"`
	// SkippedRules counts the rules among them.
	SkippedRules int `json:"-"`
}

// Source is a document the knowledge base rests on, with its licence and
// how far it can be trusted. A quote stays under its source's licence;
// sources whose terms don't allow quoting are link-only, with a locator
// (page, section) instead.
type Source struct {
	ID         string     `json:"id"`
	URL        string     `json:"url"`
	Mirror     string     `json:"mirror,omitempty"` // a copy, for when the URL fails
	Title      string     `json:"title,omitempty"`
	Doc        string     `json:"doc,omitempty"`     // the document's number
	Edition    string     `json:"edition,omitempty"` // its revision
	Published  string     `json:"published,omitempty"`
	Retrieved  string     `json:"retrieved"`
	Licence    string     `json:"licence"` // SPDX expression, or the terms' name
	Confidence Confidence `json:"confidence"`
	Quote      string     `json:"quote,omitempty"`
	LinkOnly   bool       `json:"link_only,omitempty"`
	Locator    string     `json:"locator,omitempty"`
}

// Capture is the built-in source of values a check reads from the capture
// itself (ADR 0009): measured on the machine, not documented.
var Capture = Source{
	ID: "capture", URL: "https://github.com/jiegui2025/hwspec", Title: "this capture",
	Retrieved: "2026-10-06", Licence: "GPL-3.0-or-later", Confidence: "measured",
	LinkOnly: true, Locator: "the evidence paths of the finding",
}

// Rule configures one check. The check (Go code in internal/advisor) decides
// which devices it applies to; the rule supplies the words, the data and
// the sources. Every leaf value in Data is a claim list: [{value, src}].
type Rule struct {
	ID       string          `json:"id"`
	Check    string          `json:"check"`
	Category Category        `json:"category"`
	Severity Severity        `json:"severity"`
	Title    string          `json:"title"`
	Detail   string          `json:"detail,omitempty"`
	Match    Match           `json:"match,omitzero"`
	Data     json.RawMessage `json:"data,omitempty"`
	Actions  []Action        `json:"actions,omitempty"`
	Src      []string        `json:"src"`
}

// Match narrows a rule to devices by raw IDs, never by display names.
type Match struct {
	// PCIClass holds class code prefixes in hex: "02" is any network
	// controller, "0403" an HD audio device.
	PCIClass []string `json:"pci_class,omitempty"`
}

// Action is something the user can do about a finding: what can go wrong,
// and how to go back. Commands are shown, never run. Distro is an
// os-release ID or ID_LIKE value; empty means any.
type Action struct {
	Distro   string   `json:"distro,omitempty"`
	Text     string   `json:"text"`
	Commands []string `json:"commands,omitempty"`
	Risk     string   `json:"risk,omitempty"`
	Undo     string   `json:"undo,omitempty"`
}

var (
	idRe    = regexp.MustCompile(`^[a-z0-9]+([.-][a-z0-9]+)*$`)
	classRe = regexp.MustCompile(`^([0-9a-f]{2}){1,3}$`)
)

// Date layouts: a day, and the coarser dates documents often give.
const (
	day   = "2006-01-02"
	month = "2006-01"
	year  = "2006"
)

// printable reports the first string that would reach a terminal with
// control or invisible formatting characters (escape sequences, bidi
// overrides) or invalid UTF-8: knowledge-base text is shown to people.
func printable(fields map[string]string) error {
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		s := fields[name]
		if !utf8.ValidString(s) || strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
			return fmt.Errorf("%s has control or formatting characters", name)
		}
	}
	return nil
}

// printableValue applies printable to every string in a claim's value:
// data reaches advice text too.
func printableValue(at string, v any) error {
	switch x := v.(type) {
	case string:
		return printable(map[string]string{at: x})
	case []any:
		for _, e := range x {
			if err := printableValue(at, e); err != nil {
				return err
			}
		}
	case map[string]any:
		for _, key := range slices.Sorted(maps.Keys(x)) {
			if err := printable(map[string]string{at + " key": key}); err != nil {
				return err
			}
			if err := printableValue(at+"."+key, x[key]); err != nil {
				return err
			}
		}
	}
	return nil
}

// isDate says whether s is a real date in one of the layouts.
func isDate(s string, layouts ...string) bool {
	for _, l := range layouts {
		if _, err := time.Parse(l, s); err == nil && len(s) == len(l) {
			return true
		}
	}
	return false
}

// maxSize bounds the decompressed file, so a corrupt or hostile bundle
// can't exhaust memory.
const maxSize = 32 << 20

// Parse decompresses and decodes a compiled knowledge base of this format.
// It is lenient where a newer knowledge base may have grown, so one new
// field never costs a user every rule: unknown top-level sections are
// skipped and named in Skipped, and unknown fields in sources and actions
// (descriptive, not deciding what matches) are ignored. A rule is decoded
// strictly: a rule with an unknown field or value, or citing a skipped
// source, and a source with an unknown value, are left out, named in
// Skipped, and never applied in part.
func Parse(gz []byte) (*KB, error) {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, fmt.Errorf("knowledge base: %w", err)
	}
	raw, err := io.ReadAll(io.LimitReader(zr, maxSize+1))
	if err != nil {
		return nil, fmt.Errorf("knowledge base: %w", err)
	}
	if len(raw) > maxSize {
		return nil, fmt.Errorf("knowledge base: larger than %d bytes", maxSize)
	}
	// The format first: another format's sections may not fit at all.
	var sections map[string]json.RawMessage
	if err := json.Unmarshal(raw, &sections); err != nil {
		return nil, fmt.Errorf("knowledge base: %w", err)
	}
	var format int
	if err := json.Unmarshal(sections["format"], &format); err != nil || format != Format {
		got := string(sections["format"])
		if got == "" {
			got = "missing"
		}
		return nil, fmt.Errorf("knowledge base: format %s, this build reads %d", got, Format)
	}
	var file struct {
		Format  int               `json:"format"`
		Version string            `json:"version"`
		Sources []json.RawMessage `json:"sources"`
		Rules   []json.RawMessage `json:"rules"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("knowledge base: %w", err)
	}
	if !isDate(file.Version, day) {
		return nil, fmt.Errorf("knowledge base: version %q isn't YYYY-MM-DD", file.Version)
	}
	k := &KB{Format: file.Format, Version: file.Version, Sources: []Source{}, Rules: []Rule{}}
	// A newer knowledge base may add whole sections (#25's models, #10's
	// firmware): this build can't use them, and says so.
	for _, name := range slices.Sorted(maps.Keys(sections)) {
		if !slices.Contains([]string{"format", "version", "sources", "rules"}, name) {
			k.Skipped = append(k.Skipped, fmt.Sprintf("section %s skipped: this build of hwspec doesn't use it", strconv.Quote(name)))
		}
	}
	for i, js := range file.Sources {
		var s Source
		err := json.Unmarshal(js, &s)
		if err == nil {
			err = errors.Join(s.validate()...)
		}
		if err == nil && slices.ContainsFunc(k.Sources, func(o Source) bool { return o.ID == s.ID }) {
			err = errors.New("duplicate id")
		}
		if err != nil {
			k.Skipped = append(k.Skipped, fmt.Sprintf("source %s skipped: %v", nameOf(s.ID, i), err))
			continue
		}
		k.Sources = append(k.Sources, s)
	}
	slices.SortFunc(k.Sources, func(a, b Source) int { return strings.Compare(a.ID, b.ID) })
	seen := map[string]bool{}
	for i, js := range file.Rules {
		r, err := decodeRule(js)
		if err == nil {
			err = errors.Join(k.validateRule(&r, false)...)
		}
		if err == nil && seen[r.ID] {
			err = errors.New("duplicate id")
		}
		if err != nil {
			k.Skipped = append(k.Skipped, fmt.Sprintf("rule %s skipped: %v", nameOf(r.ID, i), err))
			k.SkippedRules++
			continue
		}
		seen[r.ID] = true
		k.Rules = append(k.Rules, r)
	}
	return k, nil
}

// decodeRule decodes a rule strictly, except its actions: an unknown field
// in a rule could change what it matches, one in an action only adds words.
func decodeRule(js json.RawMessage) (Rule, error) {
	var r struct {
		Rule
		Actions []json.RawMessage `json:"actions,omitempty"`
	}
	dec := json.NewDecoder(bytes.NewReader(js))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return r.Rule, err
	}
	for _, a := range r.Actions {
		var act Action
		if err := json.Unmarshal(a, &act); err != nil {
			return r.Rule, err
		}
		r.Rule.Actions = append(r.Rule.Actions, act)
	}
	return r.Rule, nil
}

// nameOf names a rule or source in messages: its ID, quoted (it may be
// anything a hostile file holds), or its position.
func nameOf(id string, i int) string {
	if id != "" {
		return strconv.Quote(id)
	}
	return fmt.Sprintf("%d", i+1)
}

// Encode compresses k deterministically (no file name or time in the gzip
// header), so the same rules always give the same bytes.
func Encode(k *KB) ([]byte, error) {
	js, err := json.Marshal(k)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	// Neither a valid level nor writes to a bytes.Buffer can fail.
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = zw.Write(js)
	_ = zw.Close()
	return buf.Bytes(), nil
}

// Source returns the source with the given ID, the built-in Capture source
// for "capture" unless the file defines one, or nil.
func (k *KB) Source(id string) *Source {
	i, ok := slices.BinarySearchFunc(k.Sources, id, func(s Source, id string) int { return strings.Compare(s.ID, id) })
	switch {
	case ok:
		return &k.Sources[i]
	case id == Capture.ID:
		c := Capture
		return &c
	}
	return nil
}

// Weakest is the lowest confidence among the given sources: a finding is
// only as sure as the least sure source it used. An unknown source or
// confidence counts as the weakest of all.
func (k *KB) Weakest(ids []string) Confidence {
	weakest := Confidence("")
	for _, id := range ids {
		c := Confidence("unknown")
		if s := k.Source(id); s != nil {
			c = s.Confidence
		}
		if weakest == "" || c.Rank() > weakest.Rank() {
			weakest = c
		}
	}
	return weakest
}

// Validate reports every problem in k at once, strictly: tools/genkb
// refuses to build a knowledge base that Parse would have to skip parts of.
// Whether a rule's check exists is the advisor's to say.
func (k *KB) Validate() error {
	var errs []error
	if k.Format != Format {
		errs = append(errs, fmt.Errorf("format %d, want %d", k.Format, Format))
	}
	if !isDate(k.Version, day) {
		errs = append(errs, fmt.Errorf("version %q isn't YYYY-MM-DD", k.Version))
	}
	seen := map[string]bool{}
	for i, s := range k.Sources {
		name := "source " + nameOf(s.ID, i)
		for _, err := range s.validate() {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
		if seen[s.ID] {
			errs = append(errs, fmt.Errorf("%s: duplicate id", name))
		}
		seen[s.ID] = true
	}
	if !slices.IsSortedFunc(k.Sources, func(a, b Source) int { return strings.Compare(a.ID, b.ID) }) {
		errs = append(errs, errors.New("sources aren't sorted by id"))
	}
	if !slices.IsSortedFunc(k.Rules, func(a, b Rule) int { return strings.Compare(a.ID, b.ID) }) {
		errs = append(errs, errors.New("rules aren't sorted by id"))
	}
	if len(k.Rules) == 0 {
		errs = append(errs, errors.New("no rules"))
	}
	clear(seen)
	for i := range k.Rules {
		r := &k.Rules[i]
		name := "rule " + nameOf(r.ID, i)
		for _, err := range k.validateRule(r, true) {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
		if seen[r.ID] {
			errs = append(errs, fmt.Errorf("%s: duplicate id", name))
		}
		seen[r.ID] = true
	}
	return errors.Join(errs...)
}

func (s *Source) validate() []error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if err := printable(map[string]string{"id": s.ID, "url": s.URL, "mirror": s.Mirror, "title": s.Title, "doc": s.Doc,
		"edition": s.Edition, "licence": s.Licence, "quote": s.Quote, "locator": s.Locator}); err != nil {
		errs = append(errs, err)
	}
	if !idRe.MatchString(s.ID) {
		bad("id %q isn't lower-case words joined by . or -", s.ID)
	}
	if !strings.HasPrefix(s.URL, "https://") {
		bad("url %q isn't https", s.URL)
	}
	if s.Mirror != "" && !strings.HasPrefix(s.Mirror, "https://") {
		bad("mirror %q isn't https", s.Mirror)
	}
	if !isDate(s.Retrieved, day) {
		bad("retrieved %q isn't a YYYY-MM-DD date", s.Retrieved)
	}
	if s.Published != "" && !isDate(s.Published, day, month, year) {
		bad("published %q isn't a YYYY-MM-DD, YYYY-MM or YYYY date", s.Published)
	}
	if strings.TrimSpace(s.Licence) == "" {
		bad("no licence: a quote stays under its source's licence")
	}
	if !known(Confidences, s.Confidence) {
		bad("confidence %q isn't one of %s", s.Confidence, names(Confidences))
	}
	switch {
	case s.LinkOnly && s.Quote != "":
		bad("link-only, so no quote")
	case s.LinkOnly && strings.TrimSpace(s.Locator) == "":
		bad("link-only needs a locator (page or section)")
	case !s.LinkOnly && strings.TrimSpace(s.Quote) == "":
		bad("no quote (or link_only with a locator)")
	}
	return errs
}

// validateRule checks one rule. strict is for tools/genkb: claims carry
// exactly {value, src}; at run time a newer claim may carry more.
func (k *KB) validateRule(r *Rule, strict bool) []error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	text := map[string]string{"id": r.ID, "check": r.Check, "title": r.Title, "detail": r.Detail}
	for i, a := range r.Actions {
		text[fmt.Sprintf("action %d distro", i+1)] = a.Distro
		text[fmt.Sprintf("action %d text", i+1)] = a.Text
		text[fmt.Sprintf("action %d risk", i+1)] = a.Risk
		text[fmt.Sprintf("action %d undo", i+1)] = a.Undo
		for j, c := range a.Commands {
			text[fmt.Sprintf("action %d command %d", i+1, j+1)] = c
		}
	}
	if err := printable(text); err != nil {
		errs = append(errs, err)
	}
	if !idRe.MatchString(r.ID) {
		bad("id %q isn't lower-case words joined by . or -", r.ID)
	}
	if r.Check == "" {
		bad("no check")
	}
	if !known(Categories, r.Category) {
		bad("category %q isn't one of %s", r.Category, names(Categories))
	}
	if !known(Severities, r.Severity) {
		bad("severity %q isn't one of %s", r.Severity, names(Severities))
	}
	if strings.TrimSpace(r.Title) == "" {
		bad("no title")
	}
	for _, c := range r.Match.PCIClass {
		if !classRe.MatchString(c) {
			bad("pci_class %q isn't 2, 4 or 6 lower-case hex digits", c)
		}
	}
	if len(r.Data) > 0 {
		if err := k.validateClaims(r.Data, strict); err != nil {
			bad("data: %v", err)
		}
	}
	for j, a := range r.Actions {
		if strings.TrimSpace(a.Text) == "" {
			bad("action %d has no text", j+1)
		}
	}
	if len(r.Src) == 0 {
		bad("no src: every rule cites the sources it rests on")
	}
	for _, id := range r.Src {
		if k.Source(id) == nil {
			bad("src %q isn't in sources", id)
		}
	}
	return errs
}

// validateClaims checks that every leaf of a rule's data is a claim list,
// [{value, src}], citing known sources: no value without its evidence.
func (k *KB) validateClaims(data json.RawMessage, strict bool) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return errors.New("isn't valid JSON")
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return errors.New("must be an object of claim lists")
	}
	return k.walkClaims("", obj, strict)
}

func (k *KB) walkClaims(path string, obj map[string]any, strict bool) error {
	for _, key := range slices.Sorted(maps.Keys(obj)) {
		at := strings.TrimPrefix(path+"."+key, ".")
		if err := printable(map[string]string{"data key " + strconv.Quote(key): key}); err != nil {
			return err // keys name things too: #7 keys firmware by file pattern
		}
		switch val := obj[key].(type) {
		case map[string]any:
			if err := k.walkClaims(at, val, strict); err != nil {
				return err
			}
		case []any:
			if len(val) == 0 {
				return fmt.Errorf("%s: empty claim list", at)
			}
			for _, c := range val {
				claim, ok := c.(map[string]any)
				src, okSrc := claim["src"].(string)
				_, okValue := claim["value"]
				if !ok || !okSrc || !okValue || strict && len(claim) != 2 {
					return fmt.Errorf("%s: a claim is {value, src}", at)
				}
				if k.Source(src) == nil {
					return fmt.Errorf("%s: src %q isn't in sources", at, src)
				}
				if err := printableValue(at, claim["value"]); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("%s: a value without a source; write [{value: …, src: …}]", at)
		}
	}
	return nil
}

//go:embed data/advisor-v1.json.gz
var embedded []byte

// Embedded returns the knowledge base built into this binary.
func Embedded() (*KB, error) {
	return Parse(embedded)
}
