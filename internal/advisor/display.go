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

// The display answers of #9 (#109): for each connected display, its
// native mode, the best mode its output offers, and the most the GPU
// drives on that output type, so the answer can say whether a better
// display is possible. The GPU's maxima are a display_outputs group: an
// integrated GPU's on its processor's entry, since Intel publishes them
// per processor, a discrete GPU's on its device entry. The model's ports
// are its display_ports group (#25 part 4).

// gpuOutputsData is a GPU's display_outputs group, read strictly.
type gpuOutputsData struct {
	Outputs     claims[[]gpuOutput] `json:"outputs"`
	MaxDisplays claims[int]         `json:"max_displays"`
}

// gpuOutput is the most a GPU drives on one output type.
type gpuOutput struct {
	Type      string `json:"type"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	RefreshHz int    `json:"refresh_hz"`
}

// portsData is a model's display_ports group, read strictly.
type portsData struct {
	Ports claims[[]displayPort] `json:"ports"`
}

// displayPort is one kind of display port on a model: the fitted ones,
// or a factory option.
type displayPort struct {
	Type     string `json:"type"`
	Version  string `json:"version,omitempty"`
	Count    int    `json:"count"`
	Optional bool   `json:"optional,omitempty"`
}

var (
	// outputTypes names the GPU output types; portTypes adds USB-C, a
	// port that carries DisplayPort.
	outputTypes = map[string]string{"dp": "DisplayPort", "hdmi": "HDMI", "edp": "eDP", "dvi": "DVI", "vga": "VGA"}
	portTypes   = map[string]string{"dp": "DisplayPort", "hdmi": "HDMI", "dvi": "DVI", "vga": "VGA", "usb-c": "USB-C (DisplayPort)"}
	// portsFor are the port types an output type reaches.
	portsFor = map[string][]string{"dp": {"dp", "usb-c"}, "hdmi": {"hdmi"}, "dvi": {"dvi"}, "vga": {"vga"}}
	// connectorTypes maps a DRM connector's type to an output type.
	connectorTypes = map[string]string{"DP": "dp", "eDP": "edp", "HDMI": "hdmi", "DVI": "dvi", "VGA": "vga"}
	versionRe      = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)
	modeRe         = regexp.MustCompile(`^([0-9]+)x([0-9]+)i?$`)
)

func decodeGPUOutputs(raw json.RawMessage) (*gpuOutputsData, error) {
	d, err := decodeData[gpuOutputsData](raw)
	if err != nil {
		return nil, err
	}
	if len(d.Outputs)+len(d.MaxDisplays) == 0 {
		return nil, errors.New("no claims (leave the group out when the documents say nothing)")
	}
	var errs []error
	for _, c := range d.Outputs {
		if len(c.Value) == 0 {
			errs = append(errs, errors.New("outputs: an empty list (leave the claim out)"))
		}
		seen := map[string]bool{}
		for _, o := range c.Value {
			switch {
			case outputTypes[o.Type] == "":
				errs = append(errs, fmt.Errorf("outputs: %q isn't one of dp, hdmi, edp, dvi, vga", o.Type))
			case seen[o.Type]:
				errs = append(errs, fmt.Errorf("outputs: %s is given twice in one claim", o.Type))
			case o.Width <= 0 || o.Height <= 0 || o.RefreshHz <= 0:
				errs = append(errs, fmt.Errorf("outputs: %s: %d×%d @ %d Hz isn't positive", o.Type, o.Width, o.Height, o.RefreshHz))
			}
			seen[o.Type] = true
		}
	}
	for _, c := range d.MaxDisplays {
		if c.Value <= 0 {
			errs = append(errs, fmt.Errorf("max_displays: %d isn't positive", c.Value))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return &d, nil
}

func decodePorts(raw json.RawMessage) (*portsData, error) {
	d, err := decodeData[portsData](raw)
	if err != nil {
		return nil, err
	}
	if len(d.Ports) == 0 {
		return nil, errors.New("no claims (leave the group out when the documents say nothing)")
	}
	var errs []error
	for _, c := range d.Ports {
		if len(c.Value) == 0 {
			errs = append(errs, errors.New("ports: an empty list (leave the claim out)"))
		}
		for _, p := range c.Value {
			switch {
			case portTypes[p.Type] == "":
				errs = append(errs, fmt.Errorf("ports: %q isn't one of dp, hdmi, dvi, vga, usb-c", p.Type))
			case p.Count <= 0:
				errs = append(errs, fmt.Errorf("ports: %s: count %d isn't positive", p.Type, p.Count))
			case p.Version != "" && !versionRe.MatchString(p.Version):
				errs = append(errs, fmt.Errorf("ports: %s: version %q isn't N.N (\"1.2\")", p.Type, p.Version))
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return &d, nil
}

// validateOutputs reports a display_outputs group the display check
// can't read.
func validateOutputs(data map[string]json.RawMessage) []error {
	if raw, ok := data["display_outputs"]; ok {
		if _, err := decodeGPUOutputs(raw); err != nil {
			return []error{fmt.Errorf("data.display_outputs: %w", err)}
		}
	}
	return nil
}

// ValidateDevice reports what the checks can't read in a device entry's
// groups they use (display_outputs: a discrete GPU's), so genkb refuses a
// misspelt leaf or type rather than a binary leaving the group out.
func ValidateDevice(d *kb.Device) []error { return validateOutputs(d.Data) }

// ValidateCPU does the same for a CPU entry (display_outputs: its
// integrated GPU's).
func ValidateCPU(c *kb.CPU) []error { return validateOutputs(c.Data) }

// modelPorts returns the display ports group of the capture's model
// entry, or why there is none to use.
func modelPorts(k *kb.KB, r *report.Report) (*portsData, string) {
	m := modelFor(k, r)
	if m == nil {
		return nil, "the knowledge base has no entry for this model"
	}
	raw, ok := m.Data["display_ports"]
	if !ok {
		return nil, "the knowledge base has no display port data for this model"
	}
	d, err := decodePorts(raw)
	if err != nil {
		return nil, fmt.Sprintf("this build can't read the knowledge base's display port data for this model (update hwspec): %v", err)
	}
	return d, ""
}

// integrated says whether a GPU is an Intel processor's own: Intel puts
// it at 00:02.0, and a discrete Intel GPU (Arc) sits behind a port.
func integrated(g *report.GPU) bool {
	return strings.EqualFold(g.VendorID, "8086") && strings.HasSuffix(g.PCIAddress, ":00:02.0")
}

// gpuOutputsFor returns a GPU's display_outputs group, or why there is
// none to use: an integrated GPU's from its processor's entry, a discrete
// GPU's from its device entry.
func gpuOutputsFor(k *kb.KB, r *report.Report, g *report.GPU) (*gpuOutputsData, string) {
	var data map[string]json.RawMessage
	var of string
	if integrated(g) {
		n := ""
		if r.CPU.Identity != nil {
			n = kb.ProcessorNumber(r.CPU.Identity.Model)
		}
		of = "processor " + cmpOr(n, "(its number isn't in the capture)")
		c := cpuFor(k, r)
		if c == nil {
			return nil, "it's the processor's GPU, and the knowledge base has no entry for " + of
		}
		data = c.Data
	} else {
		of = "GPU " + strings.ToLower(g.VendorID+":"+g.DeviceID)
		dev := report.PCIDevice{VendorID: g.VendorID, DeviceID: g.DeviceID}
		if p, _ := pciOf(r, g.PCIAddress); p != nil {
			dev = *p
		}
		e := pciDeviceFor(k, &dev)
		if e == nil {
			return nil, "the knowledge base has no entry for " + of
		}
		data = e.Data
	}
	raw, ok := data["display_outputs"]
	if !ok {
		return nil, "the knowledge base has no output data for " + of
	}
	d, err := decodeGPUOutputs(raw)
	if err != nil {
		return nil, fmt.Sprintf("this build can't read the knowledge base's output data for %s (update hwspec): %v", of, err)
	}
	return d, ""
}

func init() {
	register("display-upgrade", check{
		run:   displayUpgrade,
		needs: []string{"displays[].best_mode", "gpus[].drm_card"},
		example: func() (kb.Rule, *report.Report) {
			return kb.Rule{ID: "display.upgrade", Check: "display-upgrade", Category: "upgrade", Severity: "info", Title: "Display", Src: []string{kb.Capture.ID}},
				displayExample()
		},
		exampleData: func() ([]kb.Source, []kb.Model) {
			return []kb.Source{
					{ID: "example-datasheet", URL: "https://example.com/ds.pdf", Published: "2019-12", Retrieved: "2026-10-07",
						Licence: "proprietary", Confidence: "oem-doc", LinkOnly: true, Locator: "p. 2"},
					{ID: "example-cpu", URL: "https://example.com/cpu", Retrieved: "2026-10-07",
						Licence: "proprietary", Confidence: "oem-doc", LinkOnly: true, Locator: "Processor Graphics"},
				},
				[]kb.Model{{ID: "hp.example", Match: kb.ModelMatch{SysVendor: "HP", ProductName: "HP EliteDesk 800 G5 Desktop Mini"},
					Data: map[string]json.RawMessage{"display_ports": json.RawMessage(`{"ports": [{"value": [
						{"type": "dp", "version": "1.2", "count": 2},
						{"type": "hdmi", "version": "2.0", "count": 1, "optional": true}], "src": "example-datasheet"}]}`)}}}
		},
		exampleInput: func(in *Input) {
			in.KB.CPUs = []kb.CPU{exampleCPU()}
		},
	})
}

// exampleCPU is the reference machine's processor entry: its UHD 630's
// maxima.
func exampleCPU() kb.CPU {
	return kb.CPU{ID: "intel.example", Match: kb.CPUMatch{Vendor: "intel", Processor: "i5-9500T"},
		Data: map[string]json.RawMessage{"display_outputs": json.RawMessage(`{
			"outputs": [{"value": [
				{"type": "hdmi", "width": 4096, "height": 2304, "refresh_hz": 24},
				{"type": "dp", "width": 4096, "height": 2304, "refresh_hz": 60},
				{"type": "edp", "width": 4096, "height": 2304, "refresh_hz": 60}], "src": "example-cpu"}],
			"max_displays": [{"value": 3, "src": "example-cpu"}]}`)}}
}

// displayExample is the reference machine's display: a 4K monitor on the
// UHD 630's third DisplayPort.
func displayExample() *report.Report {
	return &report.Report{
		System: report.System{Identity: &report.Identity{Vendor: "HP", Model: "HP EliteDesk 800 G5 Desktop Mini"}},
		CPU:    report.CPU{Identity: &report.Identity{Vendor: "GenuineIntel", Model: "Intel(R) Core(TM) i5-9500T CPU @ 2.20GHz"}},
		GPUs: []report.GPU{{PCIAddress: "0000:00:02.0", VendorID: "8086", DeviceID: "3e92", DRMCard: "card1",
			Outputs:  []string{"DP-1", "DP-2", "DP-3", "HDMI-A-1"},
			Identity: &report.Identity{Vendor: "Intel Corporation", Model: "CoffeeLake-S GT2 [UHD Graphics 630]"}}},
		Displays: []report.Display{{Connector: "card1-DP-3", DiagonalIn: 27.2, NativeWidth: 3840, NativeHeight: 2160, NativeRefreshHz: 60,
			BestMode: "3840x2160", ModeCount: 42, Identity: &report.Identity{Vendor: "Dell", Model: "DELL U2720Q"}}},
	}
}

// connectorType splits a DRM connector name ("card1-DP-3",
// "card1-HDMI-A-1") into its card and its output type (dp); the type is
// "" for one the knowledge base has no word for (LVDS, DSI, a virtual
// connector).
func connectorType(connector string) (card, typ string) {
	card, rest, _ := strings.Cut(connector, "-")
	kind, _, _ := strings.Cut(rest, "-")
	return card, connectorTypes[kind]
}

// gpuOf finds the GPU whose DRM card drives a connector, and its index.
func gpuOf(r *report.Report, card string) (*report.GPU, int) {
	for i := range r.GPUs {
		if r.GPUs[i].DRMCard == card {
			return &r.GPUs[i], i
		}
	}
	return nil, -1
}

// parseMode reads a DRM mode name, "3840x2160" or "1920x1080i".
func parseMode(m string) (w, h int, ok bool) {
	s := modeRe.FindStringSubmatch(m)
	if s == nil {
		return 0, 0, false
	}
	w, _ = strconv.Atoi(s[1])
	h, _ = strconv.Atoi(s[2])
	return w, h, true
}

// outputMax is the most a display's GPU drives on its output type, with
// the source it's from.
type outputMax struct {
	gpuOutput
	src string
}

// displayUpgrade answers #109 for each connected display: what it is,
// what its output offers, the most the GPU drives there, the model's
// ports, and whether a better display is possible.
func displayUpgrade(in *Input, _ *kb.Rule) ([]hit, error) {
	r := in.Report
	ports, why := modelPorts(in.KB, r)
	if len(r.Displays) == 0 && ports == nil {
		return nil, nil // nothing to say about displays
	}
	b := &answerer{k: in.KB}
	b.displays(r)
	b.offered(r)
	maxima := b.gpuMaxima(r)
	b.displayPorts(ports, why)
	b.displayVerdict(r, maxima, ports)
	return []hit{{evidence: b.evidence, answers: b.list, used: b.used}}, nil
}

// modeWords is "3840×2160 @ 60 Hz".
func modeWords(w, h int, hz float64) string {
	s := fmt.Sprintf("%d×%d", w, h)
	if hz > 0 {
		s += fmt.Sprintf(" @ %g Hz", hz)
	}
	return s
}

// displays answers what each display is: its name, size and native mode
// (EDID).
func (b *answerer) displays(r *report.Report) {
	if len(r.Displays) == 0 {
		b.add("display", false, "No connected display is in the capture")
		return
	}
	var parts []string
	known := true
	for i, d := range r.Displays {
		name := d.Connector
		if id := d.Identity; id != nil && strings.TrimSpace(id.Vendor+" "+id.Model) != "" {
			name += " (" + strings.TrimSpace(id.Vendor+" "+id.Model) + ")"
		}
		if d.NativeWidth <= 0 || d.NativeHeight <= 0 {
			b.capture(Absent(fmt.Sprintf("displays[%d].native_width", i)))
			parts = append(parts, name+": its native mode isn't in the capture (no EDID was read)")
			known = false
			continue
		}
		b.capture(Present(fmt.Sprintf("displays[%d].native_width", i), d.NativeWidth), Present(fmt.Sprintf("displays[%d].native_height", i), d.NativeHeight))
		if d.NativeRefreshHz > 0 {
			b.capture(Present(fmt.Sprintf("displays[%d].native_refresh_hz", i), d.NativeRefreshHz))
		}
		text := name + ": native " + modeWords(d.NativeWidth, d.NativeHeight, d.NativeRefreshHz)
		if d.DiagonalIn > 0 {
			text += fmt.Sprintf(", %.1f in", d.DiagonalIn)
		}
		parts = append(parts, text)
	}
	b.add("display", known, strings.Join(parts, "; "))
}

// offered answers the best mode each display's output offers it (DRM
// connector modes, #112).
func (b *answerer) offered(r *report.Report) {
	if len(r.Displays) == 0 {
		return
	}
	var parts []string
	known := true
	for i, d := range r.Displays {
		if d.BestMode == "" {
			b.capture(Absent(fmt.Sprintf("displays[%d].best_mode", i)))
			parts = append(parts, d.Connector+": the modes it offers aren't in the capture (one from before hwspec read them, or the driver listed none)")
			known = false
			continue
		}
		b.capture(Present(fmt.Sprintf("displays[%d].best_mode", i), d.BestMode))
		parts = append(parts, fmt.Sprintf("%s offers up to %s (%d modes)", d.Connector, strings.Replace(d.BestMode, "x", "×", 1), d.ModeCount))
	}
	b.add("offered", known, strings.Join(parts, "; "))
}

// gpuMaxima answers the most each display's GPU drives on its output
// type, and how many displays each GPU drives against how many are
// connected to it; it returns each display's maximum (nil where unknown).
func (b *answerer) gpuMaxima(r *report.Report) []*outputMax {
	if len(r.Displays) == 0 {
		return nil
	}
	maxima := make([]*outputMax, len(r.Displays))
	var parts []string
	known := true
	type gpuUse struct {
		data      *gpuOutputsData
		connected int
	}
	uses := map[int]*gpuUse{}
	var order []int
	for i, d := range r.Displays {
		card, typ := connectorType(d.Connector)
		g, j := gpuOf(r, card)
		var why string
		var data *gpuOutputsData
		switch {
		case g == nil:
			why = "the capture names no GPU for " + cmpOr(card, "this connector")
		case typ == "":
			why = "the knowledge base has no output type for " + d.Connector
		default:
			b.capture(Present(fmt.Sprintf("gpus[%d].drm_card", j), g.DRMCard), Present(fmt.Sprintf("gpus[%d].device_id", j), g.DeviceID))
			data, why = gpuOutputsFor(b.k, r, g)
		}
		if g != nil {
			if uses[j] == nil {
				uses[j] = &gpuUse{}
				order = append(order, j)
			}
			uses[j].connected++
			if data != nil {
				uses[j].data = data
			}
		}
		var on claims[gpuOutput]
		if data != nil {
			for _, c := range data.Outputs {
				if k := slices.IndexFunc(c.Value, func(o gpuOutput) bool { return o.Type == typ }); k >= 0 {
					on = append(on, claim[gpuOutput]{Value: c.Value[k], Src: c.Src, Note: c.Note})
				}
			}
			if len(on) == 0 {
				why = "the knowledge base gives no " + outputTypes[typ] + " maximum for this GPU"
			}
		}
		if len(on) == 0 {
			parts = append(parts, d.Connector+": GPU maximum unknown: "+why)
			known = false
			continue
		}
		// The sources may differ: the smallest maximum (by area, then
		// width) is the one they all allow.
		least := slices.MinFunc(on, func(x, y claim[gpuOutput]) int {
			if a := x.Value.Width*x.Value.Height - y.Value.Width*y.Value.Height; a != 0 {
				return a
			}
			return x.Value.Width - y.Value.Width
		})
		maxima[i] = &outputMax{least.Value, least.Src}
		parts = append(parts, d.Connector+": "+gpuName(g)+" drives up to "+say(b, on, func(o gpuOutput) string {
			return modeWords(o.Width, o.Height, float64(o.RefreshHz)) + " on " + outputTypes[o.Type]
		}))
	}
	for _, j := range order {
		u := uses[j]
		if u.data == nil || len(u.data.MaxDisplays) == 0 {
			continue
		}
		text := gpuName(&r.GPUs[j]) + " drives " + say(b, u.data.MaxDisplays, ints("%d displays at once"))
		if most := slices.MinFunc(u.data.MaxDisplays, func(x, y claim[int]) int { return x.Value - y.Value }).Value; u.connected > most {
			text += fmt.Sprintf("; %d are connected to it, more than that", u.connected)
			known = false
		}
		parts = append(parts, text)
	}
	b.add("gpu", known, strings.Join(parts, "; "))
	return maxima
}

// gpuName is the GPU's model, or its PCI ID.
func gpuName(g *report.GPU) string {
	if g.Identity != nil && g.Identity.Model != "" {
		return g.Identity.Model
	}
	return "GPU " + strings.ToLower(g.VendorID+":"+g.DeviceID)
}

// portWords is "2 DisplayPort 1.2; optional: 1 HDMI 2.0".
func portWords(ps []displayPort) string {
	var fitted, optional []string
	for _, p := range ps {
		s := fmt.Sprintf("%d %s", p.Count, portTypes[p.Type])
		if p.Version != "" {
			s += " " + p.Version
		}
		if p.Optional {
			optional = append(optional, s)
		} else {
			fitted = append(fitted, s)
		}
	}
	out := strings.Join(fitted, ", ")
	if len(optional) > 0 {
		out = strings.TrimPrefix(out+"; optional: "+strings.Join(optional, ", "), "; ")
	}
	return out
}

// displayPorts answers which display ports the model has, fitted and
// optional, from its documents.
func (b *answerer) displayPorts(d *portsData, why string) {
	if d == nil {
		b.add("ports", false, "The model's display ports are unknown: "+why)
		return
	}
	b.add("ports", true, say(b, d.Ports, portWords)+". Which port a connector is can't be told from the capture")
}

// portsOf are the model's port claims for an output type, each holding
// only the ports of that type; none when the model has none of them.
func portsOf(d *portsData, typ string) claims[[]displayPort] {
	if d == nil {
		return nil
	}
	var out claims[[]displayPort]
	for _, c := range d.Ports {
		var ps []displayPort
		for _, p := range c.Value {
			if slices.Contains(portsFor[typ], p.Type) {
				ps = append(ps, p)
			}
		}
		if len(ps) > 0 {
			out = append(out, claim[[]displayPort]{Value: ps, Src: c.Src, Note: c.Note})
		}
	}
	return out
}

// displayVerdict says, for each display, whether one with more pixels is
// possible on its output. Only "no" can be known, when the display is at
// least the GPU's maximum and the output offers no more than it: the
// maximum caps every port, but which port and cable a connector has, and what they
// carry, are in neither the capture nor the knowledge base, so "possible"
// stays conditional.
func (b *answerer) displayVerdict(r *report.Report, maxima []*outputMax, ports *portsData) {
	if len(r.Displays) == 0 {
		return
	}
	var parts []string
	known := true
	for i, d := range r.Displays {
		m := maxima[i]
		_, typ := connectorType(d.Connector)
		bw, bh, offered := parseMode(d.BestMode)
		cant := d.Connector + ": whether a better display is possible can't be told: "
		var text string
		switch {
		case typ == "edp":
			text = d.Connector + " is an internal panel: a replacement panel needs the model's parts data, which the knowledge base doesn't have"
		case m == nil:
			text = cant + "the GPU's maximum is unknown"
		case d.NativeWidth <= 0 || d.NativeHeight <= 0:
			text = cant + "the display's native mode is unknown"
		case offered && (bw < d.NativeWidth || bh < d.NativeHeight):
			text = fmt.Sprintf("%sthe output doesn't offer the display's native %d×%d (the best it offers is %d×%d): the port, cable or link may limit it",
				cant, d.NativeWidth, d.NativeHeight, bw, bh)
		}
		if text != "" {
			parts = append(parts, text)
			known = false
			continue
		}
		native := modeWords(d.NativeWidth, d.NativeHeight, d.NativeRefreshHz)
		most := modeWords(m.Width, m.Height, float64(m.RefreshHz)) + " on " + outputTypes[m.Type] + " per " + m.src
		switch {
		case bw > m.Width || bh > m.Height: // 0×0 when the modes weren't read
			text = fmt.Sprintf("%sthe output offers %d×%d, beyond the most the GPU drives (%s), so the capture contradicts the source", cant, bw, bh, most)
		case d.NativeWidth >= m.Width && d.NativeHeight >= m.Height:
			parts = append(parts, fmt.Sprintf("%s: no display with more pixels is possible: the native %s is already at least the most the GPU drives (%s)", d.Connector, native, most))
			continue
		case d.NativeWidth > m.Width || d.NativeHeight > m.Height:
			text = fmt.Sprintf("%sthe native %s is beyond the most the GPU drives (%s) on one side, which the source doesn't cover", cant, native, most)
		case float64(m.RefreshHz) < d.NativeRefreshHz:
			text = fmt.Sprintf("%sthe GPU's cited maximum (%s) is only at %d Hz; at %g Hz the source doesn't say", cant, most, m.RefreshHz, d.NativeRefreshHz)
		default:
			text = fmt.Sprintf("%s: a display with more pixels than the native %s is within what the GPU drives (%s), if the port and cable carry it, which the knowledge base doesn't say", d.Connector, native, most)
			if ps := portsOf(ports, typ); len(ps) > 0 {
				text += ": this model's " + outputTypes[typ] + " ports are " + say(b, ps, portWords)
			}
		}
		parts = append(parts, text)
		known = false
	}
	b.add("verdict", known, strings.Join(parts, "; "))
}
