package advisor

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jiegui2025/hwspec/internal/collect"
	"github.com/jiegui2025/hwspec/internal/kb"
	"github.com/jiegui2025/hwspec/internal/report"
)

var noon = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func sources() []kb.Source {
	return []kb.Source{
		{ID: "forum", URL: "https://forum.example/t/1", Retrieved: "2026-10-06", Licence: "CC-BY-NC-SA-3.0",
			Confidence: "community", LinkOnly: true, Locator: "post 3"},
		{ID: "kernel", URL: "https://docs.kernel.org/x.html", Retrieved: "2026-10-06", Licence: "GPL-2.0-only",
			Confidence: "upstream-doc", Quote: "words"},
	}
}

func noDriverRule() kb.Rule {
	return kb.Rule{
		ID: "pci.no-driver", Check: "pci-without-driver", Category: "needs-attention", Severity: "warning",
		Title: "No kernel driver", Detail: "Devices need a driver.",
		Match: kb.Match{PCIClass: []string{"02", "0403", "0c03"}},
		Actions: []kb.Action{{Text: "Check the wiki", Risk: "none",
			Commands: []string{"modprobe -R {modalias}", "true"}, Undo: "modprobe -r MODULE"}},
		Src: []string{"kernel"},
	}
}

func knowledge(rules ...kb.Rule) *kb.KB {
	return &kb.KB{Format: kb.Format, Version: "2026-10-06T12:00:00Z", Sources: sources(), Rules: rules}
}

func machine() *report.Report {
	return &report.Report{OS: report.OS{ID: "cachyos", IDLike: "arch"}, PCI: []report.PCIDevice{
		{Address: "0000:00:01.0", VendorID: "8086", DeviceID: "1901", ClassCode: "060400", Class: "PCI bridge"},
		{Address: "0000:02:00.0", VendorID: "8086", DeviceID: "2723", SubVendorID: "8086", SubDeviceID: "0084",
			ClassCode: "028000", Class: "Network controller", Identity: &report.Identity{Model: "Wi-Fi 6 AX200"}},
		{Address: "0000:00:1f.6", VendorID: "8086", DeviceID: "15bc", ClassCode: "020000", Class: "Ethernet controller",
			Driver: &report.Driver{Name: "e1000e"}},
		{Address: "0000:00:1f.3", VendorID: "8086", DeviceID: "a348", SubVendorID: "103c", SubDeviceID: "8595",
			ClassCode: "040300", Class: "Audio device"},
		// Driverless, but not of the rule's classes, though their codes
		// contain them: NVMe (01 08 02) and a serial controller (07 00 02).
		{Address: "0000:03:00.0", VendorID: "144d", DeviceID: "a808", ClassCode: "010802", Class: "Non-Volatile memory controller"},
		{Address: "0000:04:00.0", VendorID: "1415", DeviceID: "c158", ClassCode: "070002", Class: "Serial controller"},
		// Upper-case hex, as a hand-edited capture might have it.
		{Address: "0000:05:00.0", VendorID: "1912", DeviceID: "0014", SubVendorID: "1912", SubDeviceID: "0014",
			ClassCode: "0C0330", Class: "USB controller"},
	}}
}

// withRun replaces pci-without-driver's run for one test, keeping its
// declarations.
func withRun(t *testing.T, run func(*Input, *kb.Rule) ([]hit, error)) {
	t.Helper()
	const name = "pci-without-driver"
	old := checks[name]
	t.Cleanup(func() { checks[name] = old })
	c := old
	c.run = run
	checks[name] = c
}

func keys(a Advice) string {
	var out []string
	for _, f := range a.Findings {
		out = append(out, f.Device.Key)
	}
	return strings.Join(out, " ")
}

// The needs-attention case #7 builds on: network, audio and USB
// controllers no driver is bound to are reported, with the evidence, what
// to do and the sources; a bridge, NVMe or serial controller without a
// driver, or a NIC with one, is not.
func TestPCIDevicesWithoutADriverNeedAttention(t *testing.T) {
	a := Advise(Input{Report: machine(), KB: knowledge(noDriverRule()), Now: noon})
	if a.AdviceVersion != Version || a.KBVersion != "2026-10-06T12:00:00Z" || a.Live || a.Redacted || len(a.Warnings) != 0 ||
		a.RulesApplied != 1 || a.RulesSkipped != 0 || len(a.CaptureSHA256) != 64 {
		t.Fatalf("header %+v", a)
	}
	if got := keys(a); got != "0000:00:1f.3 0000:02:00.0 0000:05:00.0" {
		t.Fatalf("findings for %s, want the audio, Wi-Fi and USB controllers, in key order", got)
	}
	f := a.Findings[1]
	if f.ID != "pci.no-driver" || f.Category != "needs-attention" || f.Severity != "warning" || f.Title != "No kernel driver" ||
		f.Detail == "" || f.Confidence != "upstream-doc" || len(f.Sources) != 1 || f.Sources[0].ID != "kernel" ||
		f.Sources[0].Licence != "GPL-2.0-only" {
		t.Errorf("rule fields not copied: %+v", f)
	}
	if f.Device.Kind != "pci" || f.Device.Name != "Wi-Fi 6 AX200" || a.Findings[0].Device.Name != "Audio device" {
		t.Errorf("device names: %+v, %+v", f.Device, a.Findings[0].Device)
	}
	if len(f.Evidence) != 2 || f.Evidence[0].Path() != "pci[1].class_code" || f.Evidence[1].Path() != "pci[1].driver" {
		t.Errorf("evidence %+v", f.Evidence)
	}
	if v, ok := f.Evidence[0].Value(); !ok || v != "028000" {
		t.Errorf("class code evidence %v %v", v, ok)
	}
	if _, ok := f.Evidence[1].Value(); ok {
		t.Error("an unbound driver isn't evidence of a value")
	}
	// Each finding's commands get its own device's modalias, as the kernel
	// writes it in sysfs; the knowledge base's rule keeps its placeholder.
	want := map[string]string{
		"0000:00:1f.3": "modprobe -R pci:v00008086d0000A348sv0000103Csd00008595bc04sc03i00",
		"0000:02:00.0": "modprobe -R pci:v00008086d00002723sv00008086sd00000084bc02sc80i00",
		"0000:05:00.0": "modprobe -R pci:v00001912d00000014sv00001912sd00000014bc0Csc03i30",
	}
	for _, f := range a.Findings {
		if got := f.Actions[0].Commands; len(got) != 2 || got[0] != want[f.Device.Key] || got[1] != "true" || f.Actions[0].Undo == "" {
			t.Errorf("%s: commands %q, want %q", f.Device.Key, got, want[f.Device.Key])
		}
	}
	k := knowledge(noDriverRule())
	Advise(Input{Report: machine(), KB: k, Now: noon})
	if k.Rules[0].Actions[0].Commands[0] != "modprobe -R {modalias}" {
		t.Errorf("Advise changed the knowledge base's rule: %q", k.Rules[0].Actions[0].Commands)
	}
}

// A capture can be someone else's file: none of its text reaches a shell
// line. The modalias is built from IDs that must be hex; a device whose
// IDs aren't gets no command, and a warning.
func TestCaptureTextNeverReachesACommand(t *testing.T) {
	r := &report.Report{OS: report.OS{ID: "arch"}, PCI: []report.PCIDevice{
		{Address: `0000:00:00.0/modalias)"; echo INJECTED; echo "$(id`, VendorID: "8086", DeviceID: "2723",
			SubVendorID: "8086", SubDeviceID: "0084", ClassCode: "028000", Class: "Network controller"},
		{Address: "0000:03:00.0", VendorID: `80"86`, DeviceID: "2723", SubVendorID: "8086", SubDeviceID: "0084", ClassCode: "028000"},
		{Address: "0000:04:00.0", VendorID: "8086", DeviceID: "2723", ClassCode: "028000"}, // no subsystem IDs
		{Address: "0000:05:00.0", VendorID: "8086", DeviceID: "2723", SubVendorID: "8086", SubDeviceID: "0084", ClassCode: "02800;"},
	}}
	a := Advise(Input{Report: r, KB: knowledge(noDriverRule()), Now: noon})
	var dropped int
	for _, f := range a.Findings {
		for _, c := range f.Actions[0].Commands {
			if strings.ContainsAny(c, `"$;()`) || strings.Contains(c, "INJECTED") {
				t.Errorf("%s: command %q", f.Device.Key, c)
			}
		}
		if len(f.Actions[0].Commands) == 1 { // only "true" left
			dropped++
		}
	}
	w := strings.Join(a.Warnings, "\n")
	if dropped != 3 || strings.Count(w, "needs {modalias}, which the capture doesn't give") != 3 {
		t.Errorf("dropped %d, warnings %q", dropped, a.Warnings)
	}
}

// Evidence is written as exactly {path, value} or {path, absent: true}.
func TestEvidenceHasOneShapePerMeaning(t *testing.T) {
	js, _ := json.Marshal([]Evidence{Absent("pci[1].driver"), Present("pci[1].class_code", ""), Present("x", nil)})
	want := `[{"path":"pci[1].driver","absent":true},{"path":"pci[1].class_code","value":""},{"path":"x","value":null}]`
	if string(js) != want {
		t.Errorf("JSON %s\nwant %s", js, want)
	}
	var back []Evidence
	if err := json.Unmarshal(js, &back); err != nil {
		t.Fatal(err)
	}
	if _, ok := back[0].Value(); ok || back[0].Path() != "pci[1].driver" {
		t.Errorf("absent evidence read back as %+v", back[0])
	}
	if v, ok := back[1].Value(); !ok || v != "" {
		t.Errorf("empty value read back as %v %v", v, ok)
	}
	if err := json.Unmarshal([]byte(`[{"path":1}]`), &back); err == nil {
		t.Error("malformed evidence accepted")
	}
}

// A finding is only as sure as its least sure source, and lists every
// source it used once; a source the knowledge base lacks is reported.
func TestConfidenceIsTheWeakestSourceUsed(t *testing.T) {
	old := checks["pci-without-driver"]
	withRun(t, func(in *Input, rule *kb.Rule) ([]hit, error) {
		hits, err := old.run(in, rule)
		for i := range hits {
			hits[i].used = []string{"forum", "kernel", "gone"}
		}
		return hits, err
	})
	a := Advise(Input{Report: machine(), KB: knowledge(noDriverRule()), Now: noon})
	f := a.Findings[0]
	if f.Confidence != "unknown" || len(f.Sources) != 2 || f.Sources[0].ID != "forum" || f.Sources[1].ID != "kernel" {
		t.Errorf("confidence %q, sources %+v", f.Confidence, f.Sources)
	}
	if strings.Count(strings.Join(a.Warnings, "\n"), `cites source "gone"`) != 1 {
		t.Errorf("a missing source is reported once per rule: %q", a.Warnings)
	}
	withRun(t, func(in *Input, rule *kb.Rule) ([]hit, error) {
		hits, err := old.run(in, rule)
		for i := range hits {
			hits[i].used = []string{"forum"}
		}
		return hits, err
	})
	if f := Advise(Input{Report: machine(), KB: knowledge(noDriverRule()), Now: noon}).Findings[0]; f.Confidence != "community" {
		t.Errorf("confidence %q, want community", f.Confidence)
	}
}

// What this build can't apply is a warning, counted, never a failure: the
// knowledge base's own skips, an unknown check, unusable data, and the
// capture's own warnings, since what it couldn't read can hide findings.
func TestUnusableRulesAndCaptureGapsAreWarnings(t *testing.T) {
	newer := noDriverRule()
	newer.ID, newer.Check = "gpu.newer", "driver-alternative"
	noClasses := noDriverRule()
	noClasses.ID, noClasses.Match = "pci.everything", kb.Match{}
	k := knowledge(newer, noClasses, noDriverRule())
	k.Skipped = []string{`rule "x.new" skipped: json: unknown field "kernel_below"`, `section "models" skipped`, `source "s" skipped`}
	k.SkippedRules = 1
	r := machine()
	r.Warnings = []string{"usb: permission denied"}
	a := Advise(Input{Report: r, KB: k, State: &State{}, Live: true, Now: noon})
	w := strings.Join(a.Warnings, "\n")
	for _, want := range []string{
		"capture: usb: permission denied",
		`rule "x.new" skipped`,
		`rule gpu.newer needs check "driver-alternative"`,
		"rule pci.everything skipped: match.pci_class is empty",
	} {
		if !strings.Contains(w, want) {
			t.Errorf("missing %q in %q", want, a.Warnings)
		}
	}
	// Skipped sections and sources aren't rules.
	if !a.Live || a.RulesApplied != 1 || a.RulesSkipped != 3 || len(a.Findings) != 3 {
		t.Errorf("live %v, applied %d, skipped %d, %d findings", a.Live, a.RulesApplied, a.RulesSkipped, len(a.Findings))
	}
}

// A capture without the fields a check reads (older than them, or its
// collector couldn't read them) is "can't evaluate", never "nothing found".
func TestCapturesWithoutTheFieldsCantBeEvaluated(t *testing.T) {
	a := Advise(Input{Report: &report.Report{}, KB: knowledge(noDriverRule()), Now: noon})
	if len(a.Findings) != 0 || a.RulesSkipped != 1 || len(a.Warnings) != 1 ||
		a.Warnings[0] != "rule pci.no-driver can't evaluate this capture: it needs pci[].class_code, pci[].driver, which the capture doesn't have" {
		t.Errorf("findings %d, warnings %q", len(a.Findings), a.Warnings)
	}
	if a := Advise(Input{Report: &report.Report{PCI: []report.PCIDevice{}}, KB: knowledge(noDriverRule()), Now: noon}); a.RulesApplied != 1 {
		t.Error("a capture with no PCI devices can be evaluated")
	}
}

// The most severe come first, then by category, rule and device; unknown
// values last: the same inputs always give the same document.
func TestFindingsAreOrderedMostSevereFirst(t *testing.T) {
	info := noDriverRule()
	info.ID, info.Severity = "a.info", "info"
	perf := noDriverRule()
	perf.ID, perf.Severity, perf.Category = "b.perf", "critical", "performance"
	crit := noDriverRule()
	crit.ID, crit.Severity = "z.crit", "critical"
	r := &report.Report{PCI: machine().PCI[:2]}
	var got []string
	for _, f := range Advise(Input{Report: r, KB: knowledge(info, perf, crit), Now: noon}).Findings {
		got = append(got, f.ID)
	}
	if strings.Join(got, " ") != "z.crit b.perf a.info" {
		t.Errorf("order %v", got)
	}
	if compareFindings(Finding{ID: "x"}, Finding{ID: "x", Device: &DeviceRef{Kind: "pci", Key: "1"}}) >= 0 {
		t.Error("a machine-wide finding sorts before device findings of the same rule")
	}
	if compareFindings(Finding{Severity: "info"}, Finding{Severity: "blocker"}) >= 0 {
		t.Error("an unknown severity sorts first")
	}
}

// The recorded HP EliteDesk's two driverless PCI devices are a RAM
// controller and an ISA bridge: nothing the shipped rules should flag. Its
// memory gets the upgrade answers.
func TestTheRecordedMachineNeedsNoAttention(t *testing.T) {
	r, err := collect.CollectRecorded("../collect/testdata/machines/hp-elitedesk-800-g5-mini", "test")
	if err != nil {
		t.Fatal(err)
	}
	k, err := kb.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	a := Advise(Input{Report: r, KB: k, Now: noon})
	// Firmware load failures need the root-only kernel log (#213): an
	// unprivileged capture can't be evaluated for them, and says so. The
	// linux-firmware comparisons need the firmware index (#225).
	if len(a.Findings) != 1 || a.Findings[0].ID != "memory.upgrade" || a.RulesApplied != len(k.Rules)-8 || a.RulesSkipped != 8 ||
		!slices.Contains(a.Warnings, "rule firmware.load-failed can't evaluate this capture: it needs kernel.firmware_failures, which the capture doesn't have") ||
		!slices.Contains(a.Warnings, firmwareIndexWarning(&Input{})) {
		t.Errorf("findings %+v, warnings %q", a.Findings, a.Warnings)
	}
	// Against linux-firmware 20260916, whose WHENCE lists the AX200's
	// iwlwifi-cc-a0-77.ucode as 74.8dbafb52.0, the loaded 77.8dbafb52.0 is
	// that build (#225); against LVFS (2026-10-06), the drive runs the
	// latest of Lenovo's PM981 firmware (#226).
	lf := &LinuxFirmware{Tag: "20260916", FetchedAt: noon, Versions: map[string]string{"iwlwifi-cc-a0-77.ucode": "74.8dbafb52.0"}}
	a = Advise(Input{Report: r, KB: k, Now: noon, LinuxFirmware: lf, LVFS: lvfsWith(modelGUID, pm981())})
	var ids []string
	for _, f := range a.Findings {
		key := ""
		if f.Device != nil {
			key = f.Device.Kind + " " + f.Device.Key
		}
		ids = append(ids, f.ID+" "+key)
	}
	if !slices.Equal(ids, []string{"memory.upgrade ", "firmware.linux-firmware-matches pci 0000:02:00.0", "firmware.lvfs-up-to-date disk nvme0n1"}) || a.RulesSkipped != 1 {
		t.Errorf("with the index: findings %q, skipped %d", ids, a.RulesSkipped)
	}
}

// The hash is of the advised content: the same capture gives the same
// hash, another gives another.
func TestCaptureHashIsOfTheContent(t *testing.T) {
	k := knowledge(noDriverRule())
	a := Advise(Input{Report: machine(), KB: k, Now: noon}).CaptureSHA256
	b := Advise(Input{Report: machine(), KB: k, Now: noon}).CaptureSHA256
	r := machine()
	r.Hostname = "other"
	c := Advise(Input{Report: r, KB: k, Now: noon}).CaptureSHA256
	if len(a) != 64 || a != b || a == c {
		t.Errorf("hashes %s %s %s", a, b, c)
	}
}

func TestChecksAreListedSortedForTheGenerator(t *testing.T) {
	got := Checks()
	if !slices.Contains(got, "pci-without-driver") || !slices.IsSorted(got) || len(got) != len(checks) {
		t.Errorf("Checks() = %v", got)
	}
}

// Text advice shows everything a person needs. Control characters from a
// hostile capture or knowledge base never reach the terminal, whichever
// field they're in.
func TestTextAdviceIsCompleteAndTerminalSafe(t *testing.T) {
	a := Advise(Input{Report: machine(), KB: knowledge(noDriverRule()), Live: true, Now: noon})
	var buf bytes.Buffer
	if err := WriteText(&buf, a); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"this machine  ·  knowledge base 2026-10-06T12:00:00Z",
		"WARNING  No kernel driver  (needs-attention, pci.no-driver)",
		"Device     pci 0000:02:00.0  Wi-Fi 6 AX200",
		"Why        Devices need a driver.",
		"Evidence   pci[1].class_code = 028000",
		"Evidence   pci[1].driver = absent",
		"To do      Check the wiki",
		"Risk       none",
		"$ modprobe -R pci:v00008086d00002723sv00008086sd00000084bc02sc80i00",
		"Undo       modprobe -r MODULE",
		"Source     kernel  https://docs.kernel.org/x.html (upstream-doc, GPL-2.0-only, retrieved 2026-10-06)",
		"Confidence upstream-doc",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	// Every printed field, poisoned.
	const bad = "\x1b[31m\x07\r\u202e"
	hostile := Advice{KBVersion: "v" + bad, Warnings: []string{"w" + bad}, Findings: []Finding{{
		ID: "id" + bad, Category: kb.Category("cat" + bad), Severity: kb.Severity("sev" + bad), Title: "title" + bad, Detail: "detail" + bad,
		Device:     &DeviceRef{Kind: "kind" + bad, Key: "key" + bad, Name: "name" + bad},
		Evidence:   []Evidence{Present("path"+bad, "value"+bad), Absent("gone" + bad)},
		Actions:    []Action{{Distro: "distro" + bad, Text: "text" + bad, Commands: []string{"cmd" + bad}, Risk: "risk" + bad, Undo: "undo" + bad}},
		Sources:    []Source{{URL: "url" + bad, Licence: "lic" + bad, Confidence: kb.Confidence("conf" + bad), Retrieved: "ret" + bad}},
		Confidence: kb.Confidence("fconf" + bad),
	}}}
	buf.Reset()
	if err := WriteText(&buf, hostile); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(buf.String(), "\x1b\x07\r\u202e") {
		t.Errorf("a control or formatting character reached the output:\n%q", buf.String())
	}
	for _, field := range []string{"id", "cat", "SEV", "title", "detail", "kind", "key", "name", "path", "value", "gone", "distro", "text", "cmd", "risk", "undo", "url", "lic", "conf", "ret", "fconf", "w["} {
		if !strings.Contains(buf.String(), field) {
			t.Errorf("field %s missing from the output", field)
		}
	}
	for _, c := range []struct {
		a    Advice
		want string
	}{
		{Advice{KBVersion: "2026-10-06T12:00:00Z", RulesApplied: 3}, "saved capture  ·  knowledge base 2026-10-06T12:00:00Z\n\nNothing found by the 3 rules."},
		{Advice{RulesApplied: 1}, "Nothing found by the 1 rule."},
		{Advice{RulesApplied: 2, RulesSkipped: 1}, "Nothing found by 2 of 3 rules; 1 couldn't be applied (see Warnings)."},
		{Advice{RulesApplied: 1, Warnings: []string{"capture: usb: denied", "capture: dmi: denied"}}, "The capture couldn't read everything (2 warnings below), which can hide findings."},
	} {
		buf.Reset()
		if err := WriteText(&buf, c.a); err != nil || !strings.Contains(buf.String(), c.want) {
			t.Errorf("%+v: %q, want %q", c.a, buf.String(), c.want)
		}
	}
}

// Every ID that goes into the modalias must be exactly hex of its length:
// one hostile field is enough to make a command unsafe, so each is tried
// alone, and the command must be dropped with a warning.
func TestEachIDMustBeHexForTheCommand(t *testing.T) {
	base := report.PCIDevice{Address: "0000:02:00.0", VendorID: "8086", DeviceID: "2723", SubVendorID: "8086", SubDeviceID: "0084",
		ClassCode: "028000", Class: "Network controller"}
	set := map[string]func(d *report.PCIDevice, v string){
		"vendor":    func(d *report.PCIDevice, v string) { d.VendorID = v },
		"device":    func(d *report.PCIDevice, v string) { d.DeviceID = v },
		"subvendor": func(d *report.PCIDevice, v string) { d.SubVendorID = v },
		"subdevice": func(d *report.PCIDevice, v string) { d.SubDeviceID = v },
	}
	for field, f := range set {
		for _, bad := range []string{"$(w)", "8086;id", "$(id)8086", ";id;", "808", "80860", ""} {
			d := base
			f(&d, bad)
			checkDropped(t, field+"="+bad, d)
		}
	}
	for _, bad := range []string{"028000;id", "x028000", "02800", "0280000", "$(id)"} {
		d := base
		d.ClassCode = bad
		checkDroppedClass(t, bad, d)
	}
}

func checkDropped(t *testing.T, name string, d report.PCIDevice) {
	t.Helper()
	a := Advise(Input{Report: &report.Report{PCI: []report.PCIDevice{d}}, KB: knowledge(noDriverRule()), Now: noon})
	if len(a.Findings) != 1 {
		t.Fatalf("%s: %d findings", name, len(a.Findings))
	}
	if cmds := a.Findings[0].Actions[0].Commands; len(cmds) != 1 || cmds[0] != "true" ||
		!strings.Contains(strings.Join(a.Warnings, "\n"), "needs {modalias}") {
		t.Errorf("%s: commands %q, warnings %q", name, cmds, a.Warnings)
	}
}

// A class code that isn't hex still matches by prefix (it's the capture's
// claim), but must never reach the command.
func checkDroppedClass(t *testing.T, bad string, d report.PCIDevice) {
	t.Helper()
	rule := noDriverRule()
	rule.Match.PCIClass = []string{"0", "x", "$"}
	a := Advise(Input{Report: &report.Report{PCI: []report.PCIDevice{d}}, KB: knowledge(rule), Now: noon})
	for _, f := range a.Findings {
		for _, c := range f.Actions[0].Commands {
			if c != "true" {
				t.Errorf("class %q: command %q", bad, c)
			}
		}
	}
	if len(a.Findings) == 1 && !strings.Contains(strings.Join(a.Warnings, "\n"), "needs {modalias}") {
		t.Errorf("class %q: no warning: %q", bad, a.Warnings)
	}
}

// modprobe -R resolves upper-case aliases only: the modalias is upper case
// whatever case the capture uses, and exactly the kernel's format.
func TestTheModaliasIsTheKernelsUpperCaseFormat(t *testing.T) {
	for _, d := range []report.PCIDevice{
		{VendorID: "10ec", DeviceID: "c82f", SubVendorID: "1a3b", SubDeviceID: "abcd", ClassCode: "0c03fe"},
		{VendorID: "10EC", DeviceID: "C82F", SubVendorID: "1A3B", SubDeviceID: "ABCD", ClassCode: "0C03FE"},
	} {
		got, ok := pciModalias(d)
		if want := "pci:v000010ECd0000C82Fsv00001A3Bsd0000ABCDbc0Csc03iFE"; !ok || got != want {
			t.Errorf("%+v: %q, want %q", d, got, want)
		}
	}
}

// The advice shows each source as the knowledge base has it.
func TestFindingSourcesKeepEveryField(t *testing.T) {
	k := knowledge(noDriverRule())
	k.Sources = []kb.Source{{ID: "kernel", URL: "https://docs.kernel.org/x.html", Mirror: "https://mirror.example/x.pdf",
		Title: "T", Doc: "4AA7", Edition: "2", Published: "2019-12", Retrieved: "2026-10-06", Licence: "GPL-2.0-only",
		Confidence: "upstream-doc", Quote: "q", Locator: "p. 3"}}
	got := Advise(Input{Report: machine(), KB: k, Now: noon}).Findings[0].Sources
	want := []Source{{ID: "kernel", URL: "https://docs.kernel.org/x.html", Mirror: "https://mirror.example/x.pdf",
		Title: "T", Doc: "4AA7", Edition: "2", Published: "2019-12", Retrieved: "2026-10-06", Licence: "GPL-2.0-only",
		Confidence: "upstream-doc", Quote: "q", Locator: "p. 3"}}
	if !slices.Equal(got, want) {
		t.Errorf("sources\n got %+v\nwant %+v", got, want)
	}
}

// A finding about the whole machine has no device; its warnings say so
// rather than failing.
func TestMachineWideFindingsWarnWithoutADevice(t *testing.T) {
	withRun(t, func(*Input, *kb.Rule) ([]hit, error) { return []hit{{}}, nil })
	a := Advise(Input{Report: machine(), KB: knowledge(noDriverRule()), Now: noon})
	if len(a.Findings) != 1 || a.Findings[0].Device != nil ||
		!strings.Contains(strings.Join(a.Warnings, "\n"), "needs {modalias}, which the capture doesn't give for the machine") {
		t.Errorf("findings %+v, warnings %q", a.Findings, a.Warnings)
	}
}

// Identical capture warnings are the capture's to repeat; deduplication is
// per rule only.
func TestWarningsAreDedupedPerRuleOnly(t *testing.T) {
	r := machine()
	r.Warnings = []string{"usb: denied", "usb: denied"}
	a := Advise(Input{Report: r, KB: knowledge(noDriverRule()), Now: noon})
	if n := strings.Count(strings.Join(a.Warnings, "\n"), "capture: usb: denied"); n != 2 {
		t.Errorf("%d copies of the capture's warning, want 2: %q", n, a.Warnings)
	}
}

func TestPlaceholdersAreFound(t *testing.T) {
	if got := placeholders("modprobe -R {modalias} {x} {Bad}"); !slices.Equal(got, []string{"modalias", "x"}) {
		t.Errorf("placeholders = %q", got)
	}
}
