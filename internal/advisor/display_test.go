package advisor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jiegui2025/hwspec/internal/kb"
	"github.com/jiegui2025/hwspec/internal/report"
)

// The display_outputs (devices) and display_ports (models) groups are
// read strictly (#25 part 4): every leaf's values checked, an empty group
// and unknown leaves refused.
func TestDisplayGroupValidation(t *testing.T) {
	for group, want := range map[string]string{
		`{"outputs": [{"value": [{"type": "usb-c", "width": 1, "height": 1, "refresh_hz": 1}], "src": "x"}]}`: `outputs: "usb-c" isn't one of dp, hdmi, edp, dvi, vga`,
		`{"outputs": [{"value": [{"type": "dp", "width": 1, "height": 1, "refresh_hz": 1},
			{"type": "dp", "width": 2, "height": 2, "refresh_hz": 2}], "src": "x"}]}`: "outputs: dp is given twice in one claim",
		`{"outputs": [{"value": [{"type": "dp", "width": 0, "height": 2304, "refresh_hz": 60}], "src": "x"}]}`:       "outputs: dp: 0×2304 @ 60 Hz isn't positive",
		`{"outputs": [{"value": [{"type": "dp", "width": 4096, "height": -1, "refresh_hz": 60}], "src": "x"}]}`:      "outputs: dp: 4096×-1 @ 60 Hz isn't positive",
		`{"outputs": [{"value": [{"type": "dp", "width": 4096, "height": 2304, "refresh_hz": 0}], "src": "x"}]}`:     "outputs: dp: 4096×2304 @ 0 Hz isn't positive",
		`{"max_displays": [{"value": 0, "src": "x"}]}`:                                                               "max_displays: 0 isn't positive",
		`{"outputs": [{"value": [{"type": "dp", "width": 1, "height": 1, "refresh_hz": 1, "bpc": 8}], "src": "x"}]}`: `json: unknown field "bpc"`,
		`{"outputs": [{"value": [], "src": "x"}]}`:                                                                   "outputs: an empty list",
		`{}`: "no claims",
	} {
		d := kb.Device{Data: map[string]json.RawMessage{"display_outputs": json.RawMessage(group)}}
		if errs := ValidateDevice(&d); len(errs) != 1 || !strings.Contains(errs[0].Error(), "data.display_outputs: "+want) {
			t.Errorf("%s: %v", group, errs)
		}
	}
	// One claim may not give a type twice; two sources may each give it.
	ok := `{"outputs": [{"value": [{"type": "dp", "width": 4096, "height": 2304, "refresh_hz": 60}], "src": "x"},
		{"value": [{"type": "dp", "width": 3840, "height": 2160, "refresh_hz": 60}], "src": "y"}], "max_displays": [{"value": 3, "src": "x"}]}`
	if errs := ValidateDevice(&kb.Device{Data: map[string]json.RawMessage{"display_outputs": json.RawMessage(ok)}}); errs != nil {
		t.Errorf("a full group: %v", errs)
	}
	bad := map[string]json.RawMessage{"display_outputs": json.RawMessage(`{}`)}
	if errs := ValidateCPU(&kb.CPU{Data: bad}); len(errs) != 1 || !strings.Contains(errs[0].Error(), "data.display_outputs: no claims") {
		t.Errorf("a CPU's group: %v", errs)
	}
	if errs := ValidateDevice(&kb.Device{Data: map[string]json.RawMessage{"rated": json.RawMessage(`{}`)}}); errs != nil {
		t.Errorf("a group no check reads: %v", errs)
	}

	for group, want := range map[string]string{
		`{"ports": [{"value": [{"type": "thunderbolt", "count": 1}], "src": "x"}]}`:             `ports: "thunderbolt" isn't one of dp, hdmi, dvi, vga, usb-c`,
		`{"ports": [{"value": [{"type": "dp", "count": 0}], "src": "x"}]}`:                      "ports: dp: count 0 isn't positive",
		`{"ports": [{"value": [{"type": "hdmi", "version": "2", "count": 1}], "src": "x"}]}`:    `ports: hdmi: version "2" isn't N.N`,
		`{"ports": [{"value": [{"type": "hdmi", "version": "v2.0", "count": 1}], "src": "x"}]}`: `ports: hdmi: version "v2.0" isn't N.N`,
		`{"ports": [{"value": [{"type": "dp", "count": 1, "rear": true}], "src": "x"}]}`:        `json: unknown field "rear"`,
		`{"ports": [{"value": [], "src": "x"}]}`:                                                "ports: an empty list",
		`{}`:                                                                                    "no claims",
	} {
		m := kb.Model{Data: map[string]json.RawMessage{"display_ports": json.RawMessage(group)}}
		if errs := ValidateModel(&m, nil); len(errs) != 1 || !strings.Contains(errs[0].Error(), "data.display_ports: "+want) {
			t.Errorf("%s: %v", group, errs)
		}
	}
	ok = `{"ports": [{"value": [{"type": "dp", "version": "1.2", "count": 2}, {"type": "vga", "count": 1, "optional": true}], "src": "x"}]}`
	if errs := ValidateModel(&kb.Model{Data: map[string]json.RawMessage{"display_ports": json.RawMessage(ok)}}, nil); errs != nil {
		t.Errorf("a full group: %v", errs)
	}
}

// displayKB is the check's example knowledge with its processor entry;
// ports and outputs, when given, replace the example's groups ("-"
// removes one).
func displayKB(ports, outputs string) *kb.KB {
	rule, _ := checks["display-upgrade"].example()
	k := exampleKnowledge("display-upgrade", rule)
	k.CPUs = []kb.CPU{exampleCPU()}
	for _, g := range []struct {
		data       map[string]json.RawMessage
		name, with string
	}{{k.Models[0].Data, "display_ports", ports}, {k.CPUs[0].Data, "display_outputs", outputs}} {
		switch g.with {
		case "":
		case "-":
			delete(g.data, g.name)
		default:
			g.data[g.name] = json.RawMessage(g.with)
		}
	}
	return k
}

const (
	displayPortsDoc = "2 DisplayPort 1.2; optional: 1 HDMI 2.0 per example-datasheet (2019-12). Which port a connector is can't be told from the capture"
	displayGPUMax   = "card1-DP-3: CoffeeLake-S GT2 [UHD Graphics 630] drives up to 4096×2304 @ 60 Hz on DisplayPort per example-cpu; CoffeeLake-S GT2 [UHD Graphics 630] drives 3 displays at once per example-cpu"
	displayDPMax    = "4096×2304 @ 60 Hz on DisplayPort per example-cpu"
	displayCant     = "card1-DP-3: whether a better display is possible can't be told: "
)

// displayVerdictOf is the verdict answer for a report and knowledge base.
func displayVerdictOf(t *testing.T, r *report.Report, k *kb.KB) Answer {
	t.Helper()
	return wifiAnswers(t, r, k)["verdict"]
}

// #109's acceptance on the reference machine: the display's native mode,
// the best its output offers, and the UHD 630's DisplayPort maximum from
// its processor's entry, cited. A display with more pixels is within what
// the GPU drives, but whether the port and cable carry it isn't known
// (#264 round 1, B1): the verdict names the model's DisplayPort ports.
func TestDisplayReferenceMachine(t *testing.T) {
	got := wifiAnswers(t, displayExample(), displayKB("", ""))
	wantAnswers(t, got, map[string]string{
		"display": "card1-DP-3 (Dell DELL U2720Q): native 3840×2160 @ 60 Hz, 27.2 in",
		"offered": "card1-DP-3 offers up to 3840×2160 (42 modes)",
		"gpu":     displayGPUMax,
		"ports":   displayPortsDoc,
		"verdict": "card1-DP-3: a display with more pixels than the native 3840×2160 @ 60 Hz is within what the GPU drives (" + displayDPMax +
			"), if the port and cable carry it, which the knowledge base doesn't say: this model's DisplayPort ports are 2 DisplayPort 1.2 per example-datasheet (2019-12)",
	}, map[string]bool{"display": true, "offered": true, "gpu": true, "ports": true, "verdict": false})
}

// The verdicts (#264 round 1): only "no larger display" is known; one
// side beyond the maximum, a maximum at a lower refresh, a native mode
// the output doesn't offer and an internal panel are unknown, each with
// its reason; a model without ports of the type names none.
func TestDisplayVerdicts(t *testing.T) {
	for _, c := range []struct {
		name  string
		edit  func(d *report.Display)
		ports string
		want  string
		known bool
	}{
		{"as large", func(d *report.Display) { d.NativeWidth, d.NativeHeight, d.BestMode = 4096, 2304, "4096x2304" }, "",
			"card1-DP-3: no display with more pixels is possible: the native 4096×2304 @ 60 Hz is already at least the most the GPU drives (" + displayDPMax + ")", true},
		{"larger, modes unread", func(d *report.Display) { d.NativeWidth, d.NativeHeight, d.BestMode = 5120, 2880, "" }, "",
			"card1-DP-3: no display with more pixels is possible: the native 5120×2880 @ 60 Hz is already at least the most the GPU drives (" + displayDPMax + ")", true},
		// The output offering more than the cited maximum contradicts the
		// source (#264 round 2, R1).
		{"larger, offered", func(d *report.Display) { d.NativeWidth, d.NativeHeight, d.BestMode = 5120, 2880, "5120x2880" }, "",
			displayCant + "the output offers 5120×2880, beyond the most the GPU drives (" + displayDPMax + "), so the capture contradicts the source", false},
		{"offered wider than the maximum", func(d *report.Display) { d.NativeWidth, d.NativeHeight, d.BestMode = 4096, 2304, "5120x2304" }, "",
			displayCant + "the output offers 5120×2304, beyond the most the GPU drives (" + displayDPMax + "), so the capture contradicts the source", false},
		{"offered taller than the maximum", func(d *report.Display) { d.NativeWidth, d.NativeHeight, d.BestMode = 4096, 2304, "4096x2880" }, "",
			displayCant + "the output offers 4096×2880, beyond the most the GPU drives (" + displayDPMax + "), so the capture contradicts the source", false},
		{"wider only", func(d *report.Display) { d.NativeWidth, d.NativeHeight, d.BestMode = 5120, 1440, "" }, "",
			displayCant + "the native 5120×1440 @ 60 Hz is beyond the most the GPU drives (" + displayDPMax + ") on one side, which the source doesn't cover", false},
		{"taller only", func(d *report.Display) { d.NativeWidth, d.NativeHeight, d.BestMode = 2560, 2880, "" }, "",
			displayCant + "the native 2560×2880 @ 60 Hz is beyond the most the GPU drives (" + displayDPMax + ") on one side, which the source doesn't cover", false},
		{"not offered", func(d *report.Display) { d.BestMode = "2560x1440" }, "",
			displayCant + "the output doesn't offer the display's native 3840×2160 (the best it offers is 2560×1440): the port, cable or link may limit it", false},
		{"not offered, taller", func(d *report.Display) { d.BestMode = "3840x1080" }, "",
			displayCant + "the output doesn't offer the display's native 3840×2160 (the best it offers is 3840×1080): the port, cable or link may limit it", false},
		{"not offered, narrower", func(d *report.Display) { d.BestMode = "2560x2160" }, "",
			displayCant + "the output doesn't offer the display's native 3840×2160 (the best it offers is 2560×2160): the port, cable or link may limit it", false},
		{"interlaced best", func(d *report.Display) { d.BestMode = "3840x2160i" }, "-",
			"card1-DP-3: a display with more pixels than the native 3840×2160 @ 60 Hz is within what the GPU drives (" + displayDPMax + "), if the port and cable carry it, which the knowledge base doesn't say", false},
		{"no modes read", func(d *report.Display) { d.BestMode = "" }, `{"ports": [{"value": [{"type": "vga", "count": 1}], "src": "example-datasheet"}]}`,
			"card1-DP-3: a display with more pixels than the native 3840×2160 @ 60 Hz is within what the GPU drives (" + displayDPMax + "), if the port and cable carry it, which the knowledge base doesn't say", false},
		{"USB-C ports", nil, `{"ports": [{"value": [{"type": "usb-c", "count": 1}, {"type": "hdmi", "count": 1}], "src": "example-datasheet"}]}`,
			"card1-DP-3: a display with more pixels than the native 3840×2160 @ 60 Hz is within what the GPU drives (" + displayDPMax + "), if the port and cable carry it, which the knowledge base doesn't say" +
				": this model's DisplayPort ports are 1 USB-C (DisplayPort) per example-datasheet (2019-12)", false},
		{"HDMI at 24 Hz", func(d *report.Display) {
			d.Connector, d.NativeWidth, d.NativeHeight, d.BestMode = "card1-HDMI-A-1", 1920, 1080, "1920x1080"
		}, "",
			"card1-HDMI-A-1: whether a better display is possible can't be told: the GPU's cited maximum (4096×2304 @ 24 Hz on HDMI per example-cpu) is only at 24 Hz; at 60 Hz the source doesn't say", false},
		{"an internal panel", func(d *report.Display) { d.Connector = "card1-eDP-1" }, "",
			"card1-eDP-1 is an internal panel: a replacement panel needs the model's parts data, which the knowledge base doesn't have", false},
	} {
		r := displayExample()
		if c.edit != nil {
			c.edit(&r.Displays[0])
		}
		if got := displayVerdictOf(t, r, displayKB(c.ports, "")); got.Text != c.want || got.Known != c.known {
			t.Errorf("%s:\n got %q (known %v)\nwant %q", c.name, got.Text, got.Known, c.want)
		}
	}
}

// Sources that disagree: all are shown, and the smallest maximum (by
// area, then width) decides.
func TestDisplaySourcesDisagree(t *testing.T) {
	two := `{"outputs": [{"value": [{"type": "dp", "width": 4096, "height": 2304, "refresh_hz": 60}], "src": "example-cpu"},
		{"value": [{"type": "dp", "width": 5120, "height": 1440, "refresh_hz": 60}], "src": "example-datasheet"},
		{"value": [{"type": "dp", "width": 3840, "height": 1920, "refresh_hz": 60}], "src": "example-datasheet"}]}`
	r := displayExample()
	r.Displays[0].NativeWidth, r.Displays[0].NativeHeight, r.Displays[0].BestMode = 5120, 1440, ""
	got := wifiAnswers(t, r, displayKB("", two))
	wantAnswers(t, got, map[string]string{
		"gpu": "card1-DP-3: CoffeeLake-S GT2 [UHD Graphics 630] drives up to 5120×1440 @ 60 Hz on DisplayPort per example-datasheet (2019-12);" +
			" 3840×1920 @ 60 Hz on DisplayPort per example-datasheet (2019-12); 4096×2304 @ 60 Hz on DisplayPort per example-cpu",
		// 5120×1440 and 3840×1920 have the same area: the narrower decides.
		"verdict": displayCant + "the native 5120×1440 @ 60 Hz is beyond the most the GPU drives (3840×1920 @ 60 Hz on DisplayPort per example-datasheet) on one side, which the source doesn't cover",
	}, map[string]bool{"verdict": false})
}

// The smaller area decides even when it's the wider maximum.
func TestDisplaySmallestByArea(t *testing.T) {
	two := `{"outputs": [{"value": [{"type": "dp", "width": 3840, "height": 2160, "refresh_hz": 60}], "src": "example-cpu"},
		{"value": [{"type": "dp", "width": 5120, "height": 1440, "refresh_hz": 60}], "src": "example-datasheet"}]}`
	r := displayExample()
	r.Displays[0].NativeWidth, r.Displays[0].NativeHeight, r.Displays[0].BestMode = 5120, 1440, "5120x1440"
	if got := displayVerdictOf(t, r, displayKB("", two)); !got.Known || !strings.HasSuffix(got.Text, "(5120×1440 @ 60 Hz on DisplayPort per example-datasheet)") {
		t.Errorf("by area: %+v", got)
	}
}

// What can't be told is said, never guessed: "GPU maximum unknown" with
// the reason, and the verdict unknown with it. The UHD 630's maxima are
// its processor's: another processor with the same GPU gets none of them
// (#264 round 1, I5).
func TestDisplayUnknowns(t *testing.T) {
	noVerdict := displayCant + "the GPU's maximum is unknown"
	other := func(r *report.Report) { r.CPU.Identity.Model = "Intel(R) Core(TM) i7-8700 CPU @ 3.20GHz" }
	for _, c := range []struct {
		name    string
		edit    func(r *report.Report)
		k       *kb.KB
		gpu     string
		verdict string
	}{
		{"another processor", other, displayKB("", ""),
			"card1-DP-3: GPU maximum unknown: it's the processor's GPU, and the knowledge base has no entry for processor i7-8700", noVerdict},
		{"no processor number", func(r *report.Report) { r.CPU.Identity = nil }, displayKB("", ""),
			"card1-DP-3: GPU maximum unknown: it's the processor's GPU, and the knowledge base has no entry for processor (its number isn't in the capture)", noVerdict},
		{"no outputs group", nil, displayKB("", "-"),
			"card1-DP-3: GPU maximum unknown: the knowledge base has no output data for processor i5-9500T", noVerdict},
		{"a group this build can't read", nil, displayKB("", `{"outputs": [{"value": [{"type": "dp", "width": 1, "height": 1, "refresh_hz": 1, "bpc": 8}], "src": "example-cpu"}]}`),
			`card1-DP-3: GPU maximum unknown: this build can't read the knowledge base's output data for processor i5-9500T (update hwspec): json: unknown field "bpc"`, noVerdict},
		{"no maximum for the type", func(r *report.Report) { r.Displays[0].Connector = "card1-DVI-D-1" }, displayKB("", ""),
			"card1-DVI-D-1: GPU maximum unknown: the knowledge base gives no DVI maximum for this GPU; CoffeeLake-S GT2 [UHD Graphics 630] drives 3 displays at once per example-cpu",
			"card1-DVI-D-1: whether a better display is possible can't be told: the GPU's maximum is unknown"},
		{"an output type it doesn't know", func(r *report.Report) { r.Displays[0].Connector = "card1-LVDS-1" }, displayKB("", ""),
			"card1-LVDS-1: GPU maximum unknown: the knowledge base has no output type for card1-LVDS-1",
			"card1-LVDS-1: whether a better display is possible can't be told: the GPU's maximum is unknown"},
		{"no GPU for the card", func(r *report.Report) { r.GPUs[0].DRMCard = "card0" }, displayKB("", ""),
			"card1-DP-3: GPU maximum unknown: the capture names no GPU for card1", noVerdict},
		{"a connector without a card", func(r *report.Report) { r.Displays[0].Connector = "" }, displayKB("", ""),
			": GPU maximum unknown: the capture names no GPU for this connector", ": whether a better display is possible can't be told: the GPU's maximum is unknown"},
		{"no native mode", func(r *report.Report) { r.Displays[0].NativeWidth = 0 }, displayKB("", ""),
			displayGPUMax, displayCant + "the display's native mode is unknown"},
		{"no native height", func(r *report.Report) { r.Displays[0].NativeHeight = 0 }, displayKB("", ""),
			displayGPUMax, displayCant + "the display's native mode is unknown"},
	} {
		r := displayExample()
		if c.edit != nil {
			c.edit(r)
		}
		got := wifiAnswers(t, r, c.k)
		if got["gpu"].Text != c.gpu || got["verdict"].Text != c.verdict || got["verdict"].Known {
			t.Errorf("%s:\ngpu     %q\nwant    %q\nverdict %q\nwant    %q", c.name, got["gpu"].Text, c.gpu, got["verdict"].Text, c.verdict)
		}
	}

	r := displayExample()
	r.Displays[0].NativeHeight = 0
	if got := wifiAnswers(t, r, displayKB("", ""))["display"]; got.Known || !strings.HasSuffix(got.Text, "its native mode isn't in the capture (no EDID was read)") {
		t.Errorf("no native height: %+v", got)
	}

	// No EDID, no modes, no identity, a GPU named by ID only, no ports.
	r = displayExample()
	r.Displays[0] = report.Display{Connector: "card1-DP-3"}
	r.GPUs[0].Identity = nil
	got := wifiAnswers(t, r, displayKB("-", ""))
	wantAnswers(t, got, map[string]string{
		"display": "card1-DP-3: its native mode isn't in the capture (no EDID was read)",
		"offered": "card1-DP-3: the modes it offers aren't in the capture (one from before hwspec read them, or the driver listed none)",
		"gpu":     "card1-DP-3: GPU 8086:3e92 drives up to " + displayDPMax + "; GPU 8086:3e92 drives 3 displays at once per example-cpu",
		"ports":   "The model's display ports are unknown: the knowledge base has no display port data for this model",
	}, map[string]bool{"display": false, "offered": false, "gpu": true, "ports": false})

	k := displayKB(`{"ports": [{"value": [{"type": "dp", "count": 1, "rear": true}], "src": "example-datasheet"}]}`, "")
	if got := wifiAnswers(t, displayExample(), k)["ports"].Text; got != `The model's display ports are unknown: this build can't read the knowledge base's display port data for this model (update hwspec): json: unknown field "rear"` {
		t.Errorf("unreadable ports: %q", got)
	}
	r = displayExample()
	r.System.Identity = nil
	if got := wifiAnswers(t, r, displayKB("", ""))["ports"].Text; got != "The model's display ports are unknown: the knowledge base has no entry for this model" {
		t.Errorf("no model: %q", got)
	}
}

// A discrete GPU's maxima are its device entry's; one for its subsystem
// (the board maker's) wins over one for every board using the chip.
func TestDisplayDiscreteGPU(t *testing.T) {
	r := displayExample()
	r.GPUs[0].PCIAddress, r.GPUs[0].VendorID, r.GPUs[0].DeviceID, r.GPUs[0].Identity = "0000:01:00.0", "1002", "67ff", nil
	k := displayKB("", "")
	if got := wifiAnswers(t, r, k)["gpu"].Text; got != "card1-DP-3: GPU maximum unknown: the knowledge base has no entry for GPU 1002:67ff" {
		t.Errorf("no device entry: %q", got)
	}
	chip := kb.Device{ID: "amd.chip", Match: kb.DeviceMatch{Bus: "pci", ID: "1002:67ff"},
		Data: map[string]json.RawMessage{"display_outputs": json.RawMessage(`{"outputs": [{"value": [{"type": "dp", "width": 5120, "height": 2880, "refresh_hz": 60}], "src": "example-cpu"}]}`)}}
	board := kb.Device{ID: "amd.board", Match: kb.DeviceMatch{Bus: "pci", ID: "1002:67ff:103c:8595"},
		Data: map[string]json.RawMessage{"display_outputs": json.RawMessage(`{"outputs": [{"value": [{"type": "dp", "width": 3840, "height": 2160, "refresh_hz": 60}], "src": "example-datasheet"}]}`)}}
	k.Devices = []kb.Device{chip}
	if got := wifiAnswers(t, r, k)["gpu"].Text; got != "card1-DP-3: GPU 1002:67ff drives up to 5120×2880 @ 60 Hz on DisplayPort per example-cpu" {
		t.Errorf("chip entry: %q", got)
	}
	k.Devices = []kb.Device{chip, board}
	r.PCI = []report.PCIDevice{{Address: "0000:01:00.0", VendorID: "1002", DeviceID: "67ff", SubVendorID: "103c", SubDeviceID: "8595", ClassCode: "030000"}}
	if got := wifiAnswers(t, r, k)["gpu"].Text; got != "card1-DP-3: GPU 1002:67ff drives up to 3840×2160 @ 60 Hz on DisplayPort per example-datasheet (2019-12)" {
		t.Errorf("subsystem entry: %q", got)
	}
	// Another vendor's GPU at 00:02.0 isn't the processor's.
	r = displayExample()
	r.GPUs[0].VendorID = "1002"
	if got := wifiAnswers(t, r, displayKB("", ""))["gpu"].Text; got != "card1-DP-3: GPU maximum unknown: the knowledge base has no entry for GPU 1002:3e92" {
		t.Errorf("another vendor at 00:02.0: %q", got)
	}
	// An Intel GPU behind a port is discrete (Arc).
	r = displayExample()
	r.GPUs[0].PCIAddress = "0000:03:00.0"
	if got := wifiAnswers(t, r, displayKB("", ""))["gpu"].Text; got != "card1-DP-3: GPU maximum unknown: the knowledge base has no entry for GPU 8086:3e92" {
		t.Errorf("Intel behind a port: %q", got)
	}
}

// No display and no port data: no finding. Ports without a display: the
// ports alone. Only optional ports. More displays than the GPU drives,
// one unknown; a display without a refresh rate.
func TestDisplayEdges(t *testing.T) {
	r := displayExample()
	r.Displays = nil
	if a := Advise(Input{Report: r, KB: displayKB("-", ""), Now: noon}); len(a.Findings) != 0 {
		t.Errorf("nothing to say: %+v", a.Findings)
	}
	got := wifiAnswers(t, r, displayKB("", ""))
	wantAnswers(t, got, map[string]string{"display": "No connected display is in the capture", "ports": displayPortsDoc}, nil)
	if len(got) != 2 {
		t.Errorf("ports alone: %+v", got)
	}

	// Outputs without a display count: no count said.
	only := `{"outputs": [{"value": [{"type": "dp", "width": 4096, "height": 2304, "refresh_hz": 60}], "src": "example-cpu"}]}`
	if got := wifiAnswers(t, displayExample(), displayKB("", only))["gpu"].Text; got != "card1-DP-3: CoffeeLake-S GT2 [UHD Graphics 630] drives up to "+displayDPMax {
		t.Errorf("no display count: %q", got)
	}

	k := displayKB(`{"ports": [{"value": [{"type": "usb-c", "count": 1, "optional": true}], "src": "example-datasheet"}]}`, "")
	if got := wifiAnswers(t, displayExample(), k)["ports"].Text; !strings.HasPrefix(got, "optional: 1 USB-C (DisplayPort) per") {
		t.Errorf("only optional: %q", got)
	}

	r = displayExample()
	for _, c := range []string{"card1-HDMI-A-1", "card1-DP-1", "card1-DP-2"} {
		r.Displays = append(r.Displays, report.Display{Connector: c, NativeWidth: 1920, NativeHeight: 1080})
	}
	r.Displays[0].Identity = &report.Identity{}
	got = wifiAnswers(t, r, displayKB("", ""))
	wantAnswers(t, got, map[string]string{
		"display": "card1-DP-3: native 3840×2160 @ 60 Hz, 27.2 in; card1-HDMI-A-1: native 1920×1080; card1-DP-1: native 1920×1080; card1-DP-2: native 1920×1080",
	}, map[string]bool{"display": true, "offered": false, "gpu": false, "verdict": false})
	if g := got["gpu"].Text; strings.Count(g, "displays at once") != 1 || !strings.HasSuffix(g, "drives 3 displays at once per example-cpu; 4 are connected to it, more than that") {
		t.Errorf("four displays: %q", g)
	}
	if v := got["verdict"].Text; !strings.Contains(v, "; card1-HDMI-A-1: a display with more pixels than the native 1920×1080 is within what the GPU drives (4096×2304 @ 24 Hz on HDMI per example-cpu)") {
		t.Errorf("no refresh rate: %q", v)
	}
}

// As many displays as the GPU drives is fine; a later display with no
// maximum doesn't lose the GPU's count. The native refresh is evidence
// only when known.
func TestDisplayCount(t *testing.T) {
	r := displayExample()
	r.Displays = append(r.Displays, report.Display{Connector: "card1-DP-1", NativeWidth: 1920, NativeHeight: 1080},
		report.Display{Connector: "card1-LVDS-1", NativeWidth: 1920, NativeHeight: 1080})
	got := wifiAnswers(t, r, displayKB("", ""))["gpu"]
	if !strings.HasSuffix(got.Text, "; CoffeeLake-S GT2 [UHD Graphics 630] drives 3 displays at once per example-cpu") {
		t.Errorf("three displays: %+v", got)
	}
	a := Advise(Input{Report: r, KB: displayKB("", ""), Now: noon})
	var refresh []string
	for _, e := range a.Findings[0].Evidence {
		if strings.HasSuffix(e.path, "native_refresh_hz") {
			refresh = append(refresh, e.path)
		}
	}
	if len(refresh) != 1 || refresh[0] != "displays[0].native_refresh_hz" {
		t.Errorf("refresh evidence: %q", refresh)
	}
}

// The connector names DRM gives, and the types they map to.
func TestConnectorType(t *testing.T) {
	for in, want := range map[string][2]string{
		"card1-DP-3": {"card1", "dp"}, "card0-eDP-1": {"card0", "edp"}, "card1-HDMI-A-1": {"card1", "hdmi"}, "card2-HDMI-B-1": {"card2", "hdmi"},
		"card0-DVI-D-1": {"card0", "dvi"}, "card0-DVI-I-2": {"card0", "dvi"}, "card0-VGA-1": {"card0", "vga"},
		"card0-LVDS-1": {"card0", ""}, "card0-Virtual-1": {"card0", ""}, "card0": {"card0", ""}, "": {"", ""},
	} {
		if card, typ := connectorType(in); card != want[0] || typ != want[1] {
			t.Errorf("%q: %q, %q", in, card, typ)
		}
	}
	for in, want := range map[string][3]int{"3840x2160": {3840, 2160, 1}, "1920x1080i": {1920, 1080, 1}, "1920x1080p": {0, 0, 0}, "x": {0, 0, 0}, "": {0, 0, 0}} {
		if w, h, ok := parseMode(in); w != want[0] || h != want[1] || ok != (want[2] == 1) {
			t.Errorf("parseMode(%q): %d, %d, %v", in, w, h, ok)
		}
	}
}
