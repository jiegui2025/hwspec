package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jiegui2025/hwspec/internal/advisor"
	"github.com/jiegui2025/hwspec/internal/fwindex"
)

// firmwareIndex caches a linux-firmware WHENCE at tag 20260916 in which
// the AX200's iwlwifi-cc-a0-77.ucode has the given Version, as `hwspec
// firmware update` leaves it in $XDG_CACHE_HOME.
func firmwareIndex(t *testing.T, version string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("--------------------------------------------------------------------------\n\nDriver: iwlwifi - Intel Wireless Wifi\n\n")
	b.WriteString("File: intel/iwlwifi/iwlwifi-cc-a0-77.ucode\nLink: iwlwifi-cc-a0-77.ucode -> intel/iwlwifi/iwlwifi-cc-a0-77.ucode\nVersion: " + version + "\n\n")
	b.WriteString("--------------------------------------------------------------------------\n\nDriver: filler\n\n")
	for i := range 1000 {
		fmt.Fprintf(&b, "File: filler/%d.bin\n", i)
	}
	dir := fwindex.CacheDir()
	must(t, os.MkdirAll(dir, 0o700))
	whence := []byte(b.String())
	must(t, os.WriteFile(filepath.Join(dir, fwindex.WhenceName), whence, 0o644))
	sum := sha256.Sum256(whence)
	m := fwindex.Manifest{Format: 1, LinuxFirmware: &fwindex.Source{
		URL: fwindex.LinuxFirmwareBase + "plain/WHENCE?id=ab23307cfe7f9366c819025ca3e4778299bc2db2", FetchedAt: time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC),
		Tag: "20260916", Commit: "ab23307cfe7f9366c819025ca3e4778299bc2db2", Files: map[string]string{fwindex.WhenceName: hex.EncodeToString(sum[:])},
	}}
	js, err := json.Marshal(m)
	must(t, err)
	must(t, os.WriteFile(filepath.Join(dir, "manifest.json"), js, 0o644))
}

// #225's acceptance on the reference machine: its AX200 against tag
// 20260916's WHENCE matches; against master's it differs, naming master's
// build; with no index, one warning names `hwspec firmware update`; an
// index that isn't the one installed says why.
func TestAdviseComparesFirmwareWithLinuxFirmware(t *testing.T) {
	setupAdvise(t)
	stdout := out(t, "", "advise")
	for _, want := range []string{"Firmware is linux-firmware's latest release", "linux-firmware 20260916 (checked 2026-10-07) lists build 8dbafb52 for iwlwifi-cc-a0-77.ucode"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("tag 20260916: missing %q in\n%s", want, stdout)
		}
	}
	firmwareIndex(t, "74.563a6e92.0") // master's, 2026-10-07
	a := readAdvice(t, out(t, "", "advise", "-f", "json"))
	if f := findingOf(a, "firmware.linux-firmware-differs"); f == nil || !strings.Contains(f.Answers[0].Text, "lists build 563a6e92") || !strings.Contains(f.Answers[0].Text, "the loaded firmware is build 8dbafb52") {
		t.Errorf("master: %+v", f)
	}

	dir := fwindex.CacheDir()
	must(t, os.WriteFile(filepath.Join(dir, fwindex.WhenceName), []byte("edited"), 0o644))
	a = readAdvice(t, out(t, "", "advise", "-f", "json"))
	if !hasWarning(a.Warnings, "the firmware index can't be used (WHENCE isn't the file the last update installed)") || findingOf(a, "firmware.linux-firmware-matches") != nil {
		t.Errorf("edited index: %q", a.Warnings)
	}
	must(t, os.RemoveAll(dir))
	a = readAdvice(t, out(t, "", "advise", "-f", "json"))
	n := 0
	for _, w := range a.Warnings {
		if strings.Contains(w, "hwspec firmware update") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("no index: %d warnings name the command: %q", n, a.Warnings)
	}
}

// failingTransport fails any request: advise must make none (ADR 0012).
type failingTransport struct{ t *testing.T }

func (f failingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.t.Errorf("advise made a request: %s", r.URL)
	return nil, errors.New("no network in advise")
}

func TestAdviseMakesNoNetworkRequest(t *testing.T) {
	setupAdvise(t)
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	http.DefaultTransport = failingTransport{t}
	a := readAdvice(t, out(t, "", "advise", "-f", "json"))
	if findingOf(a, "firmware.linux-firmware-matches") == nil || findingOf(a, "firmware.lvfs-up-to-date") == nil {
		t.Error("the comparisons didn't run, so this proves nothing")
	}
}

func findingOf(a advisor.Advice, id string) *advisor.Finding {
	for i := range a.Findings {
		if a.Findings[i].ID == id {
			return &a.Findings[i]
		}
	}
	return nil
}

func hasWarning(warnings []string, part string) bool {
	return slices.ContainsFunc(warnings, func(w string) bool { return strings.Contains(w, part) })
}

// fakeCatalogue is a verified catalogue file as advise sees it.
type fakeCatalogue struct {
	signed  time.Time
	cat     *fwindex.Catalogue
	matches error
	parse   error
}

func (f fakeCatalogue) Signed() time.Time                  { return f.signed }
func (f fakeCatalogue) Matches(*fwindex.Source) error      { return f.matches }
func (f fakeCatalogue) Parse() (*fwindex.Catalogue, error) { return f.cat, f.parse }

// pm981Catalogue is LVFS's component for the reference drive (Lenovo's,
// as on 2026-10-06), and any others given.
func pm981Catalogue(more ...fwindex.Component) *fwindex.Catalogue {
	return fwindex.NewCatalogue(append([]fwindex.Component{{ID: "com.lenovo.PM981.256GB.firmware", Name: "PM981", Developer: "Lenovo",
		GUIDs:    []string{"9657ce89-450f-58d2-ade0-2c6ae667541a"},
		Releases: []fwindex.Release{{Version: "1L2QEXD7", Date: time.Date(2016, 7, 8, 3, 0, 0, 0, time.UTC), Urgency: "high"}},
		Requires: []fwindex.Requirement{{Kind: "firmware", Compare: "eq", Version: "NVME:0x144D", Text: "vendor-id"}}}}, more...)...)
}

// lvfsCatalogue stands in for fwupd's copy of LVFS's catalogue, signed a
// day before the test; files maps other paths to their catalogue. The
// catalogue's verification is fwindex's to test; here the opener is
// replaced.
func lvfsCatalogue(t *testing.T, dir string) map[string]catalogueFile {
	t.Helper()
	oldPath, oldOpen := fwupdCatalogue, openCatalogue
	t.Cleanup(func() { fwupdCatalogue, openCatalogue = oldPath, oldOpen })
	fwupdCatalogue = filepath.Join(dir, "fwupd-firmware.xml.zst")
	must(t, os.WriteFile(fwupdCatalogue, nil, 0o644))
	files := map[string]catalogueFile{}
	openCatalogue = func(path string, now time.Time) (catalogueFile, error) {
		if f, ok := files[path]; ok {
			return f, nil
		}
		if path == fwupdCatalogue {
			return fakeCatalogue{signed: now.Add(-24 * time.Hour), cat: pm981Catalogue()}, nil
		}
		return nil, errors.New("no signature verifies")
	}
	return files
}

// The LVFS catalogue is the later signed of hwspec's copy and fwupd's;
// hwspec's must be the one its last update installed; one over 30 days
// old isn't used, one over 7 is with a warning; the advice names the
// source and its signing date.
func TestAdviseComparesFirmwareWithLVFS(t *testing.T) {
	setupAdvise(t)
	a := readAdvice(t, out(t, "", "advise", "-f", "json"))
	f := findingOf(a, "firmware.lvfs-up-to-date")
	if f == nil || !strings.HasPrefix(f.Answers[0].Text, "LVFS (fwupd's copy, signed ") || !strings.Contains(f.Answers[0].Text, "Published by Lenovo") {
		t.Fatalf("fwupd's copy: %+v", f)
	}
	files := lvfsCatalogue(t, t.TempDir())
	dir := fwindex.CacheDir()
	m, err := fwindex.ReadManifest(dir)
	must(t, err)
	m.LVFS = &fwindex.Source{URL: fwindex.LVFSBase + fwindex.CatalogueName, Files: map[string]string{}}
	js, _ := json.Marshal(m)
	must(t, os.WriteFile(filepath.Join(dir, "manifest.json"), js, 0o644))
	ours := filepath.Join(dir, fwindex.CatalogueName)
	day := 24 * time.Hour
	nowT := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	oldNow := now
	t.Cleanup(func() { now = oldNow })
	now = func() time.Time { return nowT }

	// Listed in the manifest but failing its checks: said, and fwupd's
	// copy used.
	a = readAdvice(t, out(t, "", "advise", "-f", "json"))
	if !hasWarning(a.Warnings, "hwspec's LVFS catalogue can't be used (no signature verifies)") || findingOf(a, "firmware.lvfs-up-to-date") == nil {
		t.Errorf("failing hwspec copy: %q", a.Warnings)
	}
	// Signed at the same time as fwupd's: hwspec's own is used.
	files[ours] = fakeCatalogue{signed: nowT.Add(-day), cat: pm981Catalogue()}
	if a = readAdvice(t, out(t, "", "advise", "-f", "json")); findingOf(a, "firmware.lvfs-up-to-date") == nil ||
		!strings.HasPrefix(findingOf(a, "firmware.lvfs-up-to-date").Answers[0].Text, "LVFS (signed ") {
		t.Errorf("a tie: %+v", findingOf(a, "firmware.lvfs-up-to-date"))
	}

	// hwspec's copy, signed later and with an update for the drive (a
	// newer CVE release that needs the drive at 1L2QEXD7 or later),
	// is used, and every field reaches the advice.
	update := fwindex.Component{ID: "com.samsung.example.firmware", Name: "Example SSD", Developer: "Samsung", VersionFormat: "plain",
		GUIDs: []string{"00000000-0000-0000-0000-000000000001", "47335265-a509-51f7-841e-1c94911af66b"},
		Releases: []fwindex.Release{
			{Version: "1L2QEXD9", Date: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Urgency: "high", CVEs: []string{"CVE-2026-0001"}},
			{Version: "1L2QEXD7", Date: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}},
		Requires: []fwindex.Requirement{{Kind: "id", Compare: "ge", Version: "1.9.0", Text: "org.freedesktop.fwupd"}}}
	// And a stream in a stated format the drive's version isn't listed in.
	stated := fwindex.Component{ID: "com.samsung.other.firmware", Name: "Other", Developer: "Samsung", VersionFormat: "plain",
		GUIDs:    []string{"c9d531ea-ee7d-5562-8def-c64d0d144813"},
		Releases: []fwindex.Release{{Version: "X1"}}}
	files[ours] = fakeCatalogue{signed: nowT.Add(-time.Hour), cat: pm981Catalogue(update, stated)}
	a = readAdvice(t, out(t, "", "advise", "-f", "json"))
	if f := findingOf(a, "firmware.lvfs-differs"); f == nil || !strings.Contains(f.Answers[0].Text, "which isn't one of its releases, and versions in this format aren't ordered") {
		t.Errorf("a stated format: %+v", f)
	}
	if f := findingOf(a, "firmware.lvfs-update-urgent"); f == nil || !strings.HasPrefix(f.Answers[0].Text, "LVFS (signed ") ||
		!strings.Contains(f.Answers[0].Text, "lists 1L2QEXD9 (2026-09-01) as the most recently published Example SSD firmware (com.samsung.example.firmware); this device runs 1L2QEXD7, and LVFS published 1 release(s) after it; urgency high; fixes CVE-2026-0001. needs fwupd ge 1.9.0") {
		t.Errorf("hwspec's copy: %+v", f)
	}

	// Signed earlier than fwupd's: fwupd's is used.
	files[ours] = fakeCatalogue{signed: nowT.Add(-3 * day), cat: pm981Catalogue(update)}
	if a = readAdvice(t, out(t, "", "advise", "-f", "json")); findingOf(a, "firmware.lvfs-update-urgent") != nil {
		t.Errorf("an older copy of hwspec's was used: %q", a.Warnings)
	}

	// Not the one hwspec's update installed: said, and fwupd's used.
	files[ours] = fakeCatalogue{signed: nowT.Add(-time.Hour), cat: pm981Catalogue(update), matches: errors.New("it isn't the catalogue the last `hwspec firmware update` installed")}
	a = readAdvice(t, out(t, "", "advise", "-f", "json"))
	if !hasWarning(a.Warnings, "hwspec's LVFS catalogue can't be used (it isn't the catalogue the last `hwspec firmware update` installed)") ||
		findingOf(a, "firmware.lvfs-update-urgent") != nil || findingOf(a, "firmware.lvfs-up-to-date") == nil {
		t.Errorf("a replaced copy: %q", a.Warnings)
	}

	// hwspec's fails to parse: said, and fwupd's used.
	files[ours] = fakeCatalogue{signed: nowT.Add(-time.Hour), parse: errors.New("lists 934 components")}
	if a = readAdvice(t, out(t, "", "advise", "-f", "json")); !hasWarning(a.Warnings, "hwspec's LVFS catalogue can't be used (lists 934 components)") || findingOf(a, "firmware.lvfs-up-to-date") == nil {
		t.Errorf("unparsable: %q", a.Warnings)
	}
	delete(files, ours)

	// Eight days old: used, with a warning; 31: not used.
	files[fwupdCatalogue] = fakeCatalogue{signed: nowT.Add(-8 * day), cat: pm981Catalogue()}
	if a = readAdvice(t, out(t, "", "advise", "-f", "json")); !hasWarning(a.Warnings, "fwupd's LVFS catalogue was signed 8 days ago; run") || findingOf(a, "firmware.lvfs-up-to-date") == nil {
		t.Errorf("8 days: %q", a.Warnings)
	}
	files[fwupdCatalogue] = fakeCatalogue{signed: nowT.Add(-7 * day), cat: pm981Catalogue()}
	if a = readAdvice(t, out(t, "", "advise", "-f", "json")); hasWarning(a.Warnings, "days ago") {
		t.Errorf("7 days: %q", a.Warnings)
	}
	files[fwupdCatalogue] = fakeCatalogue{signed: nowT.Add(-31 * day), cat: pm981Catalogue()}
	if a = readAdvice(t, out(t, "", "advise", "-f", "json")); !hasWarning(a.Warnings, "fwupd's LVFS catalogue was signed 31 days ago, over 30") ||
		findingOf(a, "firmware.lvfs-up-to-date") != nil || !hasWarning(a.Warnings, "no LVFS catalogue") {
		t.Errorf("31 days: %q", a.Warnings)
	}
	delete(files, fwupdCatalogue)

	// Neither: the advisor's one warning; fwupd's unreadable: said.
	must(t, os.Remove(fwupdCatalogue))
	if a = readAdvice(t, out(t, "", "advise", "-f", "json")); !hasWarning(a.Warnings, "no LVFS catalogue: run `hwspec firmware update`") || hasWarning(a.Warnings, "fwupd's LVFS catalogue") {
		t.Errorf("neither: %q", a.Warnings)
	}
	fwupdCatalogue = filepath.Join(fwupdCatalogue, "x") // under a file: ENOTDIR
	must(t, os.WriteFile(filepath.Dir(fwupdCatalogue), nil, 0o644))
	if a = readAdvice(t, out(t, "", "advise", "-f", "json")); !hasWarning(a.Warnings, "fwupd's LVFS catalogue can't be used (") {
		t.Errorf("fwupd's unreadable: %q", a.Warnings)
	}

	// An unreadable manifest is said, as for linux-firmware, and fwupd's
	// copy still used.
	fwupdCatalogue = filepath.Dir(fwupdCatalogue)
	must(t, os.WriteFile(filepath.Join(dir, "manifest.json"), []byte("{"), 0o644))
	if a = readAdvice(t, out(t, "", "advise", "-f", "json")); !hasWarning(a.Warnings, "--allow-older") || findingOf(a, "firmware.lvfs-up-to-date") == nil {
		t.Errorf("unreadable manifest: %q", a.Warnings)
	}
}
