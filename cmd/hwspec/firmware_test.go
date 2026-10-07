package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiegui2025/hwspec/internal/fwindex"
)

// `hwspec firmware update` prints each source's outcome and date, says
// where the index is (or that a dry run wrote nothing), and exits 1 when a
// source wasn't updated or installing failed.
func TestFirmwareUpdateReportsEachSource(t *testing.T) {
	setup(t)
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	old := updateFirmware
	t.Cleanup(func() { updateFirmware = old })
	var got fwindex.Options
	answer := func(res []fwindex.Result, err error) {
		updateFirmware = func(_ context.Context, opt fwindex.Options) ([]fwindex.Result, error) {
			got = opt
			return res, err
		}
	}
	signed := time.Date(2026, 10, 6, 11, 55, 37, 0, time.UTC)
	lvfs := fwindex.Result{Source: "LVFS", Status: fwindex.Updated, Date: signed, Components: 4413}
	lf := fwindex.Result{Source: "linux-firmware", Status: fwindex.Unchanged, Tag: "20260916", Date: time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)}

	answer([]fwindex.Result{lvfs, lf}, nil)
	stdout, stderr := mustRun(t, "", "firmware", "update")
	for _, want := range []string{"LVFS            signed 2026-10-06 11:55 UTC, 4413 components  updated", "linux-firmware  release 20260916", "unchanged", "Firmware index in " + filepath.Join(cache, "hwspec", "firmware")} {
		if !strings.Contains(stdout, want) {
			t.Errorf("update: missing %q in\n%s", want, stdout)
		}
	}
	if got.Dir != filepath.Join(cache, "hwspec", "firmware") || got.DryRun || got.AllowOlder || !strings.HasPrefix(got.UserAgent, "hwspec/") || !strings.Contains(stderr, "cdn.fwupd.org") {
		t.Errorf("options %+v, stderr %q", got, stderr)
	}

	stdout, _ = mustRun(t, "", "firmware", "update", "--dry-run", "--allow-older")
	if !strings.Contains(stdout, "would update") || !strings.Contains(stdout, "Nothing written") || !got.DryRun || !got.AllowOlder {
		t.Errorf("dry run: %q, options %+v", stdout, got)
	}

	stale := lvfs
	stale.Warning = "the newest catalogue offered was signed 10 days ago"
	answer([]fwindex.Result{stale, lf}, nil)
	if _, stderr := mustRun(t, "", "firmware", "update"); !strings.Contains(stderr, "hwspec: warning: LVFS: the newest catalogue offered was signed 10 days ago") {
		t.Errorf("warning: %q", stderr)
	}

	// As root through sudo the index would land in root's cache (or make
	// the user's root's): refused before anything is fetched.
	oldEuid := geteuid
	t.Cleanup(func() { geteuid = oldEuid })
	geteuid = func() int { return 0 }
	t.Setenv("SUDO_UID", "1000")
	got = fwindex.Options{}
	if code, _, stderr := hwspec(t, "", "firmware", "update"); code != 1 || !strings.Contains(stderr, "without sudo") || got.Dir != "" {
		t.Errorf("under sudo: exit %d, %q, options %+v", code, stderr, got)
	}
	t.Setenv("SUDO_UID", "")
	if code, _, _ := hwspec(t, "", "firmware", "update"); code != 0 {
		t.Errorf("root without sudo: exit %d", code)
	}
	geteuid = oldEuid

	refused := fwindex.Result{Source: "LVFS", Status: fwindex.Refused, Reason: "the signature is for another file"}
	failed := fwindex.Result{Source: "linux-firmware", Status: fwindex.Failed, Reason: "HTTP 503"}
	answer([]fwindex.Result{refused, failed}, nil)
	code, stdout, stderr := hwspec(t, "", "firmware", "update")
	if code != 1 || !strings.Contains(stdout, "refused (the signature is for another file)") || !strings.Contains(stdout, "failed (HTTP 503)") ||
		!strings.Contains(stderr, "2 of 2 sources not updated") {
		t.Errorf("refused: exit %d\n%s%s", code, stdout, stderr)
	}

	answer([]fwindex.Result{lvfs, lf}, errors.New("disk full"))
	if code, _, stderr := hwspec(t, "", "firmware", "update"); code != 1 || !strings.Contains(stderr, "verified but not installed: disk full") {
		t.Errorf("install failure: exit %d, %q", code, stderr)
	}
	answer(nil, errors.New("cached manifest unreadable"))
	if code, _, stderr := hwspec(t, "", "firmware", "update"); code != 1 || !strings.Contains(stderr, "nothing changed: cached manifest unreadable") {
		t.Errorf("no results: exit %d, %q", code, stderr)
	}

	for _, args := range [][]string{{"firmware"}, {"firmware", "list"}, {"firmware", "update", "extra"}} {
		if code, _, stderr := hwspec(t, "", args...); code != 1 || !strings.Contains(stderr, "usage: hwspec firmware update") {
			t.Errorf("%q: exit %d, %q", args, code, stderr)
		}
	}
	if code, _, _ := hwspec(t, "", "firmware", "update", "--frob"); code != 1 {
		t.Errorf("unknown flag: exit %d", code)
	}
}
