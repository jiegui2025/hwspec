package advisor

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jiegui2025/hwspec/internal/kb"
	"github.com/jiegui2025/hwspec/internal/report"
)

// wlanData is a model's wlan_slot group (#25 part 3), read strictly: its
// M.2 WLAN slot, the antenna cables that reach it, and the cards the
// vendor fitted.
type wlanData struct {
	M2 struct {
		Count      claims[int]      `json:"count"`
		Lengths    claims[[]int]    `json:"lengths"`
		Interfaces claims[[]string] `json:"interfaces"`
	} `json:"m2"`
	Antennas       claims[int]             `json:"antennas"`
	FactoryOptions claims[[]factoryOption] `json:"factory_options"`
}

// factoryOption is a Wi-Fi card the vendor fitted to the model.
type factoryOption struct {
	Name       string `json:"name"`
	Generation string `json:"generation"`
	Chains     string `json:"chains"` // "2x2": TX × RX
}

var (
	wlanInterfaces  = map[string]string{"pcie": "PCIe", "cnvi": "CNVi", "usb": "USB"}
	wifiGenerations = []string{"Wi-Fi 4", "Wi-Fi 5", "Wi-Fi 6", "Wi-Fi 6E", "Wi-Fi 7"}
	chainsRe        = regexp.MustCompile(`^[1-8]x[1-8]$`)
)

func decodeWLAN(raw json.RawMessage) (*wlanData, error) {
	d, err := decodeData[wlanData](raw)
	if err != nil {
		return nil, err
	}
	if len(d.M2.Count)+len(d.M2.Lengths)+len(d.M2.Interfaces)+len(d.Antennas)+len(d.FactoryOptions) == 0 {
		return nil, errors.New("no claims (leave the group out when the documents say nothing)")
	}
	var errs []error
	for _, c := range d.M2.Count {
		if c.Value <= 0 {
			errs = append(errs, fmt.Errorf("m2.count: %d isn't positive", c.Value))
		}
	}
	for _, c := range d.M2.Lengths {
		if len(c.Value) == 0 {
			errs = append(errs, errors.New("m2.lengths: an empty list (leave the claim out)"))
		}
		for _, l := range c.Value {
			if !slices.Contains(m2Lengths, l) {
				errs = append(errs, fmt.Errorf("m2.lengths: %d isn't an M.2 length (%v)", l, m2Lengths))
			}
		}
	}
	for _, c := range d.M2.Interfaces {
		if len(c.Value) == 0 {
			errs = append(errs, errors.New("m2.interfaces: an empty list (leave the claim out)"))
		}
		for _, i := range c.Value {
			if wlanInterfaces[i] == "" {
				errs = append(errs, fmt.Errorf("m2.interfaces: %q isn't pcie, cnvi or usb", i))
			}
		}
	}
	for _, c := range d.Antennas {
		if c.Value <= 0 {
			errs = append(errs, fmt.Errorf("antennas: %d isn't positive", c.Value))
		}
	}
	for _, c := range d.FactoryOptions {
		if len(c.Value) == 0 {
			errs = append(errs, errors.New("factory_options: an empty list (leave the claim out)"))
		}
		for _, o := range c.Value {
			switch {
			case o.Name == "":
				errs = append(errs, errors.New("factory_options: a card without a name"))
			case !slices.Contains(wifiGenerations, o.Generation):
				errs = append(errs, fmt.Errorf("factory_options: %s: generation %q isn't one of %q", o.Name, o.Generation, wifiGenerations))
			case !chainsRe.MatchString(o.Chains):
				errs = append(errs, fmt.Errorf("factory_options: %s: chains %q isn't NxM (\"2x2\")", o.Name, o.Chains))
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return &d, nil
}

// modelWLAN returns the WLAN group of the capture's model entry, or why
// there is none to use.
func modelWLAN(k *kb.KB, r *report.Report) (*wlanData, string) {
	m := modelFor(k, r)
	if m == nil {
		return nil, "the knowledge base has no entry for this model"
	}
	raw, ok := m.Data["wlan_slot"]
	if !ok {
		return nil, "the knowledge base has no Wi-Fi slot data for this model"
	}
	d, err := decodeWLAN(raw)
	if err != nil {
		return nil, fmt.Sprintf("this build can't read the knowledge base's Wi-Fi slot data for this model (update hwspec): %v", err)
	}
	return d, ""
}

func init() {
	register("wifi-upgrade", check{
		run:   wifiUpgrade,
		needs: []string{"network[].radio"},
		example: func() (kb.Rule, *report.Report) {
			return kb.Rule{ID: "wifi.upgrade", Check: "wifi-upgrade", Category: "upgrade", Severity: "info", Title: "Wi-Fi", Src: []string{kb.Capture.ID}},
				wifiExample()
		},
		exampleData: func() ([]kb.Source, []kb.Model) {
			return []kb.Source{
					{ID: "example-datasheet", URL: "https://example.com/ds.pdf", Published: "2019-12", Retrieved: "2026-10-07",
						Licence: "proprietary", Confidence: "oem-doc", LinkOnly: true, Locator: "p. 2"},
					{ID: "example-guide", URL: "https://example.com/guide.pdf", Published: "2019-09", Retrieved: "2026-10-07",
						Licence: "proprietary", Confidence: "oem-doc", LinkOnly: true, Locator: "p. 25"},
				},
				[]kb.Model{{ID: "hp.example", Match: kb.ModelMatch{SysVendor: "HP", ProductName: "HP EliteDesk 800 G5 Desktop Mini"},
					Data: map[string]json.RawMessage{"wlan_slot": json.RawMessage(`{
						"m2": {"count": [{"value": 1, "src": "example-datasheet"}], "lengths": [{"value": [2230], "src": "example-datasheet"}],
							"interfaces": [{"value": ["pcie"], "src": "example-datasheet"}]},
						"antennas": [{"value": 2, "src": "example-guide"}],
						"factory_options": [{"value": [
							{"name": "Intel Dual Band Wi-Fi 5 9560", "generation": "Wi-Fi 5", "chains": "2x2"},
							{"name": "Intel Wi-Fi 6 AX200", "generation": "Wi-Fi 6", "chains": "2x2"}], "src": "example-datasheet"}]}`)}}}
		},
	})
}

// wifiExample is the reference machine's Wi-Fi: an Intel AX200 in its M.2
// WLAN slot.
func wifiExample() *report.Report {
	return &report.Report{
		System: report.System{Identity: &report.Identity{Vendor: "HP", Model: "HP EliteDesk 800 G5 Desktop Mini"}},
		PCI: []report.PCIDevice{{Address: "0000:02:00.0", ClassCode: "028000",
			Mounting: &report.Mounting{Kind: "slot", Slot: "Slot2 / M2 WLAN/BT", SlotType: "PCI Express Gen 3 x1", Confidence: "high"}}},
		Network: []report.NIC{{Name: "wlan0", Type: "wireless", Bus: "pci", BusAddress: "0000:02:00.0",
			Identity: &report.Identity{Vendor: "Intel Corporation", Model: "Wi-Fi 6 AX200"},
			Radio:    &report.WiFiRadio{Generation: "Wi-Fi 6", Bands: []string{"2.4 GHz", "5 GHz"}, TXChains: 2, RXChains: 2, MaxSpatialStreams: 2, Source: "nl80211"}}},
	}
}

// standards names each generation's IEEE amendment.
var standards = map[string]string{"Wi-Fi 4": "802.11n", "Wi-Fi 5": "802.11ac", "Wi-Fi 6": "802.11ax", "Wi-Fi 6E": "802.11ax, 6 GHz", "Wi-Fi 7": "802.11be"}

// generationRank orders generations; an unknown one ranks lowest.
func generationRank(g string) int { return slices.Index(wifiGenerations, g) }

// wifiUpgrade answers #110 for each wireless NIC: the card, its slot, the
// antennas, what a factory card would gain, and the allow-list.
func wifiUpgrade(in *Input, _ *kb.Rule) ([]hit, error) {
	r := in.Report
	d, why := modelWLAN(in.KB, r)
	var nics []int
	for i, n := range r.Network {
		if n.Type == "wireless" {
			nics = append(nics, i)
		}
	}
	if len(nics) == 0 && d == nil {
		return nil, nil // nothing to say about Wi-Fi
	}
	b := &answerer{k: in.KB}
	b.wifiCards(r, nics)
	b.wifiSlot(r, nics, d, why)
	b.wifiAntennas(r, nics, d)
	b.wifiFactory(r, nics, d, why)
	b.allowlist(in, "wlan", "Wi-Fi cards")
	return []hit{{evidence: b.evidence, answers: b.list, used: b.used}}, nil
}

// radioWords is "Wi-Fi 6 (802.11ax), 2.4 + 5 GHz, 2×2, 2 streams"; what
// the capture lacks is said unknown.
func radioWords(r *report.WiFiRadio) string {
	var parts []string
	if r.Generation != "" {
		parts = append(parts, r.Generation+" ("+standards[r.Generation]+")")
	} else {
		parts = append(parts, "generation unknown")
	}
	if len(r.Bands) > 0 { // "2.4 + 5 GHz"
		parts = append(parts, strings.ReplaceAll(strings.Join(r.Bands, " + "), " GHz +", " +"))
	}
	if chainsKnown(r) {
		parts = append(parts, fmt.Sprintf("%d×%d", r.TXChains, r.RXChains))
	} else {
		parts = append(parts, "chains unknown")
	}
	if r.MaxSpatialStreams > 0 {
		parts = append(parts, streamWords(r.MaxSpatialStreams))
	}
	return strings.Join(parts, ", ")
}

// streamWords is "1 stream", "2 streams".
func streamWords(n int) string {
	if n == 1 {
		return "1 stream"
	}
	return fmt.Sprintf("%d streams", n)
}

// orList is "a", "a or b", "a, b or c".
func orList(s []string) string {
	if len(s) < 2 {
		return strings.Join(s, "")
	}
	return strings.Join(s[:len(s)-1], ", ") + " or " + s[len(s)-1]
}

// chainsKnown says whether the capture has both the radio's chain counts.
func chainsKnown(r *report.WiFiRadio) bool { return r.TXChains > 0 && r.RXChains > 0 }

// streams is how many spatial streams a radio has: its MCS maps', or else
// the fewer of its chains; 0 when unknown.
func streams(r *report.WiFiRadio) int {
	switch {
	case r.MaxSpatialStreams > 0:
		return r.MaxSpatialStreams
	case chainsKnown(r):
		return min(r.TXChains, r.RXChains)
	}
	return 0
}

// wifiCards answers what each card is.
func (b *answerer) wifiCards(r *report.Report, nics []int) {
	var parts []string
	known := len(nics) > 0
	for _, i := range nics {
		n := r.Network[i]
		name := n.Name
		if id := n.Identity; id != nil {
			name += " (" + strings.TrimSpace(id.Vendor+" "+id.Model) + ")"
		}
		at := fmt.Sprintf("network[%d].radio", i)
		rd := n.Radio
		if rd == nil || rd.Generation == "" && len(rd.Bands) == 0 {
			b.capture(Absent(at))
			parts = append(parts, name+": the capture has no radio details (nl80211 didn't describe it)")
			known = false
			continue
		}
		for _, f := range []struct {
			field string
			value any
			ok    bool
		}{{"generation", rd.Generation, rd.Generation != ""}, {"tx_chains", rd.TXChains, rd.TXChains > 0}, {"rx_chains", rd.RXChains, rd.RXChains > 0}} {
			if f.ok {
				b.capture(Present(at+"."+f.field, f.value))
			} else {
				b.capture(Absent(at + "." + f.field))
				known = false
			}
		}
		parts = append(parts, name+": "+radioWords(rd))
	}
	if len(nics) == 0 {
		parts = append(parts, "No Wi-Fi card is in the capture")
	}
	b.add("card", known, strings.Join(parts, "; "))
}

// wlanPlace says where a wireless NIC is: "slot", "usb", "onboard", or
// "unknown" when the capture doesn't say.
func wlanPlace(r *report.Report, n *report.NIC) string {
	if n.Bus == "usb" {
		return "usb"
	}
	p, _ := pciOf(r, n.BusAddress)
	switch {
	case p == nil || p.Mounting == nil:
		return "unknown"
	case p.Mounting.Kind == "slot", p.Mounting.Kind == "onboard":
		return p.Mounting.Kind
	}
	return "unknown"
}

// notInSlot is why a NIC isn't the WLAN slot's card, or "" when it may be.
func notInSlot(place string) string {
	switch place {
	case "usb":
		return "is a USB adapter, not in the WLAN slot"
	case "onboard":
		return "is soldered on, not in the WLAN slot"
	}
	return ""
}

// ifInSlot qualifies what is said of a card whose slot the capture
// doesn't give.
func ifInSlot(place string) string {
	if place == "unknown" {
		return ", if it's the card in the WLAN slot"
	}
	return ""
}

// wifiSlot answers where each card sits: the firmware's slot for it, and
// the model's WLAN slot from its documents.
func (b *answerer) wifiSlot(r *report.Report, nics []int, d *wlanData, why string) {
	var parts []string
	documented := d != nil && len(d.M2.Count)+len(d.M2.Lengths)+len(d.M2.Interfaces) > 0
	if documented {
		if len(d.M2.Count) > 0 {
			parts = append(parts, say(b, d.M2.Count, func(n int) string {
				if n == 1 {
					return "1 M.2 WLAN slot"
				}
				return fmt.Sprintf("%d M.2 WLAN slots", n)
			}))
		}
		if len(d.M2.Lengths) > 0 {
			parts = append(parts, say(b, d.M2.Lengths, func(ls []int) string {
				s := make([]string, len(ls))
				for i, l := range ls {
					s[i] = fmt.Sprint(l)
				}
				return "M.2 " + strings.Join(s, "/")
			}))
		}
		if len(d.M2.Interfaces) > 0 {
			parts = append(parts, say(b, d.M2.Interfaces, func(is []string) string {
				s := make([]string, len(is))
				for i, v := range is {
					s[i] = wlanInterfaces[v]
				}
				return strings.Join(s, " or ")
			}))
		}
	} else {
		parts = append(parts, "The model's WLAN slot is unknown: "+cmpOr(why, "the knowledge base has no slot data for it"))
	}
	for _, i := range nics {
		n := r.Network[i]
		p, j := pciOf(r, n.BusAddress)
		switch {
		case n.Bus == "usb":
			parts = append(parts, n.Name+" is a USB adapter, not in a slot")
		case p == nil:
			b.capture(Absent(fmt.Sprintf("network[%d].bus_address", i)))
			parts = append(parts, "which slot holds "+n.Name+" isn't in the capture")
		case p.Mounting != nil && p.Mounting.Kind == "onboard":
			b.capture(Present(fmt.Sprintf("pci[%d].mounting.kind", j), p.Mounting.Kind))
			parts = append(parts, n.Name+" is soldered on: it can't be swapped")
		default:
			parts = append(parts, b.placement(fmt.Sprintf("pci[%d].mounting", j), n.Name, p.Mounting))
		}
	}
	b.add("slots", documented, strings.Join(parts, "; "))
}

// pciOf finds a PCI device by address, and its index.
func pciOf(r *report.Report, address string) (*report.PCIDevice, int) {
	for j := range r.PCI {
		if address != "" && r.PCI[j].Address == address {
			return &r.PCI[j], j
		}
	}
	return nil, -1
}

// wifiAntennas answers how many antennas reach the slot: only from a
// source, never from the card's chains; a card is compared with them only
// when it may be the slot's.
func (b *answerer) wifiAntennas(r *report.Report, nics []int, d *wlanData) {
	if d == nil || len(d.Antennas) == 0 {
		b.add("antennas", false, "How many antennas reach the WLAN slot is unknown: the knowledge base has no source on it (a card's chains say what it can use, not what is connected)")
		return
	}
	parts := []string{say(b, d.Antennas, ints("%d antenna cables reach the WLAN slot"))}
	// Sources may disagree: a card is compared with the range they give.
	byValue := func(a, c claim[int]) int { return a.Value - c.Value }
	lo, hi := slices.MinFunc(d.Antennas, byValue).Value, slices.MaxFunc(d.Antennas, byValue).Value
	for _, i := range nics {
		n := r.Network[i]
		place := wlanPlace(r, &n)
		rd := n.Radio
		switch {
		case notInSlot(place) != "":
			parts = append(parts, n.Name+" "+notInSlot(place))
		case rd == nil || !chainsKnown(rd):
			parts = append(parts, "how many of them "+n.Name+" uses is unknown: its chains aren't in the capture")
		case max(rd.TXChains, rd.RXChains) > hi:
			parts = append(parts, fmt.Sprintf("%s has %d chains, more than the antennas serve%s", n.Name, max(rd.TXChains, rd.RXChains), ifInSlot(place)))
		case max(rd.TXChains, rd.RXChains) < lo:
			parts = append(parts, fmt.Sprintf("%s uses %d of them%s", n.Name, max(rd.TXChains, rd.RXChains), ifInSlot(place)))
		case lo < hi:
			parts = append(parts, fmt.Sprintf("%s has %d chains: whether it uses them all depends on which source is right (%d to %d cables)%s",
				n.Name, max(rd.TXChains, rd.RXChains), lo, hi, ifInSlot(place)))
		default:
			parts = append(parts, fmt.Sprintf("%s's %d×%d uses them all%s", n.Name, rd.TXChains, rd.RXChains, ifInSlot(place)))
		}
	}
	b.add("antennas", true, strings.Join(parts, "; "))
}

// optionStreams is a factory card's streams: the fewer of its chains.
func optionStreams(o factoryOption) int {
	tx, rx, _ := strings.Cut(o.Chains, "x")
	t, _ := strconv.Atoi(tx)
	x, _ := strconv.Atoi(rx)
	return min(t, x)
}

// wifiFactory answers what the vendor's cards would gain over a card that
// may be in the slot, in generation and streams; anything beyond them (a
// newer generation) is the documents' silence, so unknown.
func (b *answerer) wifiFactory(r *report.Report, nics []int, d *wlanData, why string) {
	if d == nil || len(d.FactoryOptions) == 0 {
		b.add("upgrade", false, "Which cards fit the WLAN slot is unknown: "+cmpOr(why, "the knowledge base lists no cards for it")+". A card beyond them would need the slot's key, its bus and the vendor's allow-list checked")
		return
	}
	var best factoryOption // the newest, then the most streams
	text := say(b, d.FactoryOptions, func(os []factoryOption) string {
		s := make([]string, len(os))
		for i, o := range os {
			s[i] = fmt.Sprintf("%s (%s, %s)", o.Name, o.Generation, strings.Replace(o.Chains, "x", "×", 1))
			g, bg := generationRank(o.Generation), generationRank(best.Generation)
			if g > bg || g == bg && optionStreams(o) > optionStreams(best) {
				best = o
			}
		}
		return "the vendor fitted " + strings.Join(s, "; ")
	})
	text = strings.ToUpper(text[:1]) + text[1:]
	gained := false
	// What isn't in the documents is newer than both the newest card listed
	// and the newest that may be in the slot.
	newest := generationRank(best.Generation)
	for _, i := range nics {
		n := r.Network[i]
		place := wlanPlace(r, &n)
		if notInSlot(place) != "" {
			text += "; " + n.Name + " " + notInSlot(place) + ", so they don't replace it"
			continue
		}
		rd := n.Radio
		if rd != nil {
			newest = max(newest, generationRank(rd.Generation))
		}
		if rd == nil {
			text += "; what they would gain over " + n.Name + " is unknown: its radio isn't in the capture"
			continue
		}
		var gains, unknown []string
		switch g, c := generationRank(best.Generation), generationRank(rd.Generation); {
		case rd.Generation == "":
			unknown = append(unknown, "generation")
		case g > c:
			gains = append(gains, best.Generation+" instead of "+rd.Generation)
		}
		switch s, c := optionStreams(best), streams(rd); {
		case c == 0:
			unknown = append(unknown, "streams")
		case s > c:
			gains = append(gains, fmt.Sprintf("%s instead of %d", streamWords(s), c))
		}
		switch {
		case len(gains) > 0:
			gained = true
			text += fmt.Sprintf("; the %s would give %s %s", best.Name, n.Name, strings.Join(gains, " and "))
			if generationRank(best.Generation) < generationRank(rd.Generation) {
				text += " (but " + best.Generation + ", older than its " + rd.Generation + ")"
			}
		case len(unknown) == 0:
			text += fmt.Sprintf("; none is newer than %s's %s or has more than its %s, so none gains", n.Name, rd.Generation, streamWords(streams(rd)))
		}
		if len(unknown) > 0 {
			text += "; whether one gains in " + strings.Join(unknown, " or ") + " can't be told: the capture lacks " + n.Name + "'s " + strings.Join(unknown, " and ")
		}
		text += ifInSlot(place)
	}
	if gained {
		text += "; what a card gains in use depends on the access point"
	}
	if newer := wifiGenerations[newest+1:]; len(newer) > 0 {
		text += ". Whether a newer card (" + orList(newer) + ") fits isn't in the documents: it needs the slot's key, a bus the chipset supports and suitable antennas (6 GHz-capable for Wi-Fi 6E and 7)"
	}
	b.add("upgrade", false, text)
}
