package advisor

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jiegui2025/hwspec/internal/kb"
	"github.com/jiegui2025/hwspec/internal/report"
)

// referenceFull is the reference machine as root reads it (#107's
// evidence, dmidecode -t 17): two 16 GiB DDR4 SODIMMs in DIMM1 (channel
// B) and DIMM3 (channel A), rated 3200 MT/s, configured 2667 MT/s.
func referenceFull() *report.Report {
	yes := true
	return &report.Report{
		System: report.System{Identity: &report.Identity{Vendor: "HP", Model: "HP EliteDesk 800 G5 Desktop Mini"}},
		Board:  report.Board{Identity: &report.Identity{Model: "8595"}},
		Memory: report.Memory{
			Slots: 2,
			SlotUsage: []report.MemorySlot{
				{Locator: "DIMM1", BankLocator: "ChannelB", Populated: &yes, FormFactor: "SODIMM"},
				{Locator: "DIMM3", BankLocator: "ChannelA", Populated: &yes, FormFactor: "SODIMM"},
			},
			Modules: []report.MemoryModule{
				{Locator: "DIMM1", SizeBytes: 16 << 30, Type: "DDR4", FormFactor: "SODIMM", SpeedMTs: 3200, ConfiguredMTs: 2667},
				{Locator: "DIMM3", SizeBytes: 16 << 30, Type: "DDR4", FormFactor: "SODIMM", SpeedMTs: 3200, ConfiguredMTs: 2667},
			},
		},
	}
}

func embedded(t *testing.T) *kb.KB {
	t.Helper()
	k, err := kb.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// finding returns the advice's finding of a rule, failing without one.
func findingOf(t *testing.T, a Advice, id string) Finding {
	t.Helper()
	for _, f := range a.Findings {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("no %s finding in %+v (warnings %q)", id, a.Findings, a.Warnings)
	return Finding{}
}

// answers maps a finding's answers by topic.
func answers(f Finding) map[string]Answer {
	out := map[string]Answer{}
	for _, an := range f.Answers {
		out[an.Topic] = an
	}
	return out
}

func wantAnswer(t *testing.T, got map[string]Answer, topic string, known bool, parts ...string) {
	t.Helper()
	an, ok := got[topic]
	if !ok {
		t.Errorf("no %s answer", topic)
		return
	}
	if an.Known != known {
		t.Errorf("%s: known %v, want %v: %q", topic, an.Known, known, an.Text)
	}
	for _, p := range parts {
		if !strings.Contains(an.Text, p) {
			t.Errorf("%s: %q lacks %q", topic, an.Text, p)
		}
	}
}

// The reference machine with --full and HP's entry (#129): slots, channels
// and speeds from the capture, the rules and limits from HP's documents,
// both maxima with their dates.
func TestMemoryAnswersForTheReferenceMachine(t *testing.T) {
	k := embedded(t)
	a := Advise(Input{Report: referenceFull(), KB: k, Now: noon})
	f := findingOf(t, a, "memory.upgrade")
	got := answers(f)
	for topic, want := range map[string]string{
		"slots": "2 of 2 SODIMM slots used (DIMM1, DIMM3)",
		"channels": "Dual-channel: 16 GiB (DIMM3) in channel A and 16 GiB (DIMM1) in channel B, per hp-msg-2019-09-memory (2019-09); " +
			"slots: DIMM1 in channel B, DIMM3 in channel A per hp-msg-2019-09-memory (2019-09)",
		"pairs": "Matched pairs aren't needed: unequal capacities run in flex mode, equal ones dual-channel, per hp-msg-2019-09-memory (2019-09)",
		"speed": "DDR4 rated 3200 MT/s, running at 2667 MT/s, the platform maximum: 2666 MT/s per hp-ds-2019-12-memory (2019-12)",
		"faster": "The documents don't say whether modules rated faster are supported; they say the processor sets the memory speed, per hp-ds-2019-12-notes (2019-12); " +
			"the speed is at most 2666 MT/s per hp-ds-2019-12-memory (2019-12); the slowest module sets the speed for all, per hp-msg-2019-09-memory (2019-09)",
		"minimum_speed": "No cited minimum speed for this model; the documented modules are rated 2400 MT/s per hp-msg-2019-09-memory (2019-09), which isn't a minimum",
		"max_capacity":  "Up to 64 GB per hp-ds-2019-12-memory (2019-12); 32 GB per hp-msg-2019-09-memory (2019-09)",
		"fits": "DDR4 per hp-ds-2019-12-memory (2019-12) and hp-msg-2019-09-memory (2019-09); SODIMM per hp-ds-2019-12-memory (2019-12) and hp-msg-2019-09-memory (2019-09); " +
			"unbuffered, non-ECC, no x4 chips, 1.2 V, 260-pin per hp-msg-2019-09-memory (2019-09)",
	} {
		if got[topic].Text != want {
			t.Errorf("%s:\n got %q\nwant %q", topic, got[topic].Text, want)
		}
	}
	for topic, known := range map[string]bool{"slots": true, "channels": true, "pairs": true, "speed": true, "faster": false,
		"minimum_speed": false, "max_capacity": true, "fits": true} {
		if got[topic].Known != known {
			t.Errorf("%s: known %v", topic, got[topic].Known)
		}
	}
	if got["slots"].Claims != nil {
		t.Errorf("the documents agree with the capture, yet: %+v", got["slots"].Claims)
	}
	if c := got["channels"].Claims; len(c) != 2 || c[1].Src != "hp-msg-2019-09-memory" {
		t.Errorf("the channel answer doesn't cite its rule and its slot map: %+v", c)
	}
	if c := got["max_capacity"].Claims; len(c) != 2 || c[0].Value != 64 || c[0].Published != "2019-12" || c[1].Value != 32 {
		t.Errorf("max claims %+v", c)
	}
	var ids []string
	for _, s := range f.Sources {
		ids = append(ids, s.ID)
	}
	if want := []string{"capture", "hp-ds-2019-12-memory", "hp-ds-2019-12-notes", "hp-msg-2019-09-memory"}; !slices.Equal(ids, want) || f.Confidence != "measured" {
		t.Errorf("sources %q, confidence %s", ids, f.Confidence)
	}
	for _, f := range a.Findings {
		if f.ID == "memory.below-minimum" {
			t.Errorf("no minimum, yet %+v", f)
		}
	}
	var buf bytes.Buffer
	if err := WriteText(&buf, a); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"  Slots      2 of 2 SODIMM slots used (DIMM1, DIMM3)\n",
		"  Max total  Up to 64 GB per hp-ds-2019-12-memory (2019-12); 32 GB per hp-msg-2019-09-memory (2019-09)\n",
		"  Source     hp-msg-2019-09-memory  https://h10032.www1.hp.com/ctg/Manual/c06439994.pdf (oem-doc,",
	} {
		if !strings.Contains(buf.String(), line) {
			t.Errorf("text lacks %q:\n%s", line, buf.String())
		}
	}
}

// Without --full the slot usage is unknown, never inferred from the usable
// memory: the reference machine's own unprivileged capture. What rests on
// the documents alone is still answered.
func TestMemoryAnswersWithoutFull(t *testing.T) {
	js, err := os.ReadFile("../collect/testdata/machines/hp-elitedesk-800-g5-mini/expected.json")
	if err != nil {
		t.Fatal(err)
	}
	var r report.Report
	if err := json.Unmarshal(js, &r); err != nil {
		t.Fatal(err)
	}
	a := Advise(Input{Report: &r, KB: embedded(t), Now: noon})
	got := answers(findingOf(t, a, "memory.upgrade"))
	wantAnswer(t, got, "slots", false, "Slot usage needs --full", "never inferred from the usable memory", "the model has 2 slots per")
	wantAnswer(t, got, "channels", false, "needs --full")
	wantAnswer(t, got, "pairs", true, "Matched pairs aren't needed")
	wantAnswer(t, got, "speed", false, "Module speeds need --full or readable module EEPROMs (SPD)")
	wantAnswer(t, got, "max_capacity", true, "64 GB per", "32 GB per")
	if strings.Contains(got["slots"].Text, "1 of") || strings.Contains(got["slots"].Text, "used") {
		t.Errorf("a slot count without the slot table: %q", got["slots"].Text)
	}
	for _, w := range a.Warnings {
		if !strings.HasPrefix(w, "capture: ") && !strings.HasPrefix(w, "rule firmware.load-failed ") && w != firmwareIndexWarning(&Input{}) {
			t.Errorf("HP gives no minimum, so the speeds aren't needed: %q", w)
		}
	}
	// With a cited minimum, the rule can't be evaluated without them.
	a = Advise(Input{Report: &r, KB: memoryKB(`{"speed_mts": {"min": [{"value": 2400, "src": "guide"}]}}`, memoryRules()...), Now: noon})
	if !slices.Contains(a.Warnings, "rule memory.below-minimum can't evaluate this capture: it needs memory.modules[].speed_mts, which the capture doesn't have") {
		t.Errorf("warnings %q", a.Warnings)
	}
	// Root, and a firmware without a slot table: not "needs --full".
	r.Privileged = true
	got = answers(findingOf(t, Advise(Input{Report: &r, KB: embedded(t), Now: noon}), "memory.upgrade"))
	wantAnswer(t, got, "slots", false, "Slot usage can't be told: the firmware lists no memory slots")
	wantAnswer(t, got, "channels", false, "How the modules pair up across channels can't be told: the firmware lists no memory slots")
	wantAnswer(t, got, "speed", false, "Module speeds are unknown: neither the firmware nor the module EEPROMs (SPD) give them")
	r.Memory.Modules[0].SpeedMTs = 2400
	got = answers(findingOf(t, Advise(Input{Report: &r, KB: embedded(t), Now: noon}), "memory.upgrade"))
	wantAnswer(t, got, "speed", false, "DDR4 rated 2400 MT/s, running speed unknown")
	if strings.Contains(got["speed"].Text, "--full") {
		t.Errorf("speed: %q", got["speed"].Text)
	}
}

// memoryKB is the test sources plus one model entry with the given memory
// group, citing "guide" (2019-09) and "sheet" (2019-12).
func memoryKB(memory string, rules ...kb.Rule) *kb.KB {
	k := knowledge(rules...)
	k.Sources = append(k.Sources,
		kb.Source{ID: "guide", URL: "https://example.com/g", Published: "2019-09", Retrieved: "2026-10-06", Licence: "x", Confidence: "oem-doc", LinkOnly: true, Locator: "p. 1"},
		kb.Source{ID: "sheet", URL: "https://example.com/s", Published: "2019-12", Retrieved: "2026-10-06", Licence: "x", Confidence: "oem-doc", LinkOnly: true, Locator: "p. 2"},
	)
	slices.SortFunc(k.Sources, func(a, b kb.Source) int { return strings.Compare(a.ID, b.ID) })
	if memory != "" {
		k.Models = []kb.Model{{ID: "hp.mini", Match: kb.ModelMatch{SysVendor: "HP", ProductName: "HP EliteDesk 800 G5 Desktop Mini"},
			Data: map[string]json.RawMessage{"memory": json.RawMessage(memory)}}}
	}
	return k
}

func memoryRules() []kb.Rule {
	up, _ := checks["memory-upgrade"].example()
	low, _ := checks["memory-below-minimum"].example()
	return []kb.Rule{low, up}
}

const (
	slotMap  = `"slot_map": [{"value": [{"locator": "DIMM1", "channel": "B"}, {"locator": "DIMM3", "channel": "A"}], "src": "guide"}]`
	mapCited = "slots: DIMM1 in channel B, DIMM3 in channel A per guide (2019-09)"
)

func adviseMemory(t *testing.T, r *report.Report, memory string) (map[string]Answer, Advice) {
	t.Helper()
	a := Advise(Input{Report: r, KB: memoryKB(memory, memoryRules()...), Now: noon})
	return answers(findingOf(t, a, "memory.upgrade")), a
}

// A module rated below a cited minimum is flagged once, with the minimums
// it is below and the documents that give a lower one.
func TestModulesBelowTheMinimumAreFlagged(t *testing.T) {
	r := referenceFull()
	r.Memory.Modules[0].SpeedMTs = 2133
	got, a := adviseMemory(t, r, `{"speed_mts": {"min": [{"value": 2400, "src": "guide"}, {"value": 2133, "src": "sheet"}]}}`)
	f := findingOf(t, a, "memory.below-minimum")
	if f.Severity != "warning" || f.Device == nil || f.Device.Kind != "memory" || f.Device.Key != "DIMM1" || f.Device.Name != "16 GiB DDR4" {
		t.Errorf("finding %+v", f)
	}
	if len(f.Answers) != 1 || f.Answers[0].Text != "Rated 2133 MT/s, below the minimum 2400 MT/s per guide (2019-09); other documents give 2133 MT/s per sheet (2019-12)" ||
		len(f.Answers[0].Claims) != 2 || f.Answers[0].Claims[0].Src != "guide" {
		t.Errorf("answers %+v", f.Answers)
	}
	if len(f.Sources) != 3 || len(f.Evidence) != 1 || f.Evidence[0].Path() != "memory.modules[0].speed_mts" {
		t.Errorf("sources %+v, evidence %+v", f.Sources, f.Evidence)
	}
	n := 0
	for _, f := range a.Findings {
		if f.ID == "memory.below-minimum" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d below-minimum findings", n)
	}
	wantAnswer(t, got, "minimum_speed", true, "Minimum 2133 MT/s per sheet (2019-12); 2400 MT/s per guide (2019-09)", "slower: DIMM1 (2133 MT/s)")
	// Below every minimum: no other documents to show.
	_, a = adviseMemory(t, r, `{"speed_mts": {"min": [{"value": 2400, "src": "guide"}]}}`)
	if f := findingOf(t, a, "memory.below-minimum"); f.Answers[0].Text != "Rated 2133 MT/s, below the minimum 2400 MT/s per guide (2019-09)" {
		t.Errorf("answer %q", f.Answers[0].Text)
	}
	// A module whose rating the capture doesn't give isn't below anything.
	r.Memory.Modules[0].SpeedMTs = 0
	_, a = adviseMemory(t, r, `{"speed_mts": {"min": [{"value": 2400, "src": "guide"}]}}`)
	for _, f := range a.Findings {
		if f.ID == "memory.below-minimum" {
			t.Errorf("an unknown rating flagged: %+v", f)
		}
	}
	r.Memory.Modules[0].SpeedMTs = 2133
	// Modules without a locator still get a key.
	r.Memory.Modules[0].Locator = ""
	_, a = adviseMemory(t, r, `{"speed_mts": {"min": [{"value": 2400, "src": "guide"}]}}`)
	if f := findingOf(t, a, "memory.below-minimum"); f.Device.Key != "modules[0]" {
		t.Errorf("key %q", f.Device.Key)
	}
}

// How modules pair up across channels: each case of the population rules,
// with the rule and the slot map cited, or unknown with the reason.
func TestChannelAnswers(t *testing.T) {
	flex := `"population": [{"value": ["unequal-capacity-flex-mode", "larger-in-channel-a"], "src": "guide"}]`
	all := `"population": [{"value": ["one-channel-single-mode", "equal-capacity-dual-channel", "unequal-capacity-flex-mode", "larger-in-channel-a"], "src": "guide"}]`
	single := `"population": [{"value": ["one-channel-single-mode"], "src": "guide"}]`
	no := false
	oneModule := func(r *report.Report) { r.Memory.SlotUsage[0].Populated = &no; r.Memory.Modules = r.Memory.Modules[1:] }
	cases := []struct {
		name   string
		change func(r *report.Report)
		memory string
		known  bool
		want   string
	}{
		{"mixed capacities with the rule", func(r *report.Report) { r.Memory.Modules[0].SizeBytes = 32 << 30 }, "{" + slotMap + "," + flex + "}", true,
			"Flex mode: 16 GiB (DIMM3) in channel A and 32 GiB (DIMM1) in channel B; the matched part runs dual-channel, the rest single-channel, per guide (2019-09); " +
				"the larger module belongs in channel A, per guide (2019-09); " + mapCited},
		{"larger already in channel A", func(r *report.Report) { r.Memory.Modules[1].SizeBytes = 32 << 30 }, "{" + slotMap + "," + flex + "}", true,
			"Flex mode: 32 GiB (DIMM3) in channel A and 16 GiB (DIMM1) in channel B; the matched part runs dual-channel, the rest single-channel, per guide (2019-09); " + mapCited},
		{"mixed capacities without a rule", func(r *report.Report) { r.Memory.Modules[0].SizeBytes = 8 << 30 }, "{" + slotMap + "}", false,
			"Unequal capacities: 16 GiB (DIMM3) in channel A and 8 GiB (DIMM1) in channel B; no cited rule for this model says how they run; " + mapCited},
		{"equal without a rule", func(r *report.Report) {}, "{" + slotMap + "," + single + "}", false,
			"Equal capacities: 16 GiB (DIMM3) in channel A and 16 GiB (DIMM1) in channel B; how this model runs that isn't in the knowledge base; " + mapCited},
		{"one channel, with the rule for adding a module", oneModule, "{" + slotMap + "," + all + "}", true,
			"Single-channel: modules only in channel A, 16 GiB (DIMM3), per guide (2019-09); a module of equal capacity in channel B would make it dual-channel, per guide (2019-09); " + mapCited},
		{"one channel, no rule for adding one", oneModule, "{" + slotMap + "," + single + "}", true,
			"Single-channel: modules only in channel A, 16 GiB (DIMM3), per guide (2019-09); " + mapCited},
		{"one channel without the rule", oneModule, "{" + slotMap + "}", false,
			"Modules only in channel A: 16 GiB (DIMM3); how this model runs that isn't in the knowledge base; " + mapCited},
		{"one channel on the whole board", func(r *report.Report) {
			oneModule(r)
			r.Memory.SlotUsage[0].BankLocator, r.Memory.SlotUsage[1].BankLocator = "", ""
		}, `{"slot_map": [{"value": [{"locator": "DIMM1", "channel": "A"}, {"locator": "DIMM3", "channel": "A"}], "src": "guide"}],` + all + "}", true,
			"Single-channel: modules only in channel A, 16 GiB (DIMM3), per guide (2019-09); slots: DIMM1 in channel A, DIMM3 in channel A per guide (2019-09)"},
		{"no modules", func(r *report.Report) {
			r.Memory.SlotUsage[0].Populated, r.Memory.SlotUsage[1].Populated, r.Memory.Modules = &no, &no, nil
		}, "{" + slotMap + "}", true, "No module is in the slots the firmware lists"},
		// Review of #200, 1 and 2: a slot the firmware says nothing about
		// may hold a module.
		{"a slot's state unknown", func(r *report.Report) { r.Memory.SlotUsage[0].Populated = nil }, "{" + slotMap + "," + all + "}", false,
			"Unknown: the firmware doesn't say whether DIMM1 holds a module"},
		{"every slot's state unknown", func(r *report.Report) {
			r.Memory.SlotUsage[0].Populated, r.Memory.SlotUsage[1].Populated, r.Memory.Modules = nil, nil, nil
		}, "{" + slotMap + "}", false, "Unknown: the firmware doesn't say whether DIMM1 holds a module"},
		{"three channels", func(r *report.Report) {
			yes := true
			r.Memory.SlotUsage = append(r.Memory.SlotUsage, report.MemorySlot{Locator: "DIMM5", Populated: &yes})
			r.Memory.Modules = append(r.Memory.Modules, report.MemoryModule{Locator: "DIMM5", SizeBytes: 16 << 30})
		}, `{"slot_map": [{"value": [{"locator": "DIMM1", "channel": "B"}, {"locator": "DIMM3", "channel": "A"}, {"locator": "DIMM5", "channel": "C"}], "src": "guide"}]}`, false,
			"Modules in 3 channels; the knowledge base's rules for this model cover two; slots: DIMM1 in channel B, DIMM3 in channel A, DIMM5 in channel C per guide (2019-09)"},
		{"a slot the map doesn't have", func(r *report.Report) { r.Memory.SlotUsage[0].Locator = "XMM1" }, "{" + slotMap + "}", false,
			"Unknown: slot XMM1 isn't in the documents' slot map: DIMM1 in channel B, DIMM3 in channel A per guide (2019-09)"},
		{"no module for a used slot", func(r *report.Report) { r.Memory.Modules = r.Memory.Modules[1:] }, "{" + slotMap + "}", false,
			"Unknown: the capture has no module size for slot DIMM1"},
		{"no slot map", func(r *report.Report) {}, `{"slots": [{"value": 2, "src": "guide"}]}`, false,
			"Unknown: the knowledge base doesn't say which channel each slot is on"},
		{"no model entry", func(r *report.Report) {}, "", false, "Channels and population rules are unknown: the knowledge base has no entry for this model"},
		// 4: documents that disagree on the map are both shown.
		{"slot maps disagree", func(r *report.Report) {}, "{" + slotMap + `, "population": [{"value": ["equal-capacity-dual-channel"], "src": "guide"}],` +
			`"slot_map": [{"value": [{"locator": "DIMM1", "channel": "B"}, {"locator": "DIMM3", "channel": "A"}], "src": "guide"}, {"value": [{"locator": "DIMM1", "channel": "A"}, {"locator": "DIMM3", "channel": "B"}], "src": "sheet"}]}`, false,
			"Unknown: the documents disagree on which channel each slot is on: DIMM1 in channel A, DIMM3 in channel B per sheet (2019-12); DIMM1 in channel B, DIMM3 in channel A per guide (2019-09)"},
		{"slot maps agree", func(r *report.Report) {}, `{"population": [{"value": ["equal-capacity-dual-channel"], "src": "guide"}],` +
			`"slot_map": [{"value": [{"locator": "DIMM1", "channel": "B"}, {"locator": "DIMM3", "channel": "A"}], "src": "guide"}, {"value": [{"locator": "DIMM1", "channel": "B"}, {"locator": "DIMM3", "channel": "A"}], "src": "sheet"}]}`, true,
			"Dual-channel: 16 GiB (DIMM3) in channel A and 16 GiB (DIMM1) in channel B, per guide (2019-09); slots: DIMM1 in channel B, DIMM3 in channel A per sheet (2019-12) and guide (2019-09)"},
		// 10: the firmware's own channel contradicts the documents' map.
		{"the firmware's bank disagrees", func(r *report.Report) {}, `{"slot_map": [{"value": [{"locator": "DIMM1", "channel": "A"}, {"locator": "DIMM3", "channel": "B"}], "src": "guide"}]}`, false,
			`Unknown: the firmware puts DIMM1 in channel B (bank "ChannelB"), the documents in channel A: DIMM1 in channel A, DIMM3 in channel B per guide (2019-09)`},
		{"a bank without a channel", func(r *report.Report) { r.Memory.SlotUsage[0].BankLocator = "BANK 0" }, "{" + slotMap + "," + all + "}", true,
			"Dual-channel: "},
		{"a bank in lower case", func(r *report.Report) { r.Memory.SlotUsage[0].BankLocator = "P0 channel b" }, "{" + slotMap + "," + all + "}", true,
			"Dual-channel: "},
		// 7: a rule this build doesn't know.
		{"an unknown population code", func(r *report.Report) {}, "{" + slotMap + `, "population": [{"value": ["equal-capacity-dual-channel", "rank-matched"], "src": "guide"}]}`, false,
			"Unknown: the knowledge base has a rule this build can't apply (update hwspec): rank-matched; per guide (2019-09)"},
	}
	for _, c := range cases {
		r := referenceFull()
		c.change(r)
		got, _ := adviseMemory(t, r, c.memory)
		t.Run(c.name, func(t *testing.T) {
			an := got["channels"]
			if an.Known != c.known || !strings.HasPrefix(an.Text, c.want) || !strings.HasSuffix(c.want, " ") && an.Text != c.want {
				t.Errorf("known %v, %q\nwant %v, %q", an.Known, an.Text, c.known, c.want)
			}
		})
	}
	// 14: an answer cites only what it rests on: the map, not the
	// population rule it didn't apply.
	got, _ := adviseMemory(t, referenceFull(), "{"+slotMap+","+single+"}")
	if c := got["channels"].Claims; len(c) != 1 || c[0].Src != "guide" || c[0].Value == nil {
		t.Errorf("claims %+v", c)
	}
	if _, ok := got["channels"].Claims[0].Value.([]slotChannel); !ok {
		t.Errorf("the claim isn't the slot map: %+v", got["channels"].Claims[0])
	}
}

// Whether matched pairs are needed rests on the documents alone (review
// of #200, 9): always answered.
func TestPairAnswers(t *testing.T) {
	for memory, want := range map[string]string{
		`{"population": [{"value": ["unequal-capacity-flex-mode", "equal-capacity-dual-channel"], "src": "guide"}]}`:                              "Matched pairs aren't needed: unequal capacities run in flex mode, equal ones dual-channel, per guide (2019-09)",
		`{"population": [{"value": ["unequal-capacity-flex-mode"], "src": "guide"}, {"value": ["equal-capacity-dual-channel"], "src": "sheet"}]}`: "Matched pairs aren't needed: unequal capacities run in flex mode, equal ones dual-channel, per sheet (2019-12) and guide (2019-09)",
		`{"population": [{"value": ["unequal-capacity-flex-mode"], "src": "guide"}]}`:                                                             "Matched pairs aren't needed: unequal capacities run in flex mode, per guide (2019-09)",
		`{"population": [{"value": ["one-channel-single-mode"], "src": "guide"}]}`:                                                                "Unknown: the knowledge base doesn't say whether this model needs matched pairs",
		`{"population": [{"value": ["unequal-capacity-flex-mode", "rank-matched"], "src": "guide"}]}`:                                             "Unknown: the knowledge base has a rule this build can't apply (update hwspec): rank-matched; per guide (2019-09)",
		"": "Whether matched pairs are needed is unknown: the knowledge base has no entry for this model",
	} {
		r := referenceFull()
		r.Memory.SlotUsage = nil // the slot table doesn't matter
		got, _ := adviseMemory(t, r, memory)
		if an := got["pairs"]; an.Text != want || an.Known != strings.HasPrefix(want, "Matched") {
			t.Errorf("%s:\n got %v %q\nwant %q", memory, an.Known, an.Text, want)
		}
	}
}

// Slots: the firmware's table, with what it doesn't say, and the
// documents' count where it differs.
func TestSlotAnswers(t *testing.T) {
	r := referenceFull()
	no := false
	r.Memory.SlotUsage = append(r.Memory.SlotUsage, report.MemorySlot{Locator: "DIMM2", Populated: &no, FormFactor: "DIMM"}, report.MemorySlot{Locator: "DIMM4"})
	got, a := adviseMemory(t, r, `{"slots": [{"value": 4, "src": "guide"}, {"value": 2, "src": "sheet"}]}`)
	wantAnswer(t, got, "slots", false, "2 of 4 slots used (DIMM1, DIMM3); empty: DIMM2; the firmware doesn't say for DIMM4; the documents say 2 slots per sheet (2019-12); 4 slots per guide (2019-09): "+
		"the firmware's table and the documents disagree, so which slots are free can't be told")
	f := findingOf(t, a, "memory.upgrade")
	if !slices.ContainsFunc(f.Evidence, func(e Evidence) bool { _, ok := e.Value(); return e.Path() == "memory.slot_usage[3].populated" && !ok }) {
		t.Errorf("evidence %+v", f.Evidence)
	}
	r.Memory.SlotUsage[2].FormFactor, r.Memory.SlotUsage[3].FormFactor = "Unknown", "Other"
	got, _ = adviseMemory(t, r, "")
	wantAnswer(t, got, "slots", false, "2 of 4 SODIMM slots used")
	// Review of #200, 6: every slot's state known, but the documents give
	// another count: the free slots can't be told.
	got, _ = adviseMemory(t, referenceFull(), `{"slots": [{"value": 4, "src": "guide"}]}`)
	wantAnswer(t, got, "slots", false, "2 of 2 SODIMM slots used (DIMM1, DIMM3); the documents say 4 slots per guide (2019-09): the firmware's table and the documents disagree")
	r.Memory.SlotUsage = []report.MemorySlot{}
	got, _ = adviseMemory(t, r, "")
	wantAnswer(t, got, "slots", true, "0 of 0 slots used")
}

// Speeds: per module when they differ, against the documented maximum
// only when every module's running speed is known.
func TestSpeedAnswers(t *testing.T) {
	maximum := `{"speed_mts": {"max": [{"value": 2666, "src": "sheet"}]}}`
	for _, c := range []struct {
		name   string
		change func(r *report.Report)
		memory string
		known  bool
		want   string
	}{
		{"below the maximum", func(r *report.Report) { r.Memory.Modules[1].ConfiguredMTs = 2400 }, maximum, true,
			"DIMM1: DDR4 rated 3200 MT/s, running at 2667 MT/s; DIMM3: DDR4 rated 3200 MT/s, running at 2400 MT/s; the platform maximum is 2666 MT/s per sheet (2019-12)"},
		{"above it", func(r *report.Report) {
			r.Memory.Modules[0].ConfiguredMTs, r.Memory.Modules[1].ConfiguredMTs = 3200, 3200
		}, maximum, true, "DDR4 rated 3200 MT/s, running at 3200 MT/s; the documented maximum is 2666 MT/s per sheet (2019-12)"},
		{"documents disagree", func(r *report.Report) {}, `{"speed_mts": {"max": [{"value": 2666, "src": "sheet"}, {"value": 2400, "src": "guide"}]}}`, true,
			"DDR4 rated 3200 MT/s, running at 2667 MT/s; the documented maximum is 2666 MT/s per sheet (2019-12); 2400 MT/s per guide (2019-09)"},
		{"no rated speed", func(r *report.Report) {
			r.Memory.Modules[0].SpeedMTs, r.Memory.Modules[1].SpeedMTs, r.Memory.Modules[1].Locator, r.Memory.Modules[1].Type = 0, 0, "", ""
		}, maximum, false, "DIMM1: DDR4 rated speed unknown, running at 2667 MT/s; a module: rated speed unknown, running at 2667 MT/s"},
		// Review of #200, 11: an unknown running speed is said, and the
		// summary doesn't hide a module without speeds.
		{"no configured speed", func(r *report.Report) { r.Memory.Modules[0].ConfiguredMTs, r.Memory.Modules[1].ConfiguredMTs = 0, 0 }, maximum, false,
			"DDR4 rated 3200 MT/s, running speed unknown (needs --full)"},
		{"one module without speeds", func(r *report.Report) { r.Memory.Modules[1].SpeedMTs, r.Memory.Modules[1].ConfiguredMTs = 0, 0 }, maximum, false,
			"DIMM1: DDR4 rated 3200 MT/s, running at 2667 MT/s; DIMM3: DDR4 rated speed unknown, running speed unknown (needs --full)"},
	} {
		r := referenceFull()
		c.change(r)
		got, _ := adviseMemory(t, r, c.memory)
		t.Run(c.name, func(t *testing.T) {
			if an := got["speed"]; an.Known != c.known || an.Text != c.want {
				t.Errorf("known %v, %q\nwant %v, %q", an.Known, an.Text, c.known, c.want)
			}
		})
	}
	r := referenceFull()
	r.Memory.Modules = nil
	got, a := adviseMemory(t, r, "")
	wantAnswer(t, got, "speed", false, "Module speeds need --full or readable module EEPROMs (SPD)")
	if !slices.ContainsFunc(findingOf(t, a, "memory.upgrade").Evidence, func(e Evidence) bool { return e.Path() == "memory.modules[0].speed_mts" }) {
		t.Error("no evidence of the missing speeds")
	}
}

// Faster-rated modules: the documents say how the speed is set, not
// whether such modules are supported (review of #200, 8).
func TestFasterModuleAnswers(t *testing.T) {
	for memory, want := range map[string]string{
		"": "Whether faster-rated modules work is unknown: the knowledge base has no entry for this model",
		`{"slots": [{"value": 2, "src": "guide"}]}`:                     "The documents don't say whether modules rated faster are supported",
		`{"speed_rule": [{"value": "slowest-module", "src": "guide"}]}`: "The documents don't say whether modules rated faster are supported; they say the slowest module sets the speed for all, per guide (2019-09)",
		`{"speed_set_by": [{"value": "board", "src": "guide"}]}`:        "Unknown: the knowledge base has a rule this build can't apply (update hwspec): board",
		`{"speed_rule": [{"value": "fastest-module", "src": "guide"}]}`: "Unknown: the knowledge base has a rule this build can't apply (update hwspec): fastest-module",
	} {
		got, _ := adviseMemory(t, referenceFull(), memory)
		if an := got["faster"]; an.Known || an.Text != want {
			t.Errorf("%s:\n got %v %q\nwant %q", memory, an.Known, an.Text, want)
		}
	}
}

// What a model entry without some data, or without an entry, answers.
func TestUnknownMemoryAnswers(t *testing.T) {
	r := referenceFull()
	r.Memory.MaxCapacityBytes = 0
	got, _ := adviseMemory(t, r, "")
	wantAnswer(t, got, "minimum_speed", false, "The minimum speed is unknown: the knowledge base has no entry for this model")
	wantAnswer(t, got, "max_capacity", false, "The largest total is unknown: the knowledge base has no entry for this model, and the firmware's memory array needs --full")
	wantAnswer(t, got, "fits", false, "Installed: DDR4 SODIMM; what else fits is unknown: the knowledge base has no entry for this model")

	got, _ = adviseMemory(t, r, `{"slots": [{"value": 2, "src": "guide"}]}`)
	wantAnswer(t, got, "minimum_speed", false, "No cited minimum speed for this model")
	if strings.Contains(got["minimum_speed"].Text, "documented modules") {
		t.Errorf("minimum: %q", got["minimum_speed"].Text)
	}
	wantAnswer(t, got, "max_capacity", false, "the knowledge base has no maximum for this model")
	wantAnswer(t, got, "fits", false, "the knowledge base doesn't say for this model")
	r.Memory.Modules = nil
	got, _ = adviseMemory(t, r, `{"slots": [{"value": 2, "src": "guide"}]}`)
	wantAnswer(t, got, "fits", false, "What fits is unknown: the knowledge base doesn't say for this model")

	// A model entry without memory data, and memory data this build can't
	// read (a leaf a newer knowledge base added): the reason, never part of
	// the data.
	k := memoryKB("", memoryRules()...)
	k.Models = []kb.Model{{ID: "hp.mini", Match: kb.ModelMatch{SysVendor: "HP", ProductName: "HP EliteDesk 800 G5 Desktop Mini"},
		Data: map[string]json.RawMessage{"chipset": json.RawMessage(`[{"value": "Q370", "src": "guide"}]`)}}}
	got = answers(findingOf(t, Advise(Input{Report: referenceFull(), KB: k, Now: noon}), "memory.upgrade"))
	wantAnswer(t, got, "fits", false, "the knowledge base has no memory data for this model")
	got, a := adviseMemory(t, referenceFull(), `{"speed_mts": {"min": [{"value": 2400, "src": "guide"}], "min_rank": [{"value": 2, "src": "guide"}]}}`)
	wantAnswer(t, got, "fits", false, `this build can't read the knowledge base's memory data for this model (update hwspec): json: unknown field "min_rank"`)
	for _, f := range a.Findings {
		if f.ID == "memory.below-minimum" {
			t.Errorf("unreadable data applied: %+v", f)
		}
	}
	// One source giving a value twice is cited once.
	got, _ = adviseMemory(t, referenceFull(), `{"type": [{"value": "DDR4", "src": "guide"}, {"value": "DDR4", "src": "guide"}]}`)
	wantAnswer(t, got, "fits", true)
	if got["fits"].Text != "DDR4 per guide (2019-09)" {
		t.Errorf("fits %q", got["fits"].Text)
	}
	// A constraint a newer knowledge base added: shown as written, and the
	// answer isn't complete.
	got, _ = adviseMemory(t, referenceFull(), `{"constraints": [{"value": ["unbuffered", "cl-16"], "src": "guide"}]}`)
	wantAnswer(t, got, "fits", false, "unbuffered, cl-16 per guide (2019-09); the knowledge base has a rule this build can't apply (update hwspec): cl-16")
	// Review of #200, 15: a claim's descriptive keys are read past (ADR
	// 0009); a value's own unknown keys, or a claim without a source, aren't.
	got, _ = adviseMemory(t, referenceFull(), `{"type": [{"value": "DDR4", "src": "guide", "note": "p. 3", "page": 3}]}`)
	wantAnswer(t, got, "fits", true, "DDR4 per guide (2019-09)")
	for memory, want := range map[string]string{
		`{"type": [{"value": "DDR4"}]}`:            "a claim is {value, src}",
		`{"type": [{"src": "guide"}]}`:             "a claim is {value, src}",
		`{"type": [{"value": 4, "src": "guide"}]}`: "cannot unmarshal number",
		`{"type": ["DDR4"]}`:                       "cannot unmarshal string",
		`{"slot_map": [{"value": [{"locator": "DIMM1", "channel": "B", "rank": 2}], "src": "guide"}]}`: `unknown field "rank"`,
	} {
		got, _ = adviseMemory(t, referenceFull(), memory)
		wantAnswer(t, got, "fits", false, "this build can't read the knowledge base's memory data", want)
	}
}

func TestMemoryDataIsCheckedStrictlyForGenkb(t *testing.T) {
	model := func(memory string) *kb.Model {
		m := memoryKB(memory).Models
		if m == nil {
			return &kb.Model{ID: "hp.mini"}
		}
		return &m[0]
	}
	if errs := ValidateModel(model(""), nil); errs != nil {
		t.Errorf("no memory group: %v", errs)
	}
	if errs := ValidateModel(model("{"+slotMap+"}"), nil); errs != nil {
		t.Errorf("a good memory group: %v", errs)
	}
	for memory, want := range map[string]string{
		`{"slots": [{"value": 0, "src": "guide"}]}`:                                                       "data.memory: slots: 0 isn't positive",
		`{"slot_map": [{"value": [{"locator": "A"}, {"locator": "A", "channel": "B"}], "src": "guide"}]}`: "slot_map: {Locator:A Channel:} needs a locator, once, and a channel",
		`{"population": [{"value": ["any-order"], "src": "guide"}]}`:                                      `population: "any-order" isn't one of one-channel-single-mode`,
		`{"constraints": [{"value": ["cl-16"], "src": "guide"}]}`:                                         `constraints: "cl-16" isn't one of 1.2-volt, 260-pin, no-x4-sdram, non-ecc, unbuffered`,
		`{"speed_set_by": [{"value": "board", "src": "guide"}]}`:                                          `speed_set_by: "board" isn't one of processor`,
		`{"speed_rule": [{"value": "fastest-module", "src": "guide"}]}`:                                   `speed_rule: "fastest-module" isn't one of slowest-module`,
		`{"speed_mts": {"max": [{"value": -1, "src": "guide"}]}}`:                                         "speed_mts.max: -1 isn't positive",
		`{"slots": "two"}`: "json: cannot unmarshal",
	} {
		errs := ValidateModel(model(memory), nil)
		if len(errs) != 1 || !strings.Contains(errs[0].Error(), want) {
			t.Errorf("%s: %v, want %q", memory, errs, want)
		}
	}
	// A binary reads a code it doesn't know, and shows it.
	if _, err := decodeMemory(json.RawMessage(`{"population": [{"value": ["any-order"], "src": "guide"}]}`), false); err != nil {
		t.Error(err)
	}
}

// An answer of a topic this build has no label for still prints.
func TestUnknownAnswerTopicsPrint(t *testing.T) {
	var buf bytes.Buffer
	a := Advice{Findings: []Finding{{ID: "x", Title: "t", Answers: []Answer{{Topic: "battery", Text: "fine"}}}}}
	if err := WriteText(&buf, a); err != nil || !strings.Contains(buf.String(), "  Answer     fine\n") {
		t.Errorf("%v %q", err, buf.String())
	}
}

func TestSizes(t *testing.T) {
	for n, want := range map[uint64]string{16 << 30: "16 GiB", 512 << 20: "512 MiB", 1<<30 + 1<<29: "1536 MiB"} {
		if got := size(n); got != want {
			t.Errorf("%d: %q", n, got)
		}
	}
}
