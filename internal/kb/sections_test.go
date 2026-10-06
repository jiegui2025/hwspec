package kb

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// withSections is valid() plus one entry in each data section, citing the
// reference model's HP documents (#25).
func withSections() *KB {
	k := valid()
	k.Sources = append(k.Sources,
		Source{ID: "hp-ds-2019-12", URL: "https://www8.hp.com/h20195/v2/GetDocument.aspx?docname=4AA7-5436EEAP", Doc: "4AA7-5436EEAP",
			Published: "2019-12", Retrieved: "2026-10-06", Licence: "HP: no reuse licence", Confidence: "oem-doc", LinkOnly: true, Locator: "Memory"},
		Source{ID: "hp-msg-2019-09", URL: "https://h10032.www1.hp.com/ctg/Manual/c06439994.pdf", Edition: "2",
			Published: "2019-09", Retrieved: "2026-10-06", Licence: "HP: no reuse licence", Confidence: "oem-doc", LinkOnly: true, Locator: "p. 30"},
	)
	slices.SortFunc(k.Sources, func(a, b Source) int { return strings.Compare(a.ID, b.ID) })
	data := func(js string) map[string]json.RawMessage {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(js), &m); err != nil {
			panic(err)
		}
		return m
	}
	k.Models = []Model{{ID: "hp.elitedesk-800-g5-mini",
		Match: ModelMatch{SysVendor: "HP", ProductName: "HP EliteDesk 800 G5 Desktop Mini", BoardName: "8595"},
		Data: data(`{"memory":{"max_total_gb":[{"value":32,"src":"hp-msg-2019-09"},{"value":64,"src":"hp-ds-2019-12"}]},
			"chipset":[{"value":"Q370","src":"hp-ds-2019-12"}]}`)}}
	k.Devices = []Device{{ID: "intel.uhd-630", Match: DeviceMatch{Bus: "pci", ID: "8086:3e92"},
		Data: data(`{"display_outputs":[{"value":[{"type":"dp","max_width":4096}],"src":"kernel"}]}`)}}
	k.CPUs = []CPU{{ID: "intel.i5-9500t", Match: CPUMatch{Vendor: "intel", Processor: "i5-9500T"},
		Data: data(`{"memory_max_gb":[{"value":128,"src":"kernel"}]}`)}}
	k.Allowlists = []Allowlist{{ID: "hp.example", Match: AllowlistMatch{SysVendor: "HP", Family: "103C_53307F HP EliteDesk",
		BIOSVersion: &Range{From: "R21 Ver. 02.00.00", To: "R21 Ver. 02.30.00"}},
		Data: data(`{"restricted":[{"value":false,"src":"hp-msg-2019-09"}]}`)}}
	return k
}

// Each section reads back exactly as written, and is strictly valid.
func TestSectionsRoundTrip(t *testing.T) {
	k := withSections()
	if err := k.Validate(); err != nil {
		t.Fatalf("the sample isn't valid: %v", err)
	}
	b, err := Encode(k)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(b)
	if err != nil || len(got.Skipped) != 0 {
		t.Fatalf("parse: %v, skipped %q", err, got.Skipped)
	}
	got.Skipped = k.Skipped
	if !reflect.DeepEqual(got, k) {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, k)
	}
}

// An older binary's reader (frozen before this change) skips the new
// sections, each with a warning, and still applies its rules.
func TestOlderReadersSkipTheSections(t *testing.T) {
	b, err := Encode(withSections())
	if err != nil {
		t.Fatal(err)
	}
	old, err := parseV1Rules(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"allowlists", "cpus", "devices", "models"} {
		if !slices.Contains(old.Skipped, `section "`+s+`" skipped: this build of hwspec doesn't use it`) {
			t.Errorf("%s not named in %q", s, old.Skipped)
		}
	}
	if len(old.Rules) != 1 || old.SkippedRules != 0 {
		t.Errorf("rules %d, skipped %d", len(old.Rules), old.SkippedRules)
	}
}

// Validate refuses, and Parse leaves out, an entry that doesn't fit.
func TestBadEntriesAreRefused(t *testing.T) {
	for want, change := range map[string]func(k *KB){
		`models entry "hp.elitedesk-800-g5-mini": unknown group "colour"`: func(k *KB) {
			k.Models[0].Data["colour"] = json.RawMessage(`[{"value":"black","src":"hp-ds-2019-12"}]`)
		},
		`data: memory.max_total_gb: src "nowhere" isn't in sources`: func(k *KB) {
			k.Models[0].Data["memory"] = json.RawMessage(`{"max_total_gb":[{"value":32,"src":"nowhere"}]}`)
		},
		`data: chipset: empty claim list`:                  func(k *KB) { k.Models[0].Data["chipset"] = json.RawMessage(`[]`) },
		`data: chipset: a value without a source`:          func(k *KB) { k.Models[0].Data["chipset"] = json.RawMessage(`"Q370"`) },
		`models entry "hp.elitedesk-800-g5-mini": no data`: func(k *KB) { k.Models[0].Data = nil },
		`match needs sys_vendor and product_name`:          func(k *KB) { k.Models[0].Match.ProductName = "" },
		`matches what "hp.elitedesk-800-g5-mini" matches`: func(k *KB) {
			m := k.Models[0]
			m.ID = "hp.z-copy"
			k.Models = append(k.Models, m)
		},
		`models entry "hp.elitedesk-800-g5-mini": duplicate id`: func(k *KB) {
			m := k.Models[0]
			m.Match.SKU = "7LL88UT#ABA"
			k.Models = append(k.Models, m)
		},
		"models aren't sorted by id": func(k *KB) {
			m := k.Models[0]
			m.ID, m.Match.SKU = "a.first", "x"
			k.Models = append(k.Models, m)
		},
		`id "HP" isn't lower-case`:                       func(k *KB) { k.Models[0].ID = "HP" },
		`match.bus "isa" isn't pci or usb`:               func(k *KB) { k.Devices[0].Match.Bus = "isa" },
		`match.id "8086:3E92" isn't vendor:device`:       func(k *KB) { k.Devices[0].Match.ID = "8086:3E92" },
		`match.vendor "amd": only intel`:                 func(k *KB) { k.CPUs[0].Match.Vendor = "amd" },
		`match.processor "Core i5" isn't a processor`:    func(k *KB) { k.CPUs[0].Match.Processor = "Core i5" },
		"match needs sys_vendor":                         func(k *KB) { k.Allowlists[0].Match.SysVendor = "" },
		"match needs product_name, family or board_name": func(k *KB) { k.Allowlists[0].Match.Family = "" },
		`isn't an HP BIOS version such as "R21 Ver. 02.27.00"`: func(k *KB) {
			k.Allowlists[0].Match.BIOSVersion.To = "2.30"
		},
		`from "R21 Ver. 02.00.00" isn't before to "R21 Ver. 02.00.00"`: func(k *KB) {
			k.Allowlists[0].Match.BIOSVersion.To = "R21 Ver. 02.00.00"
		},
		"bios_version: needs from, to or both": func(k *KB) { k.Allowlists[0].Match.BIOSVersion = &Range{} },
		`"R21 Ver. 02.27" isn't an HP BIOS version`: func(k *KB) {
			k.Allowlists[0].Match.BIOSVersion = &Range{From: "R21 Ver. 02.27", To: "R21 Ver. 02.27.00"}
		},
		`models entries "hp.elitedesk-800-g5-mini" and "hp.sku" can both match one machine, and neither is more specific`: func(k *KB) {
			m := k.Models[0]
			m.ID, m.Match.BoardName, m.Match.SKU = "hp.sku", "", "7LL88UT#ABA"
			k.Models = append(k.Models, m)
		},
		`match.sys_vendor " HP" has surrounding whitespace`: func(k *KB) { k.Models[0].Match.SysVendor = " HP" },
		"match: product_name has control or formatting characters": func(k *KB) {
			k.Models[0].Match.ProductName = "HP\x1b[31m"
		},
		"match.board_name has an empty item": func(k *KB) { k.Allowlists[0].Match.BoardName = []string{""} },
		"match: product_name[1] has control or formatting characters": func(k *KB) {
			k.Allowlists[0].Match.ProductName = []string{"A", "B\u202e"}
		},
		`allowlists entry "hp.z-same": matches what "hp.example" matches`: func(k *KB) {
			a := k.Allowlists[0]
			a.ID, a.Match.ProductName = "hp.z-same", []string{"B", "A", "B"}
			a.Match.BIOSVersion = &Range{From: "R21 Ver. 2.0.0", To: "R21 Ver. 2.30.0"}
			k.Allowlists[0].Match.ProductName = []string{"A", "B"}
			k.Allowlists = append(k.Allowlists, a)
		},
		`no BIOS version comparator for vendor "Dell Inc." yet`: func(k *KB) {
			k.Allowlists[0].Match.SysVendor = "Dell Inc."
		},
	} {
		k := withSections()
		change(k)
		if err := k.Validate(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", want, err)
		}
	}
}

// Parse leaves out an entry it can't fully read, names it, and keeps the
// others: a newer knowledge base never costs the whole section.
func TestParseSkipsBadEntriesOnly(t *testing.T) {
	k := withSections()
	m := k.Models[0]
	m.ID, m.Match.SKU = "hp.z-sku", "7LL88UT#ABA"
	m.Data = map[string]json.RawMessage{"colour": json.RawMessage(`[{"value":"black","src":"hp-ds-2019-12"}]`)}
	k.Models = append(k.Models, m)
	dup := k.Models[0]
	dup.ID = "hp.zz-same-match"
	k.Models = append(k.Models, dup)
	b, err := Encode(k)
	if err != nil {
		t.Fatal(err)
	}
	// A newer field in one entry, and a section of the wrong shape.
	var raw map[string]any
	js := gunzip(t, b)
	if err := json.Unmarshal(js, &raw); err != nil {
		t.Fatal(err)
	}
	raw["devices"].([]any)[0].(map[string]any)["vendor_note"] = "x"
	raw["cpus"] = map[string]any{"intel.i5-9500t": 1}
	js, _ = json.Marshal(raw)
	got, err := Parse(gz(t, js))
	if err != nil {
		t.Fatal(err)
	}
	w := strings.Join(got.Skipped, "\n")
	for _, want := range []string{
		`models entry "hp.z-sku" skipped: unknown group "colour"`,
		`models entry "hp.zz-same-match" skipped: duplicate id or match`,
		`devices entry "intel.uhd-630" skipped: json: unknown field "vendor_note"`,
		`section "cpus" skipped: json: cannot unmarshal object`,
	} {
		if !strings.Contains(w, want) {
			t.Errorf("missing %q in %q", want, got.Skipped)
		}
	}
	if len(got.Models) != 1 || got.Models[0].ID != "hp.elitedesk-800-g5-mini" || len(got.Devices) != 0 || len(got.CPUs) != 0 || len(got.Allowlists) != 1 {
		t.Errorf("models %d, devices %d, cpus %d, allowlists %d", len(got.Models), len(got.Devices), len(got.CPUs), len(got.Allowlists))
	}
}

// The processor number comes from the brand string forms there is
// evidence for; anything else matches nothing.
func TestProcessorNumber(t *testing.T) {
	for brand, want := range map[string]string{
		"Intel(R) Core(TM) i5-9500T CPU @ 2.20GHz":  "i5-9500T", // the reference machine
		"Intel(R) Core(TM)  i5-9500T CPU @ 2.20GHz": "i5-9500T", // spacing as some firmware pads it
		"Intel(R) Core(TM) i7-8700 CPU @ 3.20GHz":   "i7-8700",
		"Intel(R) Core(TM) i9-10900K CPU @ 3.70GHz": "i9-10900K",
		"AMD Ryzen 7 5800X 8-Core Processor":        "",
		"Intel(R) Xeon(R) CPU E5-2680 v4 @ 2.40GHz": "",
		"Intel(R) Core(TM) i5-9500T CPU":            "",
		"":                                          "",
	} {
		if got := ProcessorNumber(brand); got != want {
			t.Errorf("%q: %q, want %q", brand, got, want)
		}
	}
}

// HP BIOS versions compare within a firmware family; other vendors'
// formats aren't read until there is evidence of them.
func TestBIOSVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"R21 Ver. 02.27.00", "R21 Ver. 02.27.00", 0},
		{"R21 Ver. 02.09.00", "R21 Ver. 02.27.00", -1},
		{"R21 Ver. 02.27.01", "R21 Ver. 02.27.00", 1},
		{"R21 Ver. 2.27.0", "R21 Ver. 02.27.00", 0},
	} {
		if got, err := CompareBIOS("HP", c.a, c.b); err != nil || got != c.want {
			t.Errorf("%s vs %s: %d %v", c.a, c.b, got, err)
		}
	}
	for _, bad := range [][3]string{
		{"HP", "R21 Ver. 02.27.00", "Q21 Ver. 02.27.00"},
		{"HP", "R21 Ver. 02.27.00", "02.27.00"},
		{"HP", "R21 Ver. 2.30", "R21 Ver. 02.30.00"}, // a part missing: not guessed
		{"HP", "R21 Ver. 02.27.00", "R21 Ver. 02.27.00.1"},
		{"HP", "R21 Ver. 99999999999999999999", "R21 Ver. 1"},
		{"HP", "R21 Ver. 1", "R21 Ver. 99999999999999999999"},
		{"Dell Inc.", "1.2.3", "1.2.4"},
	} {
		if _, err := CompareBIOS(bad[0], bad[1], bad[2]); err == nil {
			t.Errorf("%q: compared %q and %q", bad[0], bad[1], bad[2])
		}
	}
	r := Range{From: "R21 Ver. 02.00.00", To: "R21 Ver. 02.30.00"}
	if in, err := r.Contains("HP", "R21 Ver. 2.30"); err == nil || in {
		t.Errorf("a two-part version is in the range: %v", err)
	}
	for v, want := range map[string]bool{"R21 Ver. 02.00.00": true, "R21 Ver. 02.27.00": true, "R21 Ver. 02.30.00": false, "R21 Ver. 01.99.00": false} {
		if got, err := r.Contains("HP", v); err != nil || got != want {
			t.Errorf("%s: %v %v", v, got, err)
		}
	}
	for _, r := range []Range{{From: "R21 Ver. 02.00.00"}, {To: "R21 Ver. 03.00.00"}} {
		if in, err := r.Contains("HP", "R21 Ver. 02.27.00"); err != nil || !in {
			t.Errorf("open range %+v: %v %v", r, in, err)
		}
	}
	if in, err := r.Contains("HP", "not a version"); err == nil || in {
		t.Errorf("an unreadable version is in the range: %v", err)
	}
	if in, err := (Range{To: "R21 Ver. 03.00.00"}).Contains("HP", "garbage"); err == nil || in {
		t.Errorf("an unreadable version is below the bound: %v", err)
	}
}

// Conflicting claims are all kept, the newest document first; undated
// ones come last in their written order.
func TestClaimsNewestFirst(t *testing.T) {
	k := withSections()
	cs, err := k.Claims(json.RawMessage(`[{"value":1,"src":"forum"},{"value":32,"src":"hp-msg-2019-09"},{"value":2,"src":"gone"},{"value":64,"src":"hp-ds-2019-12"}]`))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range cs {
		got = append(got, string(c.Value)+"@"+c.Src)
	}
	if want := []string{"64@hp-ds-2019-12", "32@hp-msg-2019-09", "1@forum", "2@gone"}; !slices.Equal(got, want) {
		t.Errorf("order %q, want %q", got, want)
	}
	// A year follows its months: it can't be placed among them.
	k.Sources = append(k.Sources, Source{ID: "year-only", Published: "2019"})
	slices.SortFunc(k.Sources, func(a, b Source) int { return strings.Compare(a.ID, b.ID) })
	cs, _ = k.Claims(json.RawMessage(`[{"value":1,"src":"year-only"},{"value":2,"src":"hp-msg-2019-09"},{"value":3,"src":"hp-ds-2019-12"}]`))
	got = nil
	for _, c := range cs {
		got = append(got, string(c.Value))
	}
	if want := []string{"3", "2", "1"}; !slices.Equal(got, want) {
		t.Errorf("mixed precision: order %q, want %q", got, want)
	}
	if _, err := k.Claims(json.RawMessage(`{"value":1}`)); err == nil {
		t.Error("a claim that isn't a list decoded")
	}
}

// An allow-list is "confirmed" only when every claim cites an OEM
// document; one community or unknown source makes it a report.
func TestConfirmedIsDerivedFromTheSources(t *testing.T) {
	k := withSections()
	for js, want := range map[string]bool{
		`{"restricted":[{"value":false,"src":"hp-msg-2019-09"}]}`:                                                             true,
		`{"restricts":[{"value":["wlan"],"src":"hp-ds-2019-12"}],"error_text":{"en":[{"value":"x","src":"hp-msg-2019-09"}]}}`: true,
		`{"restricts":[{"value":["wlan"],"src":"hp-ds-2019-12"}],"error_text":[{"value":"x","src":"forum"}]}`:                 false,
		`{"restricts":[{"value":["wlan"],"src":"gone"}]}`:                                                                     false,
		`{}`:                     false,
		`{"restricts":"broken"}`: false,
		`{"restricts":[1]}`:      false,
	} {
		var data map[string]json.RawMessage
		if err := json.Unmarshal([]byte(js), &data); err != nil {
			t.Fatal(err)
		}
		if got := k.Confirmed(data); got != want {
			t.Errorf("%s: %v", js, got)
		}
	}
	if k.Confirmed(map[string]json.RawMessage{"restricted": json.RawMessage(`{`)}) {
		t.Error("unreadable data confirmed")
	}
}

func gunzip(t *testing.T, b []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// What a section's match accepts besides the reference entries: a USB
// device, an allow-list by board name only, and two models of one product
// told apart by SKU.
func TestOtherValidMatches(t *testing.T) {
	k := withSections()
	k.Devices = append(k.Devices, Device{ID: "logitech.unifying", Match: DeviceMatch{Bus: "usb", ID: "046d:c52b"},
		Data: k.Devices[0].Data})
	k.Allowlists[0].Match = AllowlistMatch{SysVendor: "HP", BoardName: []string{"8595"}}
	m := k.Models[0]
	m.ID, m.Match.SKU = "hp.z-sku", "7LL88UT#ABA"
	k.Models = append(k.Models, m)
	if err := k.Validate(); err != nil {
		t.Error(err)
	}
}
