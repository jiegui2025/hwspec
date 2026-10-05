package collect

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jiegui2025/hwspec/internal/report"
)

var update = flag.Bool("update", false, "rewrite the machines' expected.json")

// TestRecordedMachines captures each machine recorded under
// testdata/machines (with tools/snapshot) and checks the result against the
// feature rules every capture must meet, the facts known about that
// machine, and its expected.json. After an intended change in output:
//
//	go test ./internal/collect -run RecordedMachines -update
func TestRecordedMachines(t *testing.T) {
	dirs, _ := filepath.Glob("testdata/machines/*")
	if len(dirs) == 0 {
		t.Fatal("no recorded machines")
	}
	for _, dir := range dirs {
		name := filepath.Base(dir)
		t.Run(name, func(t *testing.T) {
			r := captureMachine(t, dir)
			checkFeatureRules(t, r)
			if facts, ok := machineFacts[name]; ok {
				facts(t, r)
			} else {
				t.Errorf("no facts listed for %s in machineFacts", name)
			}
			compareExpected(t, filepath.Join(dir, "expected.json"), r)
		})
	}
}

// captureMachine captures a recorded machine with a fixed time.
func captureMachine(t *testing.T, dir string) *report.Report {
	t.Helper()
	r, err := CollectRecorded(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	r.CapturedAt = time.Time{}
	r.Tool.IDDatabases = nil // where names came from, not what they are
	return r
}

// checkFeatureRules asserts what ADR 0008 promises of every capture.
func checkFeatureRules(t *testing.T, r *report.Report) {
	t.Helper()
	statuses := map[string]bool{report.StatusOK: true, report.StatusWarning: true, report.StatusFailing: true, report.StatusUnknown: true}
	walkBlocks(r, func(where string, id *report.Identity, fw *report.Firmware, d *report.Driver, h *report.Health) {
		if id != nil && id.Empty() {
			t.Errorf("%s: empty identity block (blocks appear only when something was read)", where)
		}
		if fw != nil && (fw.Version == "" || fw.Source == "") {
			t.Errorf("%s: firmware without version or source: %+v", where, fw)
		}
		if d != nil {
			if d.Name == "" {
				t.Errorf("%s: driver without a name", where)
			}
			if d.Builtin == (d.Module != "") {
				t.Errorf("%s: a driver is either built in or names its module: %+v", where, d)
			}
		}
		if h != nil {
			if !statuses[h.Status] {
				t.Errorf("%s: health status %q", where, h.Status)
			}
			if (h.Status == report.StatusWarning || h.Status == report.StatusFailing) && len(h.Reasons) == 0 {
				t.Errorf("%s: %s without a reason", where, h.Status)
			}
			if h.LifeUsedPercent != nil && h.LifeRemainingPercent != nil && *h.LifeUsedPercent+*h.LifeRemainingPercent != 100 {
				t.Errorf("%s: life used %v + remaining %v ≠ 100", where, *h.LifeUsedPercent, *h.LifeRemainingPercent)
			}
			if h.Estimate != nil && h.Estimate.Method == "" {
				t.Errorf("%s: estimate without a method", where)
			}
		}
	})
	// Collected strings are already clean.
	before, _ := json.Marshal(r)
	r.Sanitize()
	if after, _ := json.Marshal(r); !bytes.Equal(before, after) {
		t.Error("the capture holds strings Sanitize changes")
	}
}

// walkBlocks visits the identity, firmware, driver and health blocks of
// every device.
func walkBlocks(r *report.Report, fn func(where string, id *report.Identity, fw *report.Firmware, d *report.Driver, h *report.Health)) {
	fn("system", r.System.Identity, r.System.Firmware, nil, nil)
	fn("board", r.Board.Identity, nil, nil, nil)
	fn("cpu", r.CPU.Identity, r.CPU.Firmware, r.CPU.Driver, r.CPU.Health)
	for _, m := range r.Memory.Modules {
		fn("memory "+m.Locator, m.Identity, nil, nil, m.Health)
	}
	for _, d := range r.Storage {
		fn("disk "+d.Name, d.Identity, d.Firmware, d.Driver, d.Health)
	}
	for _, g := range r.GPUs {
		fn("gpu "+g.PCIAddress, g.Identity, g.Firmware, g.Driver, nil)
	}
	for _, d := range r.Displays {
		fn("display "+d.Connector, d.Identity, nil, nil, nil)
	}
	for _, n := range r.Network {
		fn("nic "+n.Name, n.Identity, n.Firmware, n.Driver, n.Health)
	}
	for _, b := range r.Bluetooth {
		fn("bluetooth "+b.Name, b.Identity, nil, b.Driver, nil)
	}
	for _, a := range r.Audio {
		fn("audio "+a.Name, nil, nil, a.Driver, nil)
		for _, c := range a.Codecs {
			fn("codec", c.Identity, nil, nil, nil)
		}
	}
	for _, b := range r.Batteries {
		fn("battery "+b.Name, b.Identity, nil, nil, b.Health)
	}
	for _, p := range r.PCI {
		fn("pci "+p.Address, p.Identity, nil, p.Driver, nil)
	}
	for _, u := range r.USB {
		fn("usb "+u.Path, u.Identity, u.Firmware, nil, nil)
		for _, d := range u.Drivers {
			fn("usb "+u.Path+" interface", nil, nil, &d, nil)
		}
	}
}

func compareExpected(t *testing.T, path string, r *report.Report) {
	t.Helper()
	got, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if !bytes.Equal(got, want) {
		gl, wl := strings.Split(string(got), "\n"), strings.Split(string(want), "\n")
		for i := 0; i < len(gl) && i < len(wl); i++ {
			if gl[i] != wl[i] {
				t.Fatalf("capture differs from %s at line %d:\n got: %s\nwant: %s\n(run with -update after an intended change)", path, i+1, gl[i], wl[i])
			}
		}
		t.Fatalf("capture differs from %s in length (%d vs %d lines)", path, len(gl), len(wl))
	}
}

// Git can't store empty directories, so a recording lists them in
// machine.json; one it forgot would vanish on a fresh clone.
func TestRecordingsListTheirEmptyDirectories(t *testing.T) {
	dirs, _ := filepath.Glob("testdata/machines/*")
	for _, dir := range dirs {
		m, err := readMachine(filepath.Join(dir, "machine.json"))
		if err != nil {
			t.Fatal(err)
		}
		listed := map[string]bool{}
		for _, d := range m.EmptyDirs {
			listed[d] = true
		}
		root := filepath.Join(dir, "root")
		filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return err
			}
			if entries, _ := os.ReadDir(path); len(entries) == 0 && !listed[strings.TrimPrefix(path, root)] {
				t.Errorf("%s: empty directory %s isn't in machine.json", filepath.Base(dir), strings.TrimPrefix(path, root))
			}
			return nil
		})
	}
}

var (
	uuidPattern = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	// GUIDs that name a standard, not a machine.
	standardGUIDs = map[string]bool{
		"8be4df61-93ca-11d2-aa0d-00e098032b8c": true, // EFI global variables
	}
)

// macText finds MAC addresses with any of the usual separators, and
// interface names built from one (enx001122334455).
var macText = regexp.MustCompile(`(?im)\b[0-9a-f]{2}(?::[0-9a-f]{2}){5}\b|\b[0-9a-f]{2}(?:-[0-9a-f]{2}){5}\b|\b[0-9a-f]{2}(?:_[0-9a-f]{2}){5}\b|\b(?:enx|wlx|wwx)[0-9a-f]{12}\b|^[A-Z0-9_]+=[0-9a-f]{12}$`)

// A committed recording must hold no identifier. This checks the files as
// committed, every one in the machine's directory, independently of the
// scrubbing rules in tools/snapshot.
func TestRecordingsHoldNoIdentifiers(t *testing.T) {
	dirs, _ := filepath.Glob("testdata/machines/*")
	// The user and host name checks only catch something on the machine
	// that made the recording; elsewhere they find nothing, by design.
	var people []*regexp.Regexp
	if u := os.Getenv("USER"); u != "" && u != "root" {
		people = append(people, regexp.MustCompile(`(?i)\b`+regexp.QuoteMeta(u)+`\b`))
	}
	if h, err := os.Hostname(); err == nil && !defaultHostname(h, dirs) {
		people = append(people, regexp.MustCompile(`(?i)\b`+regexp.QuoteMeta(h)+`\b`))
	}
	serialName := regexp.MustCompile(`^(serial|product_serial|product_uuid|board_serial|chassis_serial|board_asset_tag|chassis_asset_tag|serial_number|wwid|eui|nguid|uuid|subsysnqn|uniq)$`)
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel := strings.TrimPrefix(path, dir)
			for _, re := range people {
				if re.MatchString(rel) {
					t.Errorf("%s: path names this machine or its user", rel)
				}
			}
			if !d.Type().IsRegular() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			name := filepath.Base(rel)
			switch {
			case name == "edid":
				if len(data) >= 128 && (binary.LittleEndian.Uint32(data[12:16]) != 0 || edidSerialText(data) != "" && edidSerialText(data) != "REDACTED") {
					t.Errorf("%s holds a monitor serial", rel)
				}
				return nil
			case name == "eeprom":
				for _, off := range []int{325, 517} { // DDR4, DDR5 module serial
					if len(data) >= off+4 && (off == 325 && data[2] == 0x0C || off == 517 && data[2] == 0x12) && string(data[off:off+4]) != "\x12\x34\x56\x78" {
						t.Errorf("%s holds a module serial", rel)
					}
				}
				return nil
			case serialName.MatchString(name):
				if v := strings.TrimSpace(string(data)); v != "" && v != "REDACTED" {
					t.Errorf("%s holds %q", rel, v)
				}
			case name == "mounts" || name == "mountinfo":
				for _, place := range []string{" /home/", " /run/user", " /media", " /mnt", "luks-", "/dev/mapper/", "subvol="} {
					if strings.Contains(string(data), place) {
						t.Errorf("%s mentions %q", rel, strings.TrimSpace(place))
					}
				}
			}
			text := string(data)
			for _, m := range macText.FindAllString(text, -1) {
				hex := strings.ToLower(strings.NewReplacer(":", "", "-", "", "_", "").Replace(m))
				hex = hex[len(hex)-12:]  // without an interface-name prefix
				if hex[6:10] != "0000" { // placeholders: maker prefix (or 02:00:00), 00:00, a number
					t.Errorf("%s holds MAC %s", rel, m)
				}
			}
			for _, u := range uuidPattern.FindAllString(text, -1) {
				if !standardGUIDs[strings.ToLower(u)] {
					t.Errorf("%s holds UUID %s", rel, u)
				}
			}
			for _, re := range people {
				if re.MatchString(text) {
					t.Errorf("%s names this machine or its user", rel)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// edidSerialText is an EDID's serial-number text descriptor (0xFF).
func edidSerialText(b []byte) string {
	for _, off := range []int{54, 72, 90, 108} {
		if d := b[off : off+18]; d[0] == 0 && d[1] == 0 && d[3] == 0xFF {
			return strings.TrimSpace(strings.TrimRight(string(d[5:]), "\n "))
		}
	}
	return ""
}

// defaultHostname tells a hostname that isn't this machine's own: empty,
// localhost, or a distribution's default (cachyos, fedora, ...), which a
// recording's os-release legitimately names.
func defaultHostname(h string, machines []string) bool {
	h = strings.ToLower(h)
	switch h {
	case "", "localhost", "archlinux", "cachyos", "fedora", "ubuntu", "debian", "nixos", "linux", "mx", "linuxmint":
		return true
	}
	for _, dir := range machines {
		for _, f := range []string{"etc/os-release", "usr/lib/os-release"} {
			data, _ := os.ReadFile(filepath.Join(dir, "root", f))
			if regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(h) + `\b`).Match(data) {
				return true
			}
		}
	}
	return false
}
