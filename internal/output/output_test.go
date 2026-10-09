package output

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jiegui2025/hwspec/internal/report"
)

func sample() *report.Report {
	on := true
	return &report.Report{
		SchemaVersion: report.SchemaVersion,
		Tool:          report.Tool{Name: "hwspec", Version: "test"},
		CapturedAt:    time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC),
		Hostname:      "box",
		OS:            report.OS{PrettyName: "Test OS", SecureBoot: &on, Virtualization: "none"},
		// Strings that look like other YAML types must survive a round trip.
		System: report.System{Firmware: &report.Firmware{Version: "R21", Release: "27.0", Source: "dmi"},
			MEFirmware: &report.Firmware{Vendor: "Intel", Version: "12.0.45.1509", Source: "mei"},
			ECFirmware: &report.Firmware{Version: "8.9", Source: "dmi"}},
		TPM:    &report.TPM{SpecVersionMajor: 2},
		Board:  report.Board{Identity: &report.Identity{Model: "8595", Revision: "true"}},
		Memory: report.Memory{TotalBytes: 32 << 30, Modules: []report.MemoryModule{}},
		Storage: []report.Disk{{Name: "sda", Identity: &report.Identity{Model: "SSD", Serial: "S1"},
			Health:     &report.Health{Status: report.StatusOK, Metrics: map[string]float64{report.MetricPowerOnHours: 2541}},
			Partitions: []report.Partition{{Name: "sda1", UUID: "u"}}}},
		Network:  []report.NIC{{Name: "eth0", MAC: "00:11:22:33:44:55"}},
		Warnings: []string{"x: needs root"},
	}
}

func TestRoundTrip(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		var buf bytes.Buffer
		if err := Write(&buf, sample(), format); err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		got, err := Read(buf.Bytes())
		if err != nil {
			t.Fatalf("%s: read back: %v", format, err)
		}
		if !reflect.DeepEqual(got, sample()) {
			t.Errorf("%s round trip changed the report:\n%s", format, buf.String())
		}
	}
}

func TestText(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, sample(), "text"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Test OS", "32 GiB usable", "x: needs root",
		"Intel Management Engine firmware 12.0.45.1509", "embedded controller firmware 8.9", "TPM 2 (TCG spec major version)"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("text output lacks %q:\n%s", want, buf.String())
		}
	}
}

func TestRedact(t *testing.T) {
	r := sample()
	r.Redact()
	if r.Hostname != "" || r.Storage[0].Identity.Serial != "" || r.Storage[0].Partitions[0].UUID != "" || r.Network[0].MAC != "" || !r.Redacted {
		t.Errorf("redact left identifiers: %+v", r)
	}
}

func TestBytesStr(t *testing.T) {
	for n, want := range map[uint64]string{512: "512 B", 32 << 10: "32 KiB", 9 << 20: "9 MiB", 256060514304: "238.5 GiB"} {
		if got := bytesStr(n); got != want {
			t.Errorf("bytesStr(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestShowRejectsFilesThatAreNotCaptures(t *testing.T) {
	for _, in := range []string{`{"name":"left-pad","version":"1.0"}`, "name: x\nversion: 2\n", `{"tool":{"name":"hwspec"}}`} {
		if _, err := Read([]byte(in)); !errors.Is(err, ErrNotCapture) {
			t.Errorf("Read(%q) err = %v, want ErrNotCapture", in, err)
		}
	}
}

// Device strings come from hardware and shared files; they must not reach
// the terminal as escape sequences.
func TestTextOutputIsTerminalSafe(t *testing.T) {
	r := sample()
	r.CPU.Identity = &report.Identity{}
	r.CPU.Identity.Model = "Evil\x1b]52;c;ZXZpbA==\x07\x1b[2J CPU\x9b"
	var buf bytes.Buffer
	if err := Write(&buf, r, "text"); err != nil {
		t.Fatal(err)
	}
	for _, c := range buf.String() {
		if c != '\n' && (c < 0x20 || c == 0x7f || (c >= 0x80 && c < 0xa0)) {
			t.Fatalf("control character %U in text output", c)
		}
	}
	if !strings.Contains(buf.String(), "Evil]52;c;ZXZpbA==[2J CPU") {
		t.Errorf("visible text lost:\n%s", buf.String())
	}
}

func TestTextShowsOnlyWhatWasReported(t *testing.T) {
	r := sample()
	r.Bluetooth = []report.BluetoothController{{Name: "hci0", Manufacturer: "   "}}
	r.Batteries = []report.Battery{{Name: "BAT0", Identity: &report.Identity{Model: "5B10"}, CapacityPercent: 80}}
	r.CPU.Microarchitecture = "Zen 4" // no codename for this model
	r.OS.BootMode = ""
	var buf bytes.Buffer
	if err := Write(&buf, r, "text"); err != nil { // must not panic on a blank manufacturer
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"5B10, 80% charged", "Cores are  Zen 4", "boot mode unknown"} {
		if !strings.Contains(out, want) {
			t.Errorf("text lacks %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"0.0 of 0.0 Wh", "0% health", "0 cycles"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("text shows a missing figure as zero (%q):\n%s", unwanted, out)
		}
	}
}

// Devices that need attention are grouped in one list, with the reasons,
// so nothing is lost among the healthy ones.
func TestNeedsAttentionGroupsWarningsAndFailures(t *testing.T) {
	r := sample()
	used, left := 96.0, 4.0
	r.Storage[0].Health = &report.Health{Status: report.StatusWarning, Reasons: []string{"rated write endurance reached: plan a replacement"},
		LifeUsedPercent: &used, LifeRemainingPercent: &left}
	r.Batteries = []report.Battery{{Name: "BAT0", Health: &report.Health{Status: report.StatusFailing, Reasons: []string{"driver reports battery health: dead"}}}}
	r.Network[0].Health = &report.Health{Status: report.StatusOK}
	var buf bytes.Buffer
	if err := Write(&buf, r, "text"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	i := strings.Index(out, "Needs attention")
	if i < 0 {
		t.Fatalf("no Needs attention section:\n%s", out)
	}
	section := out[i:]
	for _, want := range []string{"WARNING disk sda: rated write endurance reached", "FAILING battery BAT0: driver reports battery health: dead"} {
		if !strings.Contains(section, want) {
			t.Errorf("attention list lacks %q:\n%s", want, section)
		}
	}
	if strings.Contains(section, "eth0") {
		t.Error("a healthy device was listed as needing attention")
	}
	if !strings.Contains(out, "96% worn (4% life left)") {
		t.Errorf("wear not shown:\n%s", out)
	}
	// The reasons are the capture's own words, which may be someone else's.
	if !strings.Contains(out, "Needs attention (as recorded in this capture)") {
		t.Errorf("section doesn't say where its reasons come from:\n%s", out)
	}
}

// A Bluetooth line names the chip maker only when the adapter's name doesn't
// already, in any case.
func TestBluetoothChipMakerNamedOnce(t *testing.T) {
	for _, c := range []struct {
		id    *report.Identity
		maker string
		chip  bool
	}{
		{&report.Identity{Vendor: "Intel Corp.", Model: "INTEL AX200 Bluetooth"}, "Intel Corp.", false},
		{&report.Identity{Vendor: "Intel Corp.", Model: "AX200 Bluetooth"}, "Intel Corp.", false},
		{&report.Identity{Vendor: "Foxconn", Model: "T77H"}, "Qualcomm", true},
	} {
		r := sample()
		r.Bluetooth = []report.BluetoothController{{Name: "hci0", Identity: c.id, Manufacturer: c.maker}}
		var buf bytes.Buffer
		if err := Write(&buf, r, "text"); err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(buf.String(), "chip by"); got != c.chip {
			t.Errorf("%+v with maker %q: chip by shown = %v, want %v:\n%s", c.id, c.maker, got, c.chip, buf.String())
		}
	}
}

// Built-in drivers say so, and a battery's capacity is shown in Wh.
func TestBuiltInDriversAndBatteryCapacity(t *testing.T) {
	r := sample()
	r.Bluetooth = []report.BluetoothController{{Name: "hci0", Driver: &report.Driver{Name: "btusb", Builtin: true}}}
	r.Batteries = []report.Battery{{Name: "BAT0", Health: &report.Health{Status: report.StatusOK,
		Metrics: map[string]float64{report.MetricFullWh: 40.5, report.MetricDesignWh: 45}}}}
	var buf bytes.Buffer
	if err := Write(&buf, r, "text"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"btusb built in", "holds 40.5 of 45 Wh"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("text lacks %q:\n%s", want, buf.String())
		}
	}
}

func TestProductDoesNotRepeatTheVendor(t *testing.T) {
	for _, c := range []struct {
		id   *report.Identity
		want string
	}{
		{&report.Identity{Vendor: "HP", Model: "HP EliteDesk 800 G5 Desktop Mini"}, "HP EliteDesk 800 G5 Desktop Mini"},
		{&report.Identity{Vendor: "LENOVO", Model: "ThinkPad T14"}, "LENOVO ThinkPad T14"},
		{&report.Identity{Vendor: "LENOVO", Model: "Lenovo ThinkCentre M720q"}, "Lenovo ThinkCentre M720q"},
		{&report.Identity{Vendor: "HP", Model: "HPE ProLiant DL360"}, "HP HPE ProLiant DL360"},
		{&report.Identity{Vendor: "HP", Model: "HP-EliteBook 840"}, "HP-EliteBook 840"},
		{&report.Identity{Vendor: " HP ", Model: "HP Z2 G9"}, "HP Z2 G9"},
		{&report.Identity{Vendor: "Framework", Model: "framework"}, "framework"},
		{&report.Identity{Vendor: "Dell Inc.", Model: "Dell G15 5520"}, "Dell G15 5520"},
		{&report.Identity{Vendor: "Dell Inc.", Model: "DELL S2721QS"}, "DELL S2721QS"},
		{&report.Identity{Vendor: "VMware, Inc.", Model: "VMware Virtual Platform"}, "VMware Virtual Platform"},
		{&report.Identity{Vendor: "Dell Inc.", Model: "OptiPlex 7090"}, "Dell Inc. OptiPlex 7090"},
		{&report.Identity{Vendor: "ASUS", Model: "AS"}, "ASUS AS"},
		{&report.Identity{Vendor: "HP", Model: "  HP Z2 G9 "}, "HP Z2 G9"},
		{&report.Identity{Vendor: "MSI", Model: "MSI1 X"}, "MSI MSI1 X"},
		{&report.Identity{Vendor: "Société", Model: "SOCIÉTÉ X"}, "SOCIÉTÉ X"},
		{&report.Identity{Vendor: "", Model: "Z2 G9"}, "Z2 G9"},
		{&report.Identity{Vendor: "Dell Inc.", Model: ""}, "Dell Inc."},
		{nil, "unknown"},
	} {
		if got := product(c.id); got != c.want {
			t.Errorf("product(%+v) = %q, want %q", c.id, got, c.want)
		}
	}
}

// An ASUS desktop whose DMI system strings were firmware defaults (cleaned
// to nothing at capture): the Machine line says unknown, or just the vendor
// when newer firmware sets one, and the Board line names the board.
func TestTextMachineWithoutSystemStrings(t *testing.T) {
	for _, c := range []struct {
		system  *report.Identity
		machine string
	}{
		{nil, "unknown"},
		{&report.Identity{Vendor: "ASUS"}, "ASUS"},
	} {
		r := sample()
		r.System.Identity = c.system
		r.Board.Identity = &report.Identity{Vendor: "ASUSTeK COMPUTER INC.", Model: "ROG STRIX X570-E GAMING WIFI II"}
		var buf bytes.Buffer
		if err := Write(&buf, r, "text"); err != nil {
			t.Fatal(err)
		}
		lines := map[string]string{}
		for l := range strings.SplitSeq(buf.String(), "\n") {
			if f := strings.Fields(l); len(f) > 1 { // "  Machine    ASUS"
				lines[f[0]] = strings.Join(f[1:], " ")
			}
		}
		if lines["Machine"] != c.machine || lines["Board"] != "ASUSTeK COMPUTER INC. ROG STRIX X570-E GAMING WIFI II" {
			t.Errorf("system %+v: Machine %q, Board %q, want %q and the board:\n%s", c.system, lines["Machine"], lines["Board"], c.machine, buf.String())
		}
	}
}

// A two-socket workstation in a VM, with everything the text output can
// show: each feature's line, in the words a reader expects.
func TestTextDescribesAFullyFeaturedMachine(t *testing.T) {
	off, out, prop := false, false, true
	used, left := 30.0, 70.0
	r := sample()
	r.OS.Virtualization = "vm"
	r.CPU = report.CPU{
		Identity: &report.Identity{Model: "Xeon w5-2455X"}, Codename: "Sapphire Rapids", Microarchitecture: "Golden Cove",
		Sockets: 2, Cores: 24, Threads: 48, CoreTypes: []report.CoreType{{Name: "performance", Threads: 48}},
		MinFreqMHz: 800, MaxFreqMHz: 4600, Driver: &report.Driver{Name: "intel_pstate", Builtin: true},
		Caches: []report.Cache{{Level: 1, Type: "Data", SizeBytes: 48 << 10, Instances: 24}, {Level: 2, Type: "Unified", SizeBytes: 2 << 20, Instances: 24}},
		Health: &report.Health{Status: report.StatusWarning},
	}
	r.Memory = report.Memory{TotalBytes: 250 << 30, InstalledBytes: 256 << 30, Slots: 16, MaxCapacityBytes: 4 << 40, Modules: []report.MemoryModule{
		{Locator: "DIMM_A1", SizeBytes: 128 << 30, Type: "DDR5", FormFactor: "RDIMM", ConfiguredMTs: 4400, SpeedMTs: 4800,
			Identity: &report.Identity{Vendor: "Samsung", PartNumber: "M321R8GA0BB0", ManufactureDate: "2023-W12"}, DRAMVendor: "SK Hynix",
			Health: &report.Health{Status: report.StatusFailing, Reasons: []string{"1 uncorrectable memory errors since boot: replace this module"}}},
		{Locator: "DIMM_B1", SizeBytes: 128 << 30, ConfiguredMTs: 4800, Identity: &report.Identity{Vendor: "Samsung"}, DRAMVendor: "Samsung"},
		{Locator: "DIMM_C1", SpeedMTs: 5600},
	}}
	r.Storage = []report.Disk{
		{Name: "sda", Type: "unknown", Transport: "usb", SizeBytes: 1 << 40, Identity: &report.Identity{Vendor: "WD"}},
		{Name: "sr0", Type: "optical", Transport: "sata"},
	}
	act, idle := 883, 0
	r.GPUs = []report.GPU{{PCIAddress: "0000:01:00.0", Identity: &report.Identity{Vendor: "NVIDIA", Model: "RTX A4000"}, VRAMBytes: 16 << 30,
		Driver: &report.Driver{Name: "nvidia", Module: "nvidia", Version: "580.95.05", InTree: &out, Proprietary: &prop}},
		{PCIAddress: "0000:00:02.0", Identity: &report.Identity{Model: "UHD Graphics 630"}, Clocks: &report.GPUClocks{MinFreqMHz: 350, MaxFreqMHz: 1100, ActualFreqMHz: &act, Source: "i915"}},
		{PCIAddress: "0000:00:02.1", Identity: &report.Identity{Model: "Idle GPU"}, Clocks: &report.GPUClocks{MinFreqMHz: 350, MaxFreqMHz: 1100, ActualFreqMHz: &idle, Source: "i915"}},
		{PCIAddress: "0000:03:00.0", Identity: &report.Identity{Model: "Radeon"}, Clocks: &report.GPUClocks{MinFreqMHz: 300, MaxFreqMHz: 2100, Source: "amdgpu"}},
		{PCIAddress: "0000:05:00.0", Identity: &report.Identity{Model: "Arc"}, Firmware: report.UnknownFirmware("no video BIOS"),
			FirmwareComponents: []report.Firmware{{Name: "GuC", Version: "70.36.0", Source: "debugfs"}, {Name: "HuC", Status: report.FirmwareUnknown, Reason: "r"}}},
		{PCIAddress: "0000:06:00.0", Identity: &report.Identity{Model: "Both"}, Firmware: &report.Firmware{Version: "113-1", Source: "vbios"},
			FirmwareComponents: []report.Firmware{{Name: "smc", Version: "0x00415b00"}}},
		{PCIAddress: "0000:04:00.0", Identity: &report.Identity{Model: "Locked"}, Clocks: &report.GPUClocks{MinFreqMHz: 1000, MaxFreqMHz: 1000, Source: "amdgpu"}}}
	r.Bluetooth = []report.BluetoothController{{Name: "hci0", Identity: &report.Identity{Model: "AX211"}, Powered: &off}}
	r.Batteries = []report.Battery{{Name: "BAT0", Health: &report.Health{Status: report.StatusOK, LifeUsedPercent: &used, LifeRemainingPercent: &left,
		Estimate: &report.Estimate{What: "charge cycles until 80% of design capacity", Value: 420, Unit: "cycles", Method: "m"}}}}

	var buf bytes.Buffer
	if err := Write(&buf, r, "text"); err != nil {
		t.Fatal(err)
	}
	text := buf.String()
	for _, want := range []string{
		"Runs in    vm",
		"Codename   Sapphire Rapids (Golden Cove cores)",
		"2 sockets, 24 cores / 48 threads, 48 performance threads",
		"800–4600 MHz (intel_pstate built in)",
		"UHD Graphics 630, 350–1100 MHz (883 MHz at capture)",
		"Idle GPU, 350–1100 MHz (idle at capture)",
		"Radeon, 300–2100 MHz\n",
		"Arc, firmware GuC 70.36.0, HuC unknown\n",
		"Both, fw 113-1, firmware smc 0x00415b00\n",
		"Locked, 1000 MHz\n",
		"L1d 48 KiB×24, L2 2 MiB×24",
		"256 GiB installed, 250 GiB usable",
		"3 used of 16, max 4 TiB",
		"128 GiB DDR5 RDIMM 4400 MT/s (rated 4800) Samsung M321R8GA0BB0, made 2023-W12, SK Hynix chips, health FAILING",
		"128 GiB 4800 MT/s Samsung",
		"5600 MT/s",
		"WD 1 TiB via usb",
		"16 GiB VRAM",
		"nvidia out-of-tree, proprietary, 580.95.05",
		"AX211, off",
		"30% worn (70% life left)",
		"~420 charge cycles until 80% of design capacity",
		"- WARNING CPU",
		"- FAILING memory DIMM_A1: 1 uncorrectable",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Samsung chips") {
		t.Error("DRAM maker repeated when it's the module maker")
	}
	r.CPU.Codename, r.CPU.Microarchitecture = "Raptor Lake", ""
	buf.Reset()
	_ = Write(&buf, r, "text")
	if !strings.Contains(buf.String(), "Codename   Raptor Lake\n") {
		t.Errorf("codename alone:\n%s", buf.String())
	}
	if err := Write(&buf, r, "xml"); err == nil || !strings.Contains(err.Error(), `unknown format "xml"`) {
		t.Errorf("unknown format: %v", err)
	}
}

// Captures are read whether they're JSON or YAML, and garbage is refused.
func TestReadRefusesMalformedCaptures(t *testing.T) {
	for name, data := range map[string]string{"json": `{"schema_version": "one"}`, "yaml": "schema_version: [1\n", "yaml type": "- a\n- b\n"} {
		if _, err := Read([]byte(data)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// Slot usage: each slot from the firmware, empty ones named; without root
// it says the count needs --full instead of inferring it from usable RAM;
// a capture from before slot_usage keeps its old line.
func TestTextMemorySlots(t *testing.T) {
	text := func(r *report.Report) string {
		var buf bytes.Buffer
		if err := Write(&buf, r, "text"); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	yes, no := new(bool), new(bool)
	*yes = true
	r := sample()
	r.Privileged = true
	r.Memory.MaxCapacityBytes = 64 << 30
	r.Memory.SlotUsage = []report.MemorySlot{{Locator: "DIMM_A1", Populated: yes}, {Locator: "DIMM_A2", BankLocator: "ChannelA", Populated: no}, {Locator: "DIMM_B1", Populated: yes}, {Locator: "DIMM_B2", Populated: no}}
	if got := text(r); !strings.Contains(got, "Slots      2 used of 4; empty: DIMM_A2 ChannelA, DIMM_B2; max 64 GiB\n") {
		t.Errorf("slot usage:\n%s", got)
	}
	r.Memory.SlotUsage, r.Memory.MaxCapacityBytes = []report.MemorySlot{{Locator: "DIMM1", Populated: yes}}, 0
	if got := text(r); !strings.Contains(got, "Slots      1 used of 1\n") {
		t.Errorf("all used, no max:\n%s", got)
	}
	// The firmware's array and its slot list disagree, and a slot doesn't
	// say whether a module is in it: both are shown as the firmware said.
	r.Memory.Slots = 4
	r.Memory.SlotUsage = []report.MemorySlot{{Locator: "DIMM_A1"}, {Locator: "DIMM_B1", Populated: yes}}
	if got := text(r); !strings.Contains(got, "Slots      1 used of 2 listed; the firmware's array says 4 slots; not said: DIMM_A1\n") {
		t.Errorf("disagreeing counts:\n%s", got)
	}
	r.Memory.Slots = 0
	r.Memory.SlotUsage, r.Privileged = nil, false
	if got := text(r); !strings.Contains(got, "Slots      how many, and which are used, needs --full") {
		t.Errorf("no root:\n%s", got)
	}
	r.Privileged = true
	if got := text(r); strings.Contains(got, "Slots ") {
		t.Errorf("root without a table:\n%s", got)
	}
}

// Each part's mounting is one line: where it sits, and why when it's
// uncertain or unknown.
func TestTextMountings(t *testing.T) {
	r := sample()
	r.CPU.Packages = []report.CPUPackage{{Designation: "U3E1", Package: "Socket LGA1151", Mounting: "socket", Populated: true},
		{Designation: "CPU1", Package: "Socket LGA1151", Mounting: "socket"}, {Designation: "CPU2", Package: "Socket AM4", Populated: true}}
	r.Memory.Modules = []report.MemoryModule{{Locator: "DIMM1", Mounting: &report.Mounting{Kind: "slot", Slot: "DIMM1", Confidence: "high"}}}
	r.PCI = []report.PCIDevice{
		{Address: "0000:00:02.0", Identity: &report.Identity{Model: "UHD 630"}, Mounting: &report.Mounting{Kind: "onboard", Confidence: "high"}},
		{Address: "0000:01:00.0", Class: "Non-Volatile memory controller", Mounting: &report.Mounting{Kind: "slot", SlotType: "PCI Express Gen 3 x4", Confidence: "medium", Reason: "which slot is unknown"}},
		{Address: "0000:02:00.0", Identity: &report.Identity{Model: "AX200"}, Mounting: &report.Mounting{Kind: "slot", Slot: "Slot2", Confidence: "high"}},
		{Address: "0000:03:00.0", Identity: &report.Identity{Model: "CPU card"}, Mounting: &report.Mounting{Kind: "socket", Confidence: "high"}},
		{Address: "0000:04:00.0", Identity: &report.Identity{Model: "Odd"}, Mounting: &report.Mounting{Kind: "slot", Confidence: "high"}},
		{Address: "0000:05:00.0", Identity: &report.Identity{Model: "Wi-Fi"}, Mounting: &report.Mounting{Kind: "unknown", Reason: "needs --full"}},
		{Address: "0000:06:00.0", Identity: &report.Identity{Model: "Bridge"}},
	}
	r.Storage = append(r.Storage, report.Disk{Name: "mmcblk0", Mounting: &report.Mounting{Kind: "onboard", Confidence: "medium", Reason: "eMMC or a removable MMC card"}})
	var buf bytes.Buffer
	if err := Write(&buf, r, "text"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Soldered or removable",
		"U3E1       CPU: in a socket (Socket LGA1151)\n",
		"DIMM1      memory module: in slot \"DIMM1\"\n",
		"00:02.0    UHD 630: soldered on\n",
		"01:00.0    Non-Volatile memory controller: in a PCI Express Gen 3 x4 slot (medium confidence; which slot is unknown)\n",
		"02:00.0    AX200: in slot \"Slot2\"\n",
		"03:00.0    CPU card: in a socket\n",
		"04:00.0    Odd: in a slot\n",
		"05:00.0    Wi-Fi: unknown: needs --full\n",
		"mmcblk0    soldered on (medium confidence; eMMC or a removable MMC card)\n",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("text lacks %q:\n%s", want, buf.String())
		}
	}
	for _, absent := range []string{"06:00.0", "CPU1", "CPU2"} {
		if strings.Contains(buf.String(), absent) {
			t.Errorf("%s got a line (no mounting, or an empty socket)", absent)
		}
	}
}

// A YAML capture is untrusted (show reads files others made), so the
// parser's limits are part of hwspec's security (SECURITY.md › Parsing): an
// alias bomb and deep nesting are refused quickly, not expanded.
func TestYAMLBombsAreRefused(t *testing.T) {
	var laughs strings.Builder
	laughs.WriteString("tool: {name: hwspec}\nschema_version: 1\na0: &a0 [lol, lol, lol, lol, lol, lol, lol, lol, lol, lol]\n")
	for i := 1; i < 10; i++ {
		fmt.Fprintf(&laughs, "a%d: &a%d [", i, i)
		for j := range 10 {
			if j > 0 {
				laughs.WriteString(", ")
			}
			fmt.Fprintf(&laughs, "*a%d", i-1)
		}
		laughs.WriteString("]\n")
	}
	deep := "hostname: " + strings.Repeat("[", 20000) + strings.Repeat("]", 20000) + "\n"
	for doc, want := range map[string]string{laughs.String(): "excessive aliasing", deep: "exceeded max depth"} {
		start := time.Now()
		if _, err := Read([]byte(doc)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want an error with %q, got %v", want, err)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("refusing %q took %v", want, d)
		}
	}
}

// BenchmarkYAMLRecordedCapture times YAML output of a real machine's
// capture (go test -bench YAML ./internal/output).
func BenchmarkYAMLRecordedCapture(b *testing.B) {
	data, err := os.ReadFile("../collect/testdata/machines/hp-elitedesk-800-g5-mini/expected.json")
	if err != nil {
		b.Fatal(err)
	}
	r, err := Read(data)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if err := YAML(io.Discard, r); err != nil {
			b.Fatal(err)
		}
	}
}

// An unknown firmware version is shown as unknown, with the reason,
// wherever a version would be: never as an empty version.
func TestTextShowsUnknownFirmwareWithTheReason(t *testing.T) {
	r := sample()
	r.System.Firmware = report.UnknownFirmware("the DMI tables give no BIOS version")
	r.System.MEFirmware = report.UnknownFirmware("the kernel got no version from the Management Engine")
	r.CPU.Firmware = report.UnknownFirmware("the kernel doesn't report CPU microcode on aarch64")
	r.TPM = &report.TPM{SpecVersionMajor: 2, Firmware: report.UnknownFirmware("needs --full")}
	r.Bluetooth = []report.BluetoothController{{Name: "hci0", Firmware: report.UnknownFirmware("not read yet, see #43")}}
	r.GPUs = []report.GPU{{PCIAddress: "0000:00:02.0", Firmware: &report.Firmware{Status: report.FirmwareUnknown}}}
	var buf bytes.Buffer
	if err := Write(&buf, r, "text"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"Firmware   unknown (the DMI tables give no BIOS version)",
		"ME         Management Engine firmware unknown (the kernel got no version from the Management Engine)",
		"Microcode  unknown (the kernel doesn't report CPU microcode on aarch64)",
		"TPM        TPM 2 (TCG spec major version), firmware unknown (needs --full)",
		"firmware unknown (not read yet, see #43)",
		"firmware unknown\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "fw ,") || strings.Contains(out, "fw \n") {
		t.Errorf("an unknown version shown as empty:\n%s", out)
	}
}

// A capture without its time or OS fields says "unknown", not a zero date
// or empty commas (#143).
func TestTextUnknownTimeAndOS(t *testing.T) {
	r := sample()
	r.CapturedAt = time.Time{}
	r.OS.PrettyName, r.OS.Kernel, r.OS.Arch = "", "", ""
	var buf bytes.Buffer
	if err := Write(&buf, r, "text"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "captured unknown") || strings.Contains(out, "0001-01-01") || !strings.Contains(out, "unknown, kernel unknown (unknown, ") {
		t.Errorf("%s", out)
	}
}

// healthPaths lists where a *report.Health can sit in a report, by Go
// field path ("Storage[].Health").
func healthPaths(typ reflect.Type, path string, out *[]string) {
	switch typ.Kind() {
	case reflect.Pointer:
		if typ == reflect.TypeFor[*report.Health]() {
			*out = append(*out, path)
			return
		}
		healthPaths(typ.Elem(), path, out)
	case reflect.Slice, reflect.Array:
		healthPaths(typ.Elem(), path+"[]", out)
	case reflect.Map:
		healthPaths(typ.Elem(), path+"{}", out)
	case reflect.Struct:
		for f := range typ.Fields() {
			if f.IsExported() {
				healthPaths(f.Type, strings.TrimPrefix(path+"."+f.Name, "."), out)
			}
		}
	}
}

// failEveryHealth sets every *report.Health field reachable from v to
// failing with a reason naming its place, and returns the reasons by path.
func failEveryHealth(v reflect.Value, path string, reasons map[string][]string) {
	switch v.Kind() {
	case reflect.Pointer:
		if v.Type() == reflect.TypeFor[*report.Health]() {
			reason := fmt.Sprintf("guard-%s-%d", path, len(reasons[path]))
			v.Set(reflect.ValueOf(&report.Health{Status: report.StatusFailing, Reasons: []string{reason}}))
			reasons[path] = append(reasons[path], reason)
			return
		}
		if !v.IsNil() {
			failEveryHealth(v.Elem(), path, reasons)
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			failEveryHealth(v.Index(i), path+"[]", reasons)
		}
	case reflect.Struct:
		for i := range v.NumField() {
			if f := v.Type().Field(i); f.IsExported() {
				failEveryHealth(v.Field(i), strings.TrimPrefix(path+"."+f.Name, "."), reasons)
			}
		}
	}
}

// Every part with a Health reaches "Needs attention" when it fails
// (#149): the sample must hold one of each, and each failing reason must
// be listed. A new device type with a Health fails here until the sample
// has one and the text output watches it.
func TestNeedsAttentionCoversEveryHealth(t *testing.T) {
	var want []string
	healthPaths(reflect.TypeFor[report.Report](), "", &want)
	r := sample() // with one of each part that has a Health
	r.Memory.Modules = append(r.Memory.Modules, report.MemoryModule{Locator: "DIMM1"})
	r.Batteries = append(r.Batteries, report.Battery{Name: "BAT0"})
	reasons := map[string][]string{}
	failEveryHealth(reflect.ValueOf(r).Elem(), "", reasons)
	for _, p := range want {
		if len(reasons[p]) == 0 {
			t.Errorf("this test's report has no %s: add a part with one, so its health is checked", p)
		}
	}
	var buf bytes.Buffer
	if err := Write(&buf, r, "text"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	i := strings.Index(out, "Needs attention")
	if i < 0 {
		t.Fatalf("no Needs attention section:\n%s", out)
	}
	for p, rs := range reasons {
		for _, reason := range rs {
			if !strings.Contains(out[i:], ": "+reason+"\n") {
				t.Errorf("%s failing isn't under Needs attention (%s): call watch for it in text.go", p, reason)
			}
		}
	}
}

// A capture from a newer schema that doesn't decode says to update hwspec,
// not what the decoder tripped on (#145).
func TestANewerCaptureThatDoesntDecodeSaysUpdate(t *testing.T) {
	for _, doc := range []string{
		`{"tool":{"name":"hwspec"},"schema_version":2,"cpu":{"cores":"many"}}`,
		"tool: {name: hwspec}\nschema_version: 2\ncpu: {cores: many}\n",
	} {
		_, err := Read([]byte(doc))
		var newer *NewerSchemaError
		if !errors.As(err, &newer) || newer.Version != 2 || !strings.Contains(err.Error(), "update hwspec") {
			t.Errorf("Read(%q) = %v", doc, err)
		}
	}
	// The same broken field in the current schema is a plain decode error.
	if _, err := Read([]byte(`{"tool":{"name":"hwspec"},"schema_version":1,"cpu":{"cores":"many"}}`)); err == nil || errors.As(err, new(*NewerSchemaError)) {
		t.Errorf("schema 1: %v", err)
	}
}

// UnknownField names a field a newer build added, in JSON and YAML, and
// nothing for a capture this build fully knows.
func TestUnknownFieldNamesAFieldFromANewerBuild(t *testing.T) {
	var js bytes.Buffer
	if err := Write(&js, sample(), "json"); err != nil {
		t.Fatal(err)
	}
	if f := UnknownField(js.Bytes()); f != "" {
		t.Fatalf("a current capture has unknown field %q", f)
	}
	newer := bytes.Replace(js.Bytes(), []byte(`"cpu": {`), []byte(`"cpu": {"l3_topology": "x", `), 1)
	if f := UnknownField(newer); f != "l3_topology" {
		t.Errorf("JSON: %q", f)
	}
	if f := UnknownField([]byte("tool: {name: hwspec}\nschema_version: 1\nhealth_v2: {}\n")); f != "health_v2" {
		t.Errorf("YAML: %q", f)
	}
}

// The RTC line (#113, #268 round 1): dead is said as a dead cell; okay
// only as the driver's word, which can't show a dead cell on Intel; no
// RTC block, no line.
func TestTextRTC(t *testing.T) {
	for status, want := range map[string]string{
		report.RTCBattDead: "coin cell dead: the clock lost its valid-time bit (driver batt_status)",
		report.RTCBattOkay: "driver batt_status okay: not evidence of a good coin cell (Intel chipsets always report okay)",
		"":                 "",
	} {
		r := sample()
		if status != "" {
			r.RTC = &report.RTC{BattStatus: status}
		}
		var buf bytes.Buffer
		if err := Write(&buf, r, "text"); err != nil {
			t.Fatal(err)
		}
		if want != "" && !strings.Contains(buf.String(), want) || want == "" && strings.Contains(buf.String(), "batt_status") {
			t.Errorf("%q:\n%s", status, buf.String())
		}
	}
}

// A part of the CPU is said so, with the package when known (#137).
func TestMountingTextInCPU(t *testing.T) {
	for m, want := range map[*report.Mounting]string{
		{Kind: report.MountedInCPU, Package: "Socket LGA1151"}: "part of the CPU (Socket LGA1151): it changes only with the processor",
		{Kind: report.MountedInCPU}:                            "part of the CPU: it changes only with the processor",
	} {
		if got := mountingText(m); got != want {
			t.Errorf("%+v: %q", m, got)
		}
	}
}

// The power section (#105): supplies with their state, contract and
// rating, the rating's absence with its reason, and the limits by zone,
// PL names and windows, a disabled zone said so; no section without one.
func TestTextPower(t *testing.T) {
	on, off := true, false
	r := sample()
	r.Power = &report.Power{
		Supplies: []report.PowerSupply{
			{Name: "AC", Type: "mains", Online: &on},
			{Name: "ucsi-source-psy-USBC000:001", Type: "usb", Online: &on, Port: "port0", ContractMV: 20000, ContractMA: 3250, RatedMaxMW: 65000, RatingSource: report.RatingFromPDOs},
			{Name: "tcpm-source-psy-x", Type: "usb", Online: &off, RatedMaxMW: 60000, RatingSource: report.RatingFromInputLimit},
			{Name: "odd", Type: "mains"},
			{Name: "half", Type: "usb", Online: &on, ContractMV: 5000},
		},
		Limits: []report.PowerLimit{
			{Domain: "cpu-package", Zone: "package-0", Name: "long_term", LimitMW: 35000, TimeWindowUS: 27983872, Enabled: &on, Source: "intel-rapl"},
			{Domain: "platform", Zone: "psys2", Name: "long_term", LimitMW: 61000, TimeWindowUS: 7995392, Enabled: &off, Source: "intel-rapl"},
			{Domain: "cpu-package", Zone: "package-0", Name: "short_term", LimitMW: 69000, TimeWindowUS: 2440, Enabled: &on, Source: "intel-rapl"},
			{Domain: "platform", Zone: "psys", Name: "long_term", LimitMW: 61000, Enabled: &off, Source: "intel-rapl"},
			{Domain: "platform", Zone: "psys", Name: "short_term", LimitMW: 69000, Source: "intel-rapl"},
			{Domain: "cpu-package", Zone: "package-0", Name: "long_term", LimitMW: 15000, Source: "intel-rapl-mmio"},
			{Domain: "gpu", Zone: "amdgpu", Device: "0000:03:00.0", Name: "cap", LimitMW: 120000, Source: "amdgpu"},
			{Domain: "dram", Zone: "dram", Name: "long_term", LimitMW: 5500, Source: "intel-rapl"},
		},
	}
	var buf bytes.Buffer
	if err := Write(&buf, r, "text"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Power\n", "Supply     AC: powering the machine\n",
		"Supply     ucsi-source-psy-USBC000:001 (USB-C port0): powering the machine at 20 V × 3.25 A; rated 65 W (the charger's largest fixed PD offer)\n",
		"Supply     tcpm-source-psy-x: not powering the machine; rated 60 W (the driver's input power limit)\n", "Supply     odd\n", "Supply     half: powering the machine\n",
		"CPU        package-0: PL1 35 W (28 s), PL2 69 W\n", "Platform   psys: PL1 61 W (enable bit off), PL2 69 W\n", "CPU (MMIO) package-0: PL1 15 W\n", "Platform   psys2: PL1 61 W (8 s, enable bit off)\n",
		"GPU        amdgpu 0000:03:00.0: cap 120 W\n", "dram       dram: PL1 5.5 W\n"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("no %q in:\n%s", want, buf.String())
		}
	}
	if strings.Contains(buf.String(), "Rating") {
		t.Errorf("a rated supply, yet:\n%s", buf.String())
	}
	r.Power = &report.Power{Supplies: []report.PowerSupply{}, Limits: []report.PowerLimit{}, RatingUnknown: "no supply reports one"}
	buf.Reset()
	if err := Write(&buf, r, "text"); err != nil || !strings.Contains(buf.String(), "Rating     unknown: no supply reports one") {
		t.Errorf("unrated: %v\n%s", err, buf.String())
	}
	r.Power = nil
	buf.Reset()
	if err := Write(&buf, r, "text"); err != nil || strings.Contains(buf.String(), "Power\n") {
		t.Errorf("no power: %v\n%s", err, buf.String())
	}
}

// One line per USB-C port: whether it charges the machine, up to how
// much, and a connected charger's offer (#115).
func TestTextUSBC(t *testing.T) {
	r := sample()
	r.USBC = []report.USBCPort{
		{Name: "port0", Location: &report.PortLocation{Panel: "top", Horizontal: "left"}, PowerRoles: []string{"source"}},
		{Name: "port1", Location: &report.PortLocation{Panel: "left", Horizontal: "center"}, PowerRoles: []string{"source", "sink"}, CanCharge: true, MaxChargeW: 65,
			PartnerSourcePDOs: []report.PDO{{PowerMW: 45000}, {PowerMW: 15000}}},
		{Name: "port2", PowerRoles: []string{"sink"}, CanCharge: true},
		{Name: "port3", PowerRoles: []string{}},
	}
	var buf bytes.Buffer
	if err := Write(&buf, r, "text"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"USB-C charging", "top-left   power out only, can't charge",
		"left       can power or charge this machine, up to 65 W; the connected charger offers up to 45 W",
		"port2      can power or charge this machine; how much isn't in the capture", "port3      can't charge"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("no %q in:\n%s", want, buf.String())
		}
	}
	r.USBC = []report.USBCPort{}
	buf.Reset()
	if err := Write(&buf, r, "text"); err != nil || strings.Contains(buf.String(), "USB-C") {
		t.Errorf("no ports: %v\n%s", err, buf.String())
	}
}

// The display line names what its output offers (#112).
func TestTextDisplayModes(t *testing.T) {
	r := sample()
	r.Displays = []report.Display{{Connector: "card1-DP-3", NativeWidth: 3840, NativeHeight: 2160, NativeRefreshHz: 60, BestMode: "3840x2160", ModeCount: 42},
		{Connector: "card1-HDMI-A-1"}}
	var buf bytes.Buffer
	if err := Write(&buf, r, "text"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "3840×2160 @ 60 Hz, output offers up to 3840×2160 (42 modes), card1-DP-3") ||
		strings.Count(buf.String(), "output offers") != 1 {
		t.Errorf("text:\n%s", buf.String())
	}
}

// A Wi-Fi radio in the text output: generation, bands, TX×RX chains and
// streams (#111); a NIC without one says nothing of it.
func TestTextWiFiRadio(t *testing.T) {
	r := sample()
	r.Network = append(r.Network, report.NIC{Name: "wlan0", Type: "wireless", State: "up",
		Radio: &report.WiFiRadio{Generation: "Wi-Fi 6E", Bands: []string{"2.4 GHz", "5 GHz", "6 GHz"}, TXChains: 1, RXChains: 2, MaxSpatialStreams: 2, Source: "nl80211"}})
	var buf bytes.Buffer
	if err := Write(&buf, r, "text"); err != nil {
		t.Fatal(err)
	}
	if out := buf.String(); !strings.Contains(out, "wireless (Wi-Fi 6E, 2.4/5/6 GHz, 1×2, 2 streams), up") || strings.Contains(out, "ethernet (") {
		t.Errorf("text:\n%s", out)
	}
	if got := radioText(&report.WiFiRadio{Bands: []string{}}); got != "" {
		t.Errorf("an empty radio: %q", got)
	}
	if got := radioText(&report.WiFiRadio{Generation: "Wi-Fi 5", Bands: []string{"5 GHz"}, TXChains: 1, RXChains: 1, MaxSpatialStreams: 1}); got != "Wi-Fi 5, 5 GHz, 1×1, 1 stream" {
		t.Errorf("a 1×1 radio: %q", got)
	}
}
