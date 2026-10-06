package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/jiegui2025/hwspec/internal/kb"
)

// withSyncedKB makes the synced knowledge base the given bytes, with the
// version the signed manifest gives, or an error.
func withSyncedKB(t *testing.T, b []byte, signed string, err error) {
	t.Helper()
	old := syncedAdvisor
	t.Cleanup(func() { syncedAdvisor = old })
	syncedAdvisor = func() ([]byte, string, error) { return b, signed, err }
}

// syncedKB is the built-in knowledge base re-dated and with one rule
// retitled, so it can be told apart.
func syncedKB(t *testing.T, version string) []byte {
	t.Helper()
	k, err := kb.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	k.Version = version
	k.Rules[0].Title = "From the synced copy"
	b, err := kb.Encode(k)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The copy with the later version is used; the built-in one wins a tie
// and beats an older one; a synced copy that can't be used gives a
// warning and the built-in one.
func TestTheNewerKnowledgeBaseIsUsed(t *testing.T) {
	builtIn, err := kb.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name    string
		b       []byte
		signed  string
		err     error
		synced  bool
		warning string
	}{
		{"none synced", nil, "", nil, false, ""},
		{"newer", syncedKB(t, "2099-01-01T00:00:00Z"), "2099-01-01T00:00:00Z", nil, true, ""},
		{"a tie", syncedKB(t, builtIn.Version), builtIn.Version, nil, false, ""},
		{"older", syncedKB(t, "2020-01-01T00:00:00Z"), "2020-01-01T00:00:00Z", nil, false, ""},
		{"not the manifest's version", syncedKB(t, "2099-01-01T00:00:00Z"), "2020-01-01T00:00:00Z", nil, false,
			"knowledge base: the synced copy's version 2099-01-01T00:00:00Z isn't the signed manifest's 2020-01-01T00:00:00Z"},
		{"unparseable", []byte("not gzip"), "x", nil, false, "knowledge base: the synced copy can't be read ("},
		{"unreadable", nil, "", errors.New("synced manifest unreadable: boom"), false, "knowledge base: the synced copy isn't used (synced manifest unreadable: boom)"},
	} {
		withSyncedKB(t, c.b, c.signed, c.err)
		k, source, warnings, err := knowledgeBase()
		if err != nil {
			t.Fatal(err)
		}
		if got := k.Rules[0].Title == "From the synced copy"; got != c.synced || (source != "embedded") != c.synced {
			t.Errorf("%s: synced copy used %v (source %q), want %v", c.name, got, source, c.synced)
		}
		w := strings.Join(warnings, "\n")
		if c.warning == "" && w != "" || c.warning != "" && !strings.Contains(w, c.warning) {
			t.Errorf("%s: warnings %q, want %q", c.name, w, c.warning)
		}
	}
}

// #81's acceptance: a synced copy that doesn't parse still gives advice,
// from the built-in copy, with a warning; `hwspec ids` lists the copy used.
func TestAdviceFallsBackToTheBuiltInKnowledgeBase(t *testing.T) {
	setupAdvise(t)
	withSyncedKB(t, []byte("not gzip"), "x", nil)
	a := readAdvice(t, out(t, "", "advise", "-f", "json"))
	if len(a.Findings) != 1 || a.Findings[0].Title == "From the synced copy" ||
		!strings.Contains(strings.Join(a.Warnings, "\n"), "knowledge base: the synced copy can't be read") {
		t.Errorf("findings %+v, warnings %q", a.Findings, a.Warnings)
	}
	withSyncedKB(t, syncedKB(t, "2099-01-01T00:00:00Z"), "2099-01-01T00:00:00Z", nil)
	a = readAdvice(t, out(t, "", "advise", "-f", "json"))
	if len(a.Findings) != 1 || a.Findings[0].Title != "From the synced copy" || a.KBVersion != "2099-01-01T00:00:00Z" {
		t.Errorf("the newer synced copy: %+v", a.Findings)
	}
	if got := out(t, "", "ids"); !strings.Contains(got, "advisor") || !strings.Contains(got, "advisor-v1.json.gz 2099-01-01T00:00:00Z") {
		t.Errorf("hwspec ids:\n%s", got)
	}
	withSyncedKB(t, []byte("not gzip"), "x", nil)
	if got := out(t, "", "ids"); !strings.Contains(got, "embedded") || !strings.Contains(got, "(knowledge base: the synced copy can't be read") {
		t.Errorf("hwspec ids with an unusable copy:\n%s", got)
	}
}
