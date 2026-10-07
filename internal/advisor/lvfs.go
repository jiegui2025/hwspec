package advisor

import (
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jiegui2025/hwspec/internal/kb"
	"github.com/jiegui2025/hwspec/internal/report"
)

// Firmware compared with LVFS's verified catalogue (#226, ADR 0012):
// parts are matched by the GUIDs a capture records (NVMe instance IDs, the
// ESRT), versions compared only through their LVFS::VersionFormat.

// LVFS is the verified catalogue as the advisor takes it.
type LVFS struct {
	Source   string    // "hwspec" (its own cache) or "fwupd" (fwupd's cache)
	SignedAt time.Time // from the catalogue's signature: the data's date
	// Components are the catalogue's firmware components by GUID (lower
	// case).
	Components map[string][]LVFSComponent
}

// LVFSComponent is one <component>. LVFS splits a firmware stream (one
// ID) across several, often a release each; the advisor merges them.
type LVFSComponent struct {
	ID, Name, Developer string
	VersionFormat       string // empty when LVFS gives none
	Releases            []LVFSRelease
}

// LVFSRelease is one version LVFS offers, with its component's
// requirements.
type LVFSRelease struct {
	Version  string
	Date     time.Time
	Urgency  string
	CVEs     []string
	Requires []LVFSRequirement
	// alternatives are the requirements of each component listing this
	// version, when several do: any of them may be met.
	alternatives [][]LVFSRequirement
}

// LVFSRequirement is one of a component's <requires>: Kind its element
// (firmware, id, hardware, not_hardware, client), Text what it names.
type LVFSRequirement struct {
	Kind, Compare, Version, Text string
}

// What a part's firmware is against LVFS.
type lvfsCase int

const (
	lvfsUpToDate    lvfsCase = iota // nothing newer fwupd would install
	lvfsUrgent                      // a newer release, urgency high or critical
	lvfsUpdate                      // a newer release, lower urgency
	lvfsOtherVendor                 // a newer release, from a vendor that is neither the machine's nor the device's
	lvfsDiffers                     // can't be ordered
)

// firmwareIndexWarning is the one warning when the firmware index is
// missing: both sources, or the one a rule needs.
func firmwareIndexWarning(in *Input) string {
	switch {
	case in.LVFS == nil && in.LinuxFirmware == nil:
		return "no firmware index: run `hwspec firmware update` to compare firmware with LVFS and linux-firmware"
	case in.LVFS == nil:
		return "no LVFS catalogue: run `hwspec firmware update` to compare firmware with LVFS"
	}
	return "no linux-firmware index: run `hwspec firmware update` to compare firmware with linux-firmware's latest release"
}

func init() {
	for _, c := range []struct {
		name  string
		want  lvfsCase
		title string
		sev   kb.Severity
	}{
		{"lvfs-up-to-date", lvfsUpToDate, "No firmware update on LVFS", "info"},
		{"lvfs-update-urgent", lvfsUrgent, "Firmware update with security or urgent fixes", "warning"},
		{"lvfs-update", lvfsUpdate, "Firmware update available", "info"},
		{"lvfs-other-vendor", lvfsOtherVendor, "Newer firmware from another vendor", "info"},
		{"lvfs-differs", lvfsDiffers, "Firmware not comparable with LVFS's", "info"},
	} {
		register(c.name, check{
			run:         lvfsCheck(c.want),
			needs:       []string{"storage[].firmware.instance_ids", "system.esrt", "the firmware index"},
			available:   func(in *Input) bool { return in.LVFS != nil },
			unavailable: firmwareIndexWarning,
			example: func() (kb.Rule, *report.Report) {
				return kb.Rule{ID: "firmware." + c.name, Check: c.name, Category: "firmware", Severity: c.sev, Title: c.title, Src: []string{"kernel"}},
					lvfsExampleReport()
			},
			exampleInput: func(in *Input) { in.LVFS = lvfsExample(c.want) },
		})
	}
}

// lvfsExampleReport is an HP machine with a Samsung NVMe drive.
func lvfsExampleReport() *report.Report {
	id := `NVME\VEN_144D&DEV_A808`
	return &report.Report{
		System: report.System{Identity: &report.Identity{Vendor: "HP"}},
		PCI:    []report.PCIDevice{{Address: "0000:01:00.0", VendorID: "144d", DeviceID: "a808", Identity: &report.Identity{Vendor: "Samsung Electronics Co Ltd"}}},
		Storage: []report.Disk{{Name: "nvme0n1", Transport: "nvme", Firmware: &report.Firmware{Version: "1.2.0", Source: "nvme",
			InstanceIDs: []report.InstanceID{{ID: id, GUID: "47335265-a509-51f7-841e-1c94911af66b"}}}}},
	}
}

// lvfsExample is a catalogue that puts the example's drive in case want.
func lvfsExample(want lvfsCase) *LVFS {
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	c := LVFSComponent{ID: "com.samsung.example.firmware", Name: "Example SSD", Developer: "Samsung", VersionFormat: "triplet",
		Releases: []LVFSRelease{{Version: "1.3.0", Date: day, Urgency: "low"}, {Version: "1.2.0", Date: day.AddDate(0, -6, 0)}}}
	switch want {
	case lvfsUpToDate:
		c.Releases = c.Releases[1:]
	case lvfsUrgent:
		c.Releases[0].Urgency, c.Releases[0].CVEs = "high", []string{"CVE-2026-0001"}
	case lvfsOtherVendor:
		c.Developer = "Lenovo"
	case lvfsDiffers:
		c.VersionFormat, c.Releases[1].Version = "plain", "1.1.0" // the installed 1.2.0 isn't listed
	}
	return &LVFS{Source: "hwspec", SignedAt: day, Components: map[string][]LVFSComponent{"47335265-a509-51f7-841e-1c94911af66b": {c}}}
}

// lvfsTarget is a part with firmware LVFS can name: its GUIDs and version.
type lvfsTarget struct {
	ref       *DeviceRef
	evidence  []Evidence
	guids     map[string]string // GUID → its evidence path
	installed func(format string) (string, bool)
	vendor    string // the device's maker, if known
	// esrt: a UEFI capsule's version is a number unless LVFS says
	// otherwise, as fwupd's uefi-capsule plugin has it.
	esrt bool
}

func lvfsTargets(r *report.Report) []lvfsTarget {
	var out []lvfsTarget
	for i, d := range r.Storage {
		fw := d.Firmware
		if !fw.Known() || len(fw.InstanceIDs) == 0 {
			continue
		}
		at := fmt.Sprintf("storage[%d].firmware.", i)
		t := lvfsTarget{ref: &DeviceRef{Kind: "disk", Key: d.Name}, evidence: []Evidence{Present(at+"version", fw.Version)},
			guids: map[string]string{}, installed: func(string) (string, bool) { return fw.Version, true }}
		if d.Identity != nil {
			t.ref.Name = d.Identity.Model
		}
		for j, id := range fw.InstanceIDs {
			t.guids[strings.ToLower(id.GUID)] = fmt.Sprintf("%sinstance_ids[%d].guid", at, j)
			if ven, ok := strings.CutPrefix(id.ID, `NVME\VEN_`); ok && len(ven) >= 4 {
				t.vendor = pciVendorName(r, strings.ToLower(ven[:4]))
			}
		}
		out = append(out, t)
	}
	if e := r.System.ESRT; e != nil {
		for i, en := range e.Entries {
			at := fmt.Sprintf("system.esrt.entries[%d].", i)
			version := en.FWVersion
			t := lvfsTarget{ref: &DeviceRef{Kind: "esrt", Key: en.FWClass, Name: en.FWType + " firmware"}, esrt: true,
				evidence:  []Evidence{Present(at+"fw_version", en.FWVersion)},
				guids:     map[string]string{strings.ToLower(en.FWClass): at + "fw_class"},
				installed: func(format string) (string, bool) { return versionFromUint32(version, format) }}
			if en.FWType == "system" && r.System.Identity != nil {
				t.vendor = r.System.Identity.Vendor
			}
			out = append(out, t)
		}
	}
	return out
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func pciVendorName(r *report.Report, vendorID string) string {
	for _, d := range r.PCI {
		if d.VendorID == vendorID && d.Identity != nil {
			return d.Identity.Vendor
		}
	}
	return ""
}

func lvfsCheck(want lvfsCase) func(*Input, *kb.Rule) ([]hit, error) {
	return func(in *Input, _ *kb.Rule) ([]hit, error) {
		var hits []hit
		for _, t := range lvfsTargets(in.Report) {
			for _, m := range lvfsStreams(in.LVFS, t) {
				got, text := lvfsCompare(in, t, m)
				if got != want {
					continue
				}
				hits = append(hits, hit{device: t.ref, evidence: append(slices.Clone(t.evidence), Present(t.guids[m.guid], m.guid)),
					answers: []Answer{{Topic: "lvfs", Known: got != lvfsDiffers, Text: text}}})
			}
		}
		return hits, nil
	}
}

// lvfsStream is one firmware stream that matched a part: every component
// with its ID under the part's GUIDs, merged.
type lvfsStream struct {
	LVFSComponent
	guid string // the GUID it matched first (sorted), for the evidence
	// formatConflict: its components give different version formats, so
	// versions aren't ordered.
	formatConflict bool
}

// lvfsStreams merges, per firmware ID, the components matching any of a
// part's GUIDs: LVFS splits one stream across many (one release each, as
// often as not), so the newest release may be in any of them. Releases
// keep their own component's requirements; a version listed in two is
// merged into one release (see below); the first name and developer
// given are kept.
func lvfsStreams(lv *LVFS, t lvfsTarget) []lvfsStream {
	var out []lvfsStream
	index := map[string]int{}
	for _, guid := range slices.Sorted(maps.Keys(t.guids)) {
		for _, c := range lv.Components[guid] {
			i, seen := index[c.ID]
			if !seen {
				i = len(out)
				index[c.ID] = i
				out = append(out, lvfsStream{LVFSComponent: LVFSComponent{ID: c.ID, VersionFormat: c.VersionFormat}, guid: guid})
			}
			s := &out[i]
			s.Name, s.Developer = cmpOr(s.Name, c.Name), cmpOr(s.Developer, c.Developer)
			if c.VersionFormat != s.VersionFormat {
				s.formatConflict = true
			}
			for _, r := range c.Releases {
				j := slices.IndexFunc(s.Releases, func(x LVFSRelease) bool { return x.Version == r.Version })
				if j < 0 {
					s.Releases = append(s.Releases, r)
					continue
				}
				// The same version in two components: one release, its CVEs
				// together, the most urgent urgency, and installable if
				// either component's requirements allow it.
				x := &s.Releases[j]
				if x.alternatives == nil {
					x.alternatives = [][]LVFSRequirement{x.Requires}
				}
				x.alternatives = append(x.alternatives, r.Requires)
				x.CVEs = slices.Compact(slices.Sorted(slices.Values(append(slices.Clone(x.CVEs), r.CVEs...))))
				x.Urgency = urgency([]LVFSRelease{*x, r})
			}
		}
	}
	return slices.DeleteFunc(out, func(s lvfsStream) bool { return len(s.Releases) == 0 })
}

// lvfsCompare puts a part's firmware against a stream's releases and says
// so in words. Versions are ordered as their format orders them; without
// such a format only the order LVFS published them in is known, and said
// as "listed after", never "newer".
func lvfsCompare(in *Input, t lvfsTarget, s lvfsStream) (lvfsCase, string) {
	src := "LVFS (signed " + in.LVFS.SignedAt.UTC().Format("2006-01-02") + ")"
	if in.LVFS.Source == "fwupd" {
		src = "LVFS (fwupd's copy, signed " + in.LVFS.SignedAt.UTC().Format("2006-01-02") + ")"
	}
	format := s.VersionFormat
	switch {
	case s.formatConflict:
		format = ""
	case format == "" && t.esrt:
		format = "number"
	}
	rels := slices.Clone(s.Releases)
	slices.SortStableFunc(rels, func(a, b LVFSRelease) int { return b.Date.Compare(a.Date) })
	installed, ok := t.installed(format)
	if !ok {
		why := fmt.Sprintf("its version format %q is one hwspec doesn't know", format)
		if s.formatConflict {
			why = "LVFS gives it different version formats"
		}
		return lvfsDiffers, fmt.Sprintf("%s lists %s firmware (%s); %s, so the versions aren't compared", src, s.Name, s.ID, why)
	}
	// In a format that orders versions, the releases written in it are
	// ordered by version; others are left out of the comparison, and said.
	ordered := false
	var notes []string
	if ordersVersions(format) && fits(installed, format) {
		fitting := slices.DeleteFunc(slices.Clone(rels), func(r LVFSRelease) bool { return !fits(r.Version, format) })
		if len(fitting) > 0 {
			if left := len(rels) - len(fitting); left > 0 {
				notes = append(notes, fmt.Sprintf("LVFS also lists %d release(s) not written as a %s version, which aren't compared", left, format))
			}
			ordered, rels = true, fitting
			slices.SortStableFunc(rels, func(a, b LVFSRelease) int { o, _ := compareVersions(b.Version, a.Version, format); return o })
		}
	}
	also := ""
	if len(notes) > 0 {
		also = ". " + strings.Join(notes, "; ")
	}
	latest := rels[0]
	what := fmt.Sprintf("%s lists %s%s as the latest %s firmware (%s)", src, latest.Version, dated(latest), s.Name, s.ID)
	if !ordered {
		what = fmt.Sprintf("%s lists %s%s as the most recently published %s firmware (%s)", src, latest.Version, dated(latest), s.Name, s.ID)
	}

	var newer []LVFSRelease
	switch i := slices.IndexFunc(rels, func(r LVFSRelease) bool { return r.Version == installed }); {
	case ordered:
		for _, r := range rels {
			if o, _ := compareVersions(r.Version, installed, format); o > 0 {
				newer = append(newer, r)
			}
		}
		if len(newer) == 0 {
			o, _ := compareVersions(installed, latest.Version, format)
			scope := vendorScope(s, rels[:1], t.vendor, in.Report)
			switch {
			case installed == latest.Version:
				return lvfsUpToDate, fmt.Sprintf("%s; this device runs it%s%s", what, also, scope)
			case o == 0:
				return lvfsUpToDate, fmt.Sprintf("%s; this device runs %s, the same version%s%s", what, installed, also, scope)
			}
			return lvfsUpToDate, fmt.Sprintf("%s; this device runs %s, newer than that%s%s", what, installed, also, scope)
		}
	case i < 0:
		why := "versions in this format aren't ordered"
		switch {
		case s.formatConflict:
			why = "LVFS gives this firmware different version formats, so versions aren't ordered"
		case format == "":
			why = "LVFS gives no version format, so versions aren't ordered"
		case ordersVersions(format) && !fits(installed, format):
			why = fmt.Sprintf("it isn't written as a %s version", format)
		case ordersVersions(format):
			why = fmt.Sprintf("no release is written as a %s version", format)
		}
		return lvfsDiffers, fmt.Sprintf("%s; this device runs %s, which isn't one of its releases, and %s", what, installed, why)
	default:
		// Only the order LVFS published them in: a release counts as later
		// only if it was published on a later day.
		inst, tied := rels[i], false
		for _, r := range rels {
			switch {
			case r.Version == installed:
			case !inst.Date.IsZero() && utcDay(r.Date).After(utcDay(inst.Date)):
				newer = append(newer, r)
			case r.Date.IsZero() || inst.Date.IsZero() || utcDay(r.Date).Equal(utcDay(inst.Date)):
				tied = true
			}
		}
		switch {
		case len(newer) > 0:
		case tied:
			return lvfsDiffers, fmt.Sprintf("%s; this device runs %s, which LVFS lists with releases of the same date (or none), and versions in this format aren't ordered, so which is newer isn't known", what, installed)
		default:
			return lvfsUpToDate, fmt.Sprintf("%s; this device runs it%s", what, vendorScope(s, rels[:1], t.vendor, in.Report))
		}
	}

	// What fwupd would install over this version: a requirement on the
	// device's own firmware can rule a release out.
	machine := vendorsOf(in.Report)
	var installable []LVFSRelease
	for _, r := range newer {
		ok, n := releaseNotes(r, installed, format, machine)
		notes = appendNew(notes, n...)
		if ok {
			installable = append(installable, r)
		}
	}
	text := fmt.Sprintf("%s; this device runs %s", what, installed)
	if !ordered {
		text += fmt.Sprintf(", and LVFS published %d release(s) after it", len(newer))
	}
	if len(installable) == 0 {
		return lvfsUpToDate, text + ". " + strings.Join(notes, "; ")
	}
	if u := urgency(installable); u != "" {
		text += "; urgency " + u
	}
	var cves []string
	for _, r := range installable {
		cves = append(cves, r.CVEs...)
	}
	slices.Sort(cves)
	if cves = slices.Compact(cves); len(cves) > 0 {
		text += "; fixes " + strings.Join(cves, ", ")
	}
	if len(notes) > 0 {
		text += ". " + strings.Join(notes, "; ")
	}
	switch scope := vendorScope(s, installable, t.vendor, in.Report); {
	case scope != "":
		return lvfsOtherVendor, text + scope + ", not an update for this machine"
	// Urgent only for a release LVFS doesn't limit to another machine's
	// vendor ID: fwupd decides those (the notes say so).
	case slices.ContainsFunc(installable, func(r LVFSRelease) bool {
		return (r.Urgency == "critical" || r.Urgency == "high") && !machine.missedBy(r)
	}):
		return lvfsUrgent, text
	}
	return lvfsUpdate, text
}

func appendNew(list []string, items ...string) []string {
	for _, s := range items {
		if !slices.Contains(list, s) {
			list = append(list, s)
		}
	}
	return list
}

func dated(r LVFSRelease) string {
	if r.Date.IsZero() {
		return ""
	}
	return " (" + r.Date.Format("2006-01-02") + ")"
}

// urgency is the most urgent of the releases' urgencies.
func urgency(rs []LVFSRelease) string {
	for _, u := range []string{"critical", "high", "medium", "low"} {
		if slices.ContainsFunc(rs, func(r LVFSRelease) bool { return r.Urgency == u }) {
			return u
		}
	}
	return ""
}

// utcDay is the UTC day a time falls on.
func utcDay(t time.Time) time.Time { return t.UTC().Truncate(24 * time.Hour) }

// machineVendors are the names a DMI vendor ID could give this machine:
// its BIOS vendor (fwupd's uefi-capsule plugin gives a capsule the vendor
// ID "DMI:<bios_vendor>" and matches requirements against it as a regex)
// and its maker. LVFS doesn't spell them as DMI does ("DMI:Lenovo" for
// LENOVO), so case is ignored.
// machineVendors is what LVFS requirements test of the machine: its BIOS
// and system vendors, its hardware IDs (CHIDs), and the fields a CHID
// needs that the capture lacks.
type machineVendors struct {
	bios, system string
	chids        map[string]bool
	chidMissing  []string
}

func vendorsOf(r *report.Report) machineVendors {
	var m machineVendors
	if r.System.Firmware != nil {
		m.bios = r.System.Firmware.Vendor
	}
	if r.System.Identity != nil {
		m.system = r.System.Identity.Vendor
	}
	m.chids, m.chidMissing = machineCHIDs(r)
	return m
}

// hardware tests a <hardware> or <not_hardware> requirement's CHIDs ("|"
// between alternatives, as fwupd reads them): whether one is this
// machine's, and whether a "no" is certain (every CHID was computed).
func (m machineVendors) hardware(text string) (match, known bool) {
	for g := range strings.SplitSeq(text, "|") {
		if m.chids[strings.ToLower(strings.TrimSpace(g))] {
			return true, true
		}
	}
	return false, len(m.chids) > 0 && len(m.chidMissing) == 0
}

// chidGap says why a hardware requirement can't be decided here.
func (m machineVendors) chidGap() string {
	if len(m.chidMissing) == 0 {
		return "hwspec has no hardware IDs for this machine"
	}
	return "the capture lacks " + strings.Join(m.chidMissing, ", ") + ", which fwupd uses for them"
}

// dmi tests a requirement: whether it is a usable DMI vendor-ID pattern,
// and whether it names this machine.
func (m machineVendors) dmi(q LVFSRequirement) (isDMI, names bool) {
	if q.Kind != "firmware" || q.Text != "vendor-id" || !strings.Contains(q.Version, "DMI:") {
		return false, false
	}
	re, err := regexp.Compile("(?i)" + q.Version)
	if err != nil {
		return false, false
	}
	for _, v := range []string{m.bios, m.system} {
		if v != "" && re.MatchString("DMI:"+v) {
			return true, true
		}
	}
	return true, false
}

// namedBy: a release, in any of its components, requires a DMI vendor ID
// naming this machine. missedBy: it requires DMI vendor IDs and none does.
func (m machineVendors) namedBy(r LVFSRelease) bool {
	named, _ := m.dmiOf(r)
	return named
}

func (m machineVendors) missedBy(r LVFSRelease) bool {
	named, hasDMI := m.dmiOf(r)
	return hasDMI && !named
}

func (m machineVendors) dmiOf(r LVFSRelease) (named, hasDMI bool) {
	for _, reqs := range requirementSets(r) {
		for _, q := range reqs {
			isDMI, names := m.dmi(q)
			hasDMI, named = hasDMI || isDMI, named || names
		}
	}
	return named, hasDMI
}

// requirementSets are a release's requirements, one set per component
// listing it.
func requirementSets(r LVFSRelease) [][]LVFSRequirement {
	if len(r.alternatives) > 0 {
		return r.alternatives
	}
	return [][]LVFSRequirement{r.Requires}
}

// vendorScope says when releases are another vendor's, for its own
// systems. A DMI vendor-ID requirement naming this machine (machineVendors)
// makes a release this machine's; one naming another decides nothing here
// (fwupd decides, and the notes say so). Otherwise the publisher
// (developer_name) is compared with the machine's maker and the device's.
func vendorScope(s lvfsStream, rels []LVFSRelease, deviceVendor string, r *report.Report) string {
	machine := vendorsOf(r)
	system := machine.system
	if slices.ContainsFunc(rels, machine.namedBy) {
		return ""
	}
	dev := vendorKey(s.Developer)
	if dev == "" || dev == vendorKey(system) || dev == vendorKey(deviceVendor) {
		return ""
	}
	who := fmt.Sprintf("not this machine's maker (%s)", cmpOr(system, "unknown"))
	if deviceVendor != "" {
		who = fmt.Sprintf("neither this machine's maker (%s) nor the device's (%s)", cmpOr(system, "unknown"), deviceVendor)
	}
	return fmt.Sprintf(". Published by %s, %s: that vendor's release for its own systems", s.Developer, who)
}

var vendorWord = regexp.MustCompile(`[a-z0-9]+`)

// vendorAliases are first words a vendor's DMI name and its LVFS name
// don't share: Hewlett-Packard and HP, Micro-Star International and MSI,
// ASUSTeK and Asus, Advanced Micro Devices and AMD.
var vendorAliases = map[string]string{"hewlett": "hp", "micro": "msi", "asustek": "asus", "advanced": "amd"}

// vendorKey is a vendor's first word, case ignored, aliases resolved.
func vendorKey(name string) string {
	w := vendorWord.FindString(strings.ToLower(name))
	if a, ok := vendorAliases[w]; ok {
		return a
	}
	return w
}

// releaseNotes says whether fwupd would install a release over the
// device's version, and what else LVFS requires: the device's own
// firmware version is tested (fwupd refuses a release whose test fails;
// for ge or gt that means another update comes first); the rest is
// fwupd's to check, and named.
func releaseNotes(r LVFSRelease, installed, format string, machine machineVendors) (bool, []string) {
	if len(r.alternatives) == 0 {
		return requirementsNotes(r.Version, r.Requires, installed, format, machine)
	}
	var all, allowed []string
	installable := false
	for _, reqs := range r.alternatives {
		ok, notes := requirementsNotes(r.Version, reqs, installed, format, machine)
		all = appendNew(all, notes...)
		if ok {
			installable, allowed = true, appendNew(allowed, notes...)
		}
	}
	if installable {
		return true, allowed
	}
	return false, all
}

// requirementsNotes is releaseNotes for one component's requirements.
func requirementsNotes(version string, reqs []LVFSRequirement, installed, format string, machine machineVendors) (bool, []string) {
	installable := true
	var notes []string
	add := func(s string) { notes = appendNew(notes, s) }
	for _, q := range reqs {
		test := strings.TrimSpace(q.Compare + " " + q.Version)
		switch {
		case q.Kind == "firmware" && q.Text == "":
			switch ok, known := requirementMet(q, installed, format); {
			case !known:
				add("fwupd also checks the firmware version (" + test + ")")
			case ok:
			case q.Compare == "ge" || q.Compare == "gt":
				add(fmt.Sprintf("LVFS installs %s only over firmware %s, so another update comes first", version, test))
			default:
				installable = false
				add(fmt.Sprintf("fwupd won't install %s over this version (it requires firmware %s)", version, test))
			}
		case q.Kind == "firmware" && q.Text == "vendor-id":
			switch isDMI, names := machine.dmi(q); {
			case names:
			case isDMI:
				add(fmt.Sprintf("LVFS limits it to vendor ID %s, which doesn't name this machine (BIOS vendor %s, maker %s): fwupd decides whether it applies",
					q.Version, cmpOr(machine.bios, "unknown"), cmpOr(machine.system, "unknown")))
			default:
				add("fwupd also checks the vendor ID (" + q.Version + ")")
			}
		case q.Kind == "firmware":
			add("fwupd also checks other firmware (" + strings.TrimSpace(q.Text+" "+test) + ")")
		case q.Kind == "id" && q.Text == "org.freedesktop.fwupd":
			add("needs fwupd " + test)
		case q.Kind == "id":
			add("needs " + strings.TrimSpace(q.Text+" "+test))
		case q.Kind == "hardware" || q.Kind == "not_hardware":
			// fwupd matches these against the machine's CHIDs (#237).
			switch match, known := machine.hardware(q.Text); {
			case q.Kind == "hardware" && match, q.Kind == "not_hardware" && known && !match:
			case q.Kind == "hardware" && known:
				installable = false
				add(fmt.Sprintf("LVFS limits %s to other models by hardware ID (CHID): fwupd won't offer it on this one", version))
			case q.Kind == "not_hardware" && match:
				installable = false
				add(fmt.Sprintf("LVFS keeps %s off this model by hardware ID (CHID): fwupd won't offer it", version))
			case q.Kind == "hardware":
				add("LVFS limits it to certain models by hardware ID (CHID), and " + machine.chidGap() + ": fwupd decides whether this is one")
			default:
				add("LVFS keeps it off certain models by hardware ID (CHID), and " + machine.chidGap() + ": fwupd decides whether this is one")
			}
		case q.Kind == "client":
			add("needs fwupd to support " + q.Text)
		}
	}
	return installable, notes
}

// requirementMet tests the device's firmware version against a
// requirement on it, as fwupd's predicates do: ok, and whether the test
// could be made.
func requirementMet(q LVFSRequirement, installed, format string) (ok, known bool) {
	switch q.Compare {
	case "regex":
		re, err := regexp.Compile(q.Version)
		if err != nil {
			return false, false
		}
		return re.MatchString(installed), true
	case "glob":
		m, err := path.Match(q.Version, installed)
		return m, err == nil
	}
	o, ok := compareVersions(installed, q.Version, format)
	if !ok {
		return false, false
	}
	switch q.Compare {
	case "eq":
		return o == 0, true
	case "ne":
		return o != 0, true
	case "lt":
		return o < 0, true
	case "le":
		return o <= 0, true
	case "gt":
		return o > 0, true
	case "ge":
		return o >= 0, true
	}
	return false, false
}
