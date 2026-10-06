package ids

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gzipped compresses s.
func gzipped(s string) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write([]byte(s))
	zw.Close()
	return buf.Bytes()
}

const kbFile = `{"format":1,"version":"2026-10-06T12:00:00Z","sources":[],"rules":[{"id":"a"},{"id":"b"}]}`

func TestCheckAdvisor(t *testing.T) {
	if v, n, err := CheckAdvisor(AdvisorFile, gzipped(kbFile)); err != nil || v != "2026-10-06T12:00:00Z" || n != 2 {
		t.Errorf("a good file: %q %d %v", v, n, err)
	}
	if v, n, err := CheckAdvisor("advisor-v2.json.gz", gzipped(strings.Replace(kbFile, `"format":1`, `"format":2`, 1))); err != nil || n != 2 || v == "" {
		t.Errorf("another format, named for it: %v", err)
	}
	for name, c := range map[string]struct {
		file, gz string
		want     string
	}{
		"name":        {"manifest.json", kbFile, "not a knowledge-base file name"},
		"zero format": {"advisor-v0.json.gz", kbFile, "not a knowledge-base file name"},
		"not gzip":    {AdvisorFile, "", "EOF"},
		"not JSON":    {AdvisorFile, "[", "unexpected end"},
		"format":      {AdvisorFile, strings.Replace(kbFile, `"format":1`, `"format":2`, 1), "format 2, but the name says 1"},
		"version":     {AdvisorFile, strings.Replace(kbFile, `"2026-10-06T12:00:00Z"`, `"soon"`, 1), `version "soon" isn't a UTC time`},
		"date only":   {AdvisorFile, strings.Replace(kbFile, `"2026-10-06T12:00:00Z"`, `"2026-10-06"`, 1), `version "2026-10-06" isn't a UTC time`},
		"not UTC":     {AdvisorFile, strings.Replace(kbFile, `"2026-10-06T12:00:00Z"`, `"2026-10-06T12:00:00+01:00"`, 1), "isn't a UTC time"},
		"fraction":    {AdvisorFile, strings.Replace(kbFile, `"2026-10-06T12:00:00Z"`, `"2026-10-06T12:00:00.5Z"`, 1), "isn't a UTC time"},
		"no such day": {AdvisorFile, strings.Replace(kbFile, `"2026-10-06T12:00:00Z"`, `"2026-02-30T12:00:00Z"`, 1), "isn't a UTC time"},
		"case-folded": {AdvisorFile, strings.Replace(kbFile, `"format"`, `"Format"`, 1), "format, version and rules"},
		"no version":  {AdvisorFile, `{"format":1,"rules":[{}]}`, "format, version and rules"},
		"no rules":    {AdvisorFile, strings.Replace(kbFile, `[{"id":"a"},{"id":"b"}]`, `[]`, 1), "no rules"},
	} {
		gz := gzipped(c.gz)
		if name == "not gzip" {
			gz = []byte("plain")
		}
		if _, _, err := CheckAdvisor(c.file, gz); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
	big := gzipped(strings.Repeat(" ", 4<<20+1))
	if _, _, err := CheckAdvisor(AdvisorFile, big); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("too large: %v", err)
	}
}

// `hwspec ids update` installs the knowledge base with the same signature
// and checksum checks as the databases, and skips a format this build
// doesn't know; one that fails its check installs nothing.
func TestUpdateInstallsTheKnowledgeBase(t *testing.T) {
	isolate(t)
	syncedDir = filepath.Join(t.TempDir(), "ids")
	b := newBundle(t)
	write(t, filepath.Join(b.dir, AdvisorFile), string(gzipped(kbFile)))
	write(t, filepath.Join(b.dir, "advisor-v2.json.gz"), string(gzipped(strings.Replace(kbFile, `"format":1`, `"format":2`, 1))))
	b.publish(time.Now().UTC().Truncate(time.Second), b.key)
	res, err := b.update(UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range res {
		names = append(names, r.File)
	}
	if !strings.Contains(strings.Join(names, " "), AdvisorFile) || strings.Contains(strings.Join(names, " "), "advisor-v2") || len(res) != len(Kinds)+1 {
		t.Errorf("updated %q", names)
	}
	got, version, err := SyncedAdvisor()
	if err != nil || !bytes.Equal(got, gzipped(kbFile)) || version != "2026-10-06T12:00:00Z" {
		t.Errorf("SyncedAdvisor = %d bytes, version %q, %v", len(got), version, err)
	}
	if _, err := os.Stat(filepath.Join(syncedDir, "advisor-v2.json.gz")); !os.IsNotExist(err) {
		t.Error("an unknown format was installed")
	}

	// A knowledge base that fails its check, though signed: nothing changes.
	write(t, filepath.Join(b.dir, AdvisorFile), string(gzipped(strings.Replace(kbFile, `"format":1`, `"format":3`, 1))))
	b.publish(time.Now().UTC().Add(time.Hour).Truncate(time.Second), b.key)
	if _, err := b.update(UpdateOptions{}); err == nil || !strings.Contains(err.Error(), "format 3, but the name says 1") {
		t.Errorf("a bad knowledge base: %v", err)
	}
	if got, _, _ := SyncedAdvisor(); !bytes.Equal(got, gzipped(kbFile)) {
		t.Error("the installed knowledge base changed")
	}

	// A valid file the signed manifest describes differently, or one from
	// the future: not installed either.
	other := strings.Replace(kbFile, `[{"id":"a"},{"id":"b"}]`, `[{"id":"a"}]`, 1)
	future := strings.Replace(kbFile, "2026-10-06T12:00:00Z", time.Now().UTC().AddDate(0, 0, 3).Format("2006-01-02T15:04:05Z"), 1)
	for name, c := range map[string]struct {
		file string
		edit func(*Manifest)
		want string
	}{
		"rule count": {other, func(m *Manifest) { f := m.Files[AdvisorFile]; f.Entries = 2; m.Files[AdvisorFile] = f }, "with 1 rules, but the manifest says"},
		"version": {strings.Replace(kbFile, `"id":"b"`, `"id":"c"`, 1), func(m *Manifest) {
			f := m.Files[AdvisorFile]
			f.Date = "2027-01-01T00:00:00Z"
			m.Files[AdvisorFile] = f
		}, "but the manifest says 2027-01-01T00:00:00Z"},
		"future": {future, func(*Manifest) {}, "is in the future"},
	} {
		write(t, filepath.Join(b.dir, AdvisorFile), string(gzipped(c.file)))
		b.publishWith(time.Now().UTC().Add(2*time.Hour).Truncate(time.Second), c.edit)
		if _, err := b.update(UpdateOptions{AllowOlder: true}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}

// The synced copy is used only with a readable manifest that lists it.
func TestSyncedAdvisor(t *testing.T) {
	isolate(t)
	syncedDir = ""
	if b, _, err := SyncedAdvisor(); b != nil || err != nil {
		t.Errorf("no synced directory: %v %v", b, err)
	}
	syncedDir = t.TempDir()
	if b, _, err := SyncedAdvisor(); b != nil || err != nil {
		t.Errorf("nothing synced: %v %v", b, err)
	}
	write(t, filepath.Join(syncedDir, AdvisorFile), "kb")
	write(t, filepath.Join(syncedDir, "manifest.json"), `{"format":1,"files":{}}`)
	if b, _, err := SyncedAdvisor(); b != nil || err != nil {
		t.Errorf("not in the manifest: %q %v", b, err)
	}
	listed := `{"format":1,"files":{"` + AdvisorFile + `":{"sha256":"` + sha([]byte("kb")) + `","size":2,"date":"2026-10-06T12:00:00Z"}}}`
	write(t, filepath.Join(syncedDir, "manifest.json"), listed)
	if b, v, err := SyncedAdvisor(); string(b) != "kb" || v != "2026-10-06T12:00:00Z" || err != nil {
		t.Errorf("listed: %q %q %v", b, v, err)
	}
	// Edited after install (same size, other bytes; or another size):
	// refused, so a local edit can't pin newer-looking advice.
	for _, edit := range []string{"kB", "kbx"} {
		write(t, filepath.Join(syncedDir, AdvisorFile), edit)
		if b, _, err := SyncedAdvisor(); b != nil || err == nil || !strings.Contains(err.Error(), "differs from the synced manifest") {
			t.Errorf("edited to %q: %q %v", edit, b, err)
		}
	}
	os.Remove(filepath.Join(syncedDir, AdvisorFile))
	if b, _, err := SyncedAdvisor(); b != nil || err != nil {
		t.Errorf("listed but gone: %q %v", b, err)
	}
	if err := os.Mkdir(filepath.Join(syncedDir, AdvisorFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SyncedAdvisor(); err == nil {
		t.Error("an unreadable file gave no error")
	}
	write(t, filepath.Join(syncedDir, "manifest.json"), `{`)
	if _, _, err := SyncedAdvisor(); err == nil || !strings.Contains(err.Error(), "synced manifest unreadable") {
		t.Errorf("unreadable manifest: %v", err)
	}
	if !IsAdvisorFile("advisor-v12.json.gz") || IsAdvisorFile("advisor.json.gz") || IsAdvisorFile("pci.ids.gz") {
		t.Error("IsAdvisorFile")
	}
}
