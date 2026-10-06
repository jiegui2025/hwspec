package ids

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type bundle struct {
	t        *testing.T
	dir      string
	key      ed25519.PrivateKey
	requests atomic.Int32
	url      string
}

// newBundle serves a signed bundle built from the embedded databases and
// trusts its key for the duration of the test.
func newBundle(t *testing.T) *bundle {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	old := trustedKeys
	trustedKeys = []string{base64.StdEncoding.EncodeToString(pub)}
	t.Cleanup(func() { trustedKeys = old })

	b := &bundle{t: t, dir: t.TempDir(), key: priv}
	for _, k := range Kinds {
		name := specs[k].file + ".gz"
		data, err := embedded.ReadFile("data/" + name)
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(b.dir, name), string(data))
	}
	b.publish(time.Now().UTC().Truncate(time.Second), priv)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.requests.Add(1)
		http.ServeFile(w, r, filepath.Join(b.dir, filepath.Base(r.URL.Path)))
	}))
	t.Cleanup(srv.Close)
	b.url = srv.URL + "/"
	return b
}

// publish writes manifest.json for the files in the bundle directory and
// signs it with key.
func (b *bundle) publish(at time.Time, key ed25519.PrivateKey) {
	m := Manifest{Format: ManifestFormat, GeneratedAt: at, Files: map[string]ManifestFile{}}
	for _, k := range Kinds {
		name := specs[k].file + ".gz"
		data, err := os.ReadFile(filepath.Join(b.dir, name))
		if err != nil {
			b.t.Fatal(err)
		}
		m.Files[name] = ManifestFile{SHA256: sha(data), Size: int64(len(data)), Date: "2026-10-05", Entries: 1}
	}
	js, _ := json.Marshal(m)
	write(b.t, filepath.Join(b.dir, "manifest.json"), string(js))
	write(b.t, filepath.Join(b.dir, "manifest.json.sig"), base64.StdEncoding.EncodeToString(ed25519.Sign(key, js)))
}

func (b *bundle) update(opt UpdateOptions) ([]FileUpdate, error) {
	opt.BaseURL = b.url
	res, err := Update(context.Background(), opt)
	if res == nil {
		return nil, err
	}
	return res.Files, err
}

func statuses(res []FileUpdate) string {
	var s []string
	for _, r := range res {
		s = append(s, r.Status)
	}
	return strings.Join(s, ",")
}

func TestUpdate(t *testing.T) {
	isolate(t)
	syncedDir = filepath.Join(t.TempDir(), "ids")
	b := newBundle(t)

	// Dry run: reports, writes nothing.
	res, err := b.update(UpdateOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if statuses(res) != strings.Repeat("new,", len(Kinds)-1)+"new" {
		t.Errorf("dry run statuses = %s", statuses(res))
	}
	if _, err := os.Stat(syncedDir); !os.IsNotExist(err) {
		t.Error("dry run created the synced directory")
	}

	// First real update installs everything, and lookups use it.
	if res, err = b.update(UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if statuses(res) != strings.Repeat("new,", len(Kinds)-1)+"new" {
		t.Errorf("statuses = %s", statuses(res))
	}
	if got := PCIVendor("8086"); got != "Intel Corporation" {
		t.Errorf("after update PCIVendor = %q", got)
	}
	if l := Layers(PCI); l[0].Source != filepath.Join(syncedDir, "pci.ids.gz") {
		t.Errorf("synced copy not used: %+v", l)
	}
	if at, err := SyncedAt(); err != nil || at.IsZero() {
		t.Error("SyncedAt is zero after update")
	}

	// Unchanged bundle: only the manifest and signature are fetched.
	b.requests.Store(0)
	if res, err = b.update(UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if statuses(res) != strings.Repeat("unchanged,", len(Kinds)-1)+"unchanged" || b.requests.Load() != 2 {
		t.Errorf("statuses = %s with %d requests", statuses(res), b.requests.Load())
	}
}

// variant returns a different but valid gzipped copy of a database.
func variant(t *testing.T, gz []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	var content bytes.Buffer
	content.ReadFrom(zr)
	content.WriteString("\n# changed\n")
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(content.Bytes())
	zw.Close()
	return buf.String()
}

func TestUpdateRejects(t *testing.T) {
	isolate(t)
	syncedDir = filepath.Join(t.TempDir(), "ids")
	b := newBundle(t)
	if _, err := b.update(UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	installed, _ := os.ReadFile(filepath.Join(syncedDir, "pci.ids.gz"))

	tiny := func() string {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		zw.Write([]byte("8086  Evil Corp\n"))
		zw.Close()
		return buf.String()
	}()
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
	later := time.Now().UTC().Add(time.Hour)
	earlier := time.Now().UTC().Add(-time.Hour)

	cases := []struct {
		name  string
		setup func()
		want  string
	}{
		{"wrong key", func() { b.publish(later, otherKey) }, "not signed by a trusted key"},
		{"file swapped after signing", func() {
			// Advertise a new, valid pci.ids, then serve something else.
			write(t, filepath.Join(b.dir, "pci.ids.gz"), variant(t, installed))
			b.publish(later, b.key)
			write(t, filepath.Join(b.dir, "pci.ids.gz"), tiny)
		}, "checksum mismatch"},
		{"signed but truncated", func() {
			write(t, filepath.Join(b.dir, "pci.ids.gz"), tiny)
			b.publish(later, b.key)
		}, "only 1 entries"},
		{"rollback", func() {
			write(t, filepath.Join(b.dir, "pci.ids.gz"), string(installed))
			b.publish(earlier, b.key)
		}, "older than the synced one"},
	}
	for _, c := range cases {
		c.setup()
		_, err := b.update(UpdateOptions{})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
		if now, _ := os.ReadFile(filepath.Join(syncedDir, "pci.ids.gz")); !bytes.Equal(now, installed) {
			t.Errorf("%s: installed file was changed", c.name)
		}
	}

	// The rollback is allowed when asked for explicitly.
	if _, err := b.update(UpdateOptions{AllowOlder: true}); err != nil {
		t.Errorf("AllowOlder: %v", err)
	}
}

// Every way an update can fail leaves the installed databases as they were
// and says why.
func TestUpdateFailureModes(t *testing.T) {
	isolate(t)
	if _, err := Update(context.Background(), UpdateOptions{}); err == nil || !strings.Contains(err.Error(), "no home directory") {
		t.Errorf("no home: %v", err)
	}
	syncedDir = filepath.Join(t.TempDir(), "ids")
	b := newBundle(t)

	// The mirror is gone, or has no signature.
	if _, err := Update(context.Background(), UpdateOptions{BaseURL: "http://127.0.0.1:1"}); err == nil {
		t.Error("unreachable mirror accepted")
	}
	os.Rename(filepath.Join(b.dir, "manifest.json.sig"), filepath.Join(b.dir, "sig.bak"))
	if _, err := b.update(UpdateOptions{}); err == nil || !strings.Contains(err.Error(), "manifest.json.sig: HTTP 404") {
		t.Errorf("missing signature: %v", err)
	}
	os.Rename(filepath.Join(b.dir, "sig.bak"), filepath.Join(b.dir, "manifest.json.sig"))

	// A manifest too large to be real.
	big, _ := os.ReadFile(filepath.Join(b.dir, "manifest.json"))
	write(t, filepath.Join(b.dir, "manifest.json"), strings.Repeat(" ", maxManifestBytes+1))
	if _, err := b.update(UpdateOptions{}); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("huge manifest: %v", err)
	}
	write(t, filepath.Join(b.dir, "manifest.json"), string(big))

	// Signed and with the right checksum, but not gzip.
	write(t, filepath.Join(b.dir, "pci.ids.gz"), "not gzip at all")
	b.publish(time.Now().UTC(), b.key)
	if _, err := b.update(UpdateOptions{}); err == nil || !strings.Contains(err.Error(), "pci.ids.gz: gzip") {
		t.Errorf("not gzip: %v", err)
	}
	if _, err := os.Stat(syncedDir); !os.IsNotExist(err) {
		t.Error("a failed update created the synced directory")
	}
}

// The update identifies itself with hwspec's User-Agent, and a database a
// newer hwspec publishes is ignored by this build.
func TestUpdateSendsItsUserAgentAndSkipsUnknownDatabases(t *testing.T) {
	isolate(t)
	syncedDir = filepath.Join(t.TempDir(), "ids")
	b := newBundle(t)
	var agent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agent = r.UserAgent()
		http.ServeFile(w, r, filepath.Join(b.dir, filepath.Base(r.URL.Path)))
	}))
	t.Cleanup(srv.Close)
	m := Manifest{Format: ManifestFormat, GeneratedAt: time.Now().UTC(), Files: map[string]ManifestFile{
		"future.ids.gz": {SHA256: sha([]byte("x")), Size: 1, Date: "2030-01-01", Entries: 1},
	}}
	js, _ := json.Marshal(m)
	write(t, filepath.Join(b.dir, "manifest.json"), string(js))
	write(t, filepath.Join(b.dir, "manifest.json.sig"), base64.StdEncoding.EncodeToString(ed25519.Sign(b.key, js)))
	res, err := Update(context.Background(), UpdateOptions{BaseURL: srv.URL, UserAgent: "hwspec/test"})
	if err != nil || len(res.Files) != 0 || agent != "hwspec/test" {
		t.Errorf("future database: %+v, %v, agent %q", res, err, agent)
	}
}

// Without a readable local manifest an update can't tell a rollback, so it
// stops until asked to replace the synced databases.
func TestUpdateStopsAtAnUnreadableLocalManifest(t *testing.T) {
	isolate(t)
	syncedDir = filepath.Join(t.TempDir(), "ids")
	b := newBundle(t)
	if err := os.MkdirAll(syncedDir, 0o750); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(syncedDir, "manifest.json"), "{broken")
	if _, err := b.update(UpdateOptions{}); err == nil || !strings.Contains(err.Error(), "--allow-older") {
		t.Errorf("unreadable local manifest: %v", err)
	}
	if _, err := b.update(UpdateOptions{AllowOlder: true}); err != nil {
		t.Errorf("--allow-older: %v", err)
	}
}

// A bundle older than the databases built into hwspec isn't installed.
func TestUpdateKeepsNewerBuiltInDatabases(t *testing.T) {
	isolate(t)
	syncedDir = filepath.Join(t.TempDir(), "ids")
	b := newBundle(t)
	b.publish(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), b.key)
	res, err := b.update(UpdateOptions{})
	if !errors.Is(err, ErrBuiltInIsNewer) || res != nil {
		t.Errorf("old bundle: %v, %v", res, err)
	}
}

// Installing after verification can still fail (a full disk, a read-only
// directory): the error says what was already written.
func TestUpdateReportsAPartialInstall(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write a read-only directory")
	}
	isolate(t)
	syncedDir = filepath.Join(t.TempDir(), "ids")
	b := newBundle(t)
	if err := os.MkdirAll(syncedDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(syncedDir, 0o700) })
	_, err := b.update(UpdateOptions{})
	var partial *InstallError
	if !errors.As(err, &partial) || len(partial.Written) != 0 || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("read-only directory: %v", err)
	}
}

// syncDir reports a directory it can't open.
func TestSyncDir(t *testing.T) {
	if err := syncDir(t.TempDir()); err != nil {
		t.Errorf("sync: %v", err)
	}
	if err := syncDir(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("a missing directory synced")
	}
}
