package output

import (
	"cmp"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/jiegui2025/hwspec/internal/report"
)

// The Markdown report (#101, part A of #98): every non-empty section as a
// table, and Mermaid diagrams of what hangs together (memory slots, disks
// and partitions, GPUs and their connectors, the USB tree). Every value
// comes from a device or the user, so it's escaped for the table and for
// Mermaid labels; output is deterministic, in the capture's order.

// Mermaid's defaults refuse a diagram over 50,000 characters or 500 edges
// (mermaid.js.org/config/schema-docs/config.html); diagrams are split well
// below both.
const (
	mermaidMaxEdges = 400
	mermaidMaxChars = 40000
)

// md builds the document.
type md struct{ b strings.Builder }

func (m *md) p(format string, args ...any) { fmt.Fprintf(&m.b, format, args...) }

// heading writes a section heading; every block ends with a blank line,
// so a heading needs none before it.
func (m *md) heading(text string) {
	m.p("## %s\n\n", escape(text, inHeading))
}

// table writes a table; columns no row fills are left out, and a table
// with none left (no rows, or none with a value) isn't written.
func (m *md) table(head []string, rows [][]string) {
	var keep []int
	for c := range head {
		if slices.ContainsFunc(rows, func(r []string) bool { return c < len(r) && r[c] != "" }) {
			keep = append(keep, c)
		}
	}
	if len(keep) == 0 {
		return
	}
	line := func(r []string) {
		var cells []string
		for _, c := range keep {
			v := ""
			if c < len(r) {
				v = r[c]
			}
			cells = append(cells, cell(v))
		}
		m.p("| %s |\n", strings.Join(cells, " | "))
	}
	line(head)
	m.p("|%s\n", strings.Repeat("---|", len(keep)))
	for _, r := range rows {
		line(r)
	}
	m.p("\n")
}

// facts writes a two-column table of the facts that are known.
func (m *md) facts(pairs ...[2]string) {
	var rows [][]string
	for _, p := range pairs {
		if p[1] != "" {
			rows = append(rows, []string{p[0], p[1]})
		}
	}
	m.table([]string{"", ""}, rows)
}

// cellKind is where an escaped value goes: block syntax only matters at
// the start of a list item, and a heading ends at a "#".
type cellKind int

const (
	inCell cellKind = iota
	inHeading
	inList
)

// cell escapes a value for a Markdown table cell; see escape.
func cell(s string) string { return escape(s, inCell) }

// escape escapes a value for a table cell, a heading or a list item: the
// pipe that would end the cell, HTML that a renderer would act on, the
// Markdown that would format or link it (GFM autolinks on "://", "www."
// and "@" included: device strings and other people's captures aren't
// trusted), the block syntax a list item or heading would take, and
// control characters. An underscore inside a word ("batt_status") formats
// nothing, so it stays.
func escape(s string, kind cellKind) string {
	var b strings.Builder
	rs := []rune(s)
	word := func(i int) bool { return i >= 0 && i < len(rs) && (unicode.IsLetter(rs[i]) || unicode.IsDigit(rs[i])) }
	at := func(i int, text string) bool { return strings.HasPrefix(strings.ToLower(string(rs[i:])), text) }
	// A list item starting "2024." or "1)" would be an ordered list.
	digits := 0
	for digits < len(rs) && unicode.IsDigit(rs[digits]) {
		digits++
	}
	for i, r := range rs {
		switch {
		case r == '_' && word(i-1) && word(i+1):
			b.WriteRune(r)
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteByte(' ')
		case unicode.IsControl(r):
		case r == '|':
			b.WriteString(`\|`)
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '&':
			b.WriteString("&amp;")
		case r == ':' && at(i, "://"):
			b.WriteString("&#58;")
		case r == '.' && i >= 3 && strings.EqualFold(string(rs[i-3:i]), "www"):
			b.WriteString("&#46;")
		case r == '@' && i > 0 && i+1 < len(rs) && !unicode.IsSpace(rs[i-1]) && !unicode.IsSpace(rs[i+1]):
			b.WriteString("&#64;") // an email address would autolink; "CPU @ 2.2GHz" can't
		case strings.ContainsRune("\\`*_[]~", r), r == '#' && (i == 0 || kind == inHeading),
			kind == inList && i == 0 && (r == '-' || r == '+' || r == '='),
			kind == inList && i == digits && digits > 0 && (r == '.' || r == ')'):
			b.WriteRune('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// label escapes a value for a Mermaid node label in double quotes:
// anything but plain letters, digits, spaces and safe punctuation becomes
// a numeric entity ("#35;"), which Mermaid decodes.
func label(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsControl(r):
			b.WriteByte(' ')
		case r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune(" .,:/-+()@%", r)):
			b.WriteRune(r)
		case r >= 128 && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '×' || r == '–' || r == '·'):
			b.WriteRune(r)
		default:
			fmt.Fprintf(&b, "#%d;", r)
		}
	}
	return b.String()
}

// diagram is one Mermaid flowchart: its nodes by id, in order, and its
// edges.
type diagram struct {
	dir   string // TB, LR
	ids   []string
	line  map[string]string
	edges [][2]string
}

// maxLabel caps a node's label, so one edge always fits in a diagram.
const maxLabel = 200

func (d *diagram) node(id, text string) {
	if d.line == nil {
		d.line = map[string]string{}
	}
	if _, ok := d.line[id]; ok {
		return
	}
	if rs := []rune(text); len(rs) > maxLabel {
		text = string(rs[:maxLabel-1]) + "…"
	}
	d.ids = append(d.ids, id)
	d.line[id] = fmt.Sprintf(`  %s["%s"]`, id, label(text))
}

func (d *diagram) edge(from, to string) { d.edges = append(d.edges, [2]string{from, to}) }

func edgeLine(e [2]string) string { return "  " + e[0] + " --> " + e[1] }

func (d *diagram) String() string {
	var b strings.Builder
	b.WriteString("```mermaid\nflowchart " + d.dir + "\n")
	for _, id := range d.ids {
		b.WriteString(d.line[id] + "\n")
	}
	for _, e := range d.edges {
		b.WriteString(edgeLine(e) + "\n")
	}
	b.WriteString("```\n")
	return b.String()
}

// tooBig says whether a diagram passes the limits it's split by.
func (d *diagram) tooBig() bool {
	return len(d.edges) > mermaidMaxEdges || len(d.String()) > mermaidMaxChars
}

// split cuts a diagram that passes the limits into several, edges in
// order, each with the nodes its edges use; one within them is itself.
func (d *diagram) split() []*diagram {
	if !d.tooBig() {
		return []*diagram{d}
	}
	var out []*diagram
	cur, size := &diagram{dir: d.dir}, 0
	for _, e := range d.edges {
		add := len(edgeLine(e)) + 1
		for _, id := range e {
			if _, ok := cur.line[id]; !ok {
				add += len(d.line[id]) + 1
			}
		}
		if len(cur.edges) > 0 && (len(cur.edges)+1 > mermaidMaxEdges || size+add > mermaidMaxChars-100) {
			out, cur, size = append(out, cur), &diagram{dir: d.dir}, 0
			add = len(edgeLine(e)) + len(d.line[e[0]]) + len(d.line[e[1]]) + 3
		}
		for _, id := range e {
			if _, ok := cur.line[id]; !ok {
				if cur.line == nil {
					cur.line = map[string]string{}
				}
				cur.ids = append(cur.ids, id)
				cur.line[id] = d.line[id]
			}
		}
		cur.edges = append(cur.edges, e)
		size += add
	}
	return append(out, cur)
}

// diagram writes a diagram, split when it would pass Mermaid's limits;
// one without edges isn't written.
func (m *md) diagram(d *diagram) {
	if len(d.edges) == 0 {
		return
	}
	for _, part := range d.split() {
		m.p("%s\n", part.String())
	}
}

// list writes a bulleted list.
func (m *md) list(items []string) {
	for _, i := range items {
		m.p("- %s\n", escape(i, inList))
	}
	m.p("\n")
}

func writeMarkdown(w io.Writer, r *report.Report) error {
	m := &md{}
	m.p("# Hardware report: %s\n\n", escape(product(r.System.Identity), inHeading))
	captured := "unknown"
	if !r.CapturedAt.IsZero() {
		captured = r.CapturedAt.Format("2006-01-02 15:04 MST")
	}
	about := fmt.Sprintf("Captured %s by %s", captured, join(r.Tool.Name, r.Tool.Version))
	if r.Hostname != "" {
		about += " on " + r.Hostname
	}
	if !r.Privileged {
		about += " · limited (not root): run with --full for the firmware's tables, serials and drive health"
	}
	if r.Redacted {
		about += " · redacted"
	}
	m.p("%s\n\n", cell(about))

	m.summary(r)
	m.system(r)
	m.cpu(r)
	m.memory(r)
	m.storage(r)
	m.graphics(r)
	m.devices(r)
	m.power(r)
	m.usb(r)
	m.pci(r)
	m.sensors(r)
	m.mountings(r)
	m.lists(r)
	_, err := io.WriteString(w, strings.TrimSuffix(m.b.String(), "\n"))
	return err
}

func (m *md) summary(r *report.Report) {
	m.heading("Summary")
	var gpus, disks []string
	for _, g := range r.GPUs {
		gpus = append(gpus, product(g.Identity))
	}
	var total uint64
	for _, d := range r.Storage {
		total += d.SizeBytes
		disks = append(disks, d.Name)
	}
	storage := ""
	if len(disks) > 0 {
		storage = fmt.Sprintf("%d (%s), %s", len(disks), strings.Join(disks, ", "), bytesStr(total))
	}
	cpu := ""
	if r.CPU.Identity != nil {
		cpu = fmt.Sprintf("%s, %d cores / %d threads", r.CPU.Identity.Model, r.CPU.Cores, r.CPU.Threads)
	}
	m.facts([2]string{"Machine", product(r.System.Identity)}, [2]string{"Board", product(r.Board.Identity)},
		[2]string{"CPU", cpu}, [2]string{"Memory", bytesStr(r.Memory.TotalBytes) + " usable"}, [2]string{"Storage", storage},
		[2]string{"Graphics", strings.Join(gpus, "; ")}, [2]string{"OS", join(r.OS.PrettyName, r.OS.Kernel)})
}

func (m *md) system(r *report.Report) {
	m.heading("System")
	fw := ""
	if f := r.System.Firmware; f.Known() {
		fw = join(f.Vendor, f.Version, f.Date)
	} else if f != nil {
		fw = unknownFirmware(f)
	}
	me := ""
	if f := r.System.MEFirmware; f.Known() {
		me = join(f.Vendor, f.Version)
	} else if f != nil {
		me = unknownFirmware(f)
	}
	ec := ""
	if f := r.System.ECFirmware; f != nil {
		ec = f.Version
	}
	tpm := ""
	if r.TPM != nil {
		tpm = withParts(fmt.Sprintf("TPM %d", r.TPM.SpecVersionMajor), fwText(r.TPM.Firmware))
	}
	rtc := ""
	if r.RTC != nil {
		rtc = "driver batt_status " + r.RTC.BattStatus
		if r.RTC.BattStatus == report.RTCBattOkay {
			rtc += " (not evidence of a good coin cell on Intel chipsets)"
		}
	}
	boot := cmp.Or(r.OS.BootMode, "boot mode unknown")
	if r.OS.SecureBoot != nil {
		boot += map[bool]string{true: ", Secure Boot on", false: ", Secure Boot off"}[*r.OS.SecureBoot]
	}
	virt := r.OS.Virtualization
	if virt == "none" {
		virt = ""
	}
	m.facts([2]string{"Machine", product(r.System.Identity)}, [2]string{"Family", r.System.Family},
		[2]string{"Chassis", r.System.ChassisType}, [2]string{"Board", product(r.Board.Identity)},
		[2]string{"Firmware", fw}, [2]string{"Management Engine", me}, [2]string{"Embedded controller", ec},
		[2]string{"TPM", tpm}, [2]string{"RTC", rtc}, [2]string{"OS", r.OS.PrettyName},
		[2]string{"Kernel", join(r.OS.Kernel, r.OS.Arch)}, [2]string{"Boot", boot}, [2]string{"Runs in", virt})
}

func (m *md) cpu(r *report.Report) {
	m.heading("CPU")
	model := ""
	if r.CPU.Identity != nil {
		model = r.CPU.Identity.Model
	}
	codename := r.CPU.Codename
	if u := r.CPU.Microarchitecture; u != "" && u != codename {
		codename = strings.TrimPrefix(codename+" ("+u+" cores)", " ")
	}
	cores := fmt.Sprintf("%d cores / %d threads", r.CPU.Cores, r.CPU.Threads)
	if r.CPU.Sockets > 1 {
		cores = fmt.Sprintf("%d sockets, ", r.CPU.Sockets) + cores
	}
	for _, t := range r.CPU.CoreTypes {
		cores += fmt.Sprintf(", %d %s threads", t.Threads, t.Name)
	}
	clock := ""
	if r.CPU.MaxFreqMHz > 0 {
		clock = fmt.Sprintf("%d–%d MHz", r.CPU.MinFreqMHz, r.CPU.MaxFreqMHz)
	}
	var caches []string
	for _, c := range r.CPU.Caches {
		caches = append(caches, fmt.Sprintf("L%d%s %s×%d", c.Level, cacheSuffix(c.Type), bytesStr(c.SizeBytes), c.Instances))
	}
	micro := ""
	if f := r.CPU.Firmware; f.Known() {
		micro = f.Version
	} else if f != nil {
		micro = unknownFirmware(f)
	}
	m.facts([2]string{"Model", model}, [2]string{"Codename", codename}, [2]string{"Cores", cores},
		[2]string{"Clock", clock}, [2]string{"Frequency driver", driverText(r.CPU.Driver)}, [2]string{"Cache", strings.Join(caches, ", ")},
		[2]string{"Microcode", micro}, [2]string{"Health", healthText(r.CPU.Health)})
}

func (m *md) memory(r *report.Report) {
	m.heading("Memory")
	total := bytesStr(r.Memory.TotalBytes) + " usable"
	if r.Memory.InstalledBytes > 0 {
		total = bytesStr(r.Memory.InstalledBytes) + " installed, " + total
	}
	slots := ""
	switch {
	case r.Memory.Slots > 0:
		slots = strconv.Itoa(r.Memory.Slots)
	case !r.Privileged:
		slots = "needs --full (the firmware's table is root only)"
	}
	maxCap := ""
	if r.Memory.MaxCapacityBytes > 0 {
		maxCap = bytesStr(r.Memory.MaxCapacityBytes)
	}
	m.facts([2]string{"Total", total}, [2]string{"Slots", slots}, [2]string{"Maximum (firmware)", maxCap})
	var rows [][]string
	for _, mod := range r.Memory.Modules {
		var maker, part, made string
		if id := mod.Identity; id != nil {
			maker, part, made = id.Vendor, id.PartNumber, id.ManufactureDate
		}
		rows = append(rows, []string{mod.Locator, sizeOrEmpty(mod.SizeBytes), mod.Type, mod.FormFactor, mts(mod.ConfiguredMTs, mod.SpeedMTs),
			maker, part, made, mod.DRAMVendor, healthShort(mod.Health)})
	}
	m.table([]string{"Slot", "Size", "Type", "Form factor", "Speed", "Maker", "Part", "Made", "Chips", "Health"}, rows)

	// The slots, used and empty, as the firmware lists them; else the
	// modules found.
	d := &diagram{dir: "LR"}
	d.node("mem", "Memory "+bytesStr(r.Memory.TotalBytes))
	for i, s := range r.Memory.SlotUsage {
		id := fmt.Sprintf("s%d", i)
		text := join(s.Locator, s.BankLocator)
		switch {
		case s.Populated == nil:
			text += ": not said"
		case !*s.Populated:
			text += ": empty"
		}
		if k := slices.IndexFunc(r.Memory.Modules, func(mod report.MemoryModule) bool { return mod.Locator == s.Locator }); k >= 0 && s.Populated != nil && *s.Populated {
			mod := r.Memory.Modules[k]
			text += ": " + join(sizeOrEmpty(mod.SizeBytes), mod.Type, mod.FormFactor)
		}
		d.node(id, text)
		d.edge("mem", id)
	}
	if len(r.Memory.SlotUsage) == 0 {
		for i, mod := range r.Memory.Modules {
			id := fmt.Sprintf("m%d", i)
			d.node(id, join(mod.Locator, sizeOrEmpty(mod.SizeBytes), mod.Type, mod.FormFactor))
			d.edge("mem", id)
		}
	}
	m.diagram(d)
}

func (m *md) storage(r *report.Report) {
	if len(r.Storage) == 0 {
		return
	}
	m.heading("Storage")
	var rows [][]string
	for _, d := range r.Storage {
		typ := d.Type
		if typ == "unknown" {
			typ = ""
		}
		rows = append(rows, []string{d.Name, model(d.Identity), bytesStr(d.SizeBytes), typ, d.Transport,
			fwText(d.Firmware), driverText(d.Driver), healthText(d.Health)})
	}
	m.table([]string{"Disk", "Model", "Size", "Type", "Transport", "Firmware", "Driver", "Health"}, rows)

	d := &diagram{dir: "LR"}
	for i, disk := range r.Storage {
		if len(disk.Partitions) == 0 {
			continue
		}
		id := fmt.Sprintf("d%d", i)
		d.node(id, join(disk.Name, bytesStr(disk.SizeBytes)))
		for j, p := range disk.Partitions {
			pid := fmt.Sprintf("d%dp%d", i, j)
			d.node(pid, join(p.Name, bytesStr(p.SizeBytes), p.Filesystem, p.MountPoint))
			d.edge(id, pid)
		}
	}
	m.diagram(d)
}

func (m *md) graphics(r *report.Report) {
	if len(r.GPUs)+len(r.Displays) == 0 {
		return
	}
	m.heading("Graphics")
	var rows [][]string
	for _, g := range r.GPUs {
		vram, clock := "", ""
		if g.VRAMBytes > 0 {
			vram = bytesStr(g.VRAMBytes)
		}
		if c := g.Clocks; c != nil {
			clock = fmt.Sprintf("%d–%d MHz", c.MinFreqMHz, c.MaxFreqMHz)
		}
		fw := fwText(g.Firmware)
		if parts := componentsText(g.FirmwareComponents); parts != "" {
			if !g.Firmware.Known() {
				fw = ""
			}
			fw = strings.TrimPrefix(fw+", firmware "+parts, ", ")
		}
		rows = append(rows, []string{strings.TrimPrefix(g.PCIAddress, "0000:"), product(g.Identity), vram, clock, fw, driverText(g.Driver)})
	}
	m.table([]string{"GPU", "Model", "VRAM", "Clocks", "Firmware", "Driver"}, rows)
	rows = nil
	for _, d := range r.Displays {
		native, size, offers, made := "", "", "", ""
		if d.NativeWidth > 0 {
			native = fmt.Sprintf("%d×%d @ %.0f Hz", d.NativeWidth, d.NativeHeight, d.NativeRefreshHz)
		}
		if d.DiagonalIn > 0 {
			size = fmt.Sprintf("%.1f\"", d.DiagonalIn)
		}
		if d.BestMode != "" {
			offers = fmt.Sprintf("%s (%d modes)", strings.Replace(d.BestMode, "x", "×", 1), d.ModeCount)
		}
		if d.Identity != nil {
			made = d.Identity.ManufactureDate
		}
		rows = append(rows, []string{d.Connector, product(d.Identity), native, size, offers, made})
	}
	m.table([]string{"Connector", "Display", "Native mode", "Size", "Output offers", "Made"}, rows)

	// Each GPU's connectors, and the display on each connected one.
	d := &diagram{dir: "LR"}
	for i, g := range r.GPUs {
		if len(g.Outputs) == 0 {
			continue
		}
		id := fmt.Sprintf("g%d", i)
		d.node(id, product(g.Identity))
		for j, out := range g.Outputs {
			cid := fmt.Sprintf("g%dc%d", i, j)
			d.node(cid, out)
			d.edge(id, cid)
			for k, disp := range r.Displays {
				if disp.Connector == g.DRMCard+"-"+out {
					did := fmt.Sprintf("g%dc%dd%d", i, j, k)
					d.node(did, product(disp.Identity))
					d.edge(cid, did)
				}
			}
		}
	}
	m.diagram(d)
}

// devices writes the network, Bluetooth, audio, battery and USB-C tables.
func (m *md) devices(r *report.Report) {
	var rows [][]string
	for _, n := range r.Network {
		speed := ""
		if n.SpeedMbps > 0 {
			speed = fmt.Sprintf("%d Mb/s", n.SpeedMbps)
		}
		rows = append(rows, []string{n.Name, product(n.Identity), n.Type, radioText(n.Radio), n.State, speed,
			fwText(n.Firmware), driverText(n.Driver), healthShort(n.Health)})
	}
	if len(rows) > 0 {
		m.heading("Network")
		m.table([]string{"Interface", "Device", "Type", "Radio", "State", "Speed", "Firmware", "Driver", "Health"}, rows)
	}
	rows = nil
	for _, bt := range r.Bluetooth {
		power := ""
		if bt.Powered != nil {
			power = map[bool]string{true: "on", false: "off"}[*bt.Powered]
		}
		rows = append(rows, []string{bt.Name, product(bt.Identity), bt.Version, bt.Manufacturer, power, fwText(bt.Firmware), driverText(bt.Driver)})
	}
	if len(rows) > 0 {
		m.heading("Bluetooth")
		m.table([]string{"Controller", "Adapter", "Version", "Chip by", "Radio", "Firmware", "Driver"}, rows)
	}
	rows = nil
	for _, a := range r.Audio {
		var codecs []string
		for _, c := range a.Codecs {
			codecs = append(codecs, model(c.Identity))
		}
		rows = append(rows, []string{strconv.Itoa(a.Index), a.Name, strings.Join(codecs, ", "), driverText(a.Driver)})
	}
	if len(rows) > 0 {
		m.heading("Audio")
		m.table([]string{"Card", "Name", "Codecs", "Driver"}, rows)
	}
	rows = nil
	for _, bt := range r.Batteries {
		charged, holds := "", ""
		if bt.CapacityPercent > 0 {
			charged = fmt.Sprintf("%d%%", bt.CapacityPercent)
		}
		if h := bt.Health; h != nil {
			full, ok1 := h.Metrics[report.MetricFullWh]
			design, ok2 := h.Metrics[report.MetricDesignWh]
			if ok1 && ok2 {
				holds = fmt.Sprintf("%s of %s Wh", trimFloat(full), trimFloat(design))
			}
		}
		rows = append(rows, []string{bt.Name, product(bt.Identity), charged, holds, healthText(bt.Health)})
	}
	if len(rows) > 0 {
		m.heading("Batteries")
		m.table([]string{"Battery", "Model", "Charged", "Holds", "Health"}, rows)
	}
	rows = nil
	for _, p := range r.USBC {
		where := ""
		if l := p.Location; l != nil {
			where = join(l.Panel, l.Horizontal, l.Vertical)
		}
		rows = append(rows, []string{p.Name, where, strings.Join(p.PowerRoles, ", "), chargeText(p)})
	}
	if len(rows) > 0 {
		m.heading("USB-C charging")
		m.table([]string{"Port", "Where", "Power roles", "Charging"}, rows)
	}
}

// power writes the supplies and the power limits (#105), and why no
// supply is rated when none is.
func (m *md) power(r *report.Report) {
	p := r.Power
	if p == nil {
		return
	}
	m.heading("Power")
	var rows [][]string
	for _, s := range p.Supplies {
		online, contract, rating := "", "", ""
		if s.Online != nil {
			online = map[bool]string{true: "yes", false: "no"}[*s.Online]
		}
		if s.ContractMV > 0 && s.ContractMA > 0 {
			contract = milli(s.ContractMV) + " V × " + milli(s.ContractMA) + " A"
		}
		if s.RatedMaxMW > 0 {
			rating = milli(s.RatedMaxMW) + " W (" + s.RatingSource + ")"
		}
		rows = append(rows, []string{s.Name, s.Type, s.Port, online, contract, rating})
	}
	m.table([]string{"Supply", "Type", "USB-C port", "Powering the machine", "Contract", "Rating"}, rows)
	if p.RatingUnknown != "" {
		m.p("Rating unknown: %s.\n\n", cell(p.RatingUnknown))
	}
	rows = nil
	for _, l := range p.Limits {
		window, enable := "", ""
		if l.TimeWindowUS > 0 {
			window = strconv.FormatFloat(float64(l.TimeWindowUS)/1e6, 'f', -1, 64) + " s"
		}
		if l.Enabled != nil {
			enable = map[bool]string{true: "on", false: "off"}[*l.Enabled]
		}
		zone := l.Zone
		if l.Device != "" {
			zone += " " + strings.TrimPrefix(l.Device, "0000:")
		}
		rows = append(rows, []string{l.Domain, zone, cmp.Or(limitNames[l.Name], l.Name), milli(l.LimitMW) + " W", window, enable, l.Source})
	}
	m.table([]string{"Domain", "Zone", "Limit", "Value", "Window", "Enable bit (PL1)", "Source"}, rows)
}

// usbParent is a USB device's parent path ("1-2.3" → "1-2", "1-2" → the
// bus "1"), and "" for a bus.
func usbParent(path string) string {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[:i]
	}
	if bus, _, ok := strings.Cut(path, "-"); ok {
		return bus
	}
	return ""
}

func (m *md) usb(r *report.Report) {
	if len(r.USB) == 0 {
		return
	}
	m.heading("USB")
	var rows [][]string
	for _, u := range r.USB {
		rows = append(rows, []string{u.Path, product(u.Identity), fwText(u.Firmware)})
	}
	m.table([]string{"Path", "Device", "Firmware"}, rows)
	for _, d := range usbDiagrams(r.USB) {
		m.diagram(d)
	}
}

// usbDiagrams draws the USB tree, one diagram per bus, and a bus too big
// for one diagram one per first-level port; a device whose parent isn't
// in the capture hangs off its bus.
func usbDiagrams(devices []report.USBDevice) []*diagram {
	name := map[string]string{}
	var buses []string
	for _, u := range devices {
		name[u.Path] = product(u.Identity)
		bus, _, _ := strings.Cut(u.Path, "-")
		if !slices.Contains(buses, bus) {
			buses = append(buses, bus)
		}
	}
	parent := func(path string) string {
		p := usbParent(path)
		for p != "" && name[p] == "" && strings.Contains(p, "-") {
			p = usbParent(p)
		}
		return p
	}
	draw := func(root string, paths []string) *diagram {
		d := &diagram{}
		ids := map[string]string{}
		id := func(path string) string {
			if ids[path] == "" {
				ids[path] = fmt.Sprintf("u%d", len(ids))
				text := name[path]
				if text == "" {
					text = "USB bus " + path
				}
				d.node(ids[path], strings.TrimSpace(path+" "+text))
			}
			return ids[path]
		}
		id(root)
		depth, width := 0, map[string]int{}
		for _, p := range paths {
			d.edge(id(parent(p)), id(p))
			depth = max(depth, strings.Count(p, ".")+1)
			width[parent(p)]++
		}
		d.dir = "TB"
		for _, n := range width {
			if n > depth+2 {
				d.dir = "LR"
			}
		}
		return d
	}
	var out []*diagram
	for _, bus := range buses {
		var paths []string
		for _, u := range devices {
			if b, _, _ := strings.Cut(u.Path, "-"); b == bus && u.Path != bus {
				paths = append(paths, u.Path)
			}
		}
		if d := draw(bus, paths); !d.tooBig() {
			out = append(out, d)
			continue
		}
		// Too big: one diagram per first-level port, under the bus.
		var tops []string
		for _, p := range paths {
			top, _, _ := strings.Cut(p, ".")
			if !slices.Contains(tops, top) {
				tops = append(tops, top)
			}
		}
		for _, top := range tops {
			var sub []string
			for _, p := range paths {
				if p == top || strings.HasPrefix(p, top+".") {
					sub = append(sub, p)
				}
			}
			out = append(out, draw(bus, sub))
		}
	}
	return out
}

func (m *md) pci(r *report.Report) {
	if len(r.PCI) == 0 {
		return
	}
	m.heading("PCI")
	var rows [][]string
	for _, d := range r.PCI {
		driver := ""
		if d.Driver != nil {
			driver = d.Driver.Name
		}
		rows = append(rows, []string{strings.TrimPrefix(d.Address, "0000:"), product(d.Identity), d.Class, driver,
			strings.TrimPrefix(d.Parent, "0000:")})
	}
	m.table([]string{"Address", "Device", "Class", "Driver", "Behind"}, rows)
}

func (m *md) sensors(r *report.Report) {
	var rows [][]string
	for _, s := range r.Sensors {
		for _, rd := range s.Readings {
			limit := ""
			if rd.Max != 0 {
				limit = strconv.FormatFloat(rd.Max, 'f', -1, 64) + " " + rd.Unit
			}
			rows = append(rows, []string{s.Chip, rd.Label, strconv.FormatFloat(rd.Value, 'f', -1, 64) + " " + rd.Unit, limit})
		}
	}
	if len(rows) > 0 {
		m.heading("Sensors")
		m.table([]string{"Chip", "Sensor", "Value", "High"}, rows)
	}
}

func (m *md) mountings(r *report.Report) {
	var rows [][]string
	for _, p := range r.CPU.Packages {
		if p.Mounting != "" && p.Populated {
			rows = append(rows, []string{p.Designation, "CPU (" + p.Package + ")", mountingText(&report.Mounting{Kind: p.Mounting})})
		}
	}
	for _, mod := range r.Memory.Modules {
		if mod.Mounting != nil {
			rows = append(rows, []string{mod.Locator, "memory module", mountingText(mod.Mounting)})
		}
	}
	for _, d := range r.PCI {
		if d.Mounting != nil {
			name := d.Class
			if d.Identity != nil && d.Identity.Model != "" {
				name = product(d.Identity)
			}
			rows = append(rows, []string{strings.TrimPrefix(d.Address, "0000:"), name, mountingText(d.Mounting)})
		}
	}
	for _, d := range r.Storage {
		if d.Mounting != nil {
			rows = append(rows, []string{d.Name, model(d.Identity), mountingText(d.Mounting)})
		}
	}
	if len(rows) > 0 {
		m.heading("Soldered or removable")
		m.table([]string{"Part", "What", "Mounting"}, rows)
	}
}

// lists writes what needs attention and what wasn't captured.
func (m *md) lists(r *report.Report) {
	var attention []string
	add := func(what string, h *report.Health) {
		if h == nil || h.Status != report.StatusWarning && h.Status != report.StatusFailing {
			return
		}
		reasons := h.Reasons
		if len(reasons) == 0 {
			reasons = []string{""}
		}
		for _, reason := range reasons {
			attention = append(attention, strings.TrimSuffix(strings.ToUpper(h.Status)+" "+what+": "+reason, ": "))
		}
	}
	add("CPU", r.CPU.Health)
	for _, mod := range r.Memory.Modules {
		add("memory "+mod.Locator, mod.Health)
	}
	for _, d := range r.Storage {
		add("disk "+d.Name, d.Health)
	}
	for _, n := range r.Network {
		add("network "+n.Name, n.Health)
	}
	for _, bt := range r.Batteries {
		add("battery "+bt.Name, bt.Health)
	}
	if len(attention) > 0 {
		m.heading("Needs attention (as recorded in this capture)")
		m.list(attention)
	}
	if len(r.Warnings) > 0 {
		m.heading("Not captured")
		m.list(r.Warnings)
	}
}
