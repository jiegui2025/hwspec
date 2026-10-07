package advisor

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jiegui2025/hwspec/internal/kb"
	"github.com/jiegui2025/hwspec/internal/report"
)

// The storage answers of #9 (#108): each NVMe drive's bus path and the
// most it allows, the model's M.2 slots, and what a faster drive would
// gain. Each comes from the capture and the model's knowledge-base entry
// (#25's storage_slots group), cites what it used, and says why when it
// can't be told.

// storageData is a model's storage_slots group, read strictly.
type storageData struct {
	M2 struct {
		Count      claims[int]      `json:"count"`
		Lengths    claims[[]int]    `json:"lengths"`
		Interfaces claims[[]string] `json:"interfaces"`
	} `json:"m2"`
}

var (
	m2Lengths    = []int{2230, 2242, 2260, 2280, 22110}
	m2Interfaces = map[string]string{"nvme": "NVMe (PCIe)", "sata": "SATA"}
)

func decodeStorage(raw json.RawMessage) (*storageData, error) {
	d, err := decodeData[storageData](raw)
	if err != nil {
		return nil, err
	}
	if len(d.M2.Count)+len(d.M2.Lengths)+len(d.M2.Interfaces) == 0 {
		return nil, errors.New("m2: no claims (leave the group out when the documents say nothing)")
	}
	var errs []error
	for _, c := range d.M2.Count {
		if c.Value <= 0 {
			errs = append(errs, fmt.Errorf("m2.count: %d isn't positive", c.Value))
		}
	}
	for _, c := range d.M2.Lengths {
		for _, l := range c.Value {
			if !slices.Contains(m2Lengths, l) {
				errs = append(errs, fmt.Errorf("m2.lengths: %d isn't an M.2 length (%v)", l, m2Lengths))
			}
		}
	}
	for _, c := range d.M2.Interfaces {
		for _, i := range c.Value {
			if m2Interfaces[i] == "" {
				errs = append(errs, fmt.Errorf("m2.interfaces: %q isn't nvme or sata", i))
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return &d, nil
}

// modelStorage returns the storage group of the capture's model entry, or
// why there is none to use.
func modelStorage(k *kb.KB, r *report.Report) (*storageData, string) {
	m := modelFor(k, r)
	if m == nil {
		return nil, "the knowledge base has no entry for this model"
	}
	raw, ok := m.Data["storage_slots"]
	if !ok {
		return nil, "the knowledge base has no storage data for this model"
	}
	d, err := decodeStorage(raw)
	if err != nil {
		return nil, fmt.Sprintf("this build can't read the knowledge base's storage data for this model (update hwspec): %v", err)
	}
	return d, ""
}

func init() {
	register("storage-upgrade", check{
		run:   storageUpgrade,
		needs: []string{"pci[].pcie_link"},
		example: func() (kb.Rule, *report.Report) {
			return kb.Rule{ID: "storage.upgrade", Check: "storage-upgrade", Category: "upgrade", Severity: "info", Title: "Storage", Src: []string{kb.Capture.ID}},
				storageExample()
		},
		exampleData: func() ([]kb.Source, []kb.Model) {
			return []kb.Source{{ID: "example-datasheet", URL: "https://example.com/ds.pdf", Published: "2019-12", Retrieved: "2026-10-07",
					Licence: "proprietary", Confidence: "oem-doc", LinkOnly: true, Locator: "p. 2"}},
				[]kb.Model{{ID: "hp.example", Match: kb.ModelMatch{SysVendor: "HP", ProductName: "HP EliteDesk 800 G5 Desktop Mini"},
					Data: map[string]json.RawMessage{"storage_slots": json.RawMessage(`{"m2": {
						"count": [{"value": 2, "src": "example-datasheet"}],
						"lengths": [{"value": [2230, 2280], "src": "example-datasheet"}],
						"interfaces": [{"value": ["nvme"], "src": "example-datasheet"}]}}`)}}}
		},
	})
}

// storageExample is the reference machine's NVMe path, behind a chipset
// root port.
func storageExample() *report.Report {
	link := &report.PCIeLink{Speed: "8.0 GT/s PCIe", Width: 4, MaxSpeed: "8.0 GT/s PCIe", MaxWidth: 4}
	return &report.Report{
		System: report.System{Identity: &report.Identity{Vendor: "HP", Model: "HP EliteDesk 800 G5 Desktop Mini"}},
		PCI: []report.PCIDevice{
			{Address: "0000:00:1b.0", ClassCode: "060400", Link: link,
				Identity: &report.Identity{Vendor: "Intel Corporation", Model: "300/C240 Series Chipset Family PCIe Root Port #21"}},
			{Address: "0000:01:00.0", Parent: "0000:00:1b.0", ClassCode: "010802", Link: link,
				Identity: &report.Identity{Vendor: "Samsung Electronics Co Ltd", Model: "NVMe SSD Controller SM981/PM981/PM983"}},
		},
	}
}

func storageUpgrade(in *Input, _ *kb.Rule) ([]hit, error) {
	r := in.Report
	b := &answerer{k: in.KB}
	d, why := modelStorage(in.KB, r)
	var paths []pciePath
	for i, p := range r.PCI {
		if strings.HasPrefix(p.ClassCode, "0108") {
			paths = append(paths, pathOf(r, i))
		}
	}
	if len(paths) == 0 && d == nil {
		return nil, nil // nothing to say about storage
	}
	b.bus(r, paths)
	b.storageSlots(r, paths, d, why)
	b.fasterDrive(r, paths, d)
	b.allowlist(in, "ssd", "SSDs")
	return []hit{{evidence: b.evidence, answers: b.list, used: b.used}}, nil
}

// pcieLink is one link on a drive's way to its root port: the port above
// it (a root port or a switch's downstream port) and the device below it
// (the drive, or a switch's upstream port), each read at its own end. It
// allows the lower maximum speed and width of its two ends.
type pcieLink struct {
	port, dev int     // indexes in r.PCI
	speed     float64 // GT/s per lane
	width     int
}

// mbps is what the link carries, as the kernel counts it.
func (l pcieLink) mbps() float64 { return laneMbps(l.speed) * float64(l.width) }

func (l pcieLink) words() string { return linkWords(l.speed, l.width) }

// pciePath is an NVMe drive's links up to its root port, nearest first,
// or why they can't be told (with the part whose link is missing).
type pciePath struct {
	index int   // the drive's index in r.PCI
	chain []int // the drive and the bridges above it, nearest first
	links []pcieLink
	why   string
	bad   int // the part whose link why is about; -1 for none
}

func (p pciePath) known() bool { return p.why == "" }

// slowest is the link that carries the least, nearest the drive on a tie:
// the most the path allows (the kernel's pcie_bandwidth_available).
func slowest(links []pcieLink) pcieLink {
	s := links[0]
	for _, l := range links[1:] {
		if l.mbps() < s.mbps() {
			s = l
		}
	}
	return s
}

// pcieWidths are the link widths PCIe defines; the kernel gives 255 for
// one it can't read.
var pcieWidths = []int{1, 2, 4, 8, 12, 16, 32}

// maxOf is the most one end of a link allows, if the capture has it.
func maxOf(l *report.PCIeLink) (float64, int, bool) {
	if l == nil || gts(l.MaxSpeed) == 0 || !slices.Contains(pcieWidths, l.MaxWidth) {
		return 0, 0, false
	}
	return gts(l.MaxSpeed), l.MaxWidth, true
}

// tunnelled says a bridge is a Thunderbolt or USB4 one, whose PCIe links
// report a nominal speed, not the tunnel's.
func tunnelled(p report.PCIDevice) bool {
	if p.Identity == nil {
		return false
	}
	n := strings.ToLower(p.Identity.Model)
	return strings.Contains(n, "thunderbolt") || strings.Contains(n, "usb4")
}

// pathOf walks from a drive up the bridges (class 0604) to its root port,
// whose parent is none or not a bridge (Intel VMD's controller), and pairs
// them into links: the drive with its port, then each switch's upstream
// port with the port above it (a switch's own ports aren't a link).
func pathOf(r *report.Report, i int) pciePath {
	byAddr := map[string]int{}
	for j, p := range r.PCI {
		byAddr[p.Address] = j
	}
	p := pciePath{index: i, chain: []int{i}, bad: -1}
	for top := i; r.PCI[top].Parent != ""; {
		j, ok := byAddr[r.PCI[top].Parent]
		switch {
		case !ok:
			p.why = fmt.Sprintf("%s, the bridge above %s, isn't in the capture", shortAddr(r.PCI[top].Parent), shortAddr(r.PCI[top].Address))
			return p
		case !strings.HasPrefix(r.PCI[j].ClassCode, "0604"):
		case slices.Contains(p.chain, j):
			p.why = "the capture's bridges above it form a loop"
			return p
		default:
			p.chain = append(p.chain, j)
			top = j
			continue
		}
		break
	}
	if len(p.chain) == 1 {
		p.why = "no PCIe port above it is in the capture"
		return p
	}
	if len(p.chain)%2 != 0 {
		p.why = "the bridges above it don't pair into links (a port without the device below it, or the reverse)"
		return p
	}
	for k := 0; k < len(p.chain); k += 2 {
		dev, port := p.chain[k], p.chain[k+1]
		ds, dw, devOK := maxOf(r.PCI[dev].Link)
		ps, pw, portOK := maxOf(r.PCI[port].Link)
		if !devOK || !portOK {
			p.bad = dev
			if devOK {
				p.bad = port
			}
			p.why = fmt.Sprintf("the most %s's PCIe link allows isn't in the capture", shortAddr(r.PCI[p.bad].Address))
			return p
		}
		p.links = append(p.links, pcieLink{port: port, dev: dev, speed: min(ds, ps), width: min(dw, pw)})
	}
	for _, j := range p.chain[1:] {
		if tunnelled(r.PCI[j]) {
			p.bad = j
			p.why = fmt.Sprintf("it's behind %s, a Thunderbolt or USB4 bridge, whose links give a nominal speed, not the tunnel's", deviceWords(r.PCI[j]))
			return p
		}
	}
	return p
}

// gts reads "8.0 GT/s PCIe" as 8.0; 0 when it can't.
func gts(s string) float64 {
	f, _, _ := strings.Cut(s, " ")
	v, err := strconv.ParseFloat(f, 64)
	if err != nil || v <= 0 || !strings.Contains(s, "GT/s") {
		return 0
	}
	return v
}

// pcieGen names a lane speed's PCIe generation.
var pcieGen = map[float64]string{2.5: "1.0", 5: "2.0", 8: "3.0", 16: "4.0", 32: "5.0", 64: "6.0"}

// laneMbps is what a lane carries after encoding, as the kernel counts it
// (drivers/pci/pci.h, PCIE_SPEED2MBS_ENC): 8b/10b to 5 GT/s, 128b/130b
// to 32 GT/s, 1/1 at 64 GT/s.
func laneMbps(gt float64) float64 {
	switch {
	case gt >= 64:
		return gt * 1000
	case gt >= 8:
		return gt * 1000 * 128 / 130
	}
	return gt * 1000 * 8 / 10
}

// linkWords is "PCIe 3.0 x4 (8.0 GT/s ×4, ≈3.9 GB/s)".
func linkWords(gt float64, width int) string {
	gen := pcieGen[gt]
	if gen == "" {
		gen = "?"
	}
	return fmt.Sprintf("PCIe %s x%d (%.1f GT/s ×%d, ≈%s)", gen, width, gt, width, gbps(laneMbps(gt)*float64(width)))
}

func gbps(mbps float64) string { return fmt.Sprintf("%.1f GB/s", mbps/8000) }

func shortAddr(a string) string { return strings.TrimPrefix(a, "0000:") }

func deviceWords(p report.PCIDevice) string {
	name := shortAddr(p.Address)
	if p.Identity != nil {
		if n := strings.TrimSpace(p.Identity.Vendor + " " + p.Identity.Model); n != "" {
			name += " (" + n + ")"
		}
	}
	return name
}

// linkName is "its link" for the drive's own, else the two ends.
func linkName(r *report.Report, p pciePath, l pcieLink) string {
	if l.dev == p.index {
		return "its link"
	}
	return fmt.Sprintf("the link between %s and %s", shortAddr(r.PCI[l.port].Address), shortAddr(r.PCI[l.dev].Address))
}

// linkEvidence notes both ends of a link, once.
func (b *answerer) linkEvidence(r *report.Report, l pcieLink) {
	for _, j := range []int{l.dev, l.port} {
		at := fmt.Sprintf("pci[%d].pcie_link", j)
		if slices.ContainsFunc(b.evidence, func(e Evidence) bool { return e.Path() == at+".max_speed" }) {
			continue
		}
		b.capture(Present(at+".max_speed", r.PCI[j].Link.MaxSpeed), Present(at+".max_width", r.PCI[j].Link.MaxWidth))
	}
}

// bus answers each NVMe drive's path: the most it allows and which link
// sets it, the bridges it goes through, a link running below its most at
// capture, and what lies beyond the root port.
func (b *answerer) bus(r *report.Report, paths []pciePath) {
	var parts []string
	for _, p := range paths {
		parts = append(parts, b.busOne(r, p))
	}
	if len(parts) == 0 {
		b.add("bus", false, "No NVMe drive is in the capture")
		return
	}
	b.add("bus", !slices.ContainsFunc(paths, func(p pciePath) bool { return !p.known() }), strings.Join(parts, "; "))
}

func (b *answerer) busOne(r *report.Report, p pciePath) string {
	dev := r.PCI[p.index]
	if !p.known() {
		if p.bad >= 0 {
			at := fmt.Sprintf("pci[%d].pcie_link", p.bad)
			if l := r.PCI[p.bad].Link; l == nil {
				b.capture(Absent(at))
			} else {
				b.capture(Present(at+".max_speed", l.MaxSpeed), Present(at+".max_width", l.MaxWidth))
			}
		}
		return fmt.Sprintf("NVMe %s: the most its path allows can't be told: %s", deviceWords(dev), p.why)
	}
	slow := slowest(p.links)
	b.linkEvidence(r, slow)
	var via []string
	for _, j := range p.chain[1:] {
		via = append(via, shortAddr(r.PCI[j].Address))
	}
	head := fmt.Sprintf("NVMe %s at %s via %s", deviceWords(dev), slow.words(), strings.Join(via, ", "))
	if slow.dev != p.index {
		head += ", set by " + linkName(r, p, slow)
	}
	parts := []string{head}
	if s, w, _ := maxOf(dev.Link); laneMbps(s)*float64(w) > slow.mbps() {
		parts = append(parts, fmt.Sprintf("the drive can do %s, but the path allows less", linkWords(s, w)))
	}
	for _, l := range p.links {
		now := r.PCI[l.dev].Link
		if s := gts(now.Speed); s > 0 && s < l.speed {
			b.capture(Present(fmt.Sprintf("pci[%d].pcie_link.speed", l.dev), now.Speed))
			parts = append(parts, fmt.Sprintf("at capture %s ran at %.1f GT/s (links may slow down when idle)", linkName(r, p, l), s))
		}
		if now.Width > 0 && now.Width < l.width { // 255, unknown, is never less
			b.capture(Present(fmt.Sprintf("pci[%d].pcie_link.width", l.dev), now.Width))
			parts = append(parts, fmt.Sprintf("at capture %s ran at x%d of x%d: idling doesn't narrow a link, so it may be badly seated or have a signal fault", linkName(r, p, l), now.Width, l.width))
		}
	}
	return strings.Join(append(parts, chipsetNote(r, p)), "; ")
}

// chipsetNote says where the chipset's link to the CPU is, which every
// device on the chipset shares, told by the bridges' names: beyond a
// chipset root port (Intel's PCH), out of the capture's sight; or on the
// path, when the chipset is a switch (AMD's), the link above its
// upstream port.
func chipsetNote(r *report.Report, p pciePath) string {
	top := -1 // the chipset bridge nearest the CPU
	for _, j := range p.chain[1:] {
		if n := strings.ToLower(identityModel(r.PCI[j])); strings.Contains(n, "chipset") || strings.Contains(n, "pch") {
			top = j
		}
	}
	root := p.chain[len(p.chain)-1]
	if top == root {
		return fmt.Sprintf("%s is a chipset port: the chipset's own link to the CPU isn't in the capture, may limit the drive further, and is shared with the chipset's other devices", shortAddr(r.PCI[root].Address))
	}
	for _, l := range p.links {
		if top >= 0 && l.dev == top {
			return fmt.Sprintf("%s is the chipset's link to the CPU, shared with the chipset's other devices", linkName(r, p, l))
		}
	}
	return fmt.Sprintf("whether %s is the CPU's port or a chipset's isn't in the capture: a chipset's link to the CPU would limit the drive further and be shared", shortAddr(r.PCI[root].Address))
}

func identityModel(p report.PCIDevice) string {
	if p.Identity == nil {
		return ""
	}
	return p.Identity.Model
}

// storageSlots answers what the model's M.2 storage slots are, from its
// documents, and which one holds each drive, from the firmware's table.
func (b *answerer) storageSlots(r *report.Report, paths []pciePath, d *storageData, why string) {
	var parts []string
	if d != nil {
		if len(d.M2.Count) > 0 {
			parts = append(parts, say(b, d.M2.Count, ints("%d M.2 storage slots")))
		}
		if len(d.M2.Lengths) > 0 {
			parts = append(parts, say(b, d.M2.Lengths, func(ls []int) string {
				s := make([]string, len(ls))
				for i, l := range ls {
					s[i] = strconv.Itoa(l)
				}
				return "M.2 " + strings.Join(s, "/")
			}))
		}
		if len(d.M2.Interfaces) > 0 {
			parts = append(parts, say(b, d.M2.Interfaces, func(is []string) string {
				s := make([]string, len(is))
				for i, v := range is {
					s[i] = m2Interfaces[v]
				}
				return strings.Join(s, " or ")
			}))
		}
	} else {
		parts = append(parts, "The model's M.2 slots are unknown: "+why)
	}
	for _, p := range paths {
		dev := r.PCI[p.index]
		parts = append(parts, b.placement(fmt.Sprintf("pci[%d].mounting", p.index), shortAddr(dev.Address), dev.Mounting))
	}
	b.add("slots", d != nil, strings.Join(parts, "; "))
}

// placement says which slot holds a device (name), as the firmware's
// slot table gives it at the capture path at, and records what it read:
// a slot it's less than sure of is "likely", with the reason.
func (b *answerer) placement(at, name string, m *report.Mounting) string {
	switch {
	case m == nil:
		b.capture(Absent(at))
		return fmt.Sprintf("which slot holds %s isn't in the capture", name)
	case m.Kind == "slot" && m.Slot != "" && m.Confidence == "high":
		b.capture(Present(at+".slot", m.Slot))
		return fmt.Sprintf("%s is in %s", name, slotWords(m))
	case m.Kind == "slot" && m.Slot != "":
		b.capture(Present(at+".slot", m.Slot), Present(at+".confidence", m.Confidence))
		return fmt.Sprintf("%s is likely in %s, with %s confidence: %s", name, slotWords(m), cmpOr(m.Confidence, "unstated"), cmpOr(m.Reason, "no reason given"))
	case m.Kind == "slot":
		b.capture(Present(at+".kind", m.Kind))
		slot := "a slot"
		if m.SlotType != "" {
			slot += " of type " + m.SlotType
		}
		return fmt.Sprintf("%s is in %s: %s", name, slot, cmpOr(m.Reason, "which one is unknown"))
	default:
		b.capture(Present(at+".kind", m.Kind))
		return fmt.Sprintf("which slot holds %s can't be told: %s", name, cmpOr(m.Reason, m.Kind))
	}
}

// slotWords is "M2_1 (its type)", the type when the firmware gives one.
func slotWords(m *report.Mounting) string {
	if m.SlotType == "" {
		return m.Slot
	}
	return m.Slot + " (" + m.SlotType + ")"
}

// m2Width is the most lanes an M.2 NVMe drive uses.
const m2Width = 4

// fasterDrive says what a faster drive would gain: an x4 drive at the
// most its port allows, no more than the links above allow; and nothing
// in slots that take SATA only.
func (b *answerer) fasterDrive(r *report.Report, paths []pciePath, d *storageData) {
	var parts []string
	known := true
	if d != nil && len(d.M2.Interfaces) > 0 && len(with(d.M2.Interfaces, "nvme")) == 0 {
		if len(paths) == 0 {
			b.add("faster", true, "The M.2 storage slots take SATA only, "+per(b, d.M2.Interfaces)+": an NVMe SSD won't work in them")
			return
		}
		var drives []string
		for _, p := range paths {
			drives = append(drives, shortAddr(r.PCI[p.index].Address))
		}
		parts = append(parts, fmt.Sprintf("The knowledge base says the M.2 storage slots take SATA only, %s, but the capture has NVMe %s: in another slot or an adapter, or the knowledge base is wrong", per(b, d.M2.Interfaces), strings.Join(drives, ", ")))
		known = false
	}
	for _, p := range paths {
		addr := shortAddr(r.PCI[p.index].Address)
		if !p.known() {
			parts = append(parts, fmt.Sprintf("what a faster drive in place of %s would get can't be told: %s", addr, p.why))
			known = false
			continue
		}
		port := r.PCI[p.links[0].port]
		ps, pw, _ := maxOf(port.Link)
		first := pcieLink{port: p.links[0].port, dev: p.index, speed: ps, width: min(pw, m2Width)}
		b.linkEvidence(r, p.links[0])
		now := slowest(p.links)
		fast := slowest(append([]pcieLink{first}, p.links[1:]...))
		gains, slower := fast.mbps() > now.mbps(), fast.mbps() < now.mbps()
		// A drive that gains names the port's generation, the most it can
		// use; one that doesn't, the generation past it, which gains
		// nothing. A wider drive (an x8 add-in card) runs faster than any
		// M.2 drive can here.
		var text string
		switch {
		case gains:
			name := "A faster"
			if g := pcieGen[ps]; g != "" {
				name = "A PCIe " + g
			}
			if _, dw, _ := maxOf(r.PCI[p.index].Link); dw < first.width {
				name += fmt.Sprintf(" x%d", first.width)
			}
			text = fmt.Sprintf("%s NVMe SSD in place of %s would run at %s here, the most %s's port allows", name, addr, first.words(), shortAddr(port.Address))
		case slower:
			text = fmt.Sprintf("An M.2 NVMe SSD in place of %s would run slower, at %s here, the most %s's port allows", addr, first.words(), shortAddr(port.Address))
		default:
			name := "A faster"
			if g := pcieGen[ps*2]; g != "" {
				name = "A PCIe " + g + " or later"
			}
			text = fmt.Sprintf("%s NVMe SSD in place of %s would still run at %s here, the most %s's port allows", name, addr, first.words(), shortAddr(port.Address))
		}
		switch {
		case pw > m2Width:
			text += fmt.Sprintf(" an M.2 drive (x%d at most; the port is x%d)", m2Width, pw)
		case pw < m2Width:
			text += fmt.Sprintf("; an x%d drive runs at x%d", m2Width, pw)
		}
		if fast != first {
			b.linkEvidence(r, fast)
			text += fmt.Sprintf("; %s allows %s, so it gets at most that", linkName(r, p, fast), fast.words())
		}
		switch {
		case gains:
			text += fmt.Sprintf("; it gets up to ≈%s (now ≈%s), and a faster one gains nothing more", gbps(fast.mbps()), gbps(now.mbps()))
		case slower:
			text += fmt.Sprintf("; that's ≈%s against ≈%s now, and no faster M.2 drive gains anything here", gbps(fast.mbps()), gbps(now.mbps()))
		default:
			text += "; no faster drive gains anything here"
		}
		parts = append(parts, text)
	}
	if len(parts) == 0 {
		b.add("faster", false, "What a faster drive would gain can't be told: no NVMe drive is in the capture")
		return
	}
	b.add("faster", known, strings.Join(parts, "; "))
}

// allowlist says what the knowledge base knows of the vendor restricting
// third-party parts of a kind (a restricts code): never "no restriction"
// from silence (#25). A policy whose restricts claims don't name the kind
// is about other parts; one without restricts claims may cover it.
func (b *answerer) allowlist(in *Input, kind, parts string) {
	applies, undetermined, otherBIOS := allowlistsFor(in.KB, in.Report)
	vendor := "the vendor"
	if id := in.Report.System.Identity; id != nil && id.Vendor != "" {
		vendor = id.Vendor
	}
	ids := func(l []*kb.Allowlist) string {
		var out []string
		for _, a := range l {
			if covers(a, kind) {
				out = append(out, a.ID)
			}
		}
		return strings.Join(out, ", ")
	}
	var says []string
	if s := ids(applies); s != "" {
		says = append(says, fmt.Sprintf("The knowledge base has %s firmware policies that may restrict third-party %s in this model (%s): check their terms before buying one", vendor, parts, s))
	}
	if s := ids(undetermined); s != "" {
		says = append(says, fmt.Sprintf("%s's policies %s may cover it, but their BIOS range can't be checked against this BIOS's version", vendor, s))
	}
	if s := ids(otherBIOS); s != "" {
		says = append(says, fmt.Sprintf("%s's policies %s cover this model with other BIOS versions, not this one", vendor, s))
	}
	if m := modelFor(in.KB, in.Report); m != nil && m.Data["allowlist"] != nil {
		says = append(says, "the model's entry has an allowlist group this build doesn't read yet")
	}
	if len(says) == 0 {
		b.add("allowlist", false, fmt.Sprintf("Whether %s's firmware restricts third-party %s in this model is unknown: the knowledge base has no source on it", vendor, parts))
		return
	}
	if len(ids(applies)) == 0 {
		says[0] = fmt.Sprintf("Whether %s's firmware restricts third-party %s in this BIOS is unknown: %s", vendor, parts, says[0])
	}
	b.add("allowlist", false, strings.Join(says, "; "))
}

// covers says a policy may restrict a kind of part: its restricts claims
// name it, or it has none (or none this build can read).
func covers(a *kb.Allowlist, kind string) bool {
	raw, ok := a.Data["restricts"]
	if !ok {
		return true
	}
	cs, err := decodeData[claims[[]string]](raw)
	if err != nil || len(cs) == 0 {
		return true
	}
	return len(with(cs, kind)) > 0
}
