package output

import (
	"bytes"
	"errors"
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
		System: report.System{Firmware: &report.Firmware{Version: "R21", Release: "27.0", Source: "dmi"}},
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
	for _, want := range []string{"Test OS", "32 GiB usable", "x: needs root"} {
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
