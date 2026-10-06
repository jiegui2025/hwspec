package ids

import (
	"bytes"
	"compress/gzip"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// isolate makes lookups use only the embedded data (as on NixOS, which has
// no hwdata path), plus whatever the test sets up, and restores afterwards.
func isolate(t *testing.T) {
	t.Helper()
	oldSys, oldSynced, oldOv := systemEnabled, syncedDir, overridesPath
	oldSpecs := map[Kind]spec{}
	maps.Copy(oldSpecs, specs)
	systemEnabled, syncedDir, overridesPath = false, "", ""
	Reset()
	t.Cleanup(func() {
		systemEnabled, syncedDir, overridesPath = oldSys, oldSynced, oldOv
		specs = oldSpecs
		Reset()
	})
}

func TestEmbeddedLookups(t *testing.T) {
	isolate(t)
	cases := []struct {
		name, got, want string
	}{
		{"pci vendor", PCIVendor("8086"), "Intel Corporation"},
		{"pci vendor 0x", PCIVendor("0x10DE"), "NVIDIA Corporation"},
		{"pci class", PCIClass("030000"), "VGA compatible controller"},
		{"pci subclass fallback", PCIClass("0380ff"), "Display controller"},
		{"pci class only", PCIClass("02"), "Network controller"},
		{"usb vendor", USBVendor("046d"), "Logitech, Inc."},
		{"usb class", USBClass("03"), "Human Interface Device"},
		{"pnp", PNPVendor("del"), "Dell Inc."},
		{"amdgpu", AMDGPUName("1114", "C2"), "AMD Radeon 860M Graphics"},
		{"oui", MACVendor("04:0e:3c:12:34:56"), "HP Inc."},
		{"bluetooth", BluetoothCompany(2), "Intel Corp."},
		{"cpu coffee lake", cpuName("GenuineIntel", 6, 0x9e, 10), "Coffee Lake|Skylake"},
		{"cpu kaby lake", cpuName("GenuineIntel", 6, 0x9e, 9), "Kaby Lake|Skylake"},
		{"cpu unknown stepping", cpuName("GenuineIntel", 6, 0x9e, 99), "Kaby Lake|Skylake"},
		{"cpu alder lake", cpuName("GenuineIntel", 6, 0x97, 2), "Alder Lake|Golden Cove / Gracemont"},
		{"cpu raphael", cpuName("AuthenticAMD", 0x19, 0x61, 2), "Raphael|Zen 4"},
		{"cpu zen range", cpuName("AuthenticAMD", 0x19, 0x62, 0), "|Zen 4"},
		{"cpu other vendor", cpuName("HygonGenuine", 0x18, 0, 0), "|"},
		{"oui local", MACVendor("02:0e:3c:12:34:56"), ""},
		{"oui garbage", MACVendor("zz"), ""},
		{"missing", PCIDevice("8086", "zzzz"), ""},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
	if PCIDevice("8086", "3e92") == "" {
		t.Error("PCIDevice(8086, 3e92) is empty")
	}
	for k, src := range Loaded() {
		if !strings.HasPrefix(src, "embedded") || strings.Contains(src, "+") {
			t.Errorf("%s loaded from %q, want embedded only", k, src)
		}
	}
}

func TestMemoryManufacturer(t *testing.T) {
	isolate(t)
	cases := map[string]string{
		"80CE":               "Samsung",
		"80AD":               "SK Hynix (former Hyundai Electronics)",
		"802C":               "Micron Technology",
		"2C00":               "Micron Technology",
		"CE00000000000000":   "Samsung",
		"00CE000000000000":   "Samsung",
		"859B":               "Crucial Technology",
		"0x859B":             "Crucial Technology",
		"7F7F7F7F7F9B":       "Crucial Technology",
		"Unknown - [0xF785]": "Avant Technology",
		"0198":               "Kingston",
		"04CB":               "A-DATA Technology",
	}
	for raw, want := range cases {
		got, _, ok := MemoryManufacturer(raw)
		if !ok || got != want {
			t.Errorf("MemoryManufacturer(%q) = %q, %v; want %q", raw, got, ok, want)
		}
	}
	for _, raw := range []string{"Samsung", "Micron Technology", "", "Unknown", "1234567", "12345678"} {
		if got, _, ok := MemoryManufacturer(raw); ok {
			t.Errorf("MemoryManufacturer(%q) = %q, want no match", raw, got)
		}
	}
}

func write(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOverrides(t *testing.T) {
	isolate(t)
	overridesPath = write(t, filepath.Join(t.TempDir(), "overrides.ids"), `# test
pci 8086 = Intel (override)
pci 8086:3e92 = My iGPU
pci class 0300 = Graphics
usb 046d:c52b = Receiver
pnp DEL = Dell
oui 04-0E-3C = Mine
jedec F785 = Avant (override)
amdgpu 1114:C2 = Radeon
this line is wrong
pci = missing key
`)
	checks := map[string][2]string{
		"pci vendor":  {PCIVendor("8086"), "Intel (override)"},
		"pci device":  {PCIDevice("8086", "3e92"), "My iGPU"},
		"pci class":   {PCIClass("030000"), "Graphics"},
		"usb":         {USBProduct("046d", "c52b"), "Receiver"},
		"pnp":         {PNPVendor("DEL"), "Dell"},
		"oui":         {MACVendor("04:0e:3c:00:00:01"), "Mine"},
		"amdgpu":      {AMDGPUName("1114", "c2"), "Radeon"},
		"other pci":   {PCIVendor("10de"), "NVIDIA Corporation"},
		"other jedec": {first(MemoryManufacturer("80CE")), "Samsung"},
		"jedec":       {first(MemoryManufacturer("Unknown - [0xF785]")), "Avant (override)"},
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s = %q, want %q", name, c[0], c[1])
		}
	}
	layers := Layers(PCI)
	last := layers[len(layers)-1]
	if last.Source != overridesPath || last.Entries != 3 || last.Err != "" {
		t.Errorf("last PCI layer = %+v, want overrides with 3 entries", last)
	}
	if err := OverridesError(); err == nil || !strings.Contains(err.Error(), "line 10") || !strings.Contains(err.Error(), "line 11") {
		t.Errorf("bad lines not reported: %v", err)
	}
	// Databases the file has no valid lines for get no overrides layer.
	for _, l := range Layers(BT) {
		if l.Source == overridesPath {
			t.Errorf("bluetooth has an overrides layer without entries: %+v", l)
		}
	}
	if got := Loaded()[PCI]; !strings.HasSuffix(got, " + overrides") {
		t.Errorf("Loaded()[pci] = %q, want it to end with + overrides", got)
	}
}

func first(name, _ string, _ bool) string { return name }

func cpuName(vendor string, family, model, stepping int) string {
	c, u := CPUCodename(vendor, family, model, stepping)
	return c + "|" + u
}

func TestNewestSourceWins(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	systemEnabled = true
	sys := filepath.Join(dir, "sys-pci.ids")
	s := specs[PCI]
	s.system = []string{sys}
	specs[PCI] = s

	// An older distro file is ignored in favour of the newer embedded one.
	write(t, sys, "#\tVersion: 2000.01.01\n8086  Old Intel\n")
	Reset()
	if got := PCIVendor("8086"); got != "Intel Corporation" {
		t.Errorf("older system file won: %q", got)
	}
	if l := Layers(PCI); len(l) != 1 || l[0].Source != "embedded" {
		t.Errorf("layers = %+v, want embedded only", l)
	}

	// A newer one (dated today, so never older than the embedded copy)
	// replaces it.
	today := time.Now().UTC().Format("2006.01.02")
	write(t, sys, "#\tVersion: "+today+"\n8086  New Intel\n")
	Reset()
	if got := PCIVendor("8086"); got != "New Intel" {
		t.Errorf("newer system file lost: %q", got)
	}

	// A date in the future is impossible and is ignored (the file is then
	// dated by its modification time), so it can't pin itself as newest.
	write(t, sys, "#\tVersion: 2999.01.01\n8086  Future Intel\n")
	past := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(sys, past, past); err != nil {
		t.Fatal(err)
	}
	Reset()
	if got := PCIVendor("8086"); got != "Intel Corporation" {
		t.Errorf("future-dated system file won: %q", got)
	}

	// A distro file without a date header is dated by its mtime.
	write(t, sys, "8086  Undated Intel\n")
	old := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(sys, old, old); err != nil {
		t.Fatal(err)
	}
	Reset()
	if got := PCIVendor("8086"); got != "Intel Corporation" {
		t.Errorf("old undated system file won: %q", got)
	}

	// A synced copy is dated by its manifest; a stale one loses to a newer
	// distro file, a fresh one wins.
	write(t, sys, "#\tVersion: "+today+"\n8086  Distro Intel\n")
	syncedDir = filepath.Join(dir, "synced")
	writeGz(t, filepath.Join(syncedDir, "pci.ids.gz"), "8086  Synced Intel\n")
	for _, c := range []struct{ date, want string }{{"2000-01-01", "Distro Intel"}, {"2999-01-01", "Synced Intel"}} {
		date, want := c.date, c.want
		write(t, filepath.Join(syncedDir, "manifest.json"),
			`{"format":1,"generated_at":"2026-01-01T00:00:00Z","files":{"pci.ids.gz":{"date":"`+date+`"}}}`)
		Reset()
		if got := PCIVendor("8086"); got != want {
			t.Errorf("synced dated %s: got %q, want %q", date, got, want)
		}
	}

	// An unreadable synced manifest means the synced files can't be dated,
	// so they aren't used, and the reason is reported.
	write(t, filepath.Join(syncedDir, "manifest.json"), "{broken")
	Reset()
	if got := PCIVendor("8086"); got != "Distro Intel" {
		t.Errorf("synced file used without a manifest: %q", got)
	}
	if l := Layers(PCI); l[0].Err == "" || !strings.Contains(l[0].Err, "manifest") {
		t.Errorf("unreadable manifest not reported: %+v", l)
	}
	if _, err := SyncedAt(); err == nil {
		t.Error("SyncedAt hides an unreadable manifest")
	}
	write(t, filepath.Join(syncedDir, "manifest.json"),
		`{"format":1,"generated_at":"2026-01-01T00:00:00Z","files":{"pci.ids.gz":{"date":"2999-01-01"}}}`)

	// An unreadable newest source falls back to the next newest.
	write(t, filepath.Join(syncedDir, "pci.ids.gz"), "not gzip")
	Reset()
	if got := PCIVendor("8086"); got != "Distro Intel" {
		t.Errorf("no fallback from a broken synced file: %q", got)
	}
	if l := Layers(PCI); len(l) != 2 || l[0].Err == "" {
		t.Errorf("layers = %+v, want the failed synced file then the distro file", l)
	}
}

func writeGz(t *testing.T, path, content string) {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write([]byte(content))
	zw.Close()
	write(t, path, buf.String())
}

func TestLookup(t *testing.T) {
	isolate(t)
	cases := []struct {
		kind      Kind
		id, want  string
		wantError bool
	}{
		{PCI, "8086", "Intel Corporation", false},
		{PCI, "class 0300", "VGA compatible controller", false},
		{USB, "046d", "Logitech, Inc.", false},
		{JEDEC, "F785", "Avant Technology", false},
		{JEDEC, "6:77", "Avant Technology", false},
		{OUI, "04:0E:3C", "HP Inc.", false},
		{AMDGPU, "1114:c2", "AMD Radeon 860M Graphics", false},
		{JEDEC, "Samsung", "", true},
		{"nope", "1", "", true},
	}
	for _, c := range cases {
		got, _, err := Lookup(c.kind, c.id)
		if (err != nil) != c.wantError || got != c.want {
			t.Errorf("Lookup(%s, %q) = %q, %v; want %q (error %v)", c.kind, c.id, got, err, c.want, c.wantError)
		}
	}
}

// A long-running program (or a test) that changes $XDG_* picks up the new
// synced and overrides locations.
func TestUseEnvironmentFollowsXDG(t *testing.T) {
	oldSynced, oldOv := syncedDir, overridesPath
	t.Cleanup(func() { syncedDir, overridesPath = oldSynced, oldOv; Reset() })
	data, config := t.TempDir(), t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("XDG_CONFIG_HOME", config)
	UseEnvironment()
	if SyncedDir() != filepath.Join(data, "hwspec/ids") || OverridesPath() != filepath.Join(config, "hwspec/overrides.ids") {
		t.Errorf("synced %s, overrides %s", SyncedDir(), OverridesPath())
	}
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", data)
	UseEnvironment()
	if SyncedDir() != filepath.Join(data, ".local/share/hwspec/ids") {
		t.Errorf("without XDG_DATA_HOME: %s", SyncedDir())
	}
}

// With only the embedded databases, names don't depend on the machine:
// distribution copies, synced databases and overrides are all ignored.
func TestUseEmbeddedOnlyIgnoresTheMachinesSources(t *testing.T) {
	oldSys, oldSynced, oldOv := systemEnabled, syncedDir, overridesPath
	t.Cleanup(func() { systemEnabled, syncedDir, overridesPath = oldSys, oldSynced, oldOv; Reset() })
	overridesPath = write(t, filepath.Join(t.TempDir(), "overrides.ids"), "pci 8086 = Mine\n")
	Reset()
	if PCIVendor("8086") != "Mine" {
		t.Fatal("override not applied before")
	}
	UseEmbeddedOnly()
	for _, k := range Kinds {
		for _, l := range Layers(k) {
			if l.Source != "embedded" {
				t.Errorf("%s uses %s", k, l.Source)
			}
		}
	}
	if got := PCIVendor("8086"); got != "Intel Corporation" {
		t.Errorf("PCIVendor = %q", got)
	}
	UseSystemDatabases(true)
	if !systemEnabled {
		t.Error("system databases not re-enabled")
	}
	// Without a synced directory nothing is read from the current one.
	t.Chdir(t.TempDir())
	write(t, "manifest.json", "{broken")
	UseEmbeddedOnly()
	if at, err := SyncedAt(); err != nil || !at.IsZero() {
		t.Errorf("SyncedAt = %v, %v", at, err)
	}
}
