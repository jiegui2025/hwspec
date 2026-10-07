package advisor

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jiegui2025/hwspec/internal/kb"
	"github.com/jiegui2025/hwspec/internal/report"
)

func lvfsKB() *kb.KB {
	var rules []kb.Rule
	for _, name := range []string{"lvfs-up-to-date", "lvfs-update-urgent", "lvfs-update", "lvfs-other-vendor", "lvfs-differs"} {
		r, _ := checks[name].example()
		rules = append(rules, r)
	}
	return knowledge(rules...)
}

const modelGUID = "9657ce89-450f-58d2-ade0-2c6ae667541a"

var day = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

// referenceDrive is the HP reference machine's NVMe drive as captured.
func referenceDrive() *report.Report {
	return &report.Report{
		System: report.System{Identity: &report.Identity{Vendor: "HP"}},
		PCI:    []report.PCIDevice{{Address: "0000:01:00.0", VendorID: "144d", DeviceID: "a808", Identity: &report.Identity{Vendor: "Samsung Electronics Co Ltd"}}},
		Storage: []report.Disk{{Name: "nvme0n1", Transport: "nvme", Identity: &report.Identity{Model: "SAMSUNG MZVLB256HAHQ-000L7"},
			Firmware: &report.Firmware{Version: "1L2QEXD7", Source: "nvme", InstanceIDs: []report.InstanceID{
				{ID: `NVME\VEN_144D&DEV_A808`, GUID: "47335265-a509-51f7-841e-1c94911af66b"},
				{ID: `NVME\VEN_144D&DEV_A808&SUBSYS_144DA801`, GUID: "c9d531ea-ee7d-5562-8def-c64d0d144813"},
				{ID: "SAMSUNG MZVLB256HAHQ-000L7", GUID: modelGUID}}}}},
	}
}

func withVersion(r *report.Report, v string) *report.Report {
	r.Storage[0].Firmware.Version = v
	return r
}

var vendorID = []LVFSRequirement{{Kind: "firmware", Compare: "eq", Version: "NVME:0x144D", Text: "vendor-id"}}

// pm981 is LVFS's component for the drive (2026-10-06): Lenovo's, one
// release, no version format.
func pm981(releases ...LVFSRelease) LVFSComponent {
	if releases == nil {
		releases = []LVFSRelease{{Version: "1L2QEXD7", Date: time.Date(2016, 7, 8, 3, 0, 0, 0, time.UTC), Urgency: "high", Requires: vendorID}}
	}
	return LVFSComponent{ID: "com.lenovo.PM981.256GB.firmware", Name: "PM981", Developer: "Lenovo", Releases: releases}
}

// stream is a Samsung component in a format, its releases given as
// version, then each one a month older.
func stream(format string, versions ...string) LVFSComponent {
	c := LVFSComponent{ID: "com.samsung.example.firmware", Name: "Example SSD", Developer: "Samsung", VersionFormat: format}
	for i, v := range versions {
		c.Releases = append(c.Releases, LVFSRelease{Version: v, Date: day.AddDate(0, -i, 0)})
	}
	return c
}

func lvfsWith(guid string, comps ...LVFSComponent) *LVFS {
	return &LVFS{Source: "hwspec", SignedAt: time.Date(2026, 10, 6, 11, 55, 37, 0, time.UTC), Components: map[string][]LVFSComponent{guid: comps}}
}

func lvfsFindings(t *testing.T, r *report.Report, lv *LVFS) []Finding {
	t.Helper()
	a := Advise(Input{Report: r, KB: lvfsKB(), Now: noon, LVFS: lv, LinuxFirmware: &LinuxFirmware{}})
	if len(a.Warnings) != 0 {
		t.Errorf("warnings %q", a.Warnings)
	}
	return a.Findings
}

// one is the only finding's ID and answer.
func one(t *testing.T, fs []Finding) (string, string) {
	t.Helper()
	if len(fs) != 1 {
		t.Fatalf("%d findings: %+v", len(fs), fs)
	}
	return fs[0].ID, fs[0].Answers[0].Text
}

// #226's acceptance on the reference machine: no update for the drive
// (1L2QEXD7), and the release is Lenovo's, for Lenovo's systems.
func TestLVFSReferenceDriveIsUpToDate(t *testing.T) {
	fs := lvfsFindings(t, referenceDrive(), lvfsWith(modelGUID, pm981()))
	id, text := one(t, fs)
	if id != "firmware.lvfs-up-to-date" || fs[0].Device.Key != "nvme0n1" || !fs[0].Answers[0].Known {
		t.Errorf("finding %+v", fs[0])
	}
	for _, want := range []string{"LVFS (signed 2026-10-06) lists 1L2QEXD7 (2016-07-08) as the most recently published PM981 firmware (com.lenovo.PM981.256GB.firmware); this device runs it",
		"Published by Lenovo, neither this machine's maker (HP) nor the device's (Samsung Electronics Co Ltd): that vendor's release for its own systems"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %q", want, text)
		}
	}
	if fs[0].Evidence[1].Path() != "storage[0].firmware.instance_ids[2].guid" {
		t.Errorf("evidence %+v", fs[0].Evidence)
	}
}

// A newer Lenovo release on an HP machine is information, not an update;
// the same from Samsung (the drive's maker) or HP is an update, urgent
// with CVEs when LVFS says so. Without a format the releases' order is
// what LVFS published, said as "after", not "newer".
func TestLVFSUpdatesAndScope(t *testing.T) {
	newer := []LVFSRelease{
		{Version: "1L2QEXD9", Date: day, Urgency: "medium", CVEs: []string{"CVE-2026-0002"}, Requires: vendorID},
		{Version: "1L2QEXD8", Date: day.AddDate(0, -1, 0), Urgency: "critical", CVEs: []string{"CVE-2026-0001", "CVE-2026-0002"}},
		{Version: "1L2QEXD7", Date: day.AddDate(-1, 0, 0), Urgency: "high"},
	}
	id, text := one(t, lvfsFindings(t, referenceDrive(), lvfsWith(modelGUID, pm981(newer...))))
	if id != "firmware.lvfs-other-vendor" || !strings.Contains(text, "Published by Lenovo") || !strings.HasSuffix(text, "not an update for this machine") {
		t.Errorf("Lenovo's newer release: %s %q", id, text)
	}
	for _, dev := range []string{"Samsung", "HP", "Hewlett-Packard", ""} {
		c := pm981(newer...)
		c.Developer = dev
		fs := lvfsFindings(t, referenceDrive(), lvfsWith(modelGUID, c))
		id, text := one(t, fs)
		if id != "firmware.lvfs-update-urgent" || fs[0].Severity != "warning" ||
			text != "LVFS (signed 2026-10-06) lists 1L2QEXD9 (2026-09-01) as the most recently published PM981 firmware (com.lenovo.PM981.256GB.firmware); "+
				"this device runs 1L2QEXD7, and LVFS published 2 release(s) after it; urgency critical; fixes CVE-2026-0001, CVE-2026-0002. fwupd also checks the vendor ID (NVME:0x144D)" {
			t.Errorf("%q: %s %q", dev, id, text)
		}
	}
	low := pm981(LVFSRelease{Version: "1L2QEXD8", Date: day, Urgency: "low"}, LVFSRelease{Version: "1L2QEXD7", Date: day.AddDate(-1, 0, 0)})
	low.Developer = "Samsung"
	if id, text := one(t, lvfsFindings(t, referenceDrive(), lvfsWith(modelGUID, low))); id != "firmware.lvfs-update" || !strings.Contains(text, "urgency low") {
		t.Errorf("a low-urgency update: %s %q", id, text)
	}
	none := pm981(LVFSRelease{Version: "1L2QEXD8", Date: day}, LVFSRelease{Version: "1L2QEXD7", Date: day.AddDate(-1, 0, 0)})
	none.Developer = "Samsung"
	if id, text := one(t, lvfsFindings(t, referenceDrive(), lvfsWith(modelGUID, none))); id != "firmware.lvfs-update" || strings.Contains(text, "urgency") {
		t.Errorf("no urgency stated: %s %q", id, text)
	}
	high := pm981(LVFSRelease{Version: "1L2QEXD8", Date: day, Urgency: "high"}, LVFSRelease{Version: "1L2QEXD7", Date: day.AddDate(-1, 0, 0)})
	high.Developer = "Samsung"
	if id, text := one(t, lvfsFindings(t, referenceDrive(), lvfsWith(modelGUID, high))); id != "firmware.lvfs-update-urgent" || !strings.Contains(text, "urgency high") {
		t.Errorf("high urgency: %s %q", id, text)
	}
}

// B1 of #234's review: LVFS splits one firmware ID across components
// (com.lenovo.ThinkPadN2HETXXP.firmware has 37), oldest first; their
// releases are compared together.
func TestLVFSMergesComponentsOfOneID(t *testing.T) {
	var comps []LVFSComponent
	for i, v := range []string{"0.1.20", "0.1.30", "0.1.40"} {
		c := stream("triplet", v)
		c.Releases[0].Date = day.AddDate(0, i, 0)
		c.Releases[0].Urgency = "medium"
		comps = append(comps, c)
	}
	if id, text := one(t, lvfsFindings(t, withVersion(referenceDrive(), "0.1.20"), lvfsWith(modelGUID, comps...))); id != "firmware.lvfs-update" ||
		!strings.Contains(text, "lists 0.1.40 (2026-11-01) as the latest Example SSD firmware") {
		t.Errorf("0.1.20: %s %q", id, text)
	}
	if id, text := one(t, lvfsFindings(t, withVersion(referenceDrive(), "0.1.40"), lvfsWith(modelGUID, comps...))); id != "firmware.lvfs-up-to-date" || !strings.Contains(text, "this device runs it") {
		t.Errorf("0.1.40: %s %q", id, text)
	}
	// The same version in two components counts once; components that
	// disagree on the format aren't ordered.
	dup := append(slices.Clone(comps), stream("triplet", "0.1.40"))
	if id, _ := one(t, lvfsFindings(t, withVersion(referenceDrive(), "0.1.30"), lvfsWith(modelGUID, dup...))); id != "firmware.lvfs-update" {
		t.Errorf("a duplicate release: %s", id)
	}
	// Without a format, the merged releases are ordered by date across
	// components (LVFS lists them oldest first), and one listed twice
	// counts once.
	a, b, c := stream("", "1L2QEXD7"), stream("", "1L2QEXD9"), stream("", "1L2QEXD9")
	a.Releases[0].Date, b.Releases[0].Date, c.Releases[0].Date = day.AddDate(-1, 0, 0), day, day
	if id, text := one(t, lvfsFindings(t, referenceDrive(), lvfsWith(modelGUID, a, b, c))); id != "firmware.lvfs-update" ||
		!strings.Contains(text, "lists 1L2QEXD9 (2026-09-01) as the most recently published Example SSD firmware (com.samsung.example.firmware); this device runs 1L2QEXD7, and LVFS published 1 release(s) after it") {
		t.Errorf("plain, merged: %s %q", id, text)
	}
	other := append(slices.Clone(comps), stream("quad", "0.1.50.0"))
	if id, text := one(t, lvfsFindings(t, withVersion(referenceDrive(), "0.1.35"), lvfsWith(modelGUID, other...))); id != "firmware.lvfs-differs" ||
		!strings.Contains(text, "LVFS gives this firmware different version formats") {
		t.Errorf("formats in conflict: %s %q", id, text)
	}
}

// B2: releases are ordered by version where the format orders them, not
// by date: Intel Arc's 23.1064 was published after 23.1065.
func TestLVFSOrdersByVersionNotDate(t *testing.T) {
	c := stream("quad", "23.1064.0.0", "23.1065.0.0")
	c.Releases[0].Urgency, c.Releases[0].CVEs = "high", []string{"CVE-2026-9"}
	if id, text := one(t, lvfsFindings(t, withVersion(referenceDrive(), "23.1065.0.0"), lvfsWith(modelGUID, c))); id != "firmware.lvfs-up-to-date" ||
		!strings.Contains(text, "lists 23.1065.0.0 (2026-08-01) as the latest") {
		t.Errorf("a downgrade: %s %q", id, text)
	}
	if id, text := one(t, lvfsFindings(t, withVersion(referenceDrive(), "23.1063.0.0"), lvfsWith(modelGUID, c))); id != "firmware.lvfs-update-urgent" || !strings.Contains(text, "fixes CVE-2026-9") {
		t.Errorf("older than both: %s %q", id, text)
	}
}

// Versions are ordered only through their format, and only when every
// version fits it; otherwise an unlisted version "differs".
func TestLVFSVersionsOnlyThroughTheirFormat(t *testing.T) {
	for _, c := range []struct {
		name, installed string
		comp            LVFSComponent
		want, text      string
	}{
		{"plain, unlisted", "1L2QEXD9", stream("", "1L2QEXD8", "1L2QEXD7"), "firmware.lvfs-differs", "which isn't one of its releases, and LVFS gives no version format, so versions aren't ordered"},
		{"an unknown format", "2.0", stream("semver", "1.0"), "firmware.lvfs-differs", "which isn't one of its releases, and versions in this format aren't ordered"},
		{"compal-bios isn't ordered", "0a.10", stream("compal-bios", "0a.11"), "firmware.lvfs-differs", "versions in this format aren't ordered"},
		{"not written in its format", "1.2.x", stream("triplet", "1.3.0"), "firmware.lvfs-differs", "it isn't written as a triplet version"},
		{"too few sections", "1.2", stream("triplet", "1.3.0"), "firmware.lvfs-differs", "it isn't written as a triplet version"},
		{"no release in its format", "1.2.0", stream("triplet", "1.3", "1.4"), "firmware.lvfs-differs", "no release is written as a triplet version"},
		{"a quad version as a triplet", "1.2.3", stream("quad", "1.2.3.0"), "firmware.lvfs-differs", "it isn't written as a quad version"},
		{"a release in another form", "1.2.0", stream("triplet", "1.3.0", "1.2.0", "old"), "firmware.lvfs-update", "this device runs 1.2.0. LVFS also lists 1 release(s) not written as a triplet version, which aren't compared"},
		{"newer, with one in another form", "1.4.0", stream("triplet", "old", "1.3.0"), "firmware.lvfs-up-to-date", "this device runs 1.4.0, newer than that. LVFS also lists 1 release(s)"},
		{"the latest, with one in another form", "1.3.0", stream("triplet", "old", "1.3.0"), "firmware.lvfs-up-to-date", "this device runs it. LVFS also lists 1 release(s)"},
		{"newer than LVFS's", "1.4.0", stream("triplet", "1.3.0", "1.2.0"), "firmware.lvfs-up-to-date", "this device runs 1.4.0, newer than that"},
		{"the latest, written otherwise", "1.03.0", stream("triplet", "1.3.0"), "firmware.lvfs-up-to-date", "this device runs 1.03.0, the same version"},
		{"older and unlisted", "1.1.5", stream("triplet", "1.3.0", "1.2.0", "1.1.0"), "firmware.lvfs-update", "this device runs 1.1.5"},
		{"older than every release", "0.9", stream("pair", "1.1", "1.0"), "firmware.lvfs-update", "this device runs 0.9"},
		{"listed, not the latest", "1.2.0", stream("triplet", "1.3.0", "1.2.0"), "firmware.lvfs-update", "this device runs 1.2.0"},
		{"an undated latest release", "1.3.0", undated(stream("triplet", "1.3.0")), "firmware.lvfs-up-to-date", "lists 1.3.0 as the latest Example SSD firmware"},
	} {
		fs := lvfsFindings(t, withVersion(referenceDrive(), c.installed), lvfsWith(modelGUID, c.comp))
		id, text := one(t, fs)
		if id != c.want || !strings.Contains(text, c.text) || fs[0].Answers[0].Known != (id != "firmware.lvfs-differs") {
			t.Errorf("%s: %s %q", c.name, id, text)
		}
	}
}

func undated(c LVFSComponent) LVFSComponent {
	c.Releases[0].Date = time.Time{}
	return c
}

// The ESRT's capsule version is written in the component's format (a
// number without one) and compared in it; B3: a number against dotted
// releases isn't ordered (Acer's Happy_WC capsule).
func TestLVFSComparesTheESRT(t *testing.T) {
	const class = "b1413ca8-c3de-4754-9e3c-2a719d79cdbc"
	esrt := func(v uint32) *report.Report {
		return &report.Report{System: report.System{Identity: &report.Identity{Vendor: "Dell Inc."},
			ESRT: &report.ESRT{Entries: []report.ESRTEntry{{FWClass: class, FWType: "system", FWVersion: v}}}}}
	}
	c := LVFSComponent{ID: "com.dell.example.firmware", Name: "Example BIOS", Developer: "Dell", VersionFormat: "dell-bios",
		Releases: []LVFSRelease{{Version: "1.3.0", Date: day, Urgency: "high"}, {Version: "1.2.3", Date: day.AddDate(0, -2, 0)}}}
	fs := lvfsFindings(t, esrt(0x00010203), lvfsWith(class, c))
	if id, text := one(t, fs); id != "firmware.lvfs-update-urgent" || *fs[0].Device != (DeviceRef{Kind: "esrt", Key: class, Name: "system firmware"}) ||
		!strings.Contains(text, "this device runs 1.2.3") || fs[0].Evidence[0].Path() != "system.esrt.entries[0].fw_version" {
		t.Errorf("dell-bios: %s %q", id, text)
	}
	c.VersionFormat, c.Releases = "", []LVFSRelease{{Version: "66051", Date: day}}
	if id, _ := one(t, lvfsFindings(t, esrt(0x00010203), lvfsWith(class, c))); id != "firmware.lvfs-up-to-date" {
		t.Errorf("no format, a number: %s", id)
	}
	acer := LVFSComponent{ID: "com.Acer.Happy_WC.firmware", Name: "Happy", Developer: "Acer",
		Releases: []LVFSRelease{{Version: "88.4.8459", Date: day, Urgency: "critical"}, {Version: "88.4.8455", Date: day.AddDate(0, -1, 0)}}}
	if id, text := one(t, lvfsFindings(t, esrt(1476665607), lvfsWith(class, acer))); id != "firmware.lvfs-differs" || !strings.Contains(text, "this device runs 1476665607, which isn't one of its releases") {
		t.Errorf("Acer: %s %q", id, text)
	}
	c.VersionFormat = "semver"
	if id, text := one(t, lvfsFindings(t, esrt(1), lvfsWith(class, c))); id != "firmware.lvfs-differs" || !strings.Contains(text, `its version format "semver" is one hwspec doesn't know`) {
		t.Errorf("an unknown format: %s %q", id, text)
	}
	conflict := []LVFSComponent{c, c}
	conflict[0].VersionFormat, conflict[1].VersionFormat = "quad", "dell-bios"
	if id, text := one(t, lvfsFindings(t, esrt(1), lvfsWith(class, conflict...))); id != "firmware.lvfs-differs" || !strings.Contains(text, "LVFS gives it different version formats") {
		t.Errorf("formats in conflict: %s %q", id, text)
	}
}

// B4: the maker's own release is an update whatever the first word of its
// DMI name; LVFS's DMI vendor-ID requirement, when there is one, decides.
func TestLVFSVendorScope(t *testing.T) {
	for _, c := range []struct{ system, developer string }{
		{"Micro-Star International Co., Ltd.", "MSI"},
		{"ASUSTeK COMPUTER INC.", "Asus"},
		{"Advanced Micro Devices, Inc.", "AMD"},
		{"Hewlett-Packard", "HP"},
		{"LENOVO", "Lenovo"},
	} {
		r := withVersion(referenceDrive(), "1.0.0")
		r.System.Identity.Vendor = c.system
		comp := stream("triplet", "1.1.0", "1.0.0")
		comp.Developer = c.developer
		if id, _ := one(t, lvfsFindings(t, r, lvfsWith(modelGUID, comp))); id != "firmware.lvfs-update" {
			t.Errorf("%s by %s: %s", c.developer, c.system, id)
		}
	}
	dmi := func(pattern string) LVFSComponent {
		c := stream("triplet", "1.1.0", "1.0.0")
		c.Developer = "Some ODM"
		c.Releases[0].Requires = []LVFSRequirement{{Kind: "firmware", Compare: "regex", Version: pattern, Text: "vendor-id"}}
		return c
	}
	if id, _ := one(t, lvfsFindings(t, withVersion(referenceDrive(), "1.0.0"), lvfsWith(modelGUID, dmi("DMI:HP|DMI:Hewlett-Packard")))); id != "firmware.lvfs-update" {
		t.Errorf("DMI:HP on HP: %s", id)
	}
	// A miss decides nothing: fwupd checks the vendor ID, and the
	// publisher is compared instead.
	if id, text := one(t, lvfsFindings(t, withVersion(referenceDrive(), "1.0.0"), lvfsWith(modelGUID, dmi("DMI:LENOVO")))); id != "firmware.lvfs-other-vendor" ||
		!strings.Contains(text, "LVFS limits it to vendor ID DMI:LENOVO, which doesn't name this machine (BIOS vendor unknown, maker HP): fwupd decides whether it applies. Published by Some ODM") {
		t.Errorf("DMI:LENOVO on HP: %s %q", id, text)
	}
	samsung := dmi("DMI:LENOVO")
	samsung.Developer = "Samsung"
	if id, _ := one(t, lvfsFindings(t, withVersion(referenceDrive(), "1.0.0"), lvfsWith(modelGUID, samsung))); id != "firmware.lvfs-update" {
		t.Errorf("DMI:LENOVO from the drive's maker: %s", id)
	}
	// D1 of #234's review: LVFS's patterns don't spell the vendor as DMI
	// does; fwupd matches them against the BIOS vendor.
	for _, c := range []struct{ pattern, system, bios string }{
		{"DMI:Lenovo", "LENOVO", "LENOVO"},
		{"DMI:framework", "Framework", "INSYDE Corp."},
		{"DMI:INSYDE Corp.", "Framework", "INSYDE Corp."},
		{"DMI:American Megatrends.*", "OnLogic", "American Megatrends International, LLC."},
		{"DMI:MSI", "MSI", "American Megatrends International, LLC."},
		{"DMI:Micro-Star International Co., Ltd.", "Micro-Star International Co., Ltd.", "American Megatrends International, LLC."},
	} {
		r := withVersion(referenceDrive(), "1.0.0")
		r.System.Identity.Vendor, r.System.Firmware = c.system, &report.Firmware{Vendor: c.bios, Version: "1"}
		if id, _ := one(t, lvfsFindings(t, r, lvfsWith(modelGUID, dmi(c.pattern)))); id != "firmware.lvfs-update" {
			t.Errorf("%s on %s (BIOS %s): %s", c.pattern, c.system, c.bios, id)
		}
	}
	// A pattern matching anything still needs a vendor to match: with
	// none known, the publisher decides.
	unknown := withVersion(referenceDrive(), "1.0.0")
	unknown.System.Identity = nil
	if id, _ := one(t, lvfsFindings(t, unknown, lvfsWith(modelGUID, dmi("DMI:.*")))); id != "firmware.lvfs-other-vendor" {
		t.Errorf("DMI:.* on an unknown machine: %s", id)
	}
	if id, _ := one(t, lvfsFindings(t, withVersion(referenceDrive(), "1.0.0"), lvfsWith(modelGUID, dmi("DMI:(")))); id != "firmware.lvfs-other-vendor" {
		t.Errorf("an unusable pattern falls back to the publisher (Some ODM): %s", id)
	}
}

// I3: a requirement on the device's own version: for ge or gt another
// update comes first; for anything else fwupd won't install the release,
// which then isn't an update.
func TestLVFSOwnVersionRequirements(t *testing.T) {
	req := func(compare, version string) LVFSComponent {
		c := stream("triplet", "0.1.45", "0.1.30")
		c.Releases[0].Urgency = "high"
		c.Releases[0].Requires = []LVFSRequirement{{Kind: "firmware", Compare: compare, Version: version}}
		return c
	}
	for _, c := range []struct{ compare, version, want, text string }{
		{"ge", "0.1.42", "firmware.lvfs-update-urgent", "LVFS installs 0.1.45 only over firmware ge 0.1.42, so another update comes first"},
		{"gt", "0.1.40", "firmware.lvfs-update-urgent", "LVFS installs 0.1.45 only over firmware gt 0.1.40"},
		{"ge", "0.1.40", "firmware.lvfs-update-urgent", "this device runs 0.1.40; urgency high"},
		{"eq", "0.1.45", "firmware.lvfs-up-to-date", "fwupd won't install 0.1.45 over this version (it requires firmware eq 0.1.45)"},
		{"ne", "0.1.40", "firmware.lvfs-up-to-date", "fwupd won't install 0.1.45 over this version (it requires firmware ne 0.1.40)"},
		{"le", "0.1.39", "firmware.lvfs-up-to-date", "requires firmware le 0.1.39"},
		{"lt", "0.1.40", "firmware.lvfs-up-to-date", "requires firmware lt 0.1.40"},
		{"regex", `^0\.2\.`, "firmware.lvfs-up-to-date", `requires firmware regex ^0\.2\.`},
		{"regex", `(`, "firmware.lvfs-update-urgent", "fwupd also checks the firmware version (regex ()"},
		{"le", "0.1.40", "firmware.lvfs-update-urgent", "this device runs 0.1.40; urgency high"},
	} {
		id, text := one(t, lvfsFindings(t, withVersion(referenceDrive(), "0.1.40"), lvfsWith(modelGUID, req(c.compare, c.version))))
		if id != c.want || !strings.Contains(text, c.text) {
			t.Errorf("%s %s: %s %q", c.compare, c.version, id, text)
		}
	}
}

// Each stream once per device, whichever of its GUIDs matched, and once
// for each of two identical drives; a stream without releases, or a part
// without IDs or a version, gives nothing; fwupd's copy is named.
func TestLVFSMatching(t *testing.T) {
	c := pm981()
	c.Developer = "Samsung"
	lv := lvfsWith(modelGUID, c)
	lv.Components["47335265-a509-51f7-841e-1c94911af66b"] = []LVFSComponent{c, {ID: "empty", Name: "No releases"}}
	lv.Source = "fwupd"
	if _, text := one(t, lvfsFindings(t, referenceDrive(), lv)); !strings.HasPrefix(text, "LVFS (fwupd's copy, signed 2026-10-06)") {
		t.Errorf("fwupd's copy: %q", text)
	}
	twins := referenceDrive()
	twins.Storage = append(twins.Storage, twins.Storage[0])
	twins.Storage[1].Name = "nvme1n1"
	if fs := lvfsFindings(t, twins, lv); len(fs) != 2 || fs[0].Device.Key == fs[1].Device.Key {
		t.Errorf("two identical drives: %+v", fs)
	}
	r := referenceDrive()
	unknown := report.UnknownFirmware("x")
	unknown.InstanceIDs = r.Storage[0].Firmware.InstanceIDs
	r.Storage = append(r.Storage, report.Disk{Name: "sda", Firmware: &report.Firmware{Version: "1"}}, report.Disk{Name: "sdb", Firmware: unknown})
	r.Storage[0].Firmware.InstanceIDs = r.Storage[0].Firmware.InstanceIDs[2:]
	r.PCI = nil
	if _, text := one(t, lvfsFindings(t, r, lvfsWith(modelGUID, pm981()))); !strings.Contains(text, "Published by Lenovo, not this machine's maker (HP)") {
		t.Errorf("without the drive's PCI device: %q", text)
	}
	r.System.Identity = nil
	if _, text := one(t, lvfsFindings(t, r, lvfsWith(modelGUID, pm981()))); !strings.Contains(text, "not this machine's maker (unknown)") {
		t.Errorf("without the machine's vendor: %q", text)
	}
}

// Without the catalogue the LVFS rules are skipped with one warning; with
// only linux-firmware missing, its own.
func TestLVFSWithoutTheCatalogue(t *testing.T) {
	k := lvfsKB()
	lf, _ := checks["linux-firmware-matches"].example()
	k.Rules = append(k.Rules, lf)
	a := Advise(Input{Report: referenceDrive(), KB: k, Now: noon})
	if !slices.Equal(a.Warnings, []string{"no firmware index: run `hwspec firmware update` to compare firmware with LVFS and linux-firmware"}) || a.RulesSkipped != 6 {
		t.Errorf("neither: %q, skipped %d", a.Warnings, a.RulesSkipped)
	}
	a = Advise(Input{Report: referenceDrive(), KB: k, Now: noon, LinuxFirmware: &LinuxFirmware{}})
	if !slices.Equal(a.Warnings, []string{"no LVFS catalogue: run `hwspec firmware update` to compare firmware with LVFS"}) {
		t.Errorf("no LVFS: %q", a.Warnings)
	}
	a = Advise(Input{Report: referenceDrive(), KB: k, Now: noon, LVFS: lvfsWith(modelGUID)})
	if !slices.Equal(a.Warnings, []string{"no linux-firmware index: run `hwspec firmware update` to compare firmware with linux-firmware's latest release"}) {
		t.Errorf("no linux-firmware: %q", a.Warnings)
	}
}

// What LVFS requires besides the GUID is named, once each.
func TestReleaseNotes(t *testing.T) {
	r := LVFSRelease{Version: "2.0.0", Requires: []LVFSRequirement{
		{Kind: "firmware", Compare: "ge", Version: "1.0.0"},
		{Kind: "firmware", Compare: "glob", Version: "1.*"},
		{Kind: "firmware", Compare: "glob", Version: "["},
		{Kind: "firmware", Compare: "ge", Version: "x"},
		{Kind: "firmware", Compare: "ge", Version: "x"},
		{Kind: "firmware", Compare: "eq", Version: "NVME:0x144D", Text: "vendor-id"},
		{Kind: "firmware", Compare: "ge", Version: "0.1", Text: "bootloader"},
		{Kind: "id", Compare: "ge", Version: "1.9.0", Text: "org.freedesktop.fwupd"},
		{Kind: "id", Compare: "ge", Version: "3", Text: "org.example.other"},
		{Kind: "hardware", Text: "6de5d951-d755-576b-bd09-c5cf66b27234"},
		{Kind: "not_hardware", Text: "x"},
		{Kind: "client", Text: "detach-action"},
	}}
	ok, got := releaseNotes(r, "1.5.0", "triplet", machineVendors{})
	want := []string{
		"fwupd also checks the firmware version (glob [)",
		"fwupd also checks the firmware version (ge x)",
		"fwupd also checks the vendor ID (NVME:0x144D)",
		"fwupd also checks other firmware (bootloader ge 0.1)",
		"needs fwupd ge 1.9.0",
		"needs org.example.other ge 3",
		"LVFS limits it to certain models by hardware ID (CHID), and hwspec has no hardware IDs for this machine: fwupd decides whether this is one",
		"LVFS keeps it off certain models by hardware ID (CHID), and hwspec has no hardware IDs for this machine: fwupd decides whether this is one",
		"needs fwupd to support detach-action",
	}
	if !ok || !slices.Equal(got, want) {
		t.Errorf("installable %v, notes:\n%s\nwant:\n%s", ok, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, c := range []struct {
		compare, version string
		ok, known        bool
	}{
		{"eq", "1.5.0", true, true}, {"eq", "1.5.1", false, true}, {"ne", "1.5.0", false, true}, {"ne", "1.5.1", true, true},
		{"lt", "1.6.0", true, true}, {"lt", "1.5.0", false, true}, {"le", "1.5.0", true, true}, {"le", "1.4.0", false, true},
		{"gt", "1.4.0", true, true}, {"gt", "1.5.0", false, true}, {"ge", "1.5.0", true, true}, {"ge", "1.6.0", false, true},
		{"regex", "^1\\.5", true, true}, {"regex", "^2", false, true}, {"like", "1.5.0", false, false},
		{"glob", "1.*", true, true}, {"glob", "2.*", false, true},
	} {
		ok, known := requirementMet(LVFSRequirement{Compare: c.compare, Version: c.version}, "1.5.0", "triplet")
		if ok != c.ok || known != c.known {
			t.Errorf("%s %s: %v %v", c.compare, c.version, ok, known)
		}
	}
}

func TestVendorKey(t *testing.T) {
	for in, want := range map[string]string{"Lenovo": "lenovo", "LENOVO": "lenovo", "Dell Inc.": "dell", "HP": "hp", "Hewlett-Packard": "hp",
		"Micro-Star International Co., Ltd.": "msi", "ASUSTeK COMPUTER INC.": "asus", "Advanced Micro Devices, Inc.": "amd", "": "", "  ": ""} {
		if got := vendorKey(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

// D2: in date order, a release published the same day as the installed
// one isn't later (PM991a's 6L1QFXM7 and 7L1QFXM7, both 2016-07-08);
// nor is an undated one.
func TestLVFSSameDayIsntLater(t *testing.T) {
	same := time.Date(2016, 7, 8, 0, 0, 0, 0, time.UTC)
	c := stream("", "6L1QFXM7", "7L1QFXM7")
	c.Releases[0].Date, c.Releases[1].Date, c.Releases[0].Urgency = same, same, "high"
	for _, installed := range []string{"7L1QFXM7", "6L1QFXM7"} {
		if id, text := one(t, lvfsFindings(t, withVersion(referenceDrive(), installed), lvfsWith(modelGUID, c))); id != "firmware.lvfs-differs" ||
			!strings.Contains(text, "which LVFS lists with releases of the same date (or none)") {
			t.Errorf("%s: %s %q", installed, id, text)
		}
	}
	undated := stream("", "A2", "A1")
	undated.Releases[1].Date = time.Time{}
	if id, _ := one(t, lvfsFindings(t, withVersion(referenceDrive(), "A1"), lvfsWith(modelGUID, undated))); id != "firmware.lvfs-differs" {
		t.Errorf("installed undated: %s", id)
	}
	if id, _ := one(t, lvfsFindings(t, withVersion(referenceDrive(), "A2"), lvfsWith(modelGUID, undated))); id != "firmware.lvfs-differs" {
		t.Errorf("an undated other: %s", id)
	}
	later := stream("", "B3", "B2", "B1")
	later.Releases[1].Date = later.Releases[2].Date
	if id, text := one(t, lvfsFindings(t, withVersion(referenceDrive(), "B1"), lvfsWith(modelGUID, later))); id != "firmware.lvfs-update" || !strings.Contains(text, "LVFS published 1 release(s) after it") {
		t.Errorf("one later, one the same day: %s %q", id, text)
	}
}

// D3: urgency, CVEs and vendor scope come only from releases fwupd would
// install over this version.
func TestLVFSOnlyInstallableReleasesCount(t *testing.T) {
	c := stream("triplet", "1.2.0", "1.1.0", "1.0.0")
	c.Releases[0].Urgency, c.Releases[0].CVEs = "critical", []string{"CVE-2026-1"}
	c.Releases[0].Requires = []LVFSRequirement{{Kind: "firmware", Compare: "eq", Version: "1.1.0"},
		{Kind: "firmware", Compare: "regex", Version: "DMI:LENOVO", Text: "vendor-id"}}
	c.Releases[1].Urgency = "low"
	c.Developer = ""
	id, text := one(t, lvfsFindings(t, withVersion(referenceDrive(), "1.0.0"), lvfsWith(modelGUID, c)))
	if id != "firmware.lvfs-update" || !strings.Contains(text, "urgency low") || strings.Contains(text, "fixes") ||
		!strings.Contains(text, "fwupd won't install 1.2.0 over this version (it requires firmware eq 1.1.0)") {
		t.Errorf("%s %q", id, text)
	}
	// A refused release naming this machine's DMI vendor doesn't make an
	// installable one from another vendor this machine's.
	scoped := stream("triplet", "1.2.0", "1.1.0", "1.0.0")
	scoped.Developer = "Lenovo"
	scoped.Releases[0].Requires = []LVFSRequirement{{Kind: "firmware", Compare: "eq", Version: "1.1.0"},
		{Kind: "firmware", Compare: "regex", Version: "DMI:HP", Text: "vendor-id"}}
	if id, _ := one(t, lvfsFindings(t, withVersion(referenceDrive(), "1.0.0"), lvfsWith(modelGUID, scoped))); id != "firmware.lvfs-other-vendor" {
		t.Errorf("scope from a refused release: %s", id)
	}
	// Both high and critical newer: the most urgent is named.
	both := stream("triplet", "1.2.0", "1.1.0", "1.0.0")
	both.Releases[0].Urgency, both.Releases[1].Urgency = "high", "critical"
	if _, text := one(t, lvfsFindings(t, withVersion(referenceDrive(), "1.0.0"), lvfsWith(modelGUID, both))); !strings.Contains(text, "urgency critical") {
		t.Errorf("high and critical: %q", text)
	}
}

// Minor 3 of round 2: one version in two components is one release, its
// CVEs together, the more urgent urgency, installable if either
// component allows it; the first component's name and publisher stay.
func TestLVFSMergesDuplicateReleases(t *testing.T) {
	a := stream("triplet", "1.1.0", "1.0.0")
	a.Releases[0].CVEs, a.Releases[0].Urgency = []string{"CVE-1"}, "low"
	a.Releases[0].Requires = []LVFSRequirement{{Kind: "firmware", Compare: "eq", Version: "0.9.0"}}
	b := stream("triplet", "1.1.0")
	b.Name, b.Developer = "Other name", "Lenovo"
	b.Releases[0].CVEs, b.Releases[0].Urgency = []string{"CVE-2"}, "high"
	for _, order := range [][]LVFSComponent{{a, b}, {b, a}} {
		_, text := one(t, lvfsFindings(t, withVersion(referenceDrive(), "1.0.0"), lvfsWith(modelGUID, order...)))
		if !strings.Contains(text, "urgency high; fixes CVE-1, CVE-2") || strings.Contains(text, "eq 0.9.0") {
			t.Errorf("merged %s first: %q", order[0].Name, text)
		}
	}
	if id, text := one(t, lvfsFindings(t, withVersion(referenceDrive(), "1.0.0"), lvfsWith(modelGUID, a, b))); id != "firmware.lvfs-update-urgent" || !strings.Contains(text, "latest Example SSD firmware") {
		t.Errorf("the first component's name and publisher: %s %q", id, text)
	}
	// Neither component allows it: not an update, and both reasons given.
	b.Releases[0].Requires = []LVFSRequirement{{Kind: "firmware", Compare: "eq", Version: "0.8.0"}}
	if id, text := one(t, lvfsFindings(t, withVersion(referenceDrive(), "1.0.0"), lvfsWith(modelGUID, a, b))); id != "firmware.lvfs-up-to-date" ||
		!strings.Contains(text, "eq 0.9.0") || !strings.Contains(text, "eq 0.8.0") {
		t.Errorf("neither allows it: %s %q", id, text)
	}
}

// The ESRT's vendor is the machine's only for system firmware; GUIDs match
// whatever their case.
func TestLVFSESRTVendorAndGUIDCase(t *testing.T) {
	r := &report.Report{System: report.System{Identity: &report.Identity{Vendor: "Dell Inc."},
		ESRT: &report.ESRT{Entries: []report.ESRTEntry{{FWClass: "B1413CA8-C3DE-4754-9E3C-2A719D79CDBC", FWType: "device", FWVersion: 0x00010203}}}}}
	c := LVFSComponent{ID: "com.intel.example.firmware", Name: "Thunderbolt", Developer: "Intel", VersionFormat: "dell-bios",
		Releases: []LVFSRelease{{Version: "1.3.0", Date: day}, {Version: "1.2.3", Date: day.AddDate(0, -1, 0)}}}
	if id, text := one(t, lvfsFindings(t, r, lvfsWith("b1413ca8-c3de-4754-9e3c-2a719d79cdbc", c))); id != "firmware.lvfs-other-vendor" ||
		!strings.Contains(text, "Published by Intel, not this machine's maker (Dell Inc.)") {
		t.Errorf("a device capsule: %s %q", id, text)
	}
	d := referenceDrive()
	d.Storage[0].Firmware.InstanceIDs[2].GUID = strings.ToUpper(modelGUID)
	if fs := lvfsFindings(t, d, lvfsWith(modelGUID, pm981())); len(fs) != 1 {
		t.Errorf("an upper-case GUID: %+v", fs)
	}
}

// Round 3 of #234's review. R1: the vendor IDs of every component listing
// a release count, whatever the components' order. R2: "a later day" is
// a later UTC day, not a later time. R3: a release LVFS limits to another
// machine's vendor ID isn't urgent, and says so.
func TestLVFSRound3(t *testing.T) {
	limited := func(pattern string) LVFSComponent {
		c := stream("triplet", "1.2.9", "1.0.0")
		c.Developer = "Lenovo"
		c.Releases[0].Urgency = "high"
		c.Releases[0].Requires = []LVFSRequirement{{Kind: "firmware", Compare: "regex", Version: pattern, Text: "vendor-id"}}
		return c
	}
	hp := func() *report.Report {
		r := withVersion(referenceDrive(), "1.0.0")
		r.System.Firmware = &report.Firmware{Vendor: "HP", Version: "R21 Ver. 02.27.00"}
		return r
	}
	for _, order := range [][]LVFSComponent{{limited("DMI:LENOVO"), limited("DMI:HP")}, {limited("DMI:HP"), limited("DMI:LENOVO")}} {
		if id, _ := one(t, lvfsFindings(t, hp(), lvfsWith(modelGUID, order...))); id != "firmware.lvfs-update-urgent" {
			t.Errorf("DMI:%s first: %s", order[0].Releases[0].Requires[0].Version, id)
		}
	}
	// A vendor ID naming this machine needs no note.
	if id, text := one(t, lvfsFindings(t, hp(), lvfsWith(modelGUID, limited("DMI:HP")))); id != "firmware.lvfs-update-urgent" || strings.Contains(text, "vendor ID") {
		t.Errorf("DMI:HP on an HP: %s %q", id, text)
	}
	// An older release naming this machine: the newer one from the same
	// publisher is still this machine's update.
	two := stream("triplet", "1.3.0", "1.2.0", "1.0.0")
	two.Developer = "Lenovo"
	two.Releases[1].Requires = []LVFSRequirement{{Kind: "firmware", Compare: "regex", Version: "DMI:HP", Text: "vendor-id"}}
	if id, _ := one(t, lvfsFindings(t, hp(), lvfsWith(modelGUID, two))); id != "firmware.lvfs-update" {
		t.Errorf("only the older release names HP: %s", id)
	}

	same := stream("", "6L1QFXM7", "7L1QFXM7")
	same.Releases[0].Date = time.Date(2016, 7, 8, 9, 0, 0, 0, time.UTC)
	same.Releases[1].Date = time.Date(2016, 7, 8, 3, 0, 0, 0, time.UTC)
	same.Releases[0].Urgency = "high"
	if id, _ := one(t, lvfsFindings(t, withVersion(referenceDrive(), "7L1QFXM7"), lvfsWith(modelGUID, same))); id != "firmware.lvfs-differs" {
		t.Errorf("same day, six hours apart: %s", id)
	}

	noPublisher := limited("DMI:LENOVO")
	noPublisher.Developer = ""
	id, text := one(t, lvfsFindings(t, hp(), lvfsWith(modelGUID, noPublisher)))
	if id != "firmware.lvfs-update" || !strings.Contains(text, "urgency high") ||
		!strings.Contains(text, "LVFS limits it to vendor ID DMI:LENOVO, which doesn't name this machine (BIOS vendor HP, maker HP): fwupd decides whether it applies") {
		t.Errorf("DMI:LENOVO on an HP, no publisher: %s %q", id, text)
	}
}

// #237: <hardware> and <not_hardware> are matched against the machine's
// CHIDs, any of "|"-separated alternatives, as fwupd does. A release for
// other models isn't installable, nor one that excludes this model; one
// that needs a field the capture lacks is left to fwupd.
func TestReleaseHardwareRequirements(t *testing.T) {
	const ours, other = "94d996ce-4c75-5095-ac40-8ddb05acc8e2", "6de5d951-d755-576b-bd09-c5cf66b27234"
	machine := vendorsOf(referenceIdentity())
	partial := referenceIdentity()
	partial.Board.Identity = nil
	gap := vendorsOf(partial)
	for _, c := range []struct {
		name    string
		req     LVFSRequirement
		machine machineVendors
		ok      bool
		note    string // "" for none
	}{
		{"for this model", LVFSRequirement{Kind: "hardware", Text: ours}, machine, true, ""},
		{"for it among others", LVFSRequirement{Kind: "hardware", Text: other + "|" + strings.ToUpper(ours)}, machine, true, ""},
		{"for other models", LVFSRequirement{Kind: "hardware", Text: other}, machine, false, "LVFS limits 2.0.0 to other models by hardware ID (CHID): fwupd won't offer it on this one"},
		{"excludes this model", LVFSRequirement{Kind: "not_hardware", Text: other + "|" + ours}, machine, false, "LVFS keeps 2.0.0 off this model by hardware ID (CHID): fwupd won't offer it"},
		{"excludes others", LVFSRequirement{Kind: "not_hardware", Text: other}, machine, true, ""},
		{"a field missing", LVFSRequirement{Kind: "hardware", Text: other}, gap, true,
			"LVFS limits it to certain models by hardware ID (CHID), and the capture lacks BaseboardManufacturer, which fwupd uses for them: fwupd decides whether this is one"},
		{"a field missing, excluded", LVFSRequirement{Kind: "not_hardware", Text: ours}, gap, false, "LVFS keeps 2.0.0 off this model by hardware ID (CHID): fwupd won't offer it"},
		{"a field missing, not excluded", LVFSRequirement{Kind: "not_hardware", Text: other}, gap, true,
			"LVFS keeps it off certain models by hardware ID (CHID), and the capture lacks BaseboardManufacturer, which fwupd uses for them: fwupd decides whether this is one"},
	} {
		ok, notes := releaseNotes(LVFSRelease{Version: "2.0.0", Requires: []LVFSRequirement{c.req}}, "1.0.0", "triplet", c.machine)
		want := []string{}
		if c.note != "" {
			want = []string{c.note}
		}
		if ok != c.ok || !slices.Equal(append([]string{}, notes...), want) {
			t.Errorf("%s: installable %v, notes %q; want %v, %q", c.name, ok, notes, c.ok, want)
		}
	}
}

// #237's acceptance, as findings: with the reference machine's identity, a
// newer release LVFS limits to other models isn't an update, one limited
// to this model is, and without the identity fwupd decides.
func TestLVFSHardwareLimitedReleases(t *testing.T) {
	const ours, other = "94d996ce-4c75-5095-ac40-8ddb05acc8e2", "6de5d951-d755-576b-bd09-c5cf66b27234"
	machine := func() *report.Report {
		r, id := referenceDrive(), referenceIdentity()
		r.System, r.Board = id.System, id.Board
		return r
	}
	release := func(chid string) LVFSComponent {
		c := pm981(LVFSRelease{Version: "1L2QEXD8", Date: day, Requires: []LVFSRequirement{{Kind: "hardware", Text: chid}}},
			LVFSRelease{Version: "1L2QEXD7", Date: day.AddDate(-1, 0, 0)})
		c.Developer = "Samsung"
		return c
	}
	for _, c := range []struct {
		name string
		r    *report.Report
		chid string
		id   string
		text string
	}{
		{"for this model", machine(), ours, "firmware.lvfs-update", ""},
		{"for other models", machine(), other, "firmware.lvfs-up-to-date", "LVFS limits 1L2QEXD8 to other models by hardware ID (CHID): fwupd won't offer it on this one"},
		{"no identity", referenceDrive(), other, "firmware.lvfs-update", "fwupd decides whether this is one"},
	} {
		id, text := one(t, lvfsFindings(t, c.r, lvfsWith(modelGUID, release(c.chid))))
		if id != c.id || !strings.Contains(text, c.text) || c.text == "" && strings.Contains(text, "hardware ID") {
			t.Errorf("%s: %s %q", c.name, id, text)
		}
	}
}
