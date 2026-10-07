package fwindex

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
)

// The catalogue is decoded within the cap, and must be an AppStream
// components document.
func TestDecodeCatalogue(t *testing.T) {
	xml := []byte(`<?xml version="1.0"?><!-- c --><components origin="lvfs"><component><x><component/></x></component><component/></components>`)
	got, n, err := DecodeCatalogue(zstdOf(t, xml))
	if err != nil || !bytes.Equal(got, xml) || n != 2 {
		t.Fatalf("%q, %d, %v", got, n, err)
	}
	if _, n, err := DecodeCatalogue(zstdOf(t, []byte("<components/>"))); err != nil || n != 0 {
		t.Errorf("empty: %d, %v", n, err)
	}
	big := bytes.Repeat([]byte(" "), 1<<16)
	for _, c := range []struct {
		name string
		zst  []byte
		want string
	}{
		{"over the cap", zstdOf(t, append([]byte("<components>"), big...)), "decodes to more than 4096 bytes"},
		{"not zstd", []byte("plain text"), "catalogue:"},
		{"truncated", zstdOf(t, xml)[:20], "catalogue:"},
		{"not XML", zstdOf(t, []byte("{}")), "isn't XML"},
		{"another document", zstdOf(t, []byte("<html/>")), "<html>, not one <components>"},
		{"two roots", zstdOf(t, []byte("<components/><components/>")), "not one <components>"},
		{"unclosed", zstdOf(t, []byte("<components><component>")), "isn't XML"},
		{"empty", zstdOf(t, nil), "isn't XML"},
	} {
		if _, _, err := decodeCatalogue(c.zst, 4096); !contains(err, c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}

func TestParseWhence(t *testing.T) {
	head := `             * WHENCE *
--------------------------------------------------------------------------

Driver: BCM-0bb4-0306 - Cypress Bluetooth firmware for HTC Vive

File: brcm/BCM-0bb4-0306.hcd
Link: brcm/BCM-0a5c-6410.hcd -> BCM-0bb4-0306.hcd

Licence: Redistributable. See LICENCE.cypress for details.

--------------------------------------------------------------------------

Driver: iwlwifi - Intel Wireless Wifi

File: intel/iwlwifi/iwlwifi-so-a0-gf-a0.pnvm
File: intel/iwlwifi/iwlwifi-cc-a0-77.ucode
Link: iwlwifi-cc-a0-77.ucode -> intel/iwlwifi/iwlwifi-cc-a0-77.ucode
Version: 77.563a6e92.0
Version: 78.ignored.0

--------------------------------------------------------------------------

Driver: DFU Driver for Atheros bluetooth chipset AR3012
File: "brcm/brcmfmac43241b4-sdio.Intel Corp.-VALLEYVIEW C0 PLATFORM.txt"
RawFile: amdtee/raw.bin
Version: 1.0
File: "unterminated
File:
`
	var many strings.Builder
	many.WriteString(head + "\n--------------------------------------------------------------------------\nVersion: orphan\n")
	for i := range minWhenceFiles {
		many.WriteString("File: x/a-" + strconv.Itoa(i) + "\n")
	}
	w, err := ParseWhence([]byte(many.String()))
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]WhenceFile{
		"brcm/BCM-0bb4-0306.hcd":                                           {Driver: "BCM-0bb4-0306"},
		"intel/iwlwifi/iwlwifi-so-a0-gf-a0.pnvm":                           {Driver: "iwlwifi"},
		"intel/iwlwifi/iwlwifi-cc-a0-77.ucode":                             {Driver: "iwlwifi", Version: "77.563a6e92.0"},
		"brcm/brcmfmac43241b4-sdio.Intel Corp.-VALLEYVIEW C0 PLATFORM.txt": {Driver: "DFU Driver for Atheros bluetooth chipset AR3012"},
		"amdtee/raw.bin":                                                   {Driver: "DFU Driver for Atheros bluetooth chipset AR3012", Version: "1.0"},
		`"unterminated`:                                                    {Driver: "DFU Driver for Atheros bluetooth chipset AR3012"},
		"x/a-0":                                                            {},
	} {
		if got, ok := w.Files[path]; !ok || got != want {
			t.Errorf("%s: %+v (listed %v), want %+v", path, got, ok, want)
		}
	}
	if _, ok := w.Files[""]; ok {
		t.Error("an empty File: line was listed")
	}
	for link, target := range map[string]string{
		"brcm/BCM-0a5c-6410.hcd": "brcm/BCM-0bb4-0306.hcd",
		"iwlwifi-cc-a0-77.ucode": "intel/iwlwifi/iwlwifi-cc-a0-77.ucode",
	} {
		if w.Links[link] != target {
			t.Errorf("link %s → %q, want %q", link, w.Links[link], target)
		}
	}

	if _, err := ParseWhence([]byte(head)); !contains(err, "lists 6 files, fewer than the 1000") {
		t.Errorf("too few files: %v", err)
	}
	if _, err := ParseWhence(make([]byte, maxWhenceBytes+1)); !contains(err, "larger than") {
		t.Errorf("too large: %v", err)
	}
	if _, err := ParseWhence([]byte("File: " + strings.Repeat("x", 70000))); !contains(err, "WHENCE:") {
		t.Errorf("a line too long: %v", err)
	}
}

func TestLatestRelease(t *testing.T) {
	refs := `51c8ef5a1aed9582f99adaa36efc5f7f4518a03f	refs/tags/20190312
b0d9583f9528890dfcb28d338fe70b50371084bf	refs/tags/20190312^{}
ab23307cfe7f9366c819025ca3e4778299bc2db2	refs/tags/20260916^{}
247f5c7b8205a287bf4c48659454fb769928f2f0	refs/tags/20260916
79104f902411a949afe1da25dc25e05e3160fab0	refs/heads/main
99999999999999999999999999999999999999ff	refs/tags/99999999-rc1
nothex	refs/tags/29990101
1234	refs/tags/29990102
3333333333333333333333333333333333333333	refs/tags/99999999
4444444444444444444444444444444444444444	refs/tags/20261008
5555555555555555555555555555555555555555	refs/tags/20261007
`
	tag, commit, err := latestRelease([]byte(refs), testNow)
	// 99999999 isn't a date and 20261008 is after tomorrow: neither can pin
	// the newest release, so tomorrow's tag (a time zone ahead) wins.
	if err != nil || tag != "20261007" || commit != "5555555555555555555555555555555555555555" {
		t.Errorf("%s %s %v", tag, commit, err)
	}
	if tag, commit, _ := latestRelease([]byte(refs), testNow.AddDate(0, 0, -2)); tag != "20260916" || commit != "ab23307cfe7f9366c819025ca3e4778299bc2db2" {
		t.Errorf("two days earlier: %s %s", tag, commit)
	}
	// A lightweight tag points at its commit directly.
	if tag, commit, _ := latestRelease([]byte("247f5c7b8205a287bf4c48659454fb769928f2f0\trefs/tags/20250101\n"), testNow); tag != "20250101" || commit != "247f5c7b8205a287bf4c48659454fb769928f2f0" {
		t.Errorf("lightweight: %s %s", tag, commit)
	}
	if _, _, err := latestRelease([]byte("79104f902411a949afe1da25dc25e05e3160fab0\trefs/heads/main\n"), testNow); !contains(err, "no release tags") {
		t.Errorf("no tags: %v", err)
	}
	if _, _, err := latestRelease(make([]byte, maxRefsBytes+1), testNow); !contains(err, "larger than") {
		t.Errorf("too large: %v", err)
	}
}
