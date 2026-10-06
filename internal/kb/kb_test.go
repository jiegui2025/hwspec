package kb

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func valid() *KB {
	return &KB{
		Format: Format, Version: "2026-10-06T12:00:00Z",
		Sources: []Source{
			{ID: "forum", URL: "https://forum.example/t/1", Retrieved: "2026-10-06", Licence: "CC-BY-NC-SA-3.0",
				Confidence: "community", LinkOnly: true, Locator: "post 3"},
			{ID: "kernel", URL: "https://docs.kernel.org/x.html", Retrieved: "2026-10-06", Licence: "GPL-2.0-only",
				Confidence: "upstream-doc", Quote: "words", Published: "2019-12"},
		},
		Rules: []Rule{{
			ID: "pci.no-driver", Check: "pci-without-driver", Category: "needs-attention", Severity: "warning",
			Title: "No driver", Match: Match{PCIClass: []string{"02", "0403"}},
			Data:    json.RawMessage(`{"alternative":[{"value":"amdgpu","src":"kernel"}],"slots":{"count":[{"value":2,"src":"forum"}]}}`),
			Actions: []Action{{Text: "Load it", Commands: []string{"modprobe -R x"}}},
			Src:     []string{"kernel"},
		}},
	}
}

func gz(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(data)
	zw.Close()
	return buf.Bytes()
}

func TestTheEmbeddedKnowledgeBaseLoadsWhole(t *testing.T) {
	k, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	if k.Format != Format || len(k.Rules) == 0 || len(k.Sources) == 0 || len(k.Skipped) != 0 {
		t.Errorf("format %d, %d rules, %d sources, skipped %q", k.Format, len(k.Rules), len(k.Sources), k.Skipped)
	}
	if err := k.Validate(); err != nil {
		t.Errorf("the embedded copy isn't strictly valid: %v", err)
	}
}

func TestEncodedRulesReadBackUnchangedAndIdentically(t *testing.T) {
	k := valid()
	a, err := Encode(k)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Encode(k)
	if !bytes.Equal(a, b) {
		t.Error("encoding the same rules twice gave different bytes")
	}
	got, err := Parse(a)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, k) {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, k)
	}
}

// Ranks: most severe and strongest first, every adjacent pair pinned, and
// a value this build doesn't know ranks last, never first.
func TestRanksPutUnknownValuesLast(t *testing.T) {
	for i := 1; i < len(Confidences); i++ {
		if Confidences[i-1].Rank() >= Confidences[i].Rank() {
			t.Errorf("%s doesn't rank above %s", Confidences[i-1], Confidences[i])
		}
	}
	order := []Confidence{"oem-doc", "upstream-doc", "measured", "community"}
	if !reflect.DeepEqual(Confidences, order) {
		t.Errorf("confidence order %v, want %v (ADR 0009)", Confidences, order)
	}
	if Confidence("rumour").Rank() <= Confidence("community").Rank() || Severity("blocker").Rank() <= Severity("info").Rank() ||
		Category("security").Rank() <= Category("maintenance").Rank() {
		t.Error("an unknown value doesn't rank last")
	}
	if Severity("critical").Rank() >= Severity("warning").Rank() || Severity("warning").Rank() >= Severity("info").Rank() {
		t.Error("severity order")
	}
}

// A finding is only as sure as the least sure source it used; a source
// the knowledge base doesn't have counts as the weakest of all.
func TestTheWeakestSourceSetsConfidence(t *testing.T) {
	k := valid()
	cases := []struct {
		ids  []string
		want Confidence
	}{
		{[]string{"kernel"}, "upstream-doc"},
		{[]string{"kernel", "forum"}, "community"},
		{[]string{"forum", "kernel"}, "community"},
		{[]string{"capture", "kernel"}, "measured"},
		{[]string{"kernel", "missing"}, "unknown"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := k.Weakest(c.ids); got != c.want {
			t.Errorf("Weakest(%v) = %q, want %q", c.ids, got, c.want)
		}
	}
	if s := k.Source("capture"); s == nil || s.Confidence != "measured" || k.Source("nope") != nil {
		t.Errorf("built-in capture source: %+v", s)
	}
}

// genkb builds only what every binary of this format can read fully;
// Validate names each problem, so an author fixes them all at once.
func TestValidateNamesEveryProblem(t *testing.T) {
	cases := []struct {
		name  string
		spoil func(k *KB)
		want  string
	}{
		{"format", func(k *KB) { k.Format = 2 }, "format 2"},
		{"version", func(k *KB) { k.Version = "2026-99-99" }, "isn't a UTC time"},
		{"rule id", func(k *KB) { k.Rules[0].ID = "PCI No Driver" }, "isn't lower-case"},
		{"duplicate rule", func(k *KB) { k.Rules = append(k.Rules, k.Rules[0]) }, `"pci.no-driver": duplicate id`},
		{"unsorted rules", func(k *KB) {
			r := k.Rules[0]
			r.ID = "a.first"
			k.Rules = append(k.Rules, r)
		}, "rules aren't sorted"},
		{"no rules", func(k *KB) { k.Rules = nil }, "no rules"},
		{"check", func(k *KB) { k.Rules[0].Check = "" }, "no check"},
		{"category", func(k *KB) { k.Rules[0].Category = "misc" }, `category "misc"`},
		{"severity", func(k *KB) { k.Rules[0].Severity = "high" }, `severity "high"`},
		{"title", func(k *KB) { k.Rules[0].Title = " " }, "no title"},
		{"escape", func(k *KB) { k.Rules[0].Title = "No\x1b[31m driver" }, "title has control or formatting characters"},
		{"bidi", func(k *KB) { k.Sources[1].Quote = "abc\u202edef" }, "quote has control or formatting characters"},
		{"command", func(k *KB) { k.Rules[0].Actions[0].Commands[0] = "rm\r-rf" }, "command 1 has control"},
		{"class", func(k *KB) { k.Rules[0].Match.PCIClass = []string{"0C03"} }, `pci_class "0C03"`},
		{"action", func(k *KB) { k.Rules[0].Actions = []Action{{Commands: []string{"x"}}} }, "action 1 has no text"},
		{"uncited rule", func(k *KB) { k.Rules[0].Src = nil }, "no src"},
		{"unknown src", func(k *KB) { k.Rules[0].Src = []string{"blog"} }, `src "blog" isn't in sources`},
		{"data JSON", func(k *KB) { k.Rules[0].Data = json.RawMessage(`{`) }, "data: isn't valid JSON"},
		{"data list", func(k *KB) { k.Rules[0].Data = json.RawMessage(`[1]`) }, "must be an object"},
		{"bare value", func(k *KB) { k.Rules[0].Data = json.RawMessage(`{"max":64}`) }, "max: a value without a source"},
		{"empty claims", func(k *KB) { k.Rules[0].Data = json.RawMessage(`{"max":[]}`) }, "max: empty claim list"},
		{"claim shape", func(k *KB) { k.Rules[0].Data = json.RawMessage(`{"a":{"max":[{"value":64}]}}`) }, "a.max: a claim is {value, src}"},
		{"claim extra", func(k *KB) { k.Rules[0].Data = json.RawMessage(`{"max":[{"value":64,"src":"kernel","note":"x"}]}`) }, "max: a claim is {value, src}"},
		{"claim src", func(k *KB) { k.Rules[0].Data = json.RawMessage(`{"max":[{"value":64,"src":"hp"}]}`) }, `max: src "hp" isn't in sources`},
		{"source id", func(k *KB) { k.Sources[1].ID = "Kernel Docs" }, `source "Kernel Docs": id`},
		{"duplicate source", func(k *KB) { k.Sources = append(k.Sources, k.Sources[1]) }, `source "kernel": duplicate id`},
		{"unsorted sources", func(k *KB) { k.Sources[0], k.Sources[1] = k.Sources[1], k.Sources[0] }, "sources aren't sorted"},
		{"url", func(k *KB) { k.Sources[1].URL = "http://x" }, "isn't https"},
		{"mirror", func(k *KB) { k.Sources[1].Mirror = "ftp://x" }, "mirror"},
		{"retrieved", func(k *KB) { k.Sources[1].Retrieved = "0000-00-00" }, `retrieved "0000-00-00"`},
		{"published", func(k *KB) { k.Sources[1].Published = "2019-13" }, `published "2019-13"`},
		{"licence", func(k *KB) { k.Sources[1].Licence = "  " }, "no licence"},
		{"confidence", func(k *KB) { k.Sources[1].Confidence = "sure" }, `confidence "sure"`},
		{"quote", func(k *KB) { k.Sources[1].Quote = "" }, "no quote"},
		{"link-only quote", func(k *KB) { k.Sources[0].Quote = "copied" }, "link-only, so no quote"},
		{"locator", func(k *KB) { k.Sources[0].Locator = "" }, "needs a locator"},
	}
	for _, c := range cases {
		k := valid()
		if err := k.Validate(); err != nil {
			t.Fatalf("valid knowledge base refused: %v", err)
		}
		c.spoil(k)
		err := k.Validate()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want it to mention %q", c.name, err, c.want)
		}
	}
	k := &KB{Format: Format, Version: "2026-10-06T12:00:00Z", Sources: []Source{{}}, Rules: []Rule{{}}}
	if err := k.Validate(); err == nil || !strings.Contains(err.Error(), "rule 1:") || !strings.Contains(err.Error(), "source 1:") {
		t.Errorf("unnamed: %v", err)
	}
}

// A newer knowledge base of the same format reaches older binaries: a new
// field or value costs one rule (or source, and the rules citing it), with
// the reason, never the whole knowledge base or a rule applied in part.
func TestParseSkipsWhatItCantFullyUnderstand(t *testing.T) {
	file := `{"format":1,"version":"2026-10-06T12:00:00Z","firmware":{"hp":{}},
	"sources":[
	 {"id":"kernel","url":"https://docs.kernel.org/x.html","retrieved":"2026-10-06","licence":"GPL-2.0-only","confidence":"upstream-doc","quote":"words","archived_url":"https://web.archive.org/x"},
	 {"id":"rumour","url":"https://x.example","retrieved":"2026-10-06","licence":"x","confidence":"hearsay","quote":"q"},
	 {"id":"bidi","url":"https://x.example","retrieved":"2026-10-06","licence":"x","confidence":"community","quote":"a\u202eb"}],
	"rules":[
	 {"id":"a.ok","check":"c","category":"needs-attention","severity":"info","title":"t","src":["kernel"],
	  "actions":[{"text":"do","video":"https://x.example"}],
	  "data":{"max":[{"value":1,"src":"kernel","note":"newer claims carry more"}]}},
	 {"id":"b.kernel-range","check":"c","category":"needs-attention","severity":"info","title":"t","src":["kernel"],"match":{"kernel_below":"6.19"}},
	 {"id":"c.security","check":"c","category":"security","severity":"info","title":"t","src":["kernel"]},
	 {"id":"d.blocker","check":"c","category":"needs-attention","severity":"blocker","title":"t","src":["kernel"]},
	 {"id":"e.rumour","check":"c","category":"needs-attention","severity":"info","title":"t","src":["rumour"]},
	 {"id":"f.new-field","check":"c","category":"needs-attention","severity":"info","title":"t","src":["kernel"],"risk_level":2},
	 {"id":"a.ok","check":"c","category":"needs-attention","severity":"info","title":"twice","src":["kernel"]},
	 "not a rule"]}`
	k, err := Parse(gz(t, []byte(file)))
	if err != nil {
		t.Fatal(err)
	}
	if len(k.Rules) != 1 || k.Rules[0].ID != "a.ok" || k.Rules[0].Title != "t" || len(k.Rules[0].Actions) != 1 || len(k.Sources) != 1 {
		t.Fatalf("kept rules %+v, sources %+v", k.Rules, k.Sources)
	}
	skipped := strings.Join(k.Skipped, "\n")
	for _, want := range []string{
		`source "rumour" skipped: confidence "hearsay"`,
		`source "bidi" skipped: quote has control`,
		`rule "b.kernel-range" skipped: json: unknown field "kernel_below"`,
		`rule "c.security" skipped: category "security"`,
		`rule "d.blocker" skipped: severity "blocker"`,
		`rule "e.rumour" skipped: src "rumour" isn't in sources`,
		`rule "f.new-field" skipped: json: unknown field "risk_level"`,
		`rule "a.ok" skipped: duplicate id`,
		`rule 8 skipped`,
		`section "firmware" skipped: this build of hwspec doesn't use it`,
	} {
		if !strings.Contains(skipped, want) {
			t.Errorf("missing %q in\n%s", want, skipped)
		}
	}
}

// A knowledge base comes from a downloaded bundle: anything malformed,
// truncated, oversized or of another format is refused, not half-read.
func TestParseRefusesWhatItCantReadAtAll(t *testing.T) {
	good, _ := Encode(valid())
	flipped := bytes.Clone(good)
	flipped[len(flipped)-10] ^= 0xff
	cases := map[string]struct {
		data []byte
		want string
	}{
		"not gzip":      {[]byte("{}"), "knowledge base: unexpected EOF"},
		"no trailer":    {good[:len(good)-8], "unexpected EOF"},
		"flipped byte":  {flipped, "knowledge base:"},
		"not JSON":      {gz(t, []byte("rules:")), "invalid character"},
		"newer format":  {gz(t, []byte(`{"format":2,"rules":{"new":"shape"}}`)), "format 2, this build reads 1"},
		"no format":     {gz(t, []byte(`{"version":"2026-10-06T12:00:00Z"}`)), "format missing, this build reads 1"},
		"no version":    {gz(t, []byte(`{"format":1,"sources":[],"rules":[]}`)), `version "" isn't`},
		"invalid date":  {gz(t, []byte(`{"format":1,"version":"2026-02-30T12:00:00Z","sources":[],"rules":[]}`)), `version "2026-02-30T12:00:00Z"`},
		"a date only":   {gz(t, []byte(`{"format":1,"version":"2026-10-06","sources":[],"rules":[]}`)), `version "2026-10-06" isn't a UTC time`},
		"too large":     {gz(t, bytes.Repeat([]byte(" "), 4<<20+1)), "larger than 4194304 bytes"},
		"wrong section": {gz(t, []byte(`{"format":1,"version":"2026-10-06T12:00:00Z","rules":{}}`)), "cannot unmarshal"},
	}
	for name, c := range cases {
		if _, err := Parse(c.data); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}

// Every text field reaches advice (text, JSON or YAML), so every one is
// checked: control and formatting characters, and invalid UTF-8.
func TestEveryTextFieldMustBePrintable(t *testing.T) {
	spoil := map[string]func(k *KB, bad string){
		"source id":   func(k *KB, bad string) { k.Sources[1].ID = "kernel" + bad },
		"url":         func(k *KB, bad string) { k.Sources[1].URL = "https://x" + bad },
		"mirror":      func(k *KB, bad string) { k.Sources[1].Mirror = "https://x" + bad },
		"title":       func(k *KB, bad string) { k.Sources[1].Title = "t" + bad },
		"doc":         func(k *KB, bad string) { k.Sources[1].Doc = "d" + bad },
		"edition":     func(k *KB, bad string) { k.Sources[1].Edition = "e" + bad },
		"licence":     func(k *KB, bad string) { k.Sources[1].Licence = "l" + bad },
		"quote":       func(k *KB, bad string) { k.Sources[1].Quote = "q" + bad },
		"locator":     func(k *KB, bad string) { k.Sources[0].Locator = "p" + bad },
		"rule id":     func(k *KB, bad string) { k.Rules[0].ID = "pci.x" + bad },
		"check":       func(k *KB, bad string) { k.Rules[0].Check = "c" + bad },
		"rule title":  func(k *KB, bad string) { k.Rules[0].Title = "t" + bad },
		"detail":      func(k *KB, bad string) { k.Rules[0].Detail = "d" + bad },
		"distro":      func(k *KB, bad string) { k.Rules[0].Actions[0].Distro = "arch" + bad },
		"action text": func(k *KB, bad string) { k.Rules[0].Actions[0].Text = "t" + bad },
		"command":     func(k *KB, bad string) { k.Rules[0].Actions[0].Commands[0] = "c" + bad },
		"risk":        func(k *KB, bad string) { k.Rules[0].Actions[0].Risk = "r" + bad },
		"undo":        func(k *KB, bad string) { k.Rules[0].Actions[0].Undo = "u" + bad },
		"claim value": func(k *KB, bad string) {
			js, _ := json.Marshal(map[string]any{"alt": []map[string]any{{"value": []string{"ok", "v" + bad}, "src": "kernel"}}})
			k.Rules[0].Data = js
		},
	}
	for name, f := range spoil {
		for _, bad := range []string{"\x1b[31m", "\u202e", "\r", "\xff"} {
			if name == "claim value" && bad == "\xff" {
				continue // JSON can't carry invalid UTF-8: encoding/json turns it into U+FFFD
			}
			k := valid()
			f(k, bad)
			if err := k.Validate(); err == nil || !strings.Contains(err.Error(), "control or formatting characters") {
				t.Errorf("%s with %q: %v", name, bad, err)
			}
		}
	}
}

// Parse keeps the first of two sources with one ID, sorts sources, and
// counts skipped rules apart from skipped sources and sections.
func TestParseKeepsSourcesInOrderAndCountsRules(t *testing.T) {
	file := `{"format":1,"version":"2026-10-06T12:00:00Z","extra":{},
	"sources":[
	 {"id":"z","url":"https://z.example","retrieved":"2026-10-06","licence":"x","confidence":"community","quote":"z"},
	 {"id":"a","url":"https://a.example","retrieved":"2026-10-06","licence":"x","confidence":"oem-doc","quote":"first"},
	 {"id":"a","url":"https://a.example","retrieved":"2026-10-06","licence":"x","confidence":"community","quote":"second"}],
	"rules":[
	 {"id":"r.ok","check":"c","category":"needs-attention","severity":"info","title":"t","src":["a","z"]},
	 {"id":"r.bad","check":"c","category":"chores","severity":"info","title":"t","src":["a"]}]}`
	k, err := Parse(gz(t, []byte(file)))
	if err != nil {
		t.Fatal(err)
	}
	if len(k.Sources) != 2 || k.Sources[0].ID != "a" || k.Sources[0].Quote != "first" || k.Sources[1].ID != "z" {
		t.Errorf("sources %+v", k.Sources)
	}
	if k.SkippedRules != 1 || len(k.Skipped) != 3 || k.Weakest([]string{"a"}) != "oem-doc" {
		t.Errorf("skipped rules %d, skipped %q", k.SkippedRules, k.Skipped)
	}
}

// Keys inside data name things too (#7 keys firmware by file pattern), so
// they're held to the same rule as values.
func TestDataKeysMustBePrintable(t *testing.T) {
	for _, data := range []string{
		`{"iwlwifi-\u202e*.ucode":{"arch":[{"value":"x","src":"kernel"}]}}`,
		`{"pkgs":{"a\u001b[31m":[{"value":"x","src":"kernel"}]}}`,
		`{"alt":[{"value":{"k\u001b":"v"},"src":"kernel"}]}`,
	} {
		k := valid()
		k.Rules[0].Data = json.RawMessage(data)
		if err := k.Validate(); err == nil || !strings.Contains(err.Error(), "control or formatting characters") {
			t.Errorf("%s: %v", data, err)
		}
	}
}

// A rule's match keys are named as the YAML writes them, and only the
// ones it sets.
func TestMatchKeysAreTheOnesSet(t *testing.T) {
	if got := (Match{}).Keys(); got != nil {
		t.Errorf("no match: %q", got)
	}
	if got := (Match{PCIClass: []string{"02"}}).Keys(); !slices.Equal(got, []string{"pci_class"}) {
		t.Errorf("pci_class: %q", got)
	}
}
