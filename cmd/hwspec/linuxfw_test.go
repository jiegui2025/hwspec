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
	if findingOf(a, "firmware.linux-firmware-matches") == nil {
		t.Error("the comparison didn't run, so this proves nothing")
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
