package collect

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/jiegui2025/hwspec/internal/report"
	"github.com/jiegui2025/hwspec/internal/smbios"
)

// mountingClasses are the PCI base classes whose mounting is reported:
// storage, network, display, multimedia and wireless controllers.
var mountingClasses = []string{"01", "02", "03", "04", "0d"}

// mountings decides, for each part it can, whether it is soldered on or
// removable, from the evidence the capture holds (#103): the firmware's
// onboard devices and slots (SMBIOS types 41 and 9, root only), the
// kernel's onboard labels (no root), the PCI topology, memory module form
// factors and MMC card types. Nothing is guessed: when the evidence is
// missing or contradicts itself, the answer is "unknown" with the reason.
func (c *collector) mountings() {
	_, tableErr := c.smbiosStructures()
	m := &mounter{c: c, byAddr: map[string]*report.PCIDevice{}, slots: c.r.Board.Slots, conflicts: map[string][]int{}, mismatches: map[string][]string{}}
	for i := range c.r.PCI {
		m.byAddr[c.r.PCI[i].Address] = &c.r.PCI[i]
	}
	noTable := c.tableReason(tableErr)
	// Every function the records place, of any class, is out of the
	// competition for slots; only the five classes get a mounting.
	placed := map[string]*report.Mounting{}
	for i := range c.r.PCI {
		d := &c.r.PCI[i]
		if m.isPort(d) {
			continue
		}
		mt := m.byName(d, noTable)
		if mt != nil {
			placed[d.Address] = mt
		}
		if reported(d) {
			d.Mounting = mt
		}
	}
	if noTable == "" && len(m.slots) > 0 {
		m.byElimination(placed)
	}
	defer func() {
		// What neither a record nor function 0 decided, with nothing to
		// match against.
		for i := range c.r.PCI {
			d := &c.r.PCI[i]
			if d.Mounting != nil || !reported(d) {
				continue
			}
			switch {
			case noTable != "":
				d.Mounting = &report.Mounting{Kind: "unknown", Reason: noTable}
			case len(m.slots) == 0:
				d.Mounting = &report.Mounting{Kind: "unknown", Reason: "the firmware lists no expansion slots and doesn't list the device as onboard"}
			}
		}
	}()
	// A function nothing else decided goes with function 0, when a record
	// placed that: they are one card (or one chip).
	for i := range c.r.PCI {
		d := &c.r.PCI[i]
		f0 := placed[functionZero(d.Address)]
		if d.Mounting != nil || !reported(d) || f0 == nil || functionZero(d.Address) == d.Address {
			continue
		}
		mt := *f0
		mt.Evidence = append([]string{"function 0, " + functionZero(d.Address) + ", has this answer from these records"}, f0.Evidence...)
		d.Mounting = &mt
	}
	if broken := m.brokenAddresses(); len(broken) > 0 {
		c.warn("smbios slots: the firmware's addresses for %s name no device; slots are matched by type and width instead", strings.Join(broken, ", "))
	}
	for i := range c.r.Memory.Modules {
		c.r.Memory.Modules[i].Mounting = moduleMounting(&c.r.Memory.Modules[i])
	}
	for i := range c.r.Storage {
		if s := &c.r.Storage[i]; s.Transport == "mmc" {
			s.Mounting = mmcMounting(readStr("/sys/block/" + s.Name + "/device/type"))
		}
	}
}

func reported(d *report.PCIDevice) bool {
	return len(d.ClassCode) >= 2 && slices.Contains(mountingClasses, d.ClassCode[:2])
}

// tableReason says why the slot table is missing, or "" when it was read.
func (c *collector) tableReason(err error) string {
	switch {
	case err == nil:
		return ""
	case os.IsPermission(err) && !c.privileged:
		return "the firmware's slot table needs root (run with --full)"
	case os.IsNotExist(err):
		return "the kernel exposes no SMBIOS table"
	}
	return "the firmware's slot table can't be read: " + err.Error()
}

type mounter struct {
	c      *collector
	byAddr map[string]*report.PCIDevice
	slots  []report.Slot
	// conflicts are, per function, the slots that name it but contradict
	// it (Available or Unavailable, or a type or width that can't hold
	// it), and mismatches those claims in words.
	conflicts  map[string][]int
	mismatches map[string][]string
}

// isPort is true for a PCIe port: a root port (a bridge whose parent
// isn't a bridge: on a root bus, or behind a VMD controller) or a
// switch's downstream port (a bridge below a switch's upstream port).
// A switch's upstream port, or any bridge directly below a port, is part
// of a card, not a port.
func (m *mounter) isPort(d *report.PCIDevice) bool {
	if !isBridge(d) {
		return false
	}
	up := m.byAddr[d.Parent]
	// No parent, or one that isn't a port (a non-bridge such as a VMD
	// controller is never one): a root port, or a switch's downstream port.
	return up == nil || !m.isPort(up)
}

func isBridge(d *report.PCIDevice) bool { return strings.HasPrefix(d.ClassCode, "0604") }

func (m *mounter) isPortAddr(addr string) bool {
	d := m.byAddr[addr]
	return d != nil && m.isPort(d)
}

// byName decides what the firmware's own records say about d: type 41 or
// the kernel's SMBIOS label (onboard), and type 9 slots that name its
// address, its function 0's or its port's. It returns nil when nothing
// places d and the slots must be matched by elimination.
func (m *mounter) byName(d *report.PCIDevice, noTable string) *report.Mounting {
	var onboard, wrongType []string
	for _, o := range m.c.r.Board.OnboardDevices {
		if o.Address != d.Address {
			continue
		}
		claim := fmt.Sprintf("SMBIOS type 41 lists %q (%s) at %s", o.Designation, o.Type, o.Address)
		if onboardFits(o.Type, d.ClassCode) {
			onboard = append(onboard, claim)
		} else {
			wrongType = append(wrongType, claim+", but the device is class "+d.ClassCode)
		}
	}
	if d.LabelSource == "smbios" && len(onboard)+len(wrongType) == 0 {
		onboard = append(onboard, fmt.Sprintf("the kernel's label %q comes from SMBIOS type 41", d.Label))
	}
	var good, empty []int
	var named, mismatch, emptyClaims []string
	for i, s := range m.slots {
		where := m.names(s, d)
		if where == "" {
			continue
		}
		claim := fmt.Sprintf("SMBIOS type 9 lists %q (%s, %s) at %s", s.Designation, orUnknown(s.Type), usage(s), where)
		switch {
		case s.Usage == "Available" || s.Usage == "Unavailable":
			empty = append(empty, i)
			emptyClaims = append(emptyClaims, claim)
		case typeFits(s.Type, d.ClassCode) == no || m.narrower(s, d):
			m.conflicts[d.Address] = append(m.conflicts[d.Address], i)
			mismatch = append(mismatch, claim+", but that slot can't hold this device")
		default:
			good = append(good, i)
			named = append(named, claim)
		}
	}
	switch {
	case len(wrongType) > 0:
		return &report.Mounting{Kind: "unknown", Evidence: slices.Concat(wrongType, onboard, named, mismatch, emptyClaims),
			Reason: "the firmware's onboard record doesn't match the device"}
	case len(onboard) > 0 && len(good)+len(mismatch) == 0:
		// An empty slot at its address or port agrees with soldered on.
		return &report.Mounting{Kind: smbios.MountOnboard, Confidence: "high", Evidence: slices.Concat(onboard, emptyClaims)}
	case len(onboard) > 0:
		return &report.Mounting{Kind: "unknown", Evidence: slices.Concat(onboard, named, mismatch),
			Reason: "the firmware lists the device both onboard and in a slot"}
	case len(good) > 0 && len(mismatch)+len(empty) > 0:
		return &report.Mounting{Kind: "unknown", Evidence: slices.Concat(named, mismatch, emptyClaims),
			Reason: "the slots that name this device contradict each other"}
	case len(good) == 1:
		s := m.slots[good[0]]
		return &report.Mounting{Kind: smbios.MountSlot, Slot: s.Designation, SlotType: s.Type, Confidence: "high", Evidence: named}
	case len(good) > 1:
		if t, same := m.sameType(good); same && t != "" {
			return &report.Mounting{Kind: smbios.MountSlot, SlotType: t, Confidence: "medium", Evidence: named,
				Reason: "several slots name this device; which one holds it is unknown"}
		}
		return &report.Mounting{Kind: "unknown", Evidence: named, Reason: "several slots of different types name this device"}
	case noTable != "" || len(m.slots) == 0:
		return nil // nothing to match against: the fallback after function 0
	}
	// Named only by contradicting or empty slots: those stay possible
	// for it, with their claims, and elimination decides.
	m.conflicts[d.Address] = append(m.conflicts[d.Address], empty...)
	m.mismatches[d.Address] = slices.Concat(mismatch, emptyClaims)
	return nil
}

// card is one PCI device in a slot: its functions (function 0 and the
// rest share bus and device), the width of its port's link, and the
// slots it could be in.
type card struct {
	key   string
	funcs []*report.PCIDevice
	width int
	slots []int
}

// byElimination places the cards no record placed. Every such card
// directly behind a port (a root port or a switch's downstream port), or
// on a root bus with its own link, competes, whatever its class: a USB card or a switch card in a slot
// takes that slot as surely as a Wi-Fi card does. A card can be in a slot
// in use that no device names, at least as wide as its port, or in a slot
// that named it but contradicted it. Cards sharing slots form groups; a
// group is placed only if every card can have a slot of its own at once
// (a matching covering the group). A card then gets a slot by name only
// when every such matching gives it that slot and the slot's type fits,
// or the slot type only when all its possible slots share it.
func (m *mounter) byElimination(placed map[string]*report.Mounting) {
	cards := map[string]*card{}
	var keys []string
	for i := range m.c.r.PCI {
		d := &m.c.r.PCI[i]
		if m.isPort(d) || placed[d.Address] != nil || placed[functionZero(d.Address)] != nil {
			continue
		}
		if d.Parent != "" && !m.isPortAddr(d.Parent) {
			// Behind a card's own bridge: its slot, if any, is the one
			// that card is in.
			if reported(d) {
				d.Mounting = &report.Mounting{Kind: "unknown", Evidence: m.mismatches[d.Address],
					Reason: "it sits behind a bridge on a card (" + d.Parent + "), not directly behind a slot's port"}
			}
			continue
		}
		key := functionZero(d.Address)
		cd := cards[key]
		if cd == nil {
			cd = &card{key: key}
			cards[key] = cd
			keys = append(keys, key)
		}
		cd.funcs = append(cd.funcs, d)
		if w := m.width(d); w > cd.width {
			cd.width = w
		}
	}
	free := m.freeSlots()
	for _, k := range keys {
		cd := cards[k]
		for _, d := range cd.funcs {
			for _, i := range m.conflicts[d.Address] {
				if !slices.Contains(cd.slots, i) {
					cd.slots = append(cd.slots, i)
				}
			}
		}
		if cd.width == 0 {
			continue // no link: it can't be matched by width, and doesn't compete
		}
		for _, i := range free {
			if w := slotWidth(m.slots[i].Width); (w == 0 || w >= cd.width) && !slices.Contains(cd.slots, i) {
				cd.slots = append(cd.slots, i)
			}
		}
		slices.Sort(cd.slots)
	}
	ordered := make([]*card, len(keys))
	for i, k := range keys {
		ordered[i] = cards[k]
	}
	freeSet := map[int]bool{}
	for _, i := range free {
		freeSet[i] = true
	}
	for _, g := range groupCards(ordered) {
		m.placeGroup(g, freeSet)
	}
}

// groupCards groups the cards that share a possible slot, transitively,
// in the order their first card comes.
func groupCards(cards []*card) [][]*card {
	root := make([]int, len(cards))
	for i := range root {
		root[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if root[i] != i {
			root[i] = find(root[i])
		}
		return root[i]
	}
	for i := range cards {
		for j := range i {
			if overlaps(cards[i].slots, cards[j].slots) {
				root[find(i)] = find(j)
			}
		}
	}
	var out [][]*card
	index := map[int]int{}
	for i, cd := range cards {
		r := find(i)
		g, ok := index[r]
		if !ok {
			g = len(out)
			index[r] = g
			out = append(out, nil)
		}
		out[g] = append(out[g], cd)
	}
	return out
}

// placeGroup decides a group of cards that share possible slots under two
// models, and answers only what both agree on. (A) Every card is in a
// slot: a matching must give each card its own slot. (B) Every free slot
// in use holds one of the cards, and a card may be in none (soldered on):
// a matching must give each such slot its own card. A slot in use may
// hold what the capture doesn't see (a SATA M.2 drive, a USB module), and
// a card may be soldered on, so neither model alone is safe.
func (m *mounter) placeGroup(group []*card, free map[int]bool) {
	var must []int
	for _, cd := range group {
		for _, s := range cd.slots {
			if free[s] && m.slots[s].Usage == "In use" && !slices.Contains(must, s) {
				must = append(must, s)
			}
		}
	}
	slices.Sort(must)
	aHolds := maxMatching(group, nil) == len(group)
	bHolds := fillSlots(must, group, nil) == len(must)
	for _, cd := range group {
		var possible []int
		if aHolds {
			for _, s := range cd.slots {
				if maxMatching(group, map[*card]int{cd: s}) == len(group) {
					possible = append(possible, s)
				}
			}
		}
		// Under B the card is in a slot only if the slots in use can't all
		// be filled without it.
		needed := bHolds && fillSlots(must, group, cd) < len(must)
		f0 := cd.funcs[0]
		if d := m.byAddr[cd.key]; d != nil {
			f0 = d
		}
		for _, d := range cd.funcs {
			if reported(d) {
				d.Mounting = m.decide(d, f0, cd, group, aHolds, bHolds, needed, possible)
			}
		}
	}
}

// decide is the answer for function d of card cd, whose function 0 is f0
// (its class decides which slot types can hold the card).
func (m *mounter) decide(d, f0 *report.PCIDevice, cd *card, group []*card, aHolds, bHolds, needed bool, possible []int) *report.Mounting {
	why := "no slot names this device at its address or its port's"
	evidence := slices.Clone(m.mismatches[d.Address])
	if len(evidence) > 0 {
		why = "the slots that name this device can't hold it as the firmware describes them"
	}
	list := func(idx []int) string {
		var names []string
		for _, i := range idx {
			s := m.slots[i]
			names = append(names, fmt.Sprintf("%q (%s) at %s, %s", s.Designation, orUnknown(s.Type), orNone(s.Address), usage(s)))
		}
		return strings.Join(names, "; ")
	}
	unknown := func(reason string) *report.Mounting {
		if len(cd.slots) > 0 {
			evidence = append(evidence, "slots it could be in: "+list(cd.slots))
		}
		return &report.Mounting{Kind: "unknown", Evidence: evidence, Reason: why + reason}
	}
	switch {
	case cd.width == 0 && len(cd.slots) == 0:
		return unknown(", and it has no PCIe link to match a slot's width")
	case len(cd.slots) == 0:
		return unknown(", and no slot in use fits it")
	case !aHolds:
		return unknown(fmt.Sprintf("; %d cards compete for too few slots in use for each to have one", len(group)))
	case !bHolds:
		return unknown("; the cards here can't fill every slot in use around them, so something the capture doesn't see (a SATA M.2 drive, a USB module) is in one, and which slots these cards are in can't be told")
	case !needed:
		return unknown("; the slots in use can each hold another card without it, so it may be soldered on")
	}
	if len(possible) == 1 {
		s := m.slots[possible[0]]
		evidence = append(evidence, "the only slot it can be in: "+list(possible))
		// (A contradicting slot that named the card is never its only
		// possible one here: such a slot names a device, so it is never a
		// slot in use that must be filled, and the card isn't needed.)
		if typeFits(s.Type, f0.ClassCode) == no {
			return &report.Mounting{Kind: "unknown", Evidence: evidence, Reason: why + "; the only slot it can be in can't hold this kind of device"}
		}
		return &report.Mounting{Kind: smbios.MountSlot, Slot: s.Designation, SlotType: s.Type, Confidence: "medium", Evidence: evidence, Reason: why}
	}
	evidence = append(evidence, "slots it could be in: "+list(possible))
	if !slices.ContainsFunc(possible, func(i int) bool { return typeFits(m.slots[i].Type, f0.ClassCode) != no }) {
		return &report.Mounting{Kind: "unknown", Evidence: evidence, Reason: why + "; none of the slots it could be in can hold this kind of device"}
	}
	// Every placement puts it in one of these slots: it is in a slot,
	// with the type when they share one.
	var names []string
	for _, i := range possible {
		names = append(names, fmt.Sprintf("%q", m.slots[i].Designation))
	}
	mt := &report.Mounting{Kind: smbios.MountSlot, Confidence: "medium", Evidence: evidence,
		Reason: why + "; which of " + strings.Join(names, " or ") + " holds it is unknown"}
	if t, same := m.sameType(possible); same {
		mt.SlotType = t
	}
	return mt
}

// maxMatching is the size of a maximum matching of cards to their slots,
// with the forced pairs fixed (augmenting paths; groups are small).
func maxMatching(group []*card, forced map[*card]int) int {
	owner := map[int]*card{}
	size := 0
	for cd, s := range forced {
		owner[s] = cd
		size++
	}
	var try func(cd *card, seen map[int]bool) bool
	try = func(cd *card, seen map[int]bool) bool {
		for _, s := range cd.slots {
			if seen[s] {
				continue
			}
			seen[s] = true
			o, taken := owner[s]
			if f, ok := forced[o]; taken && ok && f == s {
				continue
			}
			if !taken || try(o, seen) {
				owner[s] = cd
				return true
			}
		}
		return false
	}
	for _, cd := range group {
		if _, ok := forced[cd]; ok {
			continue
		}
		if try(cd, map[int]bool{}) {
			size++
		}
	}
	return size
}

// fillSlots is the size of a maximum matching of the slots to cards that
// can be in them, without the skipped card: how many of the slots can
// each hold a card of their own.
func fillSlots(slots []int, group []*card, skip *card) int {
	owner := map[*card]int{}
	size := 0
	var try func(s int, seen map[*card]bool) bool
	try = func(s int, seen map[*card]bool) bool {
		for _, cd := range group {
			if cd == skip || seen[cd] || !slices.Contains(cd.slots, s) {
				continue
			}
			seen[cd] = true
			o, taken := owner[cd]
			if !taken || try(o, seen) {
				owner[cd] = s
				return true
			}
		}
		return false
	}
	for _, s := range slots {
		if try(s, map[*card]bool{}) {
			size++
		}
	}
	return size
}

// freeSlots are the slots that no device names (the firmware's address
// for them is broken or missing) and that the firmware doesn't call
// Available or Unavailable: a card may be in one. Only those In use must
// hold something (model B); a slot whose usage isn't given may hold a
// card but needn't.
func (m *mounter) freeSlots() []int {
	var out []int
	for i, s := range m.slots {
		if s.Usage != "Available" && s.Usage != "Unavailable" && !m.namesAnyDevice(s) {
			out = append(out, i)
		}
	}
	return out
}

func (m *mounter) namesAnyDevice(s report.Slot) bool {
	for i := range m.c.r.PCI {
		if m.names(s, &m.c.r.PCI[i]) != "" {
			return true
		}
	}
	return false
}

// names says where slot s names d ("this device's address 0000:01:00.0",
// "its port's address …"), or "" when it doesn't. A slot names a device
// by the endpoint in it (DSP0134 7.10.8), which for a multi-function
// device is function 0, or, commonly, by the port above it.
func (m *mounter) names(s report.Slot, d *report.PCIDevice) string {
	switch {
	case noAddress(s.Address):
		return ""
	case s.Address == d.Address:
		return "this device's address " + s.Address
	case s.Address == functionZero(d.Address):
		return "this device's function 0, " + s.Address
	case d.Parent != "" && s.Address == d.Parent && m.isPortAddr(d.Parent):
		return "its port's address " + s.Address
	}
	return ""
}

func (m *mounter) brokenAddresses() []string {
	var out []string
	for _, s := range m.slots {
		if !noAddress(s.Address) && m.byAddr[s.Address] == nil && !m.namesAnyDevice(s) {
			out = append(out, fmt.Sprintf("%q (%s)", s.Designation, s.Address))
		}
	}
	return out
}

// width is the width of the link d's slot carries: its port's maximum
// when it has a port, else its own; 0 when unknown.
func (m *mounter) width(d *report.PCIDevice) int {
	if p := m.byAddr[d.Parent]; p != nil && p.Link != nil {
		return p.Link.MaxWidth
	}
	if d.Link != nil {
		return d.Link.MaxWidth
	}
	return 0
}

// narrower is true when both widths are known and the slot is narrower
// than d's port: a wider slot wired to a narrower port is common.
func (m *mounter) narrower(s report.Slot, d *report.PCIDevice) bool {
	w, sw := m.width(d), slotWidth(s.Width)
	return w != 0 && sw != 0 && sw < w
}

func (m *mounter) sameType(idx []int) (string, bool) {
	if len(idx) == 0 {
		return "", false
	}
	for _, i := range idx {
		if m.slots[i].Type != m.slots[idx[0]].Type {
			return "", false
		}
	}
	return m.slots[idx[0]].Type, true
}

// noAddress is true for a slot address the firmware didn't give, or gave
// as all zeros: 0000:00:00.0 is the host bridge, never a slot's device.
func noAddress(a string) bool { return a == "" || a == "0000:00:00.0" }

// functionZero is a PCI address with its function set to 0.
func functionZero(addr string) string {
	if i := strings.LastIndexByte(addr, '.'); i > 0 {
		return addr[:i] + ".0"
	}
	return addr
}

func overlaps(a, b []int) bool {
	for _, x := range a {
		if slices.Contains(b, x) {
			return true
		}
	}
	return false
}

func usage(s report.Slot) string {
	if s.Usage == "" {
		return "usage not given"
	}
	return strings.ToLower(s.Usage)
}

func orUnknown(s string) string {
	if s == "" {
		return "type not given"
	}
	return s
}

// slotWidth reads "x4" as 4; other widths as 0.
func slotWidth(w string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(w, "x"))
	return n
}

func orNone(s string) string {
	if noAddress(s) {
		return "no address"
	}
	return s
}

type fit int

const (
	no fit = iota
	yes
	unsure
)

// typeFits says whether a slot of this type (DSP0134 Table 45 name) can
// hold a device of this PCI class (pci.ids class names): M.2 Key A/E for
// wireless, Key B for storage and wireless, Key M, U.2 and EDSFF for
// storage, MXM and AGP for display, OCP NIC for network; plain PCI and
// PCI Express slots for anything. Types that don't say (Other, Unknown,
// Proprietary, a riser, a code the table lacks) are unsure: they never
// contradict and fit anything.
func typeFits(slotType, class string) fit {
	has := func(s string) bool { return strings.Contains(slotType, s) }
	storage := strings.HasPrefix(class, "01")
	wireless := strings.HasPrefix(class, "0280") || strings.HasPrefix(class, "0d")
	is := func(ok bool) fit {
		if ok {
			return yes
		}
		return no
	}
	switch {
	case has("Key A"), has("Key E"):
		return is(wireless)
	case has("Key B"):
		return is(storage || wireless)
	case has("Key M"), has("U.2"), has("EDSFF"):
		return is(storage)
	case has("MXM"), has("AGP"):
		return is(strings.HasPrefix(class, "03"))
	case has("OCP NIC"):
		return is(strings.HasPrefix(class, "02"))
	case has("PCI"):
		return yes
	case slotType == "" || slotType == "Other" || slotType == "Unknown" || slotType == "Proprietary" ||
		has("Riser") || has("CXL") || strings.HasPrefix(slotType, "code "):
		return unsure
	}
	return no
}

// onboardTypes maps SMBIOS type 41 device types (DSP0134 Table 121) to the
// PCI classes they can be (pci.ids: a SATA or SAS controller may present
// as RAID 0104, a SATA one as IDE 0101); types not listed aren't checked.
var onboardTypes = map[string][]string{
	"Video": {"03"}, "Ethernet": {"0200"}, "Wireless LAN": {"0280", "0d"}, "Sound": {"04"},
	"SCSI Controller": {"0100", "0104", "0107"}, "PATA Controller": {"0101", "0105"},
	"SATA Controller": {"0101", "0104", "0106"}, "SAS Controller": {"0104", "0107"},
	"NVMe Controller": {"0108"}, "UFS Controller": {"0109"}, "eMMC": {"0805"},
}

func onboardFits(typ, class string) bool {
	prefixes, ok := onboardTypes[typ]
	if !ok {
		return true
	}
	return slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(class, p) })
}

// moduleForms maps memory form factors, as SMBIOS type 17 (DSP0134) or
// the module's SPD (internal/spd) name them, to mountings.
var moduleForms = map[string]string{
	"DIMM": smbios.MountSlot, "SODIMM": smbios.MountSlot, "RIMM": smbios.MountSlot, "SIMM": smbios.MountSlot,
	"FB-DIMM": smbios.MountSlot, "CAMM": smbios.MountSlot,
	"RDIMM": smbios.MountSlot, "UDIMM": smbios.MountSlot, "LRDIMM": smbios.MountSlot, "Mini-RDIMM": smbios.MountSlot,
	"Mini-UDIMM": smbios.MountSlot, "72b-SO-RDIMM": smbios.MountSlot, "72b-SO-UDIMM": smbios.MountSlot,
	"16b-SO-DIMM": smbios.MountSlot, "32b-SO-DIMM": smbios.MountSlot, "CUDIMM": smbios.MountSlot,
	"CSODIMM": smbios.MountSlot, "MRDIMM": smbios.MountSlot, "CAMM2": smbios.MountSlot, "DDIMM": smbios.MountSlot,
	"Row of chips": smbios.MountOnboard, "Chip": smbios.MountOnboard, "Die": smbios.MountOnboard,
	"Solder down": smbios.MountOnboard,
}

func moduleMounting(m *report.MemoryModule) *report.Mounting {
	kind, ok := moduleForms[m.FormFactor]
	if !ok {
		return &report.Mounting{Kind: "unknown", Reason: fmt.Sprintf("form factor %q doesn't say", m.FormFactor)}
	}
	source := "SMBIOS type 17"
	if strings.HasPrefix(m.Locator, "SPD ") {
		source = "the module's SPD"
	}
	evidence := []string{fmt.Sprintf("%s gives form factor %s", source, m.FormFactor)}
	// LPDDR comes in packages soldered on or in CAMM modules; a DIMM or
	// SO-DIMM form factor with an LPDDR type contradicts itself.
	if kind == smbios.MountSlot && strings.HasPrefix(m.Type, "LPDDR") && !strings.HasPrefix(m.FormFactor, "CAMM") {
		return &report.Mounting{Kind: "unknown", Evidence: append(evidence, "its type is "+m.Type),
			Reason: "an LPDDR type with a " + m.FormFactor + " form factor contradicts itself"}
	}
	mt := &report.Mounting{Kind: kind, Confidence: "high", Evidence: evidence}
	if kind == smbios.MountSlot && source == "SMBIOS type 17" {
		mt.Slot = m.Locator
	}
	return mt
}

// mmcMounting reads the MMC card type the kernel reports
// (drivers/mmc/core/bus.c): an SD card sits in a card slot; an MMC-type
// card is usually eMMC, soldered, but a removable MMC card reads the same.
func mmcMounting(typ string) *report.Mounting {
	switch typ {
	case "SD", "SDcombo":
		return &report.Mounting{Kind: smbios.MountSlot, SlotType: "SD card", Confidence: "high", Evidence: []string{"the kernel reports card type " + typ}}
	case "MMC":
		return &report.Mounting{Kind: smbios.MountOnboard, Confidence: "medium", Evidence: []string{"the kernel reports card type MMC"},
			Reason: "eMMC is soldered, but a removable MMC card reports the same type"}
	}
	return &report.Mounting{Kind: "unknown", Reason: fmt.Sprintf("card type %q doesn't say", typ)}
}
