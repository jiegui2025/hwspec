package advisor

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jiegui2025/hwspec/internal/kb"
	"github.com/jiegui2025/hwspec/internal/report"
)

// The wlan_slot group is read strictly (#25 part 3): every leaf's values
// checked, an empty group and unknown leaves refused.
func TestWLANGroupValidation(t *testing.T) {
	for group, want := range map[string]string{
		`{"m2": {"count": [{"value": 0, "src": "x"}]}}`:                                                              "m2.count: 0 isn't positive",
		`{"m2": {"lengths": [{"value": [2250], "src": "x"}]}}`:                                                       "m2.lengths: 2250 isn't an M.2 length",
		`{"m2": {"interfaces": [{"value": ["sdio"], "src": "x"}]}}`:                                                  `m2.interfaces: "sdio" isn't pcie, cnvi or usb`,
		`{"antennas": [{"value": 0, "src": "x"}]}`:                                                                   "antennas: 0 isn't positive",
		`{"antennas": [{"value": -1, "src": "x"}]}`:                                                                  "antennas: -1 isn't positive",
		`{"factory_options": [{"value": [{"generation": "Wi-Fi 6", "chains": "2x2"}], "src": "x"}]}`:                 "factory_options: a card without a name",
		`{"factory_options": [{"value": [{"name": "A", "generation": "Wi-Fi 8", "chains": "2x2"}], "src": "x"}]}`:    `factory_options: A: generation "Wi-Fi 8" isn't one of`,
		`{"factory_options": [{"value": [{"name": "A", "generation": "Wi-Fi 6", "chains": "2 by 2"}], "src": "x"}]}`: `factory_options: A: chains "2 by 2" isn't NxM`,
		`{"m2": {"key": [{"value": "E", "src": "x"}]}}`:                                                              `json: unknown field "key"`,
		`{"factory_options": [{"value": [{"name": "A", "generation": "Wi-Fi 6", "chains": "12x2"}], "src": "x"}]}`:   `factory_options: A: chains "12x2" isn't NxM`,
		`{"factory_options": [{"value": [{"name": "A", "generation": "Wi-Fi 6", "chains": "2x2x2"}], "src": "x"}]}`:  `factory_options: A: chains "2x2x2" isn't NxM`,
		`{"factory_options": [{"value": [], "src": "x"}]}`:                                                           "factory_options: an empty list",
		`{"m2": {"lengths": [{"value": [], "src": "x"}]}}`:                                                           "m2.lengths: an empty list",
		`{"m2": {"interfaces": [{"value": [], "src": "x"}]}}`:                                                        "m2.interfaces: an empty list",
		`{}`: "no claims",
	} {
		m := kb.Model{Data: map[string]json.RawMessage{"wlan_slot": json.RawMessage(group)}}
		if errs := ValidateModel(&m, nil); len(errs) != 1 || !strings.Contains(errs[0].Error(), "data.wlan_slot: "+want) {
			t.Errorf("%s: %v", group, errs)
		}
	}
	ok := `{"m2": {"count": [{"value": 1, "src": "x"}], "lengths": [{"value": [2230], "src": "x"}], "interfaces": [{"value": ["pcie", "cnvi", "usb"], "src": "x"}]},
		"antennas": [{"value": 2, "src": "y"}], "factory_options": [{"value": [{"name": "Intel Wi-Fi 6 AX200", "generation": "Wi-Fi 6", "chains": "2x2"}], "src": "x"}]}`
	if errs := ValidateModel(&kb.Model{Data: map[string]json.RawMessage{"wlan_slot": json.RawMessage(ok)}}, nil); errs != nil {
		t.Errorf("a full group: %v", errs)
	}
}

func wifiKB(group string) *kb.KB {
	rule, _ := checks["wifi-upgrade"].example()
	k := exampleKnowledge("wifi-upgrade", rule)
	if group != "" {
		k.Models[0].Data["wlan_slot"] = json.RawMessage(group)
	}
	return k
}

func wifiAnswers(t *testing.T, r *report.Report, k *kb.KB) map[string]Answer {
	t.Helper()
	a := Advise(Input{Report: r, KB: k, Now: noon})
	if len(a.Findings) != 1 || len(a.Warnings) != 0 {
		t.Fatalf("findings %+v, warnings %q", a.Findings, a.Warnings)
	}
	out := map[string]Answer{}
	for _, an := range a.Findings[0].Answers {
		out[an.Topic] = an
	}
	return out
}

const (
	wifiSlotDoc = "1 M.2 WLAN slot per example-datasheet (2019-12); M.2 2230 per example-datasheet (2019-12); PCIe per example-datasheet (2019-12)"
	wifiFactory = "The vendor fitted Intel Dual Band Wi-Fi 5 9560 (Wi-Fi 5, 2×2); Intel Wi-Fi 6 AX200 (Wi-Fi 6, 2×2) per example-datasheet (2019-12)"
	wifiBeyond  = ". Whether a newer card (Wi-Fi 6E or Wi-Fi 7) fits isn't in the documents: it needs the slot's key, a bus the chipset supports and suitable antennas (6 GHz-capable for Wi-Fi 6E and 7)"
	wifiAP      = "; what a card gains in use depends on the access point"
	wifiNoList  = "Whether HP's firmware restricts third-party Wi-Fi cards in this model is unknown: the knowledge base has no source on it"
)

// #110's acceptance on the reference machine (as root: the firmware names
// its slot): the AX200, Wi-Fi 6, 2.4 + 5 GHz, 2×2, in the M.2 WLAN slot;
// both antennas used; nothing HP fitted is newer; beyond that, unknown.
func TestWiFiReferenceMachine(t *testing.T) {
	got := wifiAnswers(t, wifiExample(), wifiKB(""))
	wantAnswers(t, got, map[string]string{
		"card":      "wlan0 (Intel Corporation Wi-Fi 6 AX200): Wi-Fi 6 (802.11ax), 2.4 + 5 GHz, 2×2, 2 streams",
		"slots":     wifiSlotDoc + "; wlan0 is in Slot2 / M2 WLAN/BT (PCI Express Gen 3 x1)",
		"antennas":  "2 antenna cables reach the WLAN slot per example-guide (2019-09); wlan0's 2×2 uses them all",
		"upgrade":   wifiFactory + "; none is newer than wlan0's Wi-Fi 6 or has more than its 2 streams, so none gains" + wifiBeyond,
		"allowlist": wifiNoList,
	}, map[string]bool{"card": true, "slots": true, "antennas": true, "upgrade": false, "allowlist": false})
}

// A 1×1 Wi-Fi 5 card: named so, uses one antenna of two, and a Wi-Fi 6
// card HP fitted would gain; a card with more chains than antennas, and a
// medium-confidence slot, are said.
func TestWiFiOtherCards(t *testing.T) {
	r := wifiExample()
	r.Network[0].Radio = &report.WiFiRadio{Generation: "Wi-Fi 5", Bands: []string{"2.4 GHz", "5 GHz"}, TXChains: 1, RXChains: 1, MaxSpatialStreams: 1, Source: "nl80211"}
	r.PCI[0].Mounting = &report.Mounting{Kind: "slot", Slot: "Slot2", Confidence: "medium", Reason: "the only slot it can be in"}
	got := wifiAnswers(t, r, wifiKB(""))
	wantAnswers(t, got, map[string]string{
		"card":     "wlan0 (Intel Corporation Wi-Fi 6 AX200): Wi-Fi 5 (802.11ac), 2.4 + 5 GHz, 1×1, 1 stream",
		"slots":    wifiSlotDoc + "; wlan0 is likely in Slot2, with medium confidence: the only slot it can be in",
		"antennas": "2 antenna cables reach the WLAN slot per example-guide (2019-09); wlan0 uses 1 of them",
		"upgrade":  wifiFactory + "; the Intel Wi-Fi 6 AX200 would give wlan0 Wi-Fi 6 instead of Wi-Fi 5 and 2 streams instead of 1" + wifiAP + wifiBeyond,
	}, nil)

	r = wifiExample()
	r.Network[0].Radio.TXChains, r.Network[0].Radio.RXChains = 4, 4
	if got := wifiAnswers(t, r, wifiKB(""))["antennas"].Text; !strings.HasSuffix(got, "wlan0 has 4 chains, more than the antennas serve") {
		t.Errorf("more chains than antennas: %q", got)
	}
}

// Where a card can't be swapped, or isn't in a slot, or the capture
// doesn't say.
func TestWiFiPlacement(t *testing.T) {
	for _, c := range []struct {
		edit func(r *report.Report)
		want string
	}{
		{func(r *report.Report) { r.PCI[0].Mounting = &report.Mounting{Kind: "onboard", Confidence: "high"} }, "wlan0 is soldered on: it can't be swapped"},
		{func(r *report.Report) { r.Network[0].Bus, r.Network[0].BusAddress = "usb", "1-2" }, "wlan0 is a USB adapter, not in a slot"},
		{func(r *report.Report) { r.PCI[0].Mounting = nil }, "which slot holds wlan0 isn't in the capture"},
		{func(r *report.Report) {
			r.PCI[0].Mounting = &report.Mounting{Kind: "unknown", Reason: "the firmware's slot table needs root (run with --full)"}
		}, "which slot holds wlan0 can't be told: the firmware's slot table needs root (run with --full)"},
	} {
		r := wifiExample()
		c.edit(r)
		if got := wifiAnswers(t, r, wifiKB(""))["slots"].Text; got != wifiSlotDoc+"; "+c.want {
			t.Errorf("got %q\nwant %q", got, wifiSlotDoc+"; "+c.want)
		}
	}
}

// What can't be told is said, never guessed: no radio details, no
// antenna source, no model data, no Wi-Fi card; nothing at all gives no
// finding.
func TestWiFiUnknowns(t *testing.T) {
	r := wifiExample()
	r.Network[0].Radio = nil
	got := wifiAnswers(t, r, wifiKB(`{"m2": {"count": [{"value": 1, "src": "example-datasheet"}]}}`))
	wantAnswers(t, got, map[string]string{
		"card":     "wlan0 (Intel Corporation Wi-Fi 6 AX200): the capture has no radio details (nl80211 didn't describe it)",
		"antennas": "How many antennas reach the WLAN slot is unknown: the knowledge base has no source on it (a card's chains say what it can use, not what is connected)",
		"upgrade":  "Which cards fit the WLAN slot is unknown: the knowledge base lists no cards for it. A card beyond them would need the slot's key, its bus and the vendor's allow-list checked",
	}, map[string]bool{"card": false, "antennas": false, "upgrade": false})

	r = wifiExample()
	r.System.Identity.Model = "Another Model"
	got = wifiAnswers(t, r, wifiKB(""))
	wantAnswers(t, got, map[string]string{
		"slots":   "The model's WLAN slot is unknown: the knowledge base has no entry for this model; wlan0 is in Slot2 / M2 WLAN/BT (PCI Express Gen 3 x1)",
		"upgrade": "Which cards fit the WLAN slot is unknown: the knowledge base has no entry for this model. A card beyond them would need the slot's key, its bus and the vendor's allow-list checked",
	}, map[string]bool{"slots": false})

	// A group without M.2 claims doesn't make the slot known (#262 round 1,
	// I3); a model entry without the group says so.
	got = wifiAnswers(t, wifiExample(), wifiKB(`{"antennas": [{"value": 2, "src": "example-guide"}]}`))
	if s := got["slots"]; s.Known || !strings.HasPrefix(s.Text, "The model's WLAN slot is unknown: the knowledge base has no slot data for it;") {
		t.Errorf("antennas only: %+v", s)
	}
	k := wifiKB("")
	delete(k.Models[0].Data, "wlan_slot")
	if s := wifiAnswers(t, wifiExample(), k)["slots"]; s.Known || !strings.HasPrefix(s.Text, "The model's WLAN slot is unknown: the knowledge base has no Wi-Fi slot data for this model;") {
		t.Errorf("no group: %+v", s)
	}

	r = wifiExample()
	r.Network = nil
	got = wifiAnswers(t, r, wifiKB(""))
	wantAnswers(t, got, map[string]string{"card": "No Wi-Fi card is in the capture"}, map[string]bool{"card": false})

	r.System.Identity.Model = "Another Model"
	if a := Advise(Input{Report: r, KB: wifiKB(""), Now: noon}); len(a.Findings) != 0 {
		t.Errorf("no Wi-Fi, no entry: %+v", a.Findings)
	}
}

// A card the vendor's would gain over in streams only, and one they'd
// gain over in streams while being older (#262 round 1, I1); with a Wi-Fi
// 7 card listed, nothing newer is left to be unknown.
func TestWiFiGains(t *testing.T) {
	r := wifiExample()
	r.Network[0].Radio = &report.WiFiRadio{Generation: "Wi-Fi 6", Bands: []string{"2.4 GHz", "5 GHz"}, TXChains: 1, RXChains: 1, MaxSpatialStreams: 1, Source: "nl80211"}
	if got := wifiAnswers(t, r, wifiKB(""))["upgrade"].Text; got != wifiFactory+"; the Intel Wi-Fi 6 AX200 would give wlan0 2 streams instead of 1"+wifiAP+wifiBeyond {
		t.Errorf("streams only: %q", got)
	}
	r.Network[0].Radio.Generation = "Wi-Fi 6E"
	if got := wifiAnswers(t, r, wifiKB(""))["upgrade"].Text; !strings.Contains(got, "; the Intel Wi-Fi 6 AX200 would give wlan0 2 streams instead of 1 (but Wi-Fi 6, older than its Wi-Fi 6E)"+wifiAP) {
		t.Errorf("streams, but older: %q", got)
	}
	seven := `{"factory_options": [{"value": [{"name": "Intel BE200", "generation": "Wi-Fi 7", "chains": "2x2"}, {"name": "Tiny", "generation": "Wi-Fi 7", "chains": "1x1"}], "src": "example-datasheet"}]}`
	if got := wifiAnswers(t, wifiExample(), wifiKB(seven))["upgrade"].Text; got != "The vendor fitted Intel BE200 (Wi-Fi 7, 2×2); Tiny (Wi-Fi 7, 1×1) per example-datasheet (2019-12); the Intel BE200 would give wlan0 Wi-Fi 7 instead of Wi-Fi 6"+wifiAP {
		t.Errorf("Wi-Fi 7 listed: %q", got)
	}
	// A 6E card listed leaves only Wi-Fi 7 unknown; a 1x2 card has one
	// stream, so it gains none over a 1×1.
	sixE := `{"factory_options": [{"value": [{"name": "Intel AX210", "generation": "Wi-Fi 6E", "chains": "1x2"}], "src": "example-datasheet"}]}`
	r = wifiExample()
	r.Network[0].Radio.Generation, r.Network[0].Radio.MaxSpatialStreams = "Wi-Fi 6E", 1
	if got := wifiAnswers(t, r, wifiKB(sixE))["upgrade"].Text; got != "The vendor fitted Intel AX210 (Wi-Fi 6E, 1×2) per example-datasheet (2019-12); none is newer than wlan0's Wi-Fi 6E or has more than its 1 stream, so none gains."+
		" Whether a newer card (Wi-Fi 7) fits isn't in the documents: it needs the slot's key, a bus the chipset supports and suitable antennas (6 GHz-capable for Wi-Fi 6E and 7)" {
		t.Errorf("6E listed: %q", got)
	}
	// The newest card isn't the first: a later Wi-Fi 5 card with more
	// streams doesn't displace a Wi-Fi 6 one.
	mixed := `{"factory_options": [{"value": [{"name": "A", "generation": "Wi-Fi 5", "chains": "1x1"}, {"name": "B", "generation": "Wi-Fi 6", "chains": "1x1"},
		{"name": "C", "generation": "Wi-Fi 6", "chains": "2x2"}, {"name": "D", "generation": "Wi-Fi 5", "chains": "4x4"}], "src": "example-datasheet"}]}`
	r = wifiExample()
	r.Network[0].Radio.Generation, r.Network[0].Radio.MaxSpatialStreams = "Wi-Fi 5", 1
	if got := wifiAnswers(t, r, wifiKB(mixed))["upgrade"].Text; !strings.Contains(got, "; the C would give wlan0 Wi-Fi 6 instead of Wi-Fi 5 and 2 streams instead of 1") {
		t.Errorf("mixed: %q", got)
	}
}

// Only a card that may be in the WLAN slot is compared with its cables
// and the vendor's cards (#262 round 1, I2): a USB adapter and a soldered
// card aren't; a card whose slot the capture doesn't give is, said so.
func TestWiFiCardsOutsideTheSlot(t *testing.T) {
	r := wifiExample()
	r.Network = append(r.Network, report.NIC{Name: "wlan1", Type: "wireless", Bus: "usb", BusAddress: "1-2",
		Radio: &report.WiFiRadio{Generation: "Wi-Fi 5", Bands: []string{"5 GHz"}, TXChains: 2, RXChains: 2, Source: "nl80211"}})
	got := wifiAnswers(t, r, wifiKB(""))
	wantAnswers(t, got, map[string]string{
		"antennas": "2 antenna cables reach the WLAN slot per example-guide (2019-09); wlan0's 2×2 uses them all; wlan1 is a USB adapter, not in the WLAN slot",
		"upgrade":  wifiFactory + "; none is newer than wlan0's Wi-Fi 6 or has more than its 2 streams, so none gains; wlan1 is a USB adapter, not in the WLAN slot, so they don't replace it" + wifiBeyond,
	}, nil)

	r = wifiExample()
	r.PCI[0].Mounting = &report.Mounting{Kind: "onboard", Confidence: "high"}
	got = wifiAnswers(t, r, wifiKB(""))
	wantAnswers(t, got, map[string]string{
		"antennas": "2 antenna cables reach the WLAN slot per example-guide (2019-09); wlan0 is soldered on, not in the WLAN slot",
		"upgrade":  wifiFactory + "; wlan0 is soldered on, not in the WLAN slot, so they don't replace it" + wifiBeyond,
	}, nil)

	for _, m := range []*report.Mounting{nil, {Kind: "unknown", Reason: "the firmware's slot table needs root (run with --full)"}} {
		r = wifiExample()
		r.PCI[0].Mounting = m
		got = wifiAnswers(t, r, wifiKB(""))
		wantAnswers(t, got, map[string]string{
			"antennas": "2 antenna cables reach the WLAN slot per example-guide (2019-09); wlan0's 2×2 uses them all, if it's the card in the WLAN slot",
			"upgrade":  wifiFactory + "; none is newer than wlan0's Wi-Fi 6 or has more than its 2 streams, so none gains, if it's the card in the WLAN slot" + wifiBeyond,
		}, nil)
	}
}

// What the capture lacks of a radio is said and recorded absent (#262
// round 1, I5): chains, generation; a radio with bands but no generation
// still has details.
func TestWiFiPartialRadio(t *testing.T) {
	r := wifiExample()
	r.Network[0].Radio.TXChains, r.Network[0].Radio.RXChains, r.Network[0].Radio.MaxSpatialStreams = 2, 0, 0
	a := Advise(Input{Report: r, KB: wifiKB(""), Now: noon})
	got := wifiAnswers(t, r, wifiKB(""))
	wantAnswers(t, got, map[string]string{
		"card":     "wlan0 (Intel Corporation Wi-Fi 6 AX200): Wi-Fi 6 (802.11ax), 2.4 + 5 GHz, chains unknown",
		"antennas": "2 antenna cables reach the WLAN slot per example-guide (2019-09); how many of them wlan0 uses is unknown: its chains aren't in the capture",
		"upgrade":  wifiFactory + "; whether one gains in streams can't be told: the capture lacks wlan0's streams" + wifiBeyond,
	}, map[string]bool{"card": false})
	var ev []string
	for _, e := range a.Findings[0].Evidence {
		ev = append(ev, fmt.Sprintf("%s=%v/%v", e.path, e.value, e.absent))
	}
	if want := "network[0].radio.generation=Wi-Fi 6/false network[0].radio.tx_chains=2/false network[0].radio.rx_chains=<nil>/true"; !strings.HasPrefix(strings.Join(ev, " "), want) {
		t.Errorf("evidence %q, want prefix %q", ev, want)
	}

	r = wifiExample()
	r.Network[0].Radio.Generation = ""
	got = wifiAnswers(t, r, wifiKB(""))
	wantAnswers(t, got, map[string]string{
		"card":    "wlan0 (Intel Corporation Wi-Fi 6 AX200): generation unknown, 2.4 + 5 GHz, 2×2, 2 streams",
		"upgrade": wifiFactory + "; whether one gains in generation can't be told: the capture lacks wlan0's generation" + wifiBeyond,
	}, map[string]bool{"card": false})

	r = wifiExample()
	r.Network[0].Radio.Generation, r.Network[0].Radio.TXChains, r.Network[0].Radio.MaxSpatialStreams = "", 0, 0
	if got := wifiAnswers(t, r, wifiKB(""))["upgrade"].Text; !strings.Contains(got, "; whether one gains in generation or streams can't be told: the capture lacks wlan0's generation and streams") {
		t.Errorf("neither: %q", got)
	}

	// Chains but no MCS streams: the fewer chains are the streams.
	r = wifiExample()
	r.Network[0].Radio.TXChains, r.Network[0].Radio.RXChains, r.Network[0].Radio.MaxSpatialStreams = 1, 2, 0
	if got := wifiAnswers(t, r, wifiKB(""))["upgrade"].Text; !strings.Contains(got, "would give wlan0 2 streams instead of 1") {
		t.Errorf("streams from chains: %q", got)
	}

	r = wifiExample()
	r.Network[0].Radio = nil
	if got := wifiAnswers(t, r, wifiKB(""))["upgrade"].Text; !strings.Contains(got, "; what they would gain over wlan0 is unknown: its radio isn't in the capture") {
		t.Errorf("no radio: %q", got)
	}
}

// #263's follow-ups to #262: antenna claims that disagree, the generations
// left unknown above a 6E card, a slot the firmware gives without
// confidence or reason, and a claim's note printed with its source.
func TestWiFiFollowUps(t *testing.T) {
	k := wifiKB(`{"antennas": [{"value": 4, "src": "example-datasheet"}, {"value": 2, "src": "example-guide"}]}`)
	for chains, want := range map[int]string{
		1: "wlan0 uses 1 of them",
		3: "wlan0 has 3 chains: whether it uses them all depends on which source is right (2 to 4 cables)",
		4: "wlan0 has 4 chains: whether it uses them all depends on which source is right (2 to 4 cables)",
		5: "wlan0 has 5 chains, more than the antennas serve",
	} {
		r := wifiExample()
		r.Network[0].Radio.TXChains, r.Network[0].Radio.RXChains = chains, chains
		if got := wifiAnswers(t, r, k)["antennas"].Text; !strings.HasSuffix(got, "; "+want) {
			t.Errorf("%d chains: %q", chains, got)
		}
	}

	r := wifiExample()
	r.Network[0].Radio.Generation = "Wi-Fi 6E"
	if got := wifiAnswers(t, r, wifiKB(""))["upgrade"].Text; !strings.HasSuffix(got, ". Whether a newer card (Wi-Fi 7) fits isn't in the documents: it needs the slot's key, a bus the chipset supports and suitable antennas (6 GHz-capable for Wi-Fi 6E and 7)") {
		t.Errorf("a 6E card: %q", got)
	}
	r.Network[0].Radio.Generation = "Wi-Fi 7"
	if got := wifiAnswers(t, r, wifiKB(""))["upgrade"].Text; strings.Contains(got, "Whether a newer card") {
		t.Errorf("a Wi-Fi 7 card: %q", got)
	}

	r = wifiExample()
	r.PCI[0].Mounting = &report.Mounting{Kind: "slot", Slot: "Slot2 / M2 WLAN/BT"}
	if got := wifiAnswers(t, r, wifiKB(""))["slots"].Text; !strings.HasSuffix(got, "; wlan0 is likely in Slot2 / M2 WLAN/BT, though which slot isn't certain") {
		t.Errorf("no confidence or reason: %q", got)
	}
	r.PCI[0].Mounting.Reason = "the only slot it can be in"
	if got := wifiAnswers(t, r, wifiKB(""))["slots"].Text; !strings.HasSuffix(got, "; wlan0 is likely in Slot2 / M2 WLAN/BT, with unstated confidence: the only slot it can be in") {
		t.Errorf("a reason, no confidence: %q", got)
	}

	got := wifiAnswers(t, wifiExample(), wifiKB(`{"antennas": [{"value": 2, "src": "example-guide", "note": "on dual-antenna units"}]}`))["antennas"]
	if !strings.HasPrefix(got.Text, "2 antenna cables reach the WLAN slot per example-guide (2019-09; on dual-antenna units);") ||
		len(got.Claims) != 1 || got.Claims[0].Note != "on dual-antenna units" {
		t.Errorf("a note: %+v", got)
	}
}

// Two sources disagreeing on the antennas: a card is compared with the
// range they give (#263). A radio nl80211 described with nothing is "no details". A NIC
// without a bus address matches no PCI device, even a malformed one. The
// allow-list answer reads Wi-Fi policies, not SSD ones.
func TestWiFiEdges(t *testing.T) {
	r := wifiExample()
	k := wifiKB(`{"antennas": [{"value": 4, "src": "example-datasheet"}, {"value": 2, "src": "example-guide"}]}`)
	if got := wifiAnswers(t, r, k)["antennas"].Text; !strings.HasSuffix(got, "; wlan0 has 2 chains: whether it uses them all depends on which source is right (2 to 4 cables)") {
		t.Errorf("disagreeing antenna claims: %q", got)
	}

	r = wifiExample()
	r.Network[0].Radio = &report.WiFiRadio{Bands: []string{}, Source: "nl80211"}
	if got := wifiAnswers(t, r, wifiKB(""))["card"]; got.Known || !strings.HasSuffix(got.Text, "the capture has no radio details (nl80211 didn't describe it)") {
		t.Errorf("an empty radio: %+v", got)
	}

	r = wifiExample()
	r.Network[0].BusAddress = ""
	r.PCI = append(r.PCI, report.PCIDevice{Mounting: &report.Mounting{Kind: "onboard"}})
	if got := wifiAnswers(t, r, wifiKB(""))["slots"].Text; !strings.HasSuffix(got, "which slot holds wlan0 isn't in the capture") {
		t.Errorf("no bus address: %q", got)
	}

	policy := func(id, restricts string) kb.Allowlist {
		return kb.Allowlist{ID: id, Match: kb.AllowlistMatch{SysVendor: "HP", ProductName: []string{"HP EliteDesk 800 G5 Desktop Mini"}},
			Data: map[string]json.RawMessage{"restricts": json.RawMessage(`[{"value": ["` + restricts + `"], "src": "x"}]`)}}
	}
	k = wifiKB("")
	k.Allowlists = []kb.Allowlist{policy("hp.wlan", "wlan"), policy("hp.ssd", "ssd")}
	if got := wifiAnswers(t, wifiExample(), k)["allowlist"].Text; got != "The knowledge base has HP firmware policies that may restrict third-party Wi-Fi cards in this model (hp.wlan): check their terms before buying one" {
		t.Errorf("allow-list: %q", got)
	}
}
