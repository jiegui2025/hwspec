package kb

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The data sections (#25): per-model, per-device and per-CPU data, and
// vendor firmware allow-lists. An entry matches a capture by raw IDs and
// holds groups of claims, each leaf a claim list [{value, src}] like a
// rule's data: no value without its source.

// Entry is one entry of a data section: its ID, what it matches, and its
// data groups (a section's groups are listed in Sections).
type Entry[M any] struct {
	ID    string                     `json:"id"`
	Match M                          `json:"match"`
	Data  map[string]json.RawMessage `json:"data,omitempty"`
}

// The four sections' entries.
type (
	Model     = Entry[ModelMatch]
	Device    = Entry[DeviceMatch]
	CPU       = Entry[CPUMatch]
	Allowlist = Entry[AllowlistMatch]
)

// ModelMatch is a system model by its DMI names, as the kernel gives them
// (sys_vendor, product_name, board_name, product_sku).
type ModelMatch struct {
	SysVendor   string `json:"sys_vendor"`
	ProductName string `json:"product_name"`
	BoardName   string `json:"board_name,omitempty"`
	SKU         string `json:"sku,omitempty"`
}

// DeviceMatch is a PCI or USB device by its IDs: vendor:device, optionally
// with :subvendor:subdevice, in lower-case hex.
type DeviceMatch struct {
	Bus string `json:"bus"` // pci, usb
	ID  string `json:"id"`
}

// CPUMatch is a CPU model by its vendor and its processor number, as
// ProcessorNumber parses it from the CPUID brand string (ADR 0009,
// amended for #128): several SKUs share a CPUID family and model.
type CPUMatch struct {
	Vendor    string `json:"vendor"`    // intel
	Processor string `json:"processor"` // e.g. i5-9500T
}

// AllowlistMatch is a vendor firmware policy: the vendor, one or more of
// its product names, a product family or board names, and optionally the
// BIOS versions it applies to.
type AllowlistMatch struct {
	SysVendor   string   `json:"sys_vendor"`
	ProductName []string `json:"product_name,omitempty"`
	Family      string   `json:"family,omitempty"`
	BoardName   []string `json:"board_name,omitempty"`
	BIOSVersion *Range   `json:"bios_version,omitempty"`
}

// Range is a span of BIOS versions: From included, To excluded (the first
// version without the policy, e.g. from release notes that lifted it).
// Either may be empty.
type Range struct {
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// Sections lists each data section's groups, from #25; the data parts
// that fill them define their leaves. A group the section doesn't list
// is an unknown field.
var Sections = map[string][]string{
	"models": {"allowlist", "chipset", "cpu_support", "display_ports", "form_factor", "gpu_slot", "launch", "memory",
		"overclocking", "parts", "power", "rtc_battery", "storage_slots", "wlan_slot"},
	"devices":    {"display_outputs", "rated", "wifi"},
	"cpus":       {"launch", "memory_channels", "memory_max_gb", "memory_max_mts", "memory_types", "package", "socket", "tdp_w", "unlocked"},
	"allowlists": {"approved", "behaviour", "checks", "error_text", "restricted", "restricts", "soft"},
}

var (
	deviceIDRe  = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{4}(:[0-9a-f]{4}:[0-9a-f]{4})?$`)
	processorRe = regexp.MustCompile(`^i[3579]-[0-9]{4,5}[A-Z]{0,2}$`)
	// The Intel Core brand string's processor number: "Intel(R) Core(TM)
	// i5-9500T CPU @ 2.20GHz" (the reference machine). Other forms
	// (Core Ultra, Xeon, Pentium, AMD) match nothing until there is
	// evidence of how they read.
	brandRe = regexp.MustCompile(`^Intel\(R\) Core\(TM\) (i[3579]-[0-9]{4,5}[A-Z]{0,2}) CPU @ [0-9.]+GHz$`)
)

// ProcessorNumber returns the processor number in a CPUID brand string,
// or "" for a form there's no evidence for yet.
func ProcessorNumber(brand string) string {
	if m := brandRe.FindStringSubmatch(strings.Join(strings.Fields(brand), " ")); m != nil {
		return m[1]
	}
	return ""
}

// matchStrings refuses match values that would silently match nothing:
// control characters, surrounding whitespace (DMI values as the capture
// holds them are trimmed), and empty list items.
func matchStrings(fields map[string]string, lists map[string][]string) []error {
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(lists)) {
		for i, v := range lists[name] {
			if v == "" {
				errs = append(errs, fmt.Errorf("match.%s has an empty item", name))
			}
			fields[fmt.Sprintf("%s[%d]", name, i)] = v
		}
	}
	if err := printable(fields); err != nil {
		errs = append(errs, fmt.Errorf("match: %w", err))
	}
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		if v := fields[name]; strings.TrimSpace(v) != v {
			errs = append(errs, fmt.Errorf("match.%s %q has surrounding whitespace", name, v))
		}
	}
	return errs
}

// matcher is a section's match: its uniqueness key, and its problems.
type matcher interface {
	key() string
	problems() []error
}

func (m ModelMatch) key() string {
	return strings.Join([]string{m.SysVendor, m.ProductName, m.BoardName, m.SKU}, "\x00")
}

func (m ModelMatch) problems() []error {
	errs := matchStrings(map[string]string{"sys_vendor": m.SysVendor, "product_name": m.ProductName, "board_name": m.BoardName, "sku": m.SKU}, nil)
	if m.SysVendor == "" || m.ProductName == "" {
		errs = append(errs, errors.New("match needs sys_vendor and product_name"))
	}
	return errs
}

func (m DeviceMatch) key() string { return m.Bus + "\x00" + m.ID }

func (m DeviceMatch) problems() []error {
	var errs []error
	if m.Bus != "pci" && m.Bus != "usb" {
		errs = append(errs, fmt.Errorf("match.bus %q isn't pci or usb", m.Bus))
	}
	if !deviceIDRe.MatchString(m.ID) {
		errs = append(errs, fmt.Errorf("match.id %q isn't vendor:device[:subvendor:subdevice] in lower-case hex", m.ID))
	}
	return errs
}

func (m CPUMatch) key() string { return m.Vendor + "\x00" + m.Processor }

func (m CPUMatch) problems() []error {
	var errs []error
	if m.Vendor != "intel" {
		errs = append(errs, fmt.Errorf("match.vendor %q: only intel processor numbers can be matched yet", m.Vendor))
	}
	if !processorRe.MatchString(m.Processor) {
		errs = append(errs, fmt.Errorf("match.processor %q isn't a processor number such as i5-9500T", m.Processor))
	}
	return errs
}

// key is the same for the same policy however it's written: lists in
// any order or with repeats, range ends as their comparator reads them.
func (m AllowlistMatch) key() string {
	set := func(l []string) string { return strings.Join(slices.Compact(slices.Sorted(slices.Values(l))), "\x01") }
	end := func(v string) string {
		if b, err := parseBIOS(m.SysVendor, v); err == nil {
			return fmt.Sprint(b.family, b.numbers)
		}
		return v
	}
	r := Range{}
	if m.BIOSVersion != nil {
		r = *m.BIOSVersion
	}
	return strings.Join([]string{m.SysVendor, set(m.ProductName), m.Family, set(m.BoardName), end(r.From), end(r.To)}, "\x00")
}

func (m AllowlistMatch) problems() []error {
	errs := matchStrings(map[string]string{"sys_vendor": m.SysVendor, "family": m.Family},
		map[string][]string{"product_name": m.ProductName, "board_name": m.BoardName})
	if m.SysVendor == "" {
		errs = append(errs, errors.New("match needs sys_vendor"))
	}
	if len(m.ProductName) == 0 && m.Family == "" && len(m.BoardName) == 0 {
		errs = append(errs, errors.New("match needs product_name, family or board_name"))
	}
	if r := m.BIOSVersion; r != nil {
		if err := r.check(m.SysVendor); err != nil {
			errs = append(errs, fmt.Errorf("match.bios_version: %w", err))
		}
	}
	return errs
}

// check refuses a range whose ends the vendor's comparator can't read, or
// that holds no version.
func (r Range) check(vendor string) error {
	if r.From == "" && r.To == "" {
		return errors.New("needs from, to or both")
	}
	for _, v := range []string{r.From, r.To} {
		if v == "" {
			continue
		}
		if _, err := parseBIOS(vendor, v); err != nil {
			return err
		}
	}
	if r.From != "" && r.To != "" {
		if c, err := CompareBIOS(vendor, r.From, r.To); err != nil || c >= 0 {
			return fmt.Errorf("from %q isn't before to %q", r.From, r.To)
		}
	}
	return nil
}

// Contains says whether a BIOS version is in the range; err when the
// version can't be compared, which matches nothing.
func (r Range) Contains(vendor, version string) (bool, error) {
	if r.From != "" {
		if c, err := CompareBIOS(vendor, version, r.From); err != nil || c < 0 {
			return false, err
		}
	}
	if r.To != "" {
		if c, err := CompareBIOS(vendor, version, r.To); err != nil || c >= 0 {
			return false, err
		}
	}
	return true, nil
}

// biosVersion is a BIOS version as its vendor's comparator reads it: a
// firmware family, which versions must share to compare, and numbers.
type biosVersion struct {
	family  string
	numbers []int
}

// HP's BIOS versions name the firmware family and a three-part version:
// "R21 Ver. 02.27.00" (the reference machine's /sys/class/dmi/id/bios_version).
// Exactly three parts, so two versions always compare part by part: a
// shorter one ("2.27") isn't guessed to equal or precede "02.27.00".
var hpBIOS = regexp.MustCompile(`^([A-Z][0-9]{2}) Ver\. ([0-9]+\.[0-9]+\.[0-9]+)$`)

// parseBIOS reads a version with its vendor's comparator. Only formats
// there is evidence for are read; others are refused.
func parseBIOS(vendor, v string) (biosVersion, error) {
	switch vendor {
	case "HP":
		m := hpBIOS.FindStringSubmatch(v)
		if m == nil {
			return biosVersion{}, fmt.Errorf("%q isn't an HP BIOS version such as \"R21 Ver. 02.27.00\"", v)
		}
		out := biosVersion{family: m[1]}
		for f := range strings.SplitSeq(m[2], ".") {
			n, err := strconv.Atoi(f)
			if err != nil {
				return biosVersion{}, fmt.Errorf("%q: %w", v, err)
			}
			out.numbers = append(out.numbers, n)
		}
		return out, nil
	}
	return biosVersion{}, fmt.Errorf("no BIOS version comparator for vendor %q yet", vendor)
}

// CompareBIOS orders two BIOS versions of one vendor: negative when a is
// older. Versions of different firmware families don't compare.
func CompareBIOS(vendor, a, b string) (int, error) {
	x, err := parseBIOS(vendor, a)
	if err != nil {
		return 0, err
	}
	y, err := parseBIOS(vendor, b)
	if err != nil {
		return 0, err
	}
	if x.family != y.family {
		return 0, fmt.Errorf("%q and %q are different firmware families", a, b)
	}
	return slices.Compare(x.numbers, y.numbers), nil
}

// Claim is one source's value for a data leaf.
type Claim struct {
	Value json.RawMessage `json:"value"`
	Src   string          `json:"src"`
}

// Claims decodes a leaf's claims, newest document first (by the source's
// published date; undated ones last, in their written order). Conflicting
// claims are all kept: a reader sees each with its source, never a merge.
func (k *KB) Claims(leaf json.RawMessage) ([]Claim, error) {
	var cs []Claim
	if err := json.Unmarshal(leaf, &cs); err != nil {
		return nil, err
	}
	slices.SortStableFunc(cs, func(a, b Claim) int { return k.Newer(a.Src, b.Src) })
	return cs, nil
}

// Newer orders two sources' documents newest first, by their published
// dates, for sorting claims. A coarser date follows the dates within it
// ("2019-09" before "2019"): only a total order sorts consistently, and
// "2019" can't be placed among that year's months. Undated documents, and
// unknown sources, come last.
func (k *KB) Newer(a, b string) int {
	published := func(id string) string {
		if s := k.Source(id); s != nil {
			return s.Published
		}
		return ""
	}
	return strings.Compare(published(b), published(a))
}

// Confirmed says whether an entry rests only on OEM documents: an
// allow-list is "confirmed" then, and a community report otherwise. It is
// derived from the sources, so the two can't disagree (#25).
func (k *KB) Confirmed(data map[string]json.RawMessage) bool {
	n := 0
	ok := true
	for _, group := range data {
		var v any
		if json.Unmarshal(group, &v) != nil {
			return false
		}
		eachClaim(v, func(src string) {
			n++
			if s := k.Source(src); s == nil || s.Confidence != "oem-doc" {
				ok = false
			}
		})
	}
	return ok && n > 0
}

// eachClaim calls fn with the src of every claim in a decoded data tree.
func eachClaim(v any, fn func(src string)) {
	switch x := v.(type) {
	case map[string]any:
		for _, k := range slices.Sorted(maps.Keys(x)) {
			eachClaim(x[k], fn)
		}
	case []any:
		for _, c := range x {
			if claim, ok := c.(map[string]any); ok {
				src, _ := claim["src"].(string)
				fn(src)
			}
		}
	}
}

// decodeEntry decodes one entry strictly: an unknown field, in the entry
// or its match, could change what it matches.
func decodeEntry[M any](js json.RawMessage) (Entry[M], error) {
	var e Entry[M]
	dec := json.NewDecoder(bytes.NewReader(js))
	dec.DisallowUnknownFields()
	err := dec.Decode(&e)
	return e, err
}

// validateEntry checks an entry of the named section; strict is genkb's.
func validateEntry[M matcher](k *KB, section string, e *Entry[M], strict bool) []error {
	var errs []error
	if !idRe.MatchString(e.ID) {
		errs = append(errs, fmt.Errorf("id %q isn't lower-case words joined by . or -", e.ID))
	}
	errs = append(errs, e.Match.problems()...)
	if len(e.Data) == 0 {
		errs = append(errs, errors.New("no data"))
	}
	for _, g := range slices.Sorted(maps.Keys(e.Data)) {
		if !slices.Contains(Sections[section], g) {
			errs = append(errs, fmt.Errorf("unknown group %q (%s has: %s)", g, section, strings.Join(Sections[section], ", ")))
		}
	}
	if len(e.Data) > 0 {
		js, _ := json.Marshal(e.Data) // RawMessages that decoded encode again
		if err := k.validateClaims(js, strict); err != nil {
			errs = append(errs, fmt.Errorf("data: %w", err))
		}
	}
	return errs
}

// parseSection decodes a section leniently: an entry that doesn't decode
// or validate, or repeats another's ID or match, is left out and named in
// Skipped, never used in part.
func parseSection[M matcher](k *KB, section string, raw json.RawMessage) []Entry[M] {
	var out []Entry[M]
	if raw == nil {
		return out
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		k.Skipped = append(k.Skipped, fmt.Sprintf("section %s skipped: %v", strconv.Quote(section), err))
		return out
	}
	ids, keys := map[string]bool{}, map[string]bool{}
	for i, js := range list {
		e, err := decodeEntry[M](js)
		if err == nil {
			err = errors.Join(validateEntry(k, section, &e, false)...)
		}
		if err == nil && (ids[e.ID] || keys[e.Match.key()]) {
			err = errors.New("duplicate id or match")
		}
		if err != nil {
			k.Skipped = append(k.Skipped, fmt.Sprintf("%s entry %s skipped: %v", section, nameOf(e.ID, i), err))
			continue
		}
		ids[e.ID], keys[e.Match.key()] = true, true
		out = append(out, e)
	}
	return out
}

// validateSection checks a section strictly, for genkb: every problem of
// every entry, IDs sorted, and no two entries with one ID or match key.
func validateSection[M matcher](k *KB, section string, entries []Entry[M]) []error {
	var errs []error
	ids, keys := map[string]bool{}, map[string]string{}
	for i := range entries {
		e := &entries[i]
		name := section + " entry " + nameOf(e.ID, i)
		for _, err := range validateEntry(k, section, e, true) {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
		if ids[e.ID] {
			errs = append(errs, fmt.Errorf("%s: duplicate id", name))
		}
		if other, dup := keys[e.Match.key()]; dup {
			errs = append(errs, fmt.Errorf("%s: matches what %q matches", name, other))
		}
		ids[e.ID], keys[e.Match.key()] = true, e.ID
	}
	if !slices.IsSortedFunc(entries, func(a, b Entry[M]) int { return strings.Compare(a.ID, b.ID) }) {
		errs = append(errs, fmt.Errorf("%s aren't sorted by id", section))
	}
	if models, ok := any(entries).([]Model); ok {
		errs = append(errs, ambiguousModels(models)...)
	}
	return errs
}

// ambiguousModels refuses two model entries that could both match one
// machine with neither more specific than the other (one names the board,
// the other the SKU): which one's claims apply couldn't be told. A
// generic entry and a more specific one are fine: the specific one wins.
func ambiguousModels(models []Model) []error {
	var errs []error
	compatible := func(a, b string) bool { return a == "" || b == "" || a == b }
	refines := func(a, b ModelMatch) bool { // a names everything b does, the same way
		return (b.BoardName == "" || a.BoardName == b.BoardName) && (b.SKU == "" || a.SKU == b.SKU)
	}
	for i := range models {
		for j := i + 1; j < len(models); j++ {
			a, b := models[i].Match, models[j].Match
			if a.SysVendor != b.SysVendor || a.ProductName != b.ProductName ||
				!compatible(a.BoardName, b.BoardName) || !compatible(a.SKU, b.SKU) || refines(a, b) || refines(b, a) {
				continue
			}
			errs = append(errs, fmt.Errorf("models entries %q and %q can both match one machine, and neither is more specific", models[i].ID, models[j].ID))
		}
	}
	return errs
}
