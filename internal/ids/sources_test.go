package ids

import (
	"path/filepath"
	"strings"
	"testing"
)

// A distribution's database is used when it's newer than the embedded one;
// an IEEE registry in its own format is read as such, and an empty or
// unusable file is skipped with the reason.
func TestDistributionDatabases(t *testing.T) {
	isolate(t)
	allowTinySources(t)
	systemEnabled = true
	dir := t.TempDir()
	oui := write(t, filepath.Join(dir, "oui.txt"), "OUI/MA-L\tOrganization\n# Date: 2099-01-01\n00-1B-DC   (hex)\t\tVencer\n001BDC     (base 16)\t\tVencer Co., Ltd.\n12345      (base 16)\t\tToo short\n")
	empty := write(t, filepath.Join(dir, "pnp.ids"), "# nothing here\nno tab on this line\n")
	s := specs[OUI]
	s.system = []string{filepath.Join(dir, "missing.txt"), oui}
	specs[OUI] = s
	p := specs[PNP]
	p.system = []string{empty}
	specs[PNP] = p
	Reset()

	layers := Layers(OUI)
	var used bool
	for _, l := range layers {
		if l.Source == oui && l.Err == "" && l.Entries == 1 {
			used = true
		}
	}
	if !used {
		t.Errorf("IEEE registry not read: %+v", layers)
	}
	var skipped bool
	for _, l := range Layers(PNP) {
		if l.Source == empty && strings.Contains(l.Err, "no entries") {
			skipped = true
		}
	}
	if !skipped {
		t.Errorf("empty database not reported: %+v", Layers(PNP))
	}
}

// Without a home directory there is nowhere to keep synced databases or
// overrides.
func TestNoHomeMeansNoSyncedDatabases(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "")
	if got := xdgPath("XDG_DATA_HOME", ".local/share", "hwspec/ids"); got != "" {
		t.Errorf("xdgPath = %q", got)
	}
}

// After a sync, a report names the synced copy by role, not by a path in
// the user's home directory.
func TestLoadedNamesSyncedDatabasesByRole(t *testing.T) {
	isolate(t)
	syncedDir = filepath.Join(t.TempDir(), "ids")
	b := newBundle(t)
	if _, err := b.update(UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	PCIVendor("8086")
	if got := Loaded()[PCI]; !strings.HasPrefix(got, "synced (") {
		t.Errorf("Loaded()[pci] = %q", got)
	}
}

// A CPU override names both the codename and the microarchitecture, as
// "Codename | Microarchitecture".
func TestCPUOverridesNameCodenameAndCores(t *testing.T) {
	isolate(t)
	overridesPath = write(t, filepath.Join(t.TempDir(), "overrides.ids"), "cpu intel:6:9e = Coffee Lake Refresh | Skylake\ncpu = nothing\n")
	Reset()
	if c, u := CPUCodename("GenuineIntel", 6, 0x9e, 0); c != "Coffee Lake Refresh" || u != "Skylake" {
		t.Errorf("CPUCodename = %q, %q", c, u)
	}
	if err := OverridesError(); err == nil {
		t.Error("a malformed line wasn't reported")
	}
}

// Memory makers come as JEDEC codes in several shapes: bank continuation
// bytes, a single ID byte, a zero-padded pair.
func TestJEDECCodeShapes(t *testing.T) {
	for raw, want := range map[string]bool{"7F7F9E": true, "CE": true, "CE00": true, strings.Repeat("7F", 32) + "01": false, "00": false, "ZZ": false} {
		if got := len(jedecCandidates(raw)) > 0; got != want {
			t.Errorf("jedecCandidates(%q) found %v, want %v", raw, got, want)
		}
	}
	if _, ok := ouiPrefix("zz:zz:zz:00:00:00"); ok {
		t.Error("a non-hex MAC prefix was accepted")
	}
}

// What the manifest checks refuse: another format, an unknown database,
// a database that doesn't parse.
func TestManifestAndValidateRefusals(t *testing.T) {
	if _, err := ParseManifest([]byte(`{"format": 99, "generated_at": "2026-01-01T00:00:00Z", "files": {}}`)); err == nil || !strings.Contains(err.Error(), "update hwspec") {
		t.Errorf("format 99: %v", err)
	}
	if _, err := Validate(Kind("floppy"), nil); err == nil {
		t.Error("an unknown database validated")
	}
}

// A distro or synced copy dated newer than the embedded one but much too
// small to be complete (truncated, trimmed, or only touched) doesn't
// replace it: the reason shows in its layer, and names come from the
// embedded copy.
func TestTruncatedNewerSourceDoesntReplaceTheEmbeddedOne(t *testing.T) {
	isolate(t)
	systemEnabled = true
	sys := write(t, filepath.Join(t.TempDir(), "pci.ids"), "# Version: 2999.01.01\n10de  NVIDIA Corporation\n8086  Truncated Intel\n")
	s := specs[PCI]
	s.system = []string{sys}
	specs[PCI] = s
	Reset()
	if got := PCIVendor("1002"); got == "" {
		t.Error("AMD lost its name to a 2-entry distro file")
	}
	if got := PCIVendor("8086"); got == "Truncated Intel" {
		t.Error("the truncated distro file was used")
	}
	layers := Layers(PCI)
	if len(layers) != 2 || layers[0].Source != sys || !strings.Contains(layers[0].Err, "only 2 entries (expected at least 20000)") || layers[1].Source != "embedded" || layers[1].Err != "" {
		t.Errorf("layers = %+v", layers)
	}
}
