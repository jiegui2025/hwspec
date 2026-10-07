package advisor

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jiegui2025/hwspec/internal/kb"
	"github.com/jiegui2025/hwspec/internal/report"
)

func storageKB(group string) *kb.KB {
	rule, _ := checks["storage-upgrade"].example()
	k := exampleKnowledge("storage-upgrade", rule)
	if group != "" {
		k.Models[0].Data["storage_slots"] = json.RawMessage(group)
	}
	return k
}

func storageFinding(t *testing.T, r *report.Report, k *kb.KB) Finding {
	t.Helper()
	a := Advise(Input{Report: r, KB: k, Now: noon})
	if len(a.Findings) != 1 || len(a.Warnings) != 0 {
		t.Fatalf("findings %+v, warnings %q", a.Findings, a.Warnings)
	}
	return a.Findings[0]
}

func storageAnswers(t *testing.T, r *report.Report, k *kb.KB) map[string]Answer {
	t.Helper()
	out := map[string]Answer{}
	for _, an := range storageFinding(t, r, k).Answers {
		if _, seen := out[an.Topic]; seen {
			t.Errorf("%s answered twice", an.Topic)
		}
		out[an.Topic] = an
	}
	return out
}

// wantAnswers checks answers' exact text and whether each is known.
func wantAnswers(t *testing.T, got map[string]Answer, want map[string]string, known map[string]bool) {
	t.Helper()
	for topic, w := range want {
		if got[topic].Text != w {
			t.Errorf("%s:\n got %q\nwant %q", topic, got[topic].Text, w)
		}
	}
	for topic, k := range known {
		if got[topic].Known != k {
			t.Errorf("%s: known %v, want %v", topic, got[topic].Known, k)
		}
	}
}

// lk is a link running at its most.
func lk(speed string, width int) *report.PCIeLink {
	return &report.PCIeLink{Speed: speed, Width: width, MaxSpeed: speed, MaxWidth: width}
}

const (
	gen3     = "8.0 GT/s PCIe"
	gen4     = "16.0 GT/s PCIe"
	gen5     = "32.0 GT/s PCIe"
	samsung  = "01:00.0 (Samsung Electronics Co Ltd NVMe SSD Controller SM981/PM981/PM983)"
	chipset  = "; 00:1b.0 is a chipset port: the chipset's own link to the CPU isn't in the capture, may limit the drive further, and is shared with the chipset's other devices"
	slotsDoc = "2 M.2 storage slots per example-datasheet (2019-12); M.2 2230/2280 per example-datasheet (2019-12); NVMe (PCIe) per example-datasheet (2019-12)"
	noPolicy = "Whether HP's firmware restricts third-party SSDs in this model is unknown: the knowledge base has no source on it"
)

// #108's acceptance on the reference machine as root: the NVMe at PCIe
// 3.0 x4 via 00:1b.0, a chipset port; two M.2 SSD slots of that type,
// which one holds it unknown (the firmware's addresses don't match); a
// PCIe 4.0 SSD would run at 3.0 x4.
func TestStorageReferenceMachine(t *testing.T) {
	r := storageExample()
	r.Board.Slots = []report.Slot{
		{Designation: "Slot2 / M2 WLAN/BT", Type: "PCI Express Gen 3 x1"},
		{Designation: "Slot3 / M2 SSD", Type: "PCI Express Gen 3 x4"},
		{Designation: "Slot4 / M2 SSD", Type: "PCI Express Gen 3 x4"},
	}
	reason := `no slot names this device at its address or its port's; which of "Slot3 / M2 SSD" or "Slot4 / M2 SSD" holds it is unknown`
	r.PCI[1].Mounting = &report.Mounting{Kind: "slot", SlotType: "PCI Express Gen 3 x4", Confidence: "medium", Reason: reason}
	f := storageFinding(t, r, storageKB(""))
	got := map[string]Answer{}
	var topics []string
	for _, an := range f.Answers {
		got[an.Topic] = an
		topics = append(topics, an.Topic)
	}
	if !slices.Equal(topics, []string{"bus", "slots", "faster", "allowlist"}) {
		t.Errorf("topics %q", topics)
	}
	wantAnswers(t, got, map[string]string{
		"bus":       "NVMe " + samsung + " at PCIe 3.0 x4 (8.0 GT/s ×4, ≈3.9 GB/s) via 00:1b.0" + chipset,
		"slots":     slotsDoc + "; 01:00.0 is in a slot of type PCI Express Gen 3 x4: " + reason,
		"faster":    "A PCIe 4.0 or later NVMe SSD in place of 01:00.0 would still run at PCIe 3.0 x4 (8.0 GT/s ×4, ≈3.9 GB/s) here, the most 00:1b.0's port allows; no faster drive gains anything here",
		"allowlist": noPolicy,
	}, map[string]bool{"bus": true, "slots": true, "faster": true, "allowlist": false})
	var paths []string
	for _, e := range f.Evidence {
		paths = append(paths, e.Path())
	}
	for _, p := range []string{"pci[0].pcie_link.max_speed", "pci[0].pcie_link.max_width", "pci[1].pcie_link.max_speed", "pci[1].pcie_link.max_width", "pci[1].mounting.kind"} {
		if !slices.Contains(paths, p) {
			t.Errorf("evidence %q lacks %s", paths, p)
		}
	}
	if len(slices.Compact(slices.Sorted(slices.Values(paths)))) != len(paths) {
		t.Errorf("evidence repeats a value: %q", paths)
	}

	// The text output labels the new topics.
	var buf bytes.Buffer
	if err := WriteText(&buf, Advice{Findings: []Finding{f}}); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"  Bus        NVMe ", "  Slots      2 M.2", "  Faster     A PCIe", "  Allow-list Whether"} {
		if !strings.Contains(buf.String(), label) {
			t.Errorf("text lacks %q:\n%s", label, buf.String())
		}
	}
}

// switched is a drive behind a PCIe switch: drive → downstream port
// 02:00.0 → upstream port 01:00.0 → root port 00:01.0, each link given at
// both ends.
func switched(drive, down, up, root *report.PCIeLink) *report.Report {
	r := storageExample()
	r.PCI = []report.PCIDevice{
		{Address: "0000:00:01.0", ClassCode: "060400", Link: root, Identity: &report.Identity{Model: "Xeon E3-1200 v5/E3-1500 v5/6th Gen Core Processor PCIe Controller (x16)"}},
		{Address: "0000:01:00.0", Parent: "0000:00:01.0", ClassCode: "060400", Link: up},
		{Address: "0000:02:00.0", Parent: "0000:01:00.0", ClassCode: "060400", Link: down},
		{Address: "0000:03:00.0", Parent: "0000:02:00.0", ClassCode: "010802", Link: drive},
	}
	return r
}

const cpuOrChipset = "; whether 00:01.0 is the CPU's port or a chipset's isn't in the capture: a chipset's link to the CPU would limit the drive further and be shared"

// B1: each link is the lower speed and width of its two ends, and the
// path the link that carries least; a switch's own ports aren't a link.
func TestStorageSwitch(t *testing.T) {
	// A Gen4 x4 drive behind a Gen3 x16 uplink: its own link (≈7.9
	// GB/s) limits it, not "Gen3 x4".
	got := storageAnswers(t, switched(lk(gen4, 4), lk(gen4, 4), lk(gen3, 16), lk(gen3, 16)), storageKB(""))
	wantAnswers(t, got, map[string]string{
		"bus":    "NVMe 03:00.0 at PCIe 4.0 x4 (16.0 GT/s ×4, ≈7.9 GB/s) via 02:00.0, 01:00.0, 00:01.0" + cpuOrChipset,
		"faster": "A PCIe 5.0 or later NVMe SSD in place of 03:00.0 would still run at PCIe 4.0 x4 (16.0 GT/s ×4, ≈7.9 GB/s) here, the most 02:00.0's port allows; no faster drive gains anything here",
	}, map[string]bool{"bus": true, "faster": true})

	// A narrow uplink (Gen3 x2) sets the limit, named, and caps what a
	// faster drive gets.
	got = storageAnswers(t, switched(lk(gen4, 4), lk(gen4, 4), lk(gen3, 2), lk(gen3, 8)), storageKB(""))
	wantAnswers(t, got, map[string]string{
		"bus": "NVMe 03:00.0 at PCIe 3.0 x2 (8.0 GT/s ×2, ≈2.0 GB/s) via 02:00.0, 01:00.0, 00:01.0, set by the link between 00:01.0 and 01:00.0; " +
			"the drive can do PCIe 4.0 x4 (16.0 GT/s ×4, ≈7.9 GB/s), but the path allows less" + cpuOrChipset,
		"faster": "A PCIe 5.0 or later NVMe SSD in place of 03:00.0 would still run at PCIe 4.0 x4 (16.0 GT/s ×4, ≈7.9 GB/s) here, the most 02:00.0's port allows; " +
			"the link between 00:01.0 and 01:00.0 allows PCIe 3.0 x2 (8.0 GT/s ×2, ≈2.0 GB/s), so it gets at most that; no faster drive gains anything here",
	}, nil)

	// A Gen4 drive in a Gen3 x16 port (an adapter card): the port's
	// speed and the drive's width; a faster drive is still x4.
	got = storageAnswers(t, switched(lk(gen4, 4), lk(gen3, 16), lk(gen4, 16), lk(gen4, 16)), storageKB(""))
	wantAnswers(t, got, map[string]string{
		"bus": "NVMe 03:00.0 at PCIe 3.0 x4 (8.0 GT/s ×4, ≈3.9 GB/s) via 02:00.0, 01:00.0, 00:01.0; " +
			"the drive can do PCIe 4.0 x4 (16.0 GT/s ×4, ≈7.9 GB/s), but the path allows less" + cpuOrChipset,
		"faster": "A PCIe 4.0 or later NVMe SSD in place of 03:00.0 would still run at PCIe 3.0 x4 (8.0 GT/s ×4, ≈3.9 GB/s) here, the most 02:00.0's port allows an M.2 drive (x4 at most; the port is x16); no faster drive gains anything here",
	}, nil)

	// Bandwidth, not width: a Gen5 x4 drive behind a Gen3 x8 uplink
	// (≈7.9 GB/s) is held to the uplink.
	got = storageAnswers(t, switched(lk(gen5, 4), lk(gen5, 4), lk(gen3, 8), lk(gen3, 8)), storageKB(""))
	wantAnswers(t, got, map[string]string{
		"bus": "NVMe 03:00.0 at PCIe 3.0 x8 (8.0 GT/s ×8, ≈7.9 GB/s) via 02:00.0, 01:00.0, 00:01.0, set by the link between 00:01.0 and 01:00.0; " +
			"the drive can do PCIe 5.0 x4 (32.0 GT/s ×4, ≈15.8 GB/s), but the path allows less" + cpuOrChipset,
	}, nil)

	// A Gen3 drive in a Gen4 port gains up to Gen4, named as such: a
	// Gen5 drive gains nothing more.
	got = storageAnswers(t, switched(lk(gen3, 4), lk(gen4, 4), lk(gen4, 16), lk(gen4, 16)), storageKB(""))
	wantAnswers(t, got, map[string]string{
		"faster": "A PCIe 4.0 NVMe SSD in place of 03:00.0 would run at PCIe 4.0 x4 (16.0 GT/s ×4, ≈7.9 GB/s) here, the most 02:00.0's port allows; " +
			"it gets up to ≈7.9 GB/s (now ≈3.9 GB/s), and a faster one gains nothing more",
	}, map[string]bool{"faster": true})

	// An x8 add-in card: an M.2 drive in its place would run slower.
	got = storageAnswers(t, switched(lk(gen4, 8), lk(gen4, 16), lk(gen4, 16), lk(gen4, 16)), storageKB(""))
	wantAnswers(t, got, map[string]string{
		"faster": "An M.2 NVMe SSD in place of 03:00.0 would run slower, at PCIe 4.0 x4 (16.0 GT/s ×4, ≈7.9 GB/s) here, the most 02:00.0's port allows an M.2 drive (x4 at most; the port is x16); " +
			"that's ≈7.9 GB/s against ≈15.8 GB/s now, and no faster M.2 drive gains anything here",
	}, map[string]bool{"faster": true})

	// A Gen3 x4 drive in a Gen4 x16 port: the port's generation, no width
	// named (the drive is already x4).
	got = storageAnswers(t, switched(lk(gen3, 4), lk(gen4, 16), lk(gen4, 16), lk(gen4, 16)), storageKB(""))
	wantAnswers(t, got, map[string]string{
		"faster": "A PCIe 4.0 NVMe SSD in place of 03:00.0 would run at PCIe 4.0 x4 (16.0 GT/s ×4, ≈7.9 GB/s) here, the most 02:00.0's port allows an M.2 drive (x4 at most; the port is x16); " +
			"it gets up to ≈7.9 GB/s (now ≈3.9 GB/s), and a faster one gains nothing more",
	}, nil)

	// A Gen3 x2 drive in a Gen4 x4 port behind a Gen3 x4 uplink: it gets
	// up to the uplink, not the port.
	got = storageAnswers(t, switched(lk(gen3, 2), lk(gen4, 4), lk(gen3, 4), lk(gen3, 4)), storageKB(""))
	wantAnswers(t, got, map[string]string{
		"faster": "A PCIe 4.0 x4 NVMe SSD in place of 03:00.0 would run at PCIe 4.0 x4 (16.0 GT/s ×4, ≈7.9 GB/s) here, the most 02:00.0's port allows; " +
			"the link between 00:01.0 and 01:00.0 allows PCIe 3.0 x4 (8.0 GT/s ×4, ≈3.9 GB/s), so it gets at most that; it gets up to ≈3.9 GB/s (now ≈2.0 GB/s), and a faster one gains nothing more",
	}, nil)

	// Faster's limit is the uplink, Bus's the drive's link: both in the
	// evidence.
	f := storageFinding(t, switched(lk(gen3, 4), lk(gen4, 4), lk(gen4, 2), lk(gen4, 2)), storageKB(""))
	if want := "A PCIe 5.0 or later NVMe SSD in place of 03:00.0 would still run at PCIe 4.0 x4 (16.0 GT/s ×4, ≈7.9 GB/s) here, the most 02:00.0's port allows; " +
		"the link between 00:01.0 and 01:00.0 allows PCIe 4.0 x2 (16.0 GT/s ×2, ≈3.9 GB/s), so it gets at most that; no faster drive gains anything here"; f.Answers[2].Text != want {
		t.Errorf("uplink-limited faster:\n got %q\nwant %q", f.Answers[2].Text, want)
	}
	for _, e := range []string{"pci[0].pcie_link.max_speed", "pci[1].pcie_link.max_width", "pci[2].pcie_link.max_speed", "pci[3].pcie_link.max_width"} {
		if !slices.ContainsFunc(f.Evidence, func(ev Evidence) bool { return ev.Path() == e }) {
			t.Errorf("evidence lacks %s: %+v", e, f.Evidence)
		}
	}

	// Links that carry the same: the drive's own is named, no other.
	got = storageAnswers(t, switched(lk(gen3, 4), lk(gen3, 4), lk(gen3, 4), lk(gen3, 4)), storageKB(""))
	wantAnswers(t, got, map[string]string{
		"bus": "NVMe 03:00.0 at PCIe 3.0 x4 (8.0 GT/s ×4, ≈3.9 GB/s) via 02:00.0, 01:00.0, 00:01.0" + cpuOrChipset,
	}, nil)

	// An x8 add-in drive behind an x4 uplink: the uplink is the limit,
	// in the evidence; an x4 drive in its place gets as much.
	f = storageFinding(t, switched(lk(gen3, 8), lk(gen3, 8), lk(gen3, 4), lk(gen3, 4)), storageKB(""))
	if want := "NVMe 03:00.0 at PCIe 3.0 x4 (8.0 GT/s ×4, ≈3.9 GB/s) via 02:00.0, 01:00.0, 00:01.0, set by the link between 00:01.0 and 01:00.0; " +
		"the drive can do PCIe 3.0 x8 (8.0 GT/s ×8, ≈7.9 GB/s), but the path allows less" + cpuOrChipset; f.Answers[0].Text != want {
		t.Errorf("x8 drive:\n got %q\nwant %q", f.Answers[0].Text, want)
	}
	for _, e := range []string{"pci[0].pcie_link.max_width", "pci[1].pcie_link.max_width"} {
		if !slices.ContainsFunc(f.Evidence, func(ev Evidence) bool { return ev.Path() == e }) {
			t.Errorf("evidence lacks the uplink's %s: %+v", e, f.Evidence)
		}
	}
}

// cascaded is a drive behind two switches: drive 05:00.0 → 04:00.0 ↑
// 03:00.0 → 02:00.0 ↑ 01:00.0 → root port 00:01.0, three links.
func cascaded(middle, top *report.PCIeLink) *report.Report {
	r := storageExample()
	br := func(addr, parent string, l *report.PCIeLink) report.PCIDevice {
		return report.PCIDevice{Address: "0000:" + addr, Parent: parent, ClassCode: "060400", Link: l}
	}
	r.PCI = []report.PCIDevice{
		br("00:01.0", "", lk(gen4, 16)), br("01:00.0", "0000:00:01.0", top), br("02:00.0", "0000:01:00.0", lk(gen4, 16)),
		br("03:00.0", "0000:02:00.0", middle), br("04:00.0", "0000:03:00.0", lk(gen4, 4)),
		{Address: "0000:05:00.0", Parent: "0000:04:00.0", ClassCode: "010802", Link: lk(gen4, 4)},
	}
	return r
}

// A slow link anywhere on a three-link path limits it.
func TestStorageCascadedSwitches(t *testing.T) {
	const drive = "NVMe 05:00.0 at PCIe 3.0 x1 (8.0 GT/s ×1, ≈1.0 GB/s) via 04:00.0, 03:00.0, 02:00.0, 01:00.0, 00:01.0, set by the link between "
	const rest = "; the drive can do PCIe 4.0 x4 (16.0 GT/s ×4, ≈7.9 GB/s), but the path allows less" + cpuOrChipset
	got := storageAnswers(t, cascaded(lk(gen4, 16), lk(gen3, 1)), storageKB(""))
	wantAnswers(t, got, map[string]string{
		"bus": drive + "00:01.0 and 01:00.0" + rest,
		"faster": "A PCIe 5.0 or later NVMe SSD in place of 05:00.0 would still run at PCIe 4.0 x4 (16.0 GT/s ×4, ≈7.9 GB/s) here, the most 04:00.0's port allows; " +
			"the link between 00:01.0 and 01:00.0 allows PCIe 3.0 x1 (8.0 GT/s ×1, ≈1.0 GB/s), so it gets at most that; no faster drive gains anything here",
	}, map[string]bool{"bus": true})
	got = storageAnswers(t, cascaded(lk(gen3, 1), lk(gen4, 16)), storageKB(""))
	wantAnswers(t, got, map[string]string{"bus": drive + "02:00.0 and 03:00.0" + rest}, nil)
}

// Where the chipset's link to the CPU is, by the bridges' names: beyond
// a PCH root port, or on the path above an AMD chipset switch.
func TestStorageChipsetLink(t *testing.T) {
	r := storageExample()
	r.PCI[0].Identity = &report.Identity{Vendor: "Intel Corporation", Model: "Cannon Lake PCH PCI Express Root Port #21"}
	if got := storageAnswers(t, r, storageKB(""))["bus"].Text; !strings.HasSuffix(got, chipset) {
		t.Errorf("PCH: %q", got)
	}
	r = switched(lk(gen4, 4), lk(gen4, 4), lk(gen4, 4), lk(gen4, 4))
	r.PCI[0].Identity = &report.Identity{Vendor: "Advanced Micro Devices, Inc. [AMD]", Model: "Raphael/Granite Ridge GPP Bridge"}
	r.PCI[1].Identity = &report.Identity{Vendor: "Advanced Micro Devices, Inc. [AMD]", Model: "600 Series Chipset PCIe Switch Upstream Port"}
	r.PCI[2].Identity = &report.Identity{Vendor: "Advanced Micro Devices, Inc. [AMD]", Model: "600 Series Chipset PCIe Switch Downstream Port"}
	if got, want := storageAnswers(t, r, storageKB(""))["bus"].Text, "NVMe 03:00.0 at PCIe 4.0 x4 (16.0 GT/s ×4, ≈7.9 GB/s) via 02:00.0, 01:00.0, 00:01.0; "+
		"the link between 00:01.0 and 01:00.0 is the chipset's link to the CPU, shared with the chipset's other devices"; got != want {
		t.Errorf("AMD chipset switch:\n got %q\nwant %q", got, want)
	}
	// Only the downstream port named: no link to point at.
	r.PCI[1].Identity = nil
	if got := storageAnswers(t, r, storageKB(""))["bus"].Text; !strings.HasSuffix(got, cpuOrChipset) {
		t.Errorf("chipset port without its uplink: %q", got)
	}
}

// A port narrower than the drive limits it, and a faster drive gains
// over a slower one; at capture a slower link may be idle, a narrower
// one isn't, on the drive's link or one above; a slot the firmware names.
func TestStoragePaths(t *testing.T) {
	r := storageExample()
	r.PCI[0].Link = lk(gen4, 2)
	r.PCI[1].Link = &report.PCIeLink{Speed: "2.5 GT/s PCIe", Width: 2, MaxSpeed: gen3, MaxWidth: 4}
	r.PCI[1].Mounting = &report.Mounting{Kind: "slot", Slot: "M2_1", SlotType: "M.2 Socket 3 (Mechanical Key M)", Confidence: "high"}
	got := storageAnswers(t, r, storageKB(""))
	wantAnswers(t, got, map[string]string{
		"bus": "NVMe " + samsung + " at PCIe 3.0 x2 (8.0 GT/s ×2, ≈2.0 GB/s) via 00:1b.0; the drive can do PCIe 3.0 x4 (8.0 GT/s ×4, ≈3.9 GB/s), but the path allows less; " +
			"at capture its link ran at 2.5 GT/s (links may slow down when idle)" + chipset,
		"slots":  slotsDoc + "; 01:00.0 is in M2_1 (M.2 Socket 3 (Mechanical Key M))",
		"faster": "A PCIe 4.0 NVMe SSD in place of 01:00.0 would run at PCIe 4.0 x2 (16.0 GT/s ×2, ≈3.9 GB/s) here, the most 00:1b.0's port allows; an x4 drive runs at x2; it gets up to ≈3.9 GB/s (now ≈2.0 GB/s), and a faster one gains nothing more",
	}, map[string]bool{"bus": true, "slots": true, "faster": true})

	// x2 of x4 at full speed is a fault, not idling; so is a narrow
	// uplink.
	r = switched(&report.PCIeLink{Speed: gen4, Width: 2, MaxSpeed: gen4, MaxWidth: 4}, lk(gen4, 4), &report.PCIeLink{Speed: "2.5 GT/s PCIe", Width: 4, MaxSpeed: gen4, MaxWidth: 16}, lk(gen4, 16))
	got = storageAnswers(t, r, storageKB(""))
	wantAnswers(t, got, map[string]string{
		"bus": "NVMe 03:00.0 at PCIe 4.0 x4 (16.0 GT/s ×4, ≈7.9 GB/s) via 02:00.0, 01:00.0, 00:01.0; " +
			"at capture its link ran at x2 of x4: idling doesn't narrow a link, so it may be badly seated or have a signal fault; " +
			"at capture the link between 00:01.0 and 01:00.0 ran at 2.5 GT/s (links may slow down when idle); " +
			"at capture the link between 00:01.0 and 01:00.0 ran at x4 of x16: idling doesn't narrow a link, so it may be badly seated or have a signal fault" + cpuOrChipset,
	}, nil)
	f := storageFinding(t, r, storageKB(""))
	for _, e := range []string{"pci[3].pcie_link.width", "pci[1].pcie_link.speed", "pci[1].pcie_link.width"} {
		if !slices.ContainsFunc(f.Evidence, func(ev Evidence) bool { return ev.Path() == e }) {
			t.Errorf("evidence lacks %s: %+v", e, f.Evidence)
		}
	}
	// An unreadable width or speed at capture says nothing.
	for _, w := range []int{0, 255} {
		r = storageExample()
		r.PCI[1].Link = &report.PCIeLink{Speed: "Unknown", Width: w, MaxSpeed: gen3, MaxWidth: 4}
		if got := storageAnswers(t, r, storageKB("")); got["bus"].Text != "NVMe "+samsung+" at PCIe 3.0 x4 (8.0 GT/s ×4, ≈3.9 GB/s) via 00:1b.0"+chipset {
			t.Errorf("unknown current link x%d: %q", w, got["bus"].Text)
		}
	}
}

// A narrower drive gains from a full-width one of the same generation.
func TestStorageNarrowDrive(t *testing.T) {
	r := storageExample()
	r.PCI[1].Link = lk(gen3, 2)
	got := storageAnswers(t, r, storageKB(""))
	wantAnswers(t, got, map[string]string{
		"bus": "NVMe " + samsung + " at PCIe 3.0 x2 (8.0 GT/s ×2, ≈2.0 GB/s) via 00:1b.0" + chipset,
		"faster": "A PCIe 3.0 x4 NVMe SSD in place of 01:00.0 would run at PCIe 3.0 x4 (8.0 GT/s ×4, ≈3.9 GB/s) here, the most 00:1b.0's port allows; " +
			"it gets up to ≈3.9 GB/s (now ≈2.0 GB/s), and a faster one gains nothing more",
	}, nil)
}

// Intel VMD: the root port's parent is the VMD controller, not a bridge;
// the path stops at the root port.
func TestStorageBehindVMD(t *testing.T) {
	r := storageExample()
	r.PCI = append([]report.PCIDevice{{Address: "0000:00:0e.0", ClassCode: "010400"}}, r.PCI...)
	r.PCI[1].Parent = "0000:00:0e.0"
	r.PCI[2].Parent = "0000:00:1b.0"
	if got := storageAnswers(t, r, storageKB("")); got["bus"].Text != "NVMe "+samsung+" at PCIe 3.0 x4 (8.0 GT/s ×4, ≈3.9 GB/s) via 00:1b.0"+chipset || !got["bus"].Known {
		t.Errorf("VMD: %+v", got["bus"])
	}
}

// What can't be told is said, with the part, and never known: a link the
// capture lacks or can't read (width 0 or 255, speed "Unknown") at either
// end, a bridge outside the capture, bridges that don't pair, a loop, a
// Thunderbolt tunnel.
func TestStorageUnknownPaths(t *testing.T) {
	for name, c := range map[string]struct {
		r       func() *report.Report
		why     string
		absent  string
		present string
	}{
		"no drive link": {r: func() *report.Report { r := storageExample(); r.PCI[1].Link = nil; return r },
			why: "the most 01:00.0's PCIe link allows isn't in the capture", absent: "pci[1].pcie_link"},
		"no port link": {r: func() *report.Report { r := storageExample(); r.PCI[0].Link = nil; return r },
			why: "the most 00:1b.0's PCIe link allows isn't in the capture", absent: "pci[0].pcie_link"},
		"width 255": {r: func() *report.Report { r := storageExample(); r.PCI[1].Link = lk(gen3, 255); return r },
			why: "the most 01:00.0's PCIe link allows isn't in the capture", present: "pci[1].pcie_link.max_width"},
		"width 0": {r: func() *report.Report { r := storageExample(); r.PCI[0].Link = lk(gen3, 0); return r },
			why: "the most 00:1b.0's PCIe link allows isn't in the capture", present: "pci[0].pcie_link.max_width"},
		"speed unknown": {r: func() *report.Report { r := storageExample(); r.PCI[0].Link = lk("Unknown", 4); return r },
			why: "the most 00:1b.0's PCIe link allows isn't in the capture", present: "pci[0].pcie_link.max_speed"},
		"uplink unknown": {r: func() *report.Report { return switched(lk(gen4, 4), lk(gen4, 4), lk(gen3, 16), lk("Unknown", 16)) },
			why: "the most 00:01.0's PCIe link allows isn't in the capture", present: "pci[0].pcie_link.max_speed"},
		"bridge outside": {r: func() *report.Report {
			r := storageExample()
			r.PCI[0].Parent = "0000:00:1c.0"
			r.PCI[0].ClassCode = "060400"
			return r
		},
			why: "00:1c.0, the bridge above 00:1b.0, isn't in the capture"},
		"no port": {r: func() *report.Report { r := storageExample(); r.PCI[1].Parent = ""; return r },
			why: "no PCIe port above it is in the capture"},
		"odd bridges": {r: func() *report.Report {
			r := switched(lk(gen4, 4), lk(gen4, 4), lk(gen3, 16), lk(gen3, 16))
			r.PCI[1].Parent = ""
			return r
		},
			why: "the bridges above it don't pair into links (a port without the device below it, or the reverse)"},
		"loop": {r: func() *report.Report { r := storageExample(); r.PCI[0].Parent = "0000:00:1b.0"; return r },
			why: "the capture's bridges above it form a loop"},
		"thunderbolt root port": {r: func() *report.Report {
			r := storageExample()
			r.PCI[0].Identity = &report.Identity{Vendor: "Intel Corporation", Model: "Tiger Lake-LP Thunderbolt 4 PCI Express Root Port #0"}
			return r
		}, why: "it's behind 00:1b.0 (Intel Corporation Tiger Lake-LP Thunderbolt 4 PCI Express Root Port #0), a Thunderbolt or USB4 bridge, whose links give a nominal speed, not the tunnel's", present: "pci[0].pcie_link.max_speed"},
		"usb4": {r: func() *report.Report {
			r := switched(lk(gen4, 4), lk(gen4, 4), lk(gen4, 4), lk(gen4, 4))
			r.PCI[1].Identity = &report.Identity{Model: "Raptor Lake-P USB4 PCIe Upstream Port"}
			return r
		}, why: "it's behind 01:00.0 (Raptor Lake-P USB4 PCIe Upstream Port), a Thunderbolt or USB4 bridge, whose links give a nominal speed, not the tunnel's", present: "pci[1].pcie_link.max_speed"},
		"thunderbolt": {r: func() *report.Report {
			r := switched(lk(gen4, 4), lk("2.5 GT/s PCIe", 4), lk("2.5 GT/s PCIe", 4), lk(gen3, 4))
			r.PCI[2].Identity = &report.Identity{Vendor: "Intel Corporation", Model: "JHL7540 Thunderbolt 3 Bridge [Titan Ridge 4C 2018]"}
			return r
		}, why: "it's behind 02:00.0 (Intel Corporation JHL7540 Thunderbolt 3 Bridge [Titan Ridge 4C 2018]), a Thunderbolt or USB4 bridge, whose links give a nominal speed, not the tunnel's", present: "pci[2].pcie_link.max_speed"},
	} {
		t.Run(name, func(t *testing.T) {
			done := make(chan Advice)
			go func() { done <- Advise(Input{Report: c.r(), KB: storageKB(""), Now: noon}) }()
			var a Advice
			select {
			case a = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("advise didn't return")
			}
			if len(a.Findings) != 1 || len(a.Warnings) != 0 {
				t.Fatalf("findings %+v, warnings %q", a.Findings, a.Warnings)
			}
			f := a.Findings[0]
			got := map[string]Answer{}
			for _, an := range f.Answers {
				got[an.Topic] = an
			}
			drive := samsung
			if !strings.HasPrefix(got["bus"].Text, "NVMe 01:00.0") {
				drive = "03:00.0"
			}
			wantAnswers(t, got, map[string]string{
				"bus":    "NVMe " + drive + ": the most its path allows can't be told: " + c.why,
				"faster": "what a faster drive in place of " + drive[:7] + " would get can't be told: " + c.why,
			}, map[string]bool{"bus": false, "faster": false})
			for _, e := range f.Evidence {
				if c.absent != "" && e.Path() == c.absent {
					if _, ok := e.Value(); !ok {
						c.absent = ""
					}
				}
				if e.Path() == c.present {
					c.present = ""
				}
			}
			if c.absent != "" || c.present != "" {
				t.Errorf("evidence %+v lacks %s%s", f.Evidence, c.absent, c.present)
			}
		})
	}
}

// Two drives: each answered once in one answer per topic, known only
// when both are.
func TestStorageTwoDrives(t *testing.T) {
	r := storageExample()
	r.PCI = append(r.PCI,
		report.PCIDevice{Address: "0000:00:1d.0", ClassCode: "060400", Link: lk(gen3, 4)},
		report.PCIDevice{Address: "0000:02:00.0", Parent: "0000:00:1d.0", ClassCode: "010802",
			Mounting: &report.Mounting{Kind: "slot", Slot: "M2_2", SlotType: "M.2 Socket 3", Confidence: "medium", Reason: "the only slot it can be in"}})
	got := storageAnswers(t, r, storageKB(""))
	wantAnswers(t, got, map[string]string{
		"bus": "NVMe " + samsung + " at PCIe 3.0 x4 (8.0 GT/s ×4, ≈3.9 GB/s) via 00:1b.0" + chipset +
			"; NVMe 02:00.0: the most its path allows can't be told: the most 02:00.0's PCIe link allows isn't in the capture",
		"slots": slotsDoc + "; which slot holds 01:00.0 isn't in the capture; 02:00.0 is likely in M2_2 (M.2 Socket 3), with medium confidence: the only slot it can be in",
		"faster": "A PCIe 4.0 or later NVMe SSD in place of 01:00.0 would still run at PCIe 3.0 x4 (8.0 GT/s ×4, ≈3.9 GB/s) here, the most 00:1b.0's port allows; no faster drive gains anything here" +
			"; what a faster drive in place of 02:00.0 would get can't be told: the most 02:00.0's PCIe link allows isn't in the capture",
	}, map[string]bool{"bus": false, "slots": true, "faster": false})
}

// The firmware's slot facts as it gives them: a slot whose type the
// candidates don't share, one with its type, an unknown kind, no reason.
func TestStorageMountings(t *testing.T) {
	for _, c := range []struct {
		m    *report.Mounting
		want string
	}{
		{&report.Mounting{Kind: "slot", Confidence: "medium", Reason: `which of "A" or "C" holds it is unknown`}, `01:00.0 is in a slot: which of "A" or "C" holds it is unknown`},
		{&report.Mounting{Kind: "slot", Confidence: "medium"}, "01:00.0 is in a slot: which one is unknown"},
		{&report.Mounting{Kind: "slot", Slot: "A", SlotType: "x4", Confidence: "medium"}, "01:00.0 is likely in A (x4), with medium confidence: no reason given"},
		{&report.Mounting{Kind: "soldered", Reason: "the firmware lists it onboard"}, "which slot holds 01:00.0 can't be told: the firmware lists it onboard"},
		{&report.Mounting{Kind: "unknown"}, "which slot holds 01:00.0 can't be told: unknown"},
	} {
		r := storageExample()
		r.Board.Slots = []report.Slot{{Designation: "A", Type: "x4"}, {Designation: "B"}, {Designation: "C", Type: "x1"}}
		r.PCI[1].Mounting = c.m
		if got := storageAnswers(t, r, storageKB(""))["slots"].Text; got != slotsDoc+"; "+c.want {
			t.Errorf("%+v:\n got %q\nwant %q", c.m, got, slotsDoc+"; "+c.want)
		}
	}
	// A named slot: its name, its type when given, and how sure.
	for _, c := range []struct {
		m        *report.Mounting
		want     string
		evidence []string
	}{
		{&report.Mounting{Kind: "slot", Slot: "M2_1", Confidence: "high"}, "01:00.0 is in M2_1", []string{"pci[1].mounting.slot"}},
		{&report.Mounting{Kind: "slot", Slot: "M2_1", Confidence: "medium", Reason: "by elimination"}, "01:00.0 is likely in M2_1, with medium confidence: by elimination",
			[]string{"pci[1].mounting.slot", "pci[1].mounting.confidence"}},
	} {
		r := storageExample()
		r.PCI[1].Mounting = c.m
		f := storageFinding(t, r, storageKB(""))
		if got := f.Answers[1].Text; got != slotsDoc+"; "+c.want {
			t.Errorf("%+v: %q", c.m, got)
		}
		for _, e := range c.evidence {
			if !slices.ContainsFunc(f.Evidence, func(ev Evidence) bool { return ev.Path() == e }) {
				t.Errorf("%+v: evidence lacks %s", c.m, e)
			}
		}
	}
}

// SATA-only slots refuse an NVMe suggestion; an NVMe drive in the capture
// contradicts them and makes the answer unknown; mixed claims, or a claim
// of both, aren't SATA only.
func TestStorageSATAOnlySlots(t *testing.T) {
	sata := `{"m2": {"count": [{"value": 1, "src": "example-datasheet"}], "interfaces": [{"value": ["sata"], "src": "example-datasheet"}]}}`
	r := storageExample()
	r.PCI = nil
	got := storageAnswers(t, r, storageKB(sata))
	wantAnswers(t, got, map[string]string{
		"faster": "The M.2 storage slots take SATA only, per example-datasheet (2019-12): an NVMe SSD won't work in them",
		"slots":  "1 M.2 storage slots per example-datasheet (2019-12); SATA per example-datasheet (2019-12)",
		"bus":    "No NVMe drive is in the capture",
	}, map[string]bool{"faster": true, "bus": false})

	got = storageAnswers(t, storageExample(), storageKB(sata))
	wantAnswers(t, got, map[string]string{
		"faster": "The knowledge base says the M.2 storage slots take SATA only, per example-datasheet (2019-12), but the capture has NVMe 01:00.0: in another slot or an adapter, or the knowledge base is wrong" +
			"; A PCIe 4.0 or later NVMe SSD in place of 01:00.0 would still run at PCIe 3.0 x4 (8.0 GT/s ×4, ≈3.9 GB/s) here, the most 00:1b.0's port allows; no faster drive gains anything here",
	}, map[string]bool{"faster": false})

	for _, group := range []string{
		`{"m2": {"interfaces": [{"value": ["sata"], "src": "example-datasheet"}, {"value": ["nvme"], "src": "example-datasheet"}]}}`,
		`{"m2": {"interfaces": [{"value": ["nvme", "sata"], "src": "example-datasheet"}]}}`,
		`{"m2": {"count": [{"value": 2, "src": "example-datasheet"}]}}`,
	} {
		got := storageAnswers(t, storageExample(), storageKB(group))
		if !strings.HasPrefix(got["faster"].Text, "A PCIe 4.0 or later NVMe SSD") || !got["faster"].Known {
			t.Errorf("%s: %+v", group, got["faster"])
		}
	}
}

// The model's slots without an entry, a group or a readable group; no
// NVMe and no entry gives no finding.
func TestStorageUnknownModel(t *testing.T) {
	r := storageExample()
	r.PCI[1].Mounting = &report.Mounting{Kind: "unknown", Reason: "the firmware's slot table needs root (run with --full)"}
	r.System.Identity.Model = "Another Model"
	got := storageAnswers(t, r, storageKB(""))
	wantAnswers(t, got, map[string]string{
		"slots": "The model's M.2 slots are unknown: the knowledge base has no entry for this model; which slot holds 01:00.0 can't be told: the firmware's slot table needs root (run with --full)",
	}, map[string]bool{"slots": false, "bus": true})

	r.PCI = nil
	if a := Advise(Input{Report: r, KB: storageKB(""), Now: noon}); len(a.Findings) != 0 {
		t.Errorf("no NVMe, no entry: %+v", a.Findings)
	}
	r = storageExample()
	r.PCI = nil
	wantAnswers(t, storageAnswers(t, r, storageKB("")), map[string]string{
		"faster": "What a faster drive would gain can't be told: no NVMe drive is in the capture",
	}, map[string]bool{"faster": false, "slots": true})

	k := storageKB("")
	delete(k.Models[0].Data, "storage_slots")
	wantAnswers(t, storageAnswers(t, storageExample(), k), map[string]string{
		"slots": "The model's M.2 slots are unknown: the knowledge base has no storage data for this model; which slot holds 01:00.0 isn't in the capture",
	}, map[string]bool{"slots": false})
	wantAnswers(t, storageAnswers(t, storageExample(), storageKB(`{"m2": {}}`)), map[string]string{
		"slots": "The model's M.2 slots are unknown: this build can't read the knowledge base's storage data for this model (update hwspec): m2: no claims (leave the group out when the documents say nothing); which slot holds 01:00.0 isn't in the capture",
	}, map[string]bool{"slots": false})
}

// A vendor firmware policy is named, not interpreted; one about other
// parts, one for other BIOS versions, one whose range can't be checked
// and the model's own group are each said for what they are.
func TestStorageAllowlist(t *testing.T) {
	policy := func(id string, restricts string, bios *kb.Range) kb.Allowlist {
		a := kb.Allowlist{ID: id, Match: kb.AllowlistMatch{SysVendor: "HP", ProductName: []string{"HP EliteDesk 800 G5 Desktop Mini"}, BIOSVersion: bios}}
		if restricts != "" {
			a.Data = map[string]json.RawMessage{"restricts": json.RawMessage(restricts)}
		}
		return a
	}
	r := storageExample()
	r.System.Firmware = &report.Firmware{Version: "R21 Ver. 02.27.00"}
	for _, c := range []struct {
		policies []kb.Allowlist
		model    bool
		want     string
	}{
		{[]kb.Allowlist{policy("hp.ssd", "", nil)}, false,
			"The knowledge base has HP firmware policies that may restrict third-party SSDs in this model (hp.ssd): check their terms before buying one"},
		{[]kb.Allowlist{policy("hp.ssd", `[{"value": ["wlan", "ssd"], "src": "x"}]`, nil), policy("hp.wlan", `[{"value": ["wlan"], "src": "x"}]`, nil), policy("hp.broken", `"broken"`, nil)}, false,
			"The knowledge base has HP firmware policies that may restrict third-party SSDs in this model (hp.ssd, hp.broken): check their terms before buying one"},
		{[]kb.Allowlist{policy("hp.wlan", `[{"value": ["wlan"], "src": "x"}]`, nil)}, false, noPolicy},
		{[]kb.Allowlist{policy("hp.old", "", &kb.Range{To: "R21 Ver. 02.10.00"})}, false,
			"Whether HP's firmware restricts third-party SSDs in this BIOS is unknown: HP's policies hp.old cover this model with other BIOS versions, not this one"},
		{[]kb.Allowlist{policy("hp.q", "", &kb.Range{From: "Q21 Ver. 01.00.00"})}, false,
			"Whether HP's firmware restricts third-party SSDs in this BIOS is unknown: HP's policies hp.q may cover it, but their BIOS range can't be checked against this BIOS's version"},
		{[]kb.Allowlist{policy("hp.wlan-old", `[{"value": ["wlan"], "src": "x"}]`, &kb.Range{To: "R21 Ver. 02.10.00"}),
			policy("hp.wlan-q", `[{"value": ["wlan"], "src": "x"}]`, &kb.Range{From: "Q21 Ver. 01.00.00"})}, false, noPolicy},
		{[]kb.Allowlist{policy("hp.empty", `[]`, nil)}, false,
			"The knowledge base has HP firmware policies that may restrict third-party SSDs in this model (hp.empty): check their terms before buying one"},
		{nil, true, "Whether HP's firmware restricts third-party SSDs in this BIOS is unknown: the model's entry has an allowlist group this build doesn't read yet"},
	} {
		k := storageKB("")
		k.Allowlists = c.policies
		if c.model {
			k.Models[0].Data["allowlist"] = json.RawMessage(`{}`)
		}
		if got := storageAnswers(t, r, k)["allowlist"]; got.Text != c.want || got.Known {
			t.Errorf("%+v:\n got %q\nwant %q", c.policies, got.Text, c.want)
		}
	}
	r.System.Identity.Vendor = ""
	k := storageKB("")
	k.Models[0].Match.SysVendor = ""
	if got := storageAnswers(t, r, k)["allowlist"].Text; got != "Whether the vendor's firmware restricts third-party SSDs in this model is unknown: the knowledge base has no source on it" {
		t.Errorf("no vendor: %q", got)
	}
}

func TestStorageGroupValidation(t *testing.T) {
	for group, want := range map[string]string{
		`{"m2": {"count": [{"value": -1, "src": "x"}]}}`:            "m2.count: -1 isn't positive",
		`{"m2": {"count": [{"value": 0, "src": "x"}]}}`:             "m2.count: 0 isn't positive",
		`{"m2": {"lengths": [{"value": [2250], "src": "x"}]}}`:      "m2.lengths: 2250 isn't an M.2 length",
		`{"m2": {"interfaces": [{"value": ["pcie"], "src": "x"}]}}`: `m2.interfaces: "pcie" isn't nvme or sata`,
		`{"m2": {"slots": [{"value": 2, "src": "x"}]}}`:             `json: unknown field "slots"`,
		`{}`: "m2: no claims",
	} {
		m := kb.Model{Data: map[string]json.RawMessage{"storage_slots": json.RawMessage(group)}}
		if errs := ValidateModel(&m, nil); len(errs) != 1 || !strings.Contains(errs[0].Error(), "data.storage_slots: "+want) {
			t.Errorf("%s: %v", group, errs)
		}
	}
}

func TestLinkWords(t *testing.T) {
	for _, c := range []struct {
		gt    float64
		width int
		want  string
	}{
		{2.5, 1, "PCIe 1.0 x1 (2.5 GT/s ×1, ≈0.2 GB/s)"},
		{5, 4, "PCIe 2.0 x4 (5.0 GT/s ×4, ≈2.0 GB/s)"},
		{8, 4, "PCIe 3.0 x4 (8.0 GT/s ×4, ≈3.9 GB/s)"},
		{16, 4, "PCIe 4.0 x4 (16.0 GT/s ×4, ≈7.9 GB/s)"},
		{32, 4, "PCIe 5.0 x4 (32.0 GT/s ×4, ≈15.8 GB/s)"},
		{64, 4, "PCIe 6.0 x4 (64.0 GT/s ×4, ≈32.0 GB/s)"},
		{12, 1, "PCIe ? x1 (12.0 GT/s ×1, ≈1.5 GB/s)"},
	} {
		if got := linkWords(c.gt, c.width); got != c.want {
			t.Errorf("%v x%d: %q", c.gt, c.width, got)
		}
	}
	for in, want := range map[string]float64{"8.0 GT/s PCIe": 8, "2.5 GT/s": 2.5, "Unknown": 0, "8.0 Gb/s": 0, "": 0, "0 GT/s": 0, "-8 GT/s": 0} {
		if got := gts(in); got != want {
			t.Errorf("gts(%q) = %v", in, got)
		}
	}
}
