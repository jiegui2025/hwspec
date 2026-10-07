package advisor

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/jiegui2025/hwspec/internal/kb"
	"github.com/jiegui2025/hwspec/internal/report"
)

// The memory answers of #9 (#107): which slots are used, how the modules
// pair up across channels, their speeds, the largest total and what fits.
// Each answer comes from the capture and the model's knowledge-base entry
// (#25's memory group, #129), cites what it used, and says why when it
// can't be told: never a slot count inferred from the usable memory.

func init() {
	register("memory-upgrade", check{
		run:         memoryUpgrade,
		needs:       []string{"memory"},
		example:     memoryExample("memory.upgrade", "memory-upgrade", "info"),
		exampleData: memoryExampleData,
	})
	register("memory-below-minimum", check{
		run:   memoryBelowMinimum,
		needs: []string{"memory.modules[].speed_mts"},
		// The speeds matter only when the model's documents give a minimum.
		available: func(in *Input) bool {
			d, _ := modelMemory(in.KB, in.Report)
			return d == nil || len(d.SpeedMTs.Min) == 0 ||
				slices.ContainsFunc(in.Report.Memory.Modules, func(m report.MemoryModule) bool { return m.SpeedMTs > 0 })
		},
		example:     memoryExample("memory.below-minimum", "memory-below-minimum", "warning"),
		exampleData: memoryExampleData,
	})
}

// memoryData is a model's memory group as the memory checks read it,
// strictly: a leaf this build doesn't know (it may qualify a value, ADR
// 0009) makes the group unusable rather than read in part.
type memoryData struct {
	Slots            claims[int]           `json:"slots"`
	SlotMap          claims[[]slotChannel] `json:"slot_map"`
	Type             claims[string]        `json:"type"`
	ModuleFormFactor claims[string]        `json:"module_form_factor"`
	MaxTotalGB       claims[int]           `json:"max_total_gb"`
	SpeedMTs         struct {
		Max claims[int] `json:"max"`
		Min claims[int] `json:"min"`
		// Compliance is the speed of the modules the vendor qualified,
		// which isn't a minimum.
		Compliance claims[int] `json:"compliance"`
	} `json:"speed_mts"`
	SpeedSetBy  claims[string]   `json:"speed_set_by"`
	SpeedRule   claims[string]   `json:"speed_rule"`
	Population  claims[[]string] `json:"population"`
	Constraints claims[[]string] `json:"constraints"`
}

// slotChannel is a memory slot, by the locator the firmware gives it, and
// the channel it is on.
type slotChannel struct {
	Locator string `json:"locator"`
	Channel string `json:"channel"`
}

// The codes of the memory group's rules. A code not listed is one a newer
// knowledge base added: genkb refuses it, and a binary shows it as written
// without applying it.
const (
	oneChannelSingle = "one-channel-single-mode"
	equalDual        = "equal-capacity-dual-channel"
	unequalFlex      = "unequal-capacity-flex-mode"
	largerInChannelA = "larger-in-channel-a"
	setByProcessor   = "processor"
	slowestModule    = "slowest-module"
)

var (
	populationCodes = []string{oneChannelSingle, equalDual, unequalFlex, largerInChannelA}
	constraintWords = map[string]string{
		"unbuffered": "unbuffered", "non-ecc": "non-ECC", "no-x4-sdram": "no x4 chips",
		"1.2-volt": "1.2 V", "260-pin": "260-pin",
	}
)

// decodeMemory reads a memory group. strict (genkb) also refuses codes
// this build doesn't know.
func decodeMemory(raw json.RawMessage, strict bool) (*memoryData, error) {
	d, err := decodeData[memoryData](raw)
	if err != nil {
		return nil, err
	}
	var errs []error
	positive := func(name string, cs claims[int]) {
		for _, c := range cs {
			if c.Value <= 0 {
				errs = append(errs, fmt.Errorf("%s: %d isn't positive", name, c.Value))
			}
		}
	}
	positive("slots", d.Slots)
	positive("max_total_gb", d.MaxTotalGB)
	positive("speed_mts.max", d.SpeedMTs.Max)
	positive("speed_mts.min", d.SpeedMTs.Min)
	positive("speed_mts.compliance", d.SpeedMTs.Compliance)
	for _, c := range d.SlotMap {
		seen := map[string]bool{}
		for _, s := range c.Value {
			if s.Locator == "" || s.Channel == "" || seen[s.Locator] {
				errs = append(errs, fmt.Errorf("slot_map: %+v needs a locator, once, and a channel", s))
			}
			seen[s.Locator] = true
		}
	}
	if strict {
		codes := func(name string, known []string, cs claims[[]string]) {
			for _, c := range cs {
				for _, v := range c.Value {
					if !slices.Contains(known, v) {
						errs = append(errs, fmt.Errorf("%s: %q isn't one of %s", name, v, strings.Join(known, ", ")))
					}
				}
			}
		}
		codes("population", populationCodes, d.Population)
		codes("constraints", slices.Sorted(maps.Keys(constraintWords)), d.Constraints)
		codes("speed_set_by", []string{setByProcessor}, lists(d.SpeedSetBy))
		codes("speed_rule", []string{slowestModule}, lists(d.SpeedRule))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return &d, nil
}

// ValidateModel reports what the checks can't read in a model entry's
// groups they use (memory, storage_slots, vendor_firmware), so genkb refuses a misspelt
// leaf or code, or a release without its date, rather than a binary
// leaving the group out.
func ValidateModel(m *kb.Model, source func(string) *kb.Source) []error {
	var errs []error
	if raw, ok := m.Data["memory"]; ok {
		if _, err := decodeMemory(raw, true); err != nil {
			errs = append(errs, fmt.Errorf("data.memory: %w", err))
		}
	}
	if raw, ok := m.Data["storage_slots"]; ok {
		if _, err := decodeStorage(raw); err != nil {
			errs = append(errs, fmt.Errorf("data.storage_slots: %w", err))
		}
	}
	if raw, ok := m.Data["wlan_slot"]; ok {
		if _, err := decodeWLAN(raw); err != nil {
			errs = append(errs, fmt.Errorf("data.wlan_slot: %w", err))
		}
	}
	if raw, ok := m.Data["vendor_firmware"]; ok {
		if err := validateVendorFirmware(raw, source); err != nil {
			errs = append(errs, fmt.Errorf("data.vendor_firmware: %w", err))
		}
	}
	return errs
}

// modelMemory returns the memory group of the capture's model entry, or
// why there is none to use.
func modelMemory(k *kb.KB, r *report.Report) (*memoryData, string) {
	m := modelFor(k, r)
	if m == nil {
		return nil, "the knowledge base has no entry for this model"
	}
	raw, ok := m.Data["memory"]
	if !ok {
		return nil, "the knowledge base has no memory data for this model"
	}
	d, err := decodeMemory(raw, false)
	if err != nil {
		return nil, fmt.Sprintf("this build can't read the knowledge base's memory data for this model (update hwspec): %v", err)
	}
	return d, ""
}

// answerer collects one finding's answers, with the evidence and sources
// they used.
type answerer struct {
	k        *kb.KB
	list     []Answer
	claims   []AnswerClaim // the next answer's
	evidence []Evidence
	used     []string
}

func (b *answerer) add(topic string, known bool, text string) {
	b.list = append(b.list, Answer{Topic: topic, Known: known, Text: text, Claims: b.claims})
	b.claims = nil
}

// capture notes a value read from the capture, or one it doesn't have.
func (b *answerer) capture(e ...Evidence) {
	b.evidence = append(b.evidence, e...)
	b.cite(kb.Capture.ID)
}

// cite records a source used without a claim of its own in the answer (a
// slot map, the capture).
func (b *answerer) cite(src string) {
	if !slices.Contains(b.used, src) {
		b.used = append(b.used, src)
	}
}

// say writes claims as "64 GB per a (2019-12); 32 GB per b (2019-09)",
// newest document first and a value several sources give once, and
// records them for the next answer.
func say[V any](b *answerer, cs claims[V], format func(V) string) string {
	sorted := slices.Clone(cs)
	slices.SortStableFunc(sorted, func(x, y claim[V]) int { return b.k.Newer(x.Src, y.Src) })
	var values []string
	cites := map[string][]string{}
	for _, c := range sorted {
		v := format(c.Value)
		if _, seen := cites[v]; !seen {
			values = append(values, v)
		}
		cite, published := c.Src, ""
		if s := b.k.Source(c.Src); s != nil && s.Published != "" {
			published = s.Published
			cite += " (" + published + ")"
		}
		if !slices.Contains(cites[v], cite) {
			cites[v] = append(cites[v], cite)
		}
		b.claims = append(b.claims, AnswerClaim{Value: c.Value, Src: c.Src, Published: published})
		b.cite(c.Src)
	}
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = v + " per " + strings.Join(cites[v], " and ")
	}
	return strings.Join(parts, "; ")
}

// per writes the sources of claims whose value goes without saying (a
// rule's code): "per a (2019-09) and b", recording them like say.
func per[V any](b *answerer, cs claims[V]) string {
	return strings.TrimSpace(say(b, cs, func(V) string { return "" }))
}

// with returns the claims whose value holds the code.
func with(cs claims[[]string], code string) claims[[]string] {
	var out claims[[]string]
	for _, c := range cs {
		if slices.Contains(c.Value, code) {
			out = append(out, c)
		}
	}
	return out
}

// saying returns the claims whose value is v.
func saying[V comparable](cs claims[V], v V) claims[V] {
	var out claims[V]
	for _, c := range cs {
		if c.Value == v {
			out = append(out, c)
		}
	}
	return out
}

// unknownCodes returns the codes in claims that this build doesn't know.
func unknownCodes(known []string, cs claims[[]string]) []string {
	var out []string
	for _, c := range cs {
		for _, code := range c.Value {
			if !slices.Contains(known, code) && !slices.Contains(out, code) {
				out = append(out, code)
			}
		}
	}
	return out
}

// lists turns claims of one code into claims of a list of codes.
func lists(cs claims[string]) claims[[]string] {
	out := make(claims[[]string], len(cs))
	for i, c := range cs {
		out[i] = claim[[]string]{Value: []string{c.Value}, Src: c.Src}
	}
	return out
}

// cantApply says that an answer rests on rules this build can't read.
func cantApply(codes []string) string {
	return "the knowledge base has a rule this build can't apply (update hwspec): " + strings.Join(codes, ", ")
}

func memoryUpgrade(in *Input, _ *kb.Rule) ([]hit, error) {
	b := &answerer{k: in.KB}
	r := in.Report
	d, why := modelMemory(in.KB, r)
	b.slots(r, d)
	b.channels(r, d, why)
	b.pairs(d, why)
	b.speed(r, d)
	b.faster(d, why)
	b.minimum(&r.Memory, d, why)
	b.maxCapacity(&r.Memory, d, why)
	b.fits(&r.Memory, d, why)
	return []hit{{evidence: b.evidence, answers: b.list, used: b.used}}, nil
}

func ints(format string) func(int) string {
	return func(n int) string { return fmt.Sprintf(format, n) }
}

func asIs(s string) string { return s }

// noSlotTable says why a capture has no slot table: it wasn't read as
// root, or the firmware lists none.
func noSlotTable(r *report.Report, what string) string {
	if r.Privileged {
		return what + " can't be told: the firmware lists no memory slots"
	}
	return what + " needs --full (root reads the firmware's slot table)"
}

// slots answers which slots are used, from the firmware's slot table, and
// gives the documents' slot count where it differs or is all there is.
func (b *answerer) slots(r *report.Report, d *memoryData) {
	mem := &r.Memory
	var documented claims[int]
	if d != nil {
		documented = d.Slots
	}
	if mem.SlotUsage == nil {
		b.capture(Absent("memory.slot_usage"))
		text := noSlotTable(r, "Slot usage") + "; it is never inferred from the usable memory"
		if len(documented) > 0 {
			text += "; the model has " + say(b, documented, ints("%d slots"))
		}
		b.add("slots", false, text)
		return
	}
	var used, empty, unsaid, forms []string
	for i, s := range mem.SlotUsage {
		path := fmt.Sprintf("memory.slot_usage[%d].populated", i)
		switch {
		case s.Populated == nil:
			b.capture(Absent(path))
			unsaid = append(unsaid, s.Locator)
		case *s.Populated:
			b.capture(Present(path, true))
			used = append(used, s.Locator)
		default:
			b.capture(Present(path, false))
			empty = append(empty, s.Locator)
		}
		if f := s.FormFactor; f != "" && f != "Unknown" && f != "Other" && !slices.Contains(forms, f) {
			forms = append(forms, f)
		}
	}
	kind := "slots"
	if len(forms) == 1 {
		kind = forms[0] + " slots"
	}
	text := fmt.Sprintf("%d of %d %s used", len(used), len(mem.SlotUsage), kind)
	if len(used) > 0 {
		text += " (" + strings.Join(used, ", ") + ")"
	}
	if len(empty) > 0 {
		text += "; empty: " + strings.Join(empty, ", ")
	}
	if len(unsaid) > 0 {
		text += "; the firmware doesn't say for " + strings.Join(unsaid, ", ")
	}
	disagree := len(documented) != len(saying(documented, len(mem.SlotUsage)))
	if disagree {
		text += "; the documents say " + say(b, documented, ints("%d slots")) +
			": the firmware's table and the documents disagree, so which slots are free can't be told"
	}
	b.add("slots", len(unsaid) == 0 && !disagree, text)
}

// moduleIn returns the module in a slot, by locator, and its index.
func moduleIn(mem *report.Memory, locator string) (int, *report.MemoryModule) {
	for i := range mem.Modules {
		if mem.Modules[i].Locator == locator {
			return i, &mem.Modules[i]
		}
	}
	return -1, nil
}

// bankChannel is the channel a firmware bank locator names, e.g. "A" in
// "ChannelA" (the reference machine) or "P0 CHANNEL A"; "" if none.
var bankChannel = regexp.MustCompile(`(?i)\bchannel[ _-]?([a-z])\b`)

func mapText(m []slotChannel) string {
	parts := make([]string, len(m))
	for i, s := range m {
		parts[i] = s.Locator + " in channel " + s.Channel
	}
	return strings.Join(parts, ", ")
}

// channels answers how the modules pair up across channels: the model's
// slot map and population rules applied to the slots in use.
func (b *answerer) channels(r *report.Report, d *memoryData, why string) {
	mem := &r.Memory
	switch {
	case mem.SlotUsage == nil:
		b.add("channels", false, noSlotTable(r, "How the modules pair up across channels"))
		return
	case d == nil:
		b.add("channels", false, "Channels and population rules are unknown: "+why)
		return
	case len(d.SlotMap) == 0:
		b.add("channels", false, "Unknown: the knowledge base doesn't say which channel each slot is on")
		return
	}
	slotMap := d.SlotMap[0].Value
	for _, c := range d.SlotMap[1:] {
		if mapText(c.Value) != mapText(slotMap) {
			b.add("channels", false, "Unknown: the documents disagree on which channel each slot is on: "+say(b, d.SlotMap, mapText))
			return
		}
	}
	channelOf := map[string]string{}
	var all []string
	for _, s := range slotMap {
		channelOf[s.Locator] = s.Channel
		if !slices.Contains(all, s.Channel) {
			all = append(all, s.Channel)
		}
	}
	capacity := map[string]uint64{}
	slotsOf := map[string][]string{}
	for i, s := range mem.SlotUsage {
		ch, mapped := channelOf[s.Locator]
		if m := bankChannel.FindStringSubmatch(s.BankLocator); m != nil && mapped && !strings.EqualFold(m[1], ch) {
			b.capture(Present(fmt.Sprintf("memory.slot_usage[%d].bank_locator", i), s.BankLocator))
			b.add("channels", false, fmt.Sprintf("Unknown: the firmware puts %s in channel %s (bank %q), the documents in channel %s: %s",
				s.Locator, strings.ToUpper(m[1]), s.BankLocator, ch, say(b, d.SlotMap, mapText)))
			return
		}
		switch {
		case s.Populated == nil:
			b.add("channels", false, fmt.Sprintf("Unknown: the firmware doesn't say whether %s holds a module", s.Locator))
			return
		case !*s.Populated:
			continue
		case !mapped:
			b.add("channels", false, fmt.Sprintf("Unknown: slot %s isn't in the documents' slot map: %s", s.Locator, say(b, d.SlotMap, mapText)))
			return
		}
		i, m := moduleIn(mem, s.Locator)
		if m == nil || m.SizeBytes == 0 {
			b.add("channels", false, fmt.Sprintf("Unknown: the capture has no module size for slot %s", s.Locator))
			return
		}
		b.capture(Present(fmt.Sprintf("memory.modules[%d].size_bytes", i), m.SizeBytes))
		capacity[ch] += m.SizeBytes
		slotsOf[ch] = append(slotsOf[ch], s.Locator)
	}
	if codes := unknownCodes(populationCodes, d.Population); len(codes) > 0 {
		b.add("channels", false, "Unknown: "+cantApply(codes)+"; "+per(b, d.Population))
		return
	}
	used := slices.Sorted(maps.Keys(capacity))
	in := func(ch string) string {
		return fmt.Sprintf("%s (%s)", size(capacity[ch]), strings.Join(slotsOf[ch], ", "))
	}
	both := func() string {
		return fmt.Sprintf("%s in channel %s and %s in channel %s", in(used[0]), used[0], in(used[1]), used[1])
	}
	// Each case cites the rule it applies, and only then.
	rule := func(code string) (claims[[]string], bool) { cs := with(d.Population, code); return cs, len(cs) > 0 }
	var text string
	known := true
	switch {
	case len(used) == 0:
		text = "No module is in the slots the firmware lists"
	case len(used) == 1:
		text = fmt.Sprintf("Modules only in channel %s: %s", used[0], in(used[0]))
		cs, ok := rule(oneChannelSingle)
		if !ok {
			text += "; how this model runs that isn't in the knowledge base"
			known = false
			break
		}
		text = fmt.Sprintf("Single-channel: modules only in channel %s, %s, %s", used[0], in(used[0]), per(b, cs))
		free := slices.DeleteFunc(slices.Clone(all), func(ch string) bool { return ch == used[0] })
		if cs, ok := rule(equalDual); ok && len(free) > 0 {
			text += fmt.Sprintf("; a module of equal capacity in channel %s would make it dual-channel, %s", strings.Join(free, " or "), per(b, cs))
		}
	case len(used) == 2 && capacity[used[0]] == capacity[used[1]]:
		if cs, ok := rule(equalDual); ok {
			text = "Dual-channel: " + both() + ", " + per(b, cs)
		} else {
			text = "Equal capacities: " + both() + "; how this model runs that isn't in the knowledge base"
			known = false
		}
	case len(used) == 2:
		cs, ok := rule(unequalFlex)
		if !ok {
			text = "Unequal capacities: " + both() + "; no cited rule for this model says how they run"
			known = false
			break
		}
		text = "Flex mode: " + both() + "; the matched part runs dual-channel, the rest single-channel, " + per(b, cs)
		other := used[0]
		if other == "A" {
			other = used[1]
		}
		if a, ok := capacity["A"]; ok && capacity[other] > a {
			if cs, ok := rule(largerInChannelA); ok {
				text += "; the larger module belongs in channel A, " + per(b, cs)
			}
		}
	default:
		text = fmt.Sprintf("Modules in %d channels; the knowledge base's rules for this model cover two", len(used))
		known = false
	}
	if len(used) > 0 {
		text += "; slots: " + say(b, d.SlotMap, mapText)
	}
	b.add("channels", known, text)
}

// pairs answers whether modules must be matched pairs: the documents'
// rules alone, so it is answered with or without the slot table.
func (b *answerer) pairs(d *memoryData, why string) {
	if d == nil {
		b.add("pairs", false, "Whether matched pairs are needed is unknown: "+why)
		return
	}
	if codes := unknownCodes(populationCodes, d.Population); len(codes) > 0 {
		b.add("pairs", false, "Unknown: "+cantApply(codes)+"; "+per(b, d.Population))
		return
	}
	flex := with(d.Population, unequalFlex)
	if len(flex) == 0 {
		b.add("pairs", false, "Unknown: the knowledge base doesn't say whether this model needs matched pairs")
		return
	}
	text := "Matched pairs aren't needed: unequal capacities run in flex mode"
	cs := flex
	if equal := with(d.Population, equalDual); len(equal) > 0 {
		text += ", equal ones dual-channel"
		for _, c := range equal {
			if !slices.ContainsFunc(cs, func(o claim[[]string]) bool { return o.Src == c.Src }) {
				cs = append(cs, c)
			}
		}
	}
	b.add("pairs", true, text+", "+per(b, cs))
}

// speed answers how fast each module is rated and runs, against the
// platform's maximum.
func (b *answerer) speed(r *report.Report, d *memoryData) {
	mem := &r.Memory
	var each, named []string
	complete, read := true, false
	running := 0
	for i, m := range mem.Modules {
		rated, run := "rated speed unknown", "running speed unknown"
		if m.SpeedMTs > 0 {
			b.capture(Present(fmt.Sprintf("memory.modules[%d].speed_mts", i), m.SpeedMTs))
			rated, read = fmt.Sprintf("rated %d MT/s", m.SpeedMTs), true
		} else {
			complete = false
		}
		if m.ConfiguredMTs > 0 {
			b.capture(Present(fmt.Sprintf("memory.modules[%d].configured_speed_mts", i), m.ConfiguredMTs))
			run, read = fmt.Sprintf("running at %d MT/s", m.ConfiguredMTs), true
			if running == 0 || m.ConfiguredMTs < running {
				running = m.ConfiguredMTs
			}
		} else {
			complete = false
			if !r.Privileged {
				run += " (needs --full)"
			}
		}
		p := strings.TrimSpace(m.Type+" "+rated) + ", " + run
		if !slices.Contains(each, p) {
			each = append(each, p)
		}
		named = append(named, cmp.Or(m.Locator, "a module")+": "+p)
	}
	if !read {
		b.capture(Absent("memory.modules[0].speed_mts"))
		text := "Module speeds need --full or readable module EEPROMs (SPD)"
		if r.Privileged {
			text = "Module speeds are unknown: neither the firmware nor the module EEPROMs (SPD) give them"
		}
		b.add("speed", false, text)
		return
	}
	text := strings.Join(named, "; ")
	if len(each) == 1 {
		text = strings.ToUpper(each[0][:1]) + each[0][1:]
	}
	if d != nil && len(d.SpeedMTs.Max) > 0 && complete {
		var at, below, above bool
		for _, c := range d.SpeedMTs.Max {
			switch diff := running - c.Value; {
			case diff >= -1 && diff <= 1: // DDR4-2666 is 2666.67 MT/s: firmware rounds it either way
				at = true
			case diff < 0:
				below = true
			default:
				above = true
			}
		}
		documented := say(b, d.SpeedMTs.Max, ints("%d MT/s"))
		switch {
		case at && !below && !above:
			text += ", the platform maximum: " + documented
		case below && !at && !above:
			text += "; the platform maximum is " + documented
		default:
			text += "; the documented maximum is " + documented
		}
	}
	b.add("speed", complete, text)
}

// faster answers whether faster-rated modules work: the documents give
// how the speed is set and its maximum, not whether such modules are
// supported, so the answer says so.
func (b *answerer) faster(d *memoryData, why string) {
	if d == nil {
		b.add("faster", false, "Whether faster-rated modules work is unknown: "+why)
		return
	}
	if codes := append(unknownCodes([]string{setByProcessor}, lists(d.SpeedSetBy)), unknownCodes([]string{slowestModule}, lists(d.SpeedRule))...); len(codes) > 0 {
		b.add("faster", false, "Unknown: "+cantApply(codes))
		return
	}
	var parts []string
	if cs := saying(d.SpeedSetBy, setByProcessor); len(cs) > 0 {
		parts = append(parts, "the processor sets the memory speed, "+per(b, cs))
	}
	if len(d.SpeedMTs.Max) > 0 {
		parts = append(parts, "the speed is at most "+say(b, d.SpeedMTs.Max, ints("%d MT/s")))
	}
	if cs := saying(d.SpeedRule, slowestModule); len(cs) > 0 {
		parts = append(parts, "the slowest module sets the speed for all, "+per(b, cs))
	}
	text := "The documents don't say whether modules rated faster are supported"
	if len(parts) > 0 {
		text += "; they say " + strings.Join(parts, "; ")
	}
	b.add("faster", false, text)
}

// minimum answers the slowest supported speed, and which installed module
// is slower.
func (b *answerer) minimum(mem *report.Memory, d *memoryData, why string) {
	switch {
	case d == nil:
		b.add("minimum_speed", false, "The minimum speed is unknown: "+why)
		return
	case len(d.SpeedMTs.Min) == 0:
		text := "No cited minimum speed for this model"
		if len(d.SpeedMTs.Compliance) > 0 {
			text += "; the documented modules are rated " + say(b, d.SpeedMTs.Compliance, ints("%d MT/s")) + ", which isn't a minimum"
		}
		b.add("minimum_speed", false, text)
		return
	}
	text := "Minimum " + say(b, d.SpeedMTs.Min, ints("%d MT/s"))
	floor := 0
	for _, c := range d.SpeedMTs.Min {
		floor = max(floor, c.Value)
	}
	var slow []string
	for _, m := range mem.Modules {
		if m.SpeedMTs > 0 && m.SpeedMTs < floor {
			slow = append(slow, fmt.Sprintf("%s (%d MT/s)", cmp.Or(m.Locator, "a module"), m.SpeedMTs))
		}
	}
	if len(slow) > 0 {
		text += "; slower: " + strings.Join(slow, ", ")
	}
	b.add("minimum_speed", true, text)
}

// maxCapacity answers the largest total, with every document's claim and
// the firmware's own figure.
func (b *answerer) maxCapacity(mem *report.Memory, d *memoryData, why string) {
	var parts []string
	if d != nil && len(d.MaxTotalGB) > 0 {
		parts = append(parts, say(b, d.MaxTotalGB, ints("%d GB")))
	}
	if mem.MaxCapacityBytes > 0 {
		b.capture(Present("memory.max_capacity_bytes", mem.MaxCapacityBytes))
		parts = append(parts, size(mem.MaxCapacityBytes)+" per the firmware's memory array")
	}
	if len(parts) == 0 {
		b.add("max_capacity", false, "The largest total is unknown: "+cmp.Or(why, "the knowledge base has no maximum for this model")+
			", and the firmware's memory array needs --full")
		return
	}
	b.add("max_capacity", true, "Up to "+strings.Join(parts, "; "))
}

// fits answers which modules fit: type, form factor and constraints.
func (b *answerer) fits(mem *report.Memory, d *memoryData, why string) {
	var parts []string
	known := true
	if d != nil {
		if len(d.Type) > 0 {
			parts = append(parts, say(b, d.Type, asIs))
		}
		if len(d.ModuleFormFactor) > 0 {
			parts = append(parts, say(b, d.ModuleFormFactor, asIs))
		}
		if len(d.Constraints) > 0 {
			parts = append(parts, say(b, d.Constraints, func(codes []string) string {
				words := make([]string, len(codes))
				for i, c := range codes {
					words[i] = cmp.Or(constraintWords[c], c)
				}
				return strings.Join(words, ", ")
			}))
			if codes := unknownCodes(slices.Collect(maps.Keys(constraintWords)), d.Constraints); len(codes) > 0 {
				parts = append(parts, cantApply(codes))
				known = false
			}
		}
	}
	if len(parts) > 0 {
		b.add("fits", known, strings.Join(parts, "; "))
		return
	}
	why = cmp.Or(why, "the knowledge base doesn't say for this model")
	var installed []string
	for _, m := range mem.Modules {
		if p := strings.TrimSpace(m.Type + " " + m.FormFactor); p != "" && !slices.Contains(installed, p) {
			installed = append(installed, p)
		}
	}
	text := "What fits is unknown: " + why
	if len(installed) > 0 {
		text = "Installed: " + strings.Join(installed, ", ") + "; what else fits is unknown: " + why
	}
	b.add("fits", false, text)
}

// size writes a byte count in whole GiB, or MiB otherwise.
func size(n uint64) string {
	if n >= 1<<30 && n%(1<<30) == 0 {
		return fmt.Sprintf("%d GiB", n>>30)
	}
	return fmt.Sprintf("%d MiB", n>>20)
}

// memoryBelowMinimum warns about each module rated slower than a minimum
// the model's documents give, and shows the documents that say otherwise.
func memoryBelowMinimum(in *Input, _ *kb.Rule) ([]hit, error) {
	d, _ := modelMemory(in.KB, in.Report)
	if d == nil {
		return nil, nil
	}
	var hits []hit
	for i, m := range in.Report.Memory.Modules {
		var below, others claims[int]
		for _, c := range d.SpeedMTs.Min {
			if m.SpeedMTs > 0 && m.SpeedMTs < c.Value {
				below = append(below, c)
			} else {
				others = append(others, c)
			}
		}
		if len(below) == 0 {
			continue
		}
		b := &answerer{k: in.KB}
		b.capture(Present(fmt.Sprintf("memory.modules[%d].speed_mts", i), m.SpeedMTs))
		text := fmt.Sprintf("Rated %d MT/s, below the minimum %s", m.SpeedMTs, say(b, below, ints("%d MT/s")))
		if len(others) > 0 {
			text += "; other documents give " + say(b, others, ints("%d MT/s"))
		}
		b.add("minimum_speed", true, text)
		hits = append(hits, hit{
			device:   &DeviceRef{Kind: "memory", Key: cmp.Or(m.Locator, fmt.Sprintf("modules[%d]", i)), Name: strings.TrimSpace(size(m.SizeBytes) + " " + m.Type)},
			evidence: b.evidence, answers: b.list, used: b.used,
		})
	}
	return hits, nil
}

// memoryExample is the reference machine's model with one module rated
// below the minimum the example's model entry gives, in unequal channels.
func memoryExample(id, name string, severity kb.Severity) func() (kb.Rule, *report.Report) {
	return func() (kb.Rule, *report.Report) {
		yes := true
		return kb.Rule{ID: id, Check: name, Category: "upgrade", Severity: severity, Title: "Memory", Src: []string{kb.Capture.ID}},
			&report.Report{
				System: report.System{Identity: &report.Identity{Vendor: "HP", Model: "HP EliteDesk 800 G5 Desktop Mini"}},
				Memory: report.Memory{
					MaxCapacityBytes: 64 << 30,
					SlotUsage: []report.MemorySlot{
						{Locator: "DIMM1", BankLocator: "ChannelB", Populated: &yes, FormFactor: "SODIMM"},
						{Locator: "DIMM3", BankLocator: "ChannelA", Populated: &yes, FormFactor: "SODIMM"},
					},
					Modules: []report.MemoryModule{
						{Locator: "DIMM1", SizeBytes: 16 << 30, Type: "DDR4", FormFactor: "SODIMM", SpeedMTs: 2133, ConfiguredMTs: 2133},
						{Locator: "DIMM3", SizeBytes: 8 << 30, Type: "DDR4", FormFactor: "SODIMM", SpeedMTs: 3200, ConfiguredMTs: 2133},
					},
				},
			}
	}
}

func memoryExampleData() ([]kb.Source, []kb.Model) {
	return []kb.Source{{ID: "example-guide", URL: "https://example.com/guide.pdf", Published: "2019-09", Retrieved: "2026-10-06",
			Licence: "proprietary", Confidence: "oem-doc", LinkOnly: true, Locator: "p. 30"}},
		[]kb.Model{{ID: "hp.example", Match: kb.ModelMatch{SysVendor: "HP", ProductName: "HP EliteDesk 800 G5 Desktop Mini"},
			Data: map[string]json.RawMessage{"memory": json.RawMessage(`{
				"slot_map": [{"value": [{"locator": "DIMM1", "channel": "B"}, {"locator": "DIMM3", "channel": "A"}], "src": "example-guide"}],
				"population": [{"value": ["unequal-capacity-flex-mode", "larger-in-channel-a"], "src": "example-guide"}],
				"speed_mts": {"min": [{"value": 2400, "src": "example-guide"}]}}`)}}}
}
