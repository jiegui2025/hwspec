package output

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jiegui2025/hwspec/internal/report"
)

var update = flag.Bool("update", false, "rewrite the golden Markdown reports")

// golden compares got with a golden file, or rewrites it with -update.
func golden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/output -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from the output (run go test ./internal/output -update after an intended change):\n%s", path, got)
	}
}

// Every recorded machine has a golden Markdown report beside its
// expected capture (#101); CI renders its diagrams.
func TestMarkdownRecordedMachines(t *testing.T) {
	captures, _ := filepath.Glob("../collect/testdata/machines/*/expected.json")
	if len(captures) == 0 {
		t.Fatal("no recorded machines")
	}
	for _, c := range captures {
		data, err := os.ReadFile(c)
		if err != nil {
			t.Fatal(err)
		}
		r, err := Read(data)
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := Write(&buf, r, "md"); err != nil {
			t.Fatal(err)
		}
		golden(t, filepath.Join(filepath.Dir(c), "expected.md"), buf.Bytes())
		for _, want := range []string{"# Hardware report: ", "## Summary", "## Memory", "## Storage", "## Graphics", "## USB", "## PCI"} {
			if !strings.Contains(buf.String(), want) {
				t.Errorf("%s: no %q", c, want)
			}
		}
		if n := strings.Count(buf.String(), "```mermaid"); n < 4 {
			t.Errorf("%s: %d diagrams, want the memory, storage, graphics and USB ones", c, n)
		}
	}
}

// hostile is a report whose every string tries to break a table, a
// heading, the HTML around it or a Mermaid label.
func hostile() *report.Report {
	evil := "a|b <script>x</script> & \"q\" 'q' `c` *s* _u_ [l](http://x) #h ~~d~~ \\e {}; end --> x\nnew\x1b[2J" +
		" https://track.example/p.png www.www.example a@mail.example %%{init: {'theme':'dark'}}%% click u0 call alert() Füße 日本 ﬂ°lt¶ß ##"
	id := &report.Identity{Vendor: evil, Model: evil}
	r := sample()
	r.System.Identity = id
	r.Memory.Modules = []report.MemoryModule{{Locator: evil, SizeBytes: 8 << 30, Type: evil, Identity: id}}
	r.Storage[0].Identity = id
	r.Storage[0].Partitions = []report.Partition{{Name: evil, MountPoint: evil}}
	r.GPUs = []report.GPU{{PCIAddress: "0000:00:02.0", DRMCard: "card0", Outputs: []string{evil, "DP-1"}, Identity: id}}
	r.Displays = []report.Display{{Connector: "card0-DP-1", Identity: id}}
	r.USB = []report.USBDevice{{Path: "1-1", Identity: id}, {Path: "1-1.2", Identity: id}}
	r.Hostname = evil
	r.OS.PrettyName = evil
	r.CPU.Identity = &report.Identity{Model: "CPU @ 2.20GHz"}
	r.Storage[0].Health = &report.Health{Status: report.StatusFailing, Reasons: []string{evil}}
	r.Warnings = []string{evil, "---", "2024. a year", "1) one", "+ plus", "= setext"}
	return r
}

// Device strings are escaped (#101): every table row has as many cells as
// its header, no HTML or control character gets through, and Mermaid
// labels hold only safe characters. The golden copy is a tracked .md, so
// CI's check-mermaid.sh renders its diagrams too.
func TestMarkdownEscapesHostileStrings(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, hostile(), "md"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	golden(t, "testdata/hostile.md", buf.Bytes())
	for _, c := range out {
		if c != '\n' && c < 0x20 {
			t.Fatalf("control character %U", c)
		}
	}
	if strings.Contains(out, "<script>") || regexp.MustCompile(`[^\\]\]\(http`).MatchString(out) {
		t.Errorf("HTML or a link got through:\n%s", out)
	}
	// #276 round 1: no GFM autolink trigger survives, list items don't
	// start block syntax, and the heading keeps its "#"s.
	// Mermaid labels render as SVG text, never links, so only the
	// Markdown around them is checked.
	text := regexp.MustCompile("(?s)```mermaid.*?```").ReplaceAllString(out, "")
	for _, raw := range []string{"://", "www.", "@mail", "\n- ---\n", "\n- 2024. ", "\n- 1) ", "\n- + ", "\n- = "} {
		if strings.Contains(text, raw) {
			t.Errorf("%q got through", raw)
		}
	}
	if first, _, _ := strings.Cut(out, "\n"); !strings.HasSuffix(first, " \\#\\#") {
		t.Errorf("the heading's #s: %q", first)
	}
	for _, want := range []string{"\n- \\---\n", "\n- 2024\\. a year\n", "\n- 1\\) one\n", "Füße 日本 ﬂ", "a&#64;mail", "CPU @ 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q", want)
		}
	}
	if regexp.MustCompile(`\["[^"]*[°¶][^"]*"\]`).MatchString(out) {
		t.Error("a label holds Mermaid's entity placeholder characters raw")
	}
	unescapedPipe := regexp.MustCompile(`[^\\]\|`)
	cells := 0
	inDiagram := false
	labelRe := regexp.MustCompile(`\["([^"]*)"\]`)
	for line := range strings.SplitSeq(out, "\n") {
		switch {
		case strings.HasPrefix(line, "```mermaid"):
			inDiagram = true
		case line == "```":
			inDiagram = false
		case inDiagram && strings.Contains(line, `["`):
			m := labelRe.FindStringSubmatch(line)
			if m == nil || strings.ContainsAny(m[1], "\"<>[]{}|`;") && !regexp.MustCompile(`#\d+;`).MatchString(m[1]) {
				t.Errorf("label not escaped: %q", line)
			}
			if strings.ContainsAny(regexp.MustCompile(`#\d+;`).ReplaceAllString(m[1], ""), "\"<>[]{}|`;#&") {
				t.Errorf("label has a raw special character: %q", line)
			}
		case strings.HasPrefix(line, "|"):
			n := len(unescapedPipe.FindAllString(" "+line, -1))
			if strings.HasPrefix(line, "|---") {
				continue
			}
			if cells == 0 || strings.HasPrefix(line, "| ") && n != cells {
				if cells != 0 {
					t.Errorf("a row with %d cell borders, the table has %d: %q", n, cells, line)
				}
			}
		default:
			cells = 0
			continue
		}
		if strings.HasPrefix(line, "|") && cells == 0 {
			cells = len(unescapedPipe.FindAllString(" "+line, -1))
		}
	}
}

// A bus with more devices than one diagram allows is drawn as several,
// none over the limits, with every device in one of them.
func TestMarkdownSplitsBigUSBTrees(t *testing.T) {
	var devices []report.USBDevice
	for hub := 1; hub <= 6; hub++ {
		devices = append(devices, report.USBDevice{Path: fmt.Sprintf("3-%d", hub), Identity: &report.Identity{Model: "hub"}})
		for port := 1; port <= 99; port++ {
			devices = append(devices, report.USBDevice{Path: fmt.Sprintf("3-%d.%d", hub, port), Identity: &report.Identity{Model: "device"}})
		}
	}
	devices = append(devices, report.USBDevice{Path: "4-1", Identity: &report.Identity{Model: "lone"}}, report.USBDevice{Path: "4-2.5.1", Identity: &report.Identity{Model: "orphan"}})
	ds := usbDiagrams(devices)
	if len(ds) < 3 {
		t.Fatalf("%d diagrams", len(ds))
	}
	seen := map[string]bool{}
	for _, d := range ds {
		if d.tooBig() {
			t.Errorf("a diagram with %d edges, %d characters", len(d.edges), len(d.String()))
		}
		for _, id := range d.ids {
			n := d.line[id]
			path := strings.Fields(strings.SplitN(n, `"`, 2)[1])[0]
			seen[path] = true
		}
	}
	for _, u := range devices {
		if !seen[u.Path] {
			t.Errorf("%s isn't drawn", u.Path)
		}
	}
	// A wide tree goes left to right, a deep one top to bottom.
	if ds[0].dir != "LR" || usbDiagrams([]report.USBDevice{{Path: "1-1"}, {Path: "1-1.1"}, {Path: "1-1.1.1"}})[0].dir != "TB" {
		t.Errorf("directions %q", ds[0].dir)
	}
	if got := usbParent("2"); got != "" {
		t.Errorf("a bus's parent: %q", got)
	}
}

// --redact works as for the other formats: the report is redacted, so no
// serial, UUID, MAC or hostname reaches the Markdown.
func TestMarkdownRedacted(t *testing.T) {
	r := sample()
	r.Redact()
	var buf bytes.Buffer
	if err := Write(&buf, r, "md"); err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"S1", "00:11:22:33:44:55", " on box", "| u |"} {
		if strings.Contains(buf.String(), leak) {
			t.Errorf("%q in the redacted report:\n%s", leak, buf.String())
		}
	}
	if !strings.Contains(buf.String(), "· redacted") {
		t.Errorf("not marked redacted:\n%s", buf.String())
	}
	if FormatFromPath("report.md") != "md" || FormatFromPath("REPORT.MD") != "md" {
		t.Error("-o report.md doesn't pick md")
	}
}

// What isn't there isn't drawn or tabled: no memory modules, no table or
// diagram for them; a disk without partitions isn't in the disk diagram;
// with a slot table, the slots are drawn, not the modules again.
func TestMarkdownLeavesOutTheEmpty(t *testing.T) {
	r := sample()
	r.Storage = append(r.Storage, report.Disk{Name: "sdb", SizeBytes: 1 << 30})
	var buf bytes.Buffer
	if err := Write(&buf, r, "md"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "| Slot |") || strings.Contains(out, "\n|\n") || strings.Count(out, "```mermaid") != 1 || strings.Contains(out, "sdb 1 GiB") {
		t.Errorf("empty parts drawn:\n%s", out)
	}

	on, off := true, false
	r = sample()
	r.Memory.Modules = []report.MemoryModule{{Locator: "DIMM1", SizeBytes: 8 << 30}}
	r.Memory.SlotUsage = []report.MemorySlot{{Locator: "DIMM1", Populated: &on}, {Locator: "DIMM2", Populated: &off}, {Locator: "DIMM3"}}
	buf.Reset()
	if err := Write(&buf, r, "md"); err != nil {
		t.Fatal(err)
	}
	out = buf.String()
	for _, want := range []string{`s0["DIMM1: 8 GiB"]`, `s1["DIMM2: empty"]`, `s2["DIMM3: not said"]`} {
		if !strings.Contains(out, want) {
			t.Errorf("no %s in:\n%s", want, out)
		}
	}
	if strings.Contains(out, `m0["`) {
		t.Errorf("modules drawn besides the slots:\n%s", out)
	}
}

// USB parents: a nested device's is the hub above it; one whose hub isn't
// in the capture hangs off the bus; a bus alone is no diagram; port 1's
// group doesn't take ports 10 to 12; long names split a diagram by size.
func TestMarkdownUSBTreeShape(t *testing.T) {
	if got := usbParent("1-2.3.4"); got != "1-2.3" {
		t.Errorf("usbParent: %q", got)
	}
	ds := usbDiagrams([]report.USBDevice{{Path: "4-2.5.1", Identity: &report.Identity{Model: "orphan"}}, {Path: "5"}})
	if len(ds) != 2 || len(ds[0].edges) != 1 || ds[0].edges[0] != [2]string{"u0", "u1"} || strings.Contains(ds[0].String(), "4-2.5 ") ||
		len(ds[1].edges) != 0 {
		t.Errorf("orphan and bare bus: %+v", ds)
	}

	var devices []report.USBDevice
	for hub := 1; hub <= 12; hub++ {
		devices = append(devices, report.USBDevice{Path: fmt.Sprintf("3-%d", hub), Identity: &report.Identity{Model: "hub"}})
		for port := 1; port <= 40; port++ {
			devices = append(devices, report.USBDevice{Path: fmt.Sprintf("3-%d.%d", hub, port), Identity: &report.Identity{Model: "device"}})
		}
	}
	count := map[string]int{}
	for _, d := range usbDiagrams(devices) {
		for _, id := range d.ids {
			n := d.line[id]
			count[strings.Fields(strings.SplitN(n, `"`, 2)[1])[0]]++
		}
	}
	for _, u := range devices {
		if count[u.Path] != 1 {
			t.Errorf("%s drawn %d times", u.Path, count[u.Path])
		}
	}

	long := strings.Repeat("x", 300)
	devices = nil
	for port := 1; port <= 250; port++ {
		devices = append(devices, report.USBDevice{Path: fmt.Sprintf("6-%d", port), Identity: &report.Identity{Model: long}})
	}
	ds = usbDiagrams(devices)
	if len(ds) < 2 {
		t.Errorf("250 edges with long names in %d diagram(s), %d characters", len(ds), len(ds[0].String()))
	}
	for _, x := range ds {
		if x.tooBig() {
			t.Errorf("a diagram of %d characters", len(x.String()))
		}
	}
}

// The power section (#105): a rated, online supply with its contract, a
// GPU limit with its device; no section in a capture without one.
func TestMarkdownPower(t *testing.T) {
	on := true
	r := sample()
	r.Power = &report.Power{
		Supplies: []report.PowerSupply{{Name: "ucsi", Type: "usb", Port: "port0", Online: &on, ContractMV: 20000, ContractMA: 3250, RatedMaxMW: 65000, RatingSource: report.RatingFromPDOs},
			{Name: "half", Type: "usb", Online: &on, ContractMV: 5000}},
		Limits: []report.PowerLimit{{Domain: "gpu", Zone: "amdgpu", Device: "0000:03:00.0", Name: "cap", LimitMW: 120000, Source: "amdgpu"}},
	}
	var buf bytes.Buffer
	if err := Write(&buf, r, "md"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"| ucsi | usb | port0 | yes | 20 V × 3.25 A | 65 W (usb-pd-source-capabilities) |", "| half | usb |  | yes |  |  |", "| gpu | amdgpu 03:00.0 | cap | 120 W | amdgpu |"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("no %q in:\n%s", want, buf.String())
		}
	}
	if strings.Contains(buf.String(), "Rating unknown") {
		t.Errorf("rated, yet:\n%s", buf.String())
	}
	r.Power = nil
	buf.Reset()
	if err := Write(&buf, r, "md"); err != nil || strings.Contains(buf.String(), "## Power") {
		t.Errorf("no power: %v", err)
	}
}

// Every diagram keeps to Mermaid's limits, not only the USB tree: many
// disks' partitions, GPUs with long names, and one hub with more devices
// than a diagram allows are split; long labels are capped; a table whose
// columns are all empty isn't written (#276 round 1).
func TestMarkdownDiagramLimits(t *testing.T) {
	r := sample()
	r.Storage = nil
	for i := range 40 {
		d := report.Disk{Name: fmt.Sprintf("sd%d", i), SizeBytes: 1 << 40}
		for j := range 16 {
			d.Partitions = append(d.Partitions, report.Partition{Name: fmt.Sprintf("sd%dp%d", i, j), SizeBytes: 1 << 30})
		}
		r.Storage = append(r.Storage, d)
	}
	quotes := strings.Repeat(`"<>`, 100)
	for i := range 20 {
		var outs []string
		for j := range 8 {
			outs = append(outs, fmt.Sprintf("DP-%d", j))
		}
		r.GPUs = append(r.GPUs, report.GPU{PCIAddress: fmt.Sprintf("0000:%02x:00.0", i), Outputs: outs, Identity: &report.Identity{Model: quotes}})
	}
	for port := 1; port <= 600; port++ {
		r.USB = append(r.USB, report.USBDevice{Path: fmt.Sprintf("1-1.%d", port)})
	}
	r.Memory.Modules = []report.MemoryModule{{}}
	var buf bytes.Buffer
	if err := Write(&buf, r, "md"); err != nil {
		t.Fatal(err)
	}
	blocks := strings.Split(buf.String(), "```mermaid\n")[1:]
	if len(blocks) < 6 {
		t.Errorf("%d diagrams", len(blocks))
	}
	for _, b := range blocks {
		b, _, _ = strings.Cut(b, "```")
		if edges := strings.Count(b, " --> "); edges > mermaidMaxEdges || len(b) > mermaidMaxChars {
			t.Errorf("a diagram with %d edges, %d characters", edges, len(b))
		}
	}
	for i := range 40 {
		for j := range 16 {
			if !strings.Contains(buf.String(), fmt.Sprintf(`["sd%dp%d `, i, j)) {
				t.Errorf("sd%dp%d isn't drawn", i, j)
			}
		}
	}
	if strings.Contains(buf.String(), "\n|\n") || strings.Contains(buf.String(), "| Slot |") {
		t.Error("a table of empty columns")
	}
	d := &diagram{}
	d.node("a", strings.Repeat("x", 500))
	if n := strings.Count(d.line["a"], "x"); n != maxLabel-1 || !strings.Contains(d.line["a"], "#8230;") {
		t.Errorf("label of %d x", n)
	}
	r = sample()
	r.OS.BootMode = ""
	buf.Reset()
	if err := Write(&buf, r, "md"); err != nil || !strings.Contains(buf.String(), "| Boot | boot mode unknown, Secure Boot on |") {
		t.Errorf("boot: %v\n%s", err, buf.String())
	}
}

// split cuts by characters as well as edges: 60 edges of 1,000-character
// labels pass 40,000 characters, not 400 edges.
func TestDiagramSplitsByCharacters(t *testing.T) {
	d := &diagram{dir: "LR"}
	d.node("root", "root")
	for i := range 60 {
		id := fmt.Sprintf("n%d", i)
		d.node(id, strings.Repeat("#", 190))
		d.edge("root", id)
	}
	parts := d.split()
	if len(parts) < 2 {
		t.Fatalf("%d parts of %d characters", len(parts), len(d.String()))
	}
	edges := 0
	for _, p := range parts {
		edges += len(p.edges)
		if p.tooBig() {
			t.Errorf("a part of %d characters", len(p.String()))
		}
	}
	if edges != 60 {
		t.Errorf("%d edges across the parts", edges)
	}
}
