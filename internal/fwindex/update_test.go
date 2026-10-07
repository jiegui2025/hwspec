package fwindex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/jiegui2025/hwspec/internal/netrule"
)

// mirror serves both sources as their hosts do: LVFS's catalogue and
// .jcat (with an ETag, answering a matching If-None-Match with 304), and
// linux-firmware's tag list and WHENCE by commit.
type mirror struct {
	t   *testing.T
	pki *testPKI
	srv *httptest.Server

	mu       sync.Mutex
	zst      []byte
	jcat     []byte
	etag     string
	refs     string
	whence   map[string][]byte // by commit
	requests []string
	agent    string
	status   map[string]int // a path answered with this status instead
}

func newMirror(t *testing.T) *mirror {
	m := &mirror{t: t, pki: newPKI(t, nil), whence: map[string][]byte{}, status: map[string]int{}}
	m.srv = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.srv.Close)
	m.publish(catalogueOf(2100, "0.9"), testNow.Add(-time.Hour))
	m.release("20260916", "ab23307cfe7f9366c819025ca3e4778299bc2db2", whenceText(1200))
	return m
}

// publish signs a new catalogue at signedAt.
func (m *mirror) publish(xml []byte, signedAt time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.zst = zstdOf(m.t, xml)
	sum := sha256Of(m.zst)
	der := cms{attrs: lvfsAttrs(m.t, sum, signedAt)}.build(m.t, m.pki, m.zst)
	m.jcat = jcatOf(m.t, CatalogueName, pkcs7Blob(der))
	m.etag = fmt.Sprintf(`"%x"`, sum[:4])
}

// release tags a new linux-firmware release.
func (m *mirror) release(tag, commit string, whence []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refs += fmt.Sprintf("1111111111111111111111111111111111111111\trefs/tags/%s\n%s\trefs/tags/%s^{}\n", tag, commit, tag)
	m.whence[commit] = whence
}

func (m *mirror) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, r.URL.RequestURI())
	m.agent = r.UserAgent()
	if code := m.status[r.URL.Path]; code != 0 {
		http.Error(w, "no", code)
		return
	}
	switch r.URL.Path {
	case "/downloads/" + JcatName:
		if inm := r.Header.Get("If-None-Match"); inm != "" && inm == m.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", m.etag)
		w.Write(m.jcat)
	case "/downloads/" + CatalogueName:
		w.Write(m.zst)
	case "/lf/info/refs":
		fmt.Fprint(w, m.refs)
	case "/lf/plain/WHENCE":
		b, ok := m.whence[r.URL.Query().Get("id")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	default:
		http.NotFound(w, r)
	}
}

func (m *mirror) options(dir string) Options {
	return Options{Dir: dir, UserAgent: "hwspec/test", lvfsBase: m.srv.URL + "/downloads/", kernelBase: m.srv.URL + "/lf/", roots: m.pki.roots, now: testNow}
}

func (m *mirror) took() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.requests
	m.requests = nil
	return r
}

// catalogueOf is an LVFS-style catalogue listing n components.
func catalogueOf(n int, version string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, `<?xml version="1.0"?>`+"\n"+`<components origin="lvfs" version=%q>`, version)
	for i := range n {
		fmt.Fprintf(&b, `<component type="firmware"><id>org.example.fw%d</id></component>`, i)
	}
	b.WriteString("</components>\n")
	return []byte(b.String())
}

func zstdOf(t *testing.T, b []byte) []byte {
	t.Helper()
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	return enc.EncodeAll(b, nil)
}

func sha256Of(b []byte) []byte {
	s := []byte(sha(b))
	out := make([]byte, len(s)/2)
	fmt.Sscanf(string(s), "%x", &out)
	return out
}

// whenceText is a WHENCE with n files, one of them versioned.
func whenceText(n int) []byte {
	var b strings.Builder
	b.WriteString("WHENCE\n\n------------------------------------------------\n\nDriver: iwlwifi - Intel Wireless\n\n")
	for i := range n {
		fmt.Fprintf(&b, "File: intel/fw-%d.bin\n", i)
	}
	b.WriteString("Version: 77.563a6e92.0\n\nLicence: Redistributable.\n")
	return []byte(b.String())
}

func statuses(rs []Result) string {
	var s []string
	for _, r := range rs {
		s = append(s, r.Source+" "+r.Status)
	}
	return strings.Join(s, ", ")
}

// The first update installs both sources; a dry run before it writes
// nothing; the next asks for an unchanged copy and gets 304 for LVFS and
// the same tag for linux-firmware, so nothing large is downloaded again.
func TestUpdate(t *testing.T) {
	m := newMirror(t)
	dir := filepath.Join(t.TempDir(), "firmware")

	dry := m.options(dir)
	dry.DryRun = true
	res, err := Update(context.Background(), dry)
	if err != nil || statuses(res) != "LVFS updated, linux-firmware updated" {
		t.Fatalf("dry run: %s, %v", statuses(res), err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("the dry run wrote to the cache")
	}
	m.took()

	res, err = Update(context.Background(), m.options(dir))
	if err != nil || statuses(res) != "LVFS updated, linux-firmware updated" {
		t.Fatalf("first update: %s, %v", statuses(res), err)
	}
	if !res[0].Date.Equal(testNow.Add(-time.Hour)) || res[1].Tag != "20260916" || res[1].Date.Format("2006-01-02") != "2026-09-16" {
		t.Errorf("dates: %+v", res)
	}
	if m.agent != "hwspec/test" {
		t.Errorf("User-Agent %q", m.agent)
	}
	man, err := ReadManifest(dir)
	if err != nil || man.LVFS == nil || man.LinuxFirmware == nil || man.LinuxFirmware.Commit != "ab23307cfe7f9366c819025ca3e4778299bc2db2" || man.LVFS.ETag == "" {
		t.Fatalf("manifest: %+v, %v", man, err)
	}
	if !man.LVFS.intact(dir) || !man.LinuxFirmware.intact(dir) {
		t.Error("installed files don't match the manifest")
	}
	if got := strings.Join(m.took(), " "); !strings.Contains(got, "/lf/plain/WHENCE?id=ab23307cfe7f9366c819025ca3e4778299bc2db2") {
		t.Errorf("requests: %s", got)
	}

	later := m.options(dir)
	later.now = testNow.Add(time.Hour)
	res, err = Update(context.Background(), later)
	if err != nil || statuses(res) != "LVFS unchanged, linux-firmware unchanged" {
		t.Fatalf("second update: %s, %v", statuses(res), err)
	}
	if got := m.took(); len(got) != 2 {
		t.Errorf("an unchanged index took %d requests: %q", len(got), got)
	}
	if man, _ := ReadManifest(dir); !man.LVFS.FetchedAt.Equal(later.now) || !man.LinuxFirmware.FetchedAt.Equal(later.now) {
		t.Errorf("fetch time not recorded: %+v", man)
	}

	// A new catalogue and release replace the cached ones.
	m.publish(catalogueOf(2100, "1.0"), testNow)
	m.release("20261006", "cd23307cfe7f9366c819025ca3e4778299bc2db2", whenceText(1300))
	res, err = Update(context.Background(), m.options(dir))
	if err != nil || statuses(res) != "LVFS updated, linux-firmware updated" {
		t.Fatalf("new data: %s, %v", statuses(res), err)
	}
	if man, _ := ReadManifest(dir); man.LinuxFirmware.Tag != "20261006" || !man.LVFS.SignedAt.Equal(testNow) {
		t.Errorf("manifest after new data: %+v", man)
	}
}

// Data older than the cached copy is refused unless AllowOlder; a refused
// source leaves its cached copy and doesn't stop the other one.
func TestUpdateRefusesOlderData(t *testing.T) {
	m := newMirror(t)
	dir := t.TempDir()
	if _, err := Update(context.Background(), m.options(dir)); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, CatalogueName))

	m.publish(catalogueOf(2100, "old"), testNow.Add(-48*time.Hour))
	res, err := Update(context.Background(), m.options(dir))
	if err != nil || res[0].Status != Refused || !strings.Contains(res[0].Reason, "before the cached catalogue") || res[1].Status != Unchanged {
		t.Fatalf("older catalogue: %+v, %v", res, err)
	}
	if after, _ := os.ReadFile(filepath.Join(dir, CatalogueName)); string(after) != string(before) {
		t.Error("a refused catalogue was installed")
	}
	allow := m.options(dir)
	allow.AllowOlder = true
	if res, err := Update(context.Background(), allow); err != nil || res[0].Status != Updated {
		t.Errorf("--allow-older: %+v, %v", res, err)
	}

	// The cache says 20260916; the host now lists an older release only.
	m.mu.Lock()
	m.refs = "ef23307cfe7f9366c819025ca3e4778299bc2db2\trefs/tags/20260810\n"
	m.whence["ef23307cfe7f9366c819025ca3e4778299bc2db2"] = whenceText(1100)
	m.mu.Unlock()
	res, err = Update(context.Background(), m.options(dir))
	if err != nil || res[1].Status != Refused || !strings.Contains(res[1].Reason, "older than the cached 20260916") {
		t.Fatalf("older release: %+v, %v", res, err)
	}
	if res, err := Update(context.Background(), allow); err != nil || res[1].Status != Updated || res[1].Tag != "20260810" {
		t.Errorf("--allow-older: %+v, %v", res, err)
	}
}

// Each source fails or is refused on its own, with the reason. The mirror
// is changed between requests only, so its fields are set without a lock.
func TestUpdateReasons(t *testing.T) {
	for _, c := range []struct {
		name   string
		setup  func(m *mirror)
		source int
		status string
		want   string
	}{
		{"jcat missing", func(m *mirror) { m.status["/downloads/"+JcatName] = 404 }, 0, Failed, "HTTP 404"},
		{"catalogue missing", func(m *mirror) { m.status["/downloads/"+CatalogueName] = 500 }, 0, Failed, "HTTP 500"},
		{"tampered catalogue", func(m *mirror) { m.zst = append(m.zst, 0) }, 0, Refused, "for another file"},
		{"catalogue not XML", func(m *mirror) { m.publish([]byte("not xml"), testNow) }, 0, Refused, "isn't XML"},
		{"tag list missing", func(m *mirror) { m.status["/lf/info/refs"] = 503 }, 1, Failed, "HTTP 503"},
		{"no release tags", func(m *mirror) { m.refs = "79104f902411a949afe1da25dc25e05e3160fab0\trefs/heads/main\n" }, 1, Refused, "no release tags"},
		{"WHENCE missing", func(m *mirror) { m.whence = map[string][]byte{} }, 1, Failed, "HTTP 404"},
		{"WHENCE truncated", func(m *mirror) { m.whence["ab23307cfe7f9366c819025ca3e4778299bc2db2"] = whenceText(10) }, 1, Refused, "lists 10 files"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := newMirror(t)
			c.setup(m)
			dir := t.TempDir()
			res, err := Update(context.Background(), m.options(dir))
			if err != nil || res[c.source].Status != c.status || !strings.Contains(res[c.source].Reason, c.want) {
				t.Errorf("%+v, %v; want %s %q", res, err, c.status, c.want)
			}
			if other := res[1-c.source]; other.Status != Updated {
				t.Errorf("the other source: %+v", other)
			}
			man, _ := ReadManifest(dir)
			if man == nil || (c.source == 0) != (man.LVFS == nil) {
				t.Errorf("manifest: %+v", man)
			}
		})
	}
}

// Nothing to install when both sources fail; no cache directory without
// a home; an unreadable manifest stops the update until --allow-older; a
// cache file edited after install is replaced; a failed install is
// reported.
func TestUpdateFailureModes(t *testing.T) {
	m := newMirror(t)
	if _, err := Update(context.Background(), Options{}); !contains(err, "no home directory") {
		t.Errorf("no directory: %v", err)
	}

	dir := filepath.Join(t.TempDir(), "fw")
	m.status["/downloads/"+JcatName] = 404
	m.status["/lf/info/refs"] = 404
	if res, err := Update(context.Background(), m.options(dir)); err != nil || statuses(res) != "LVFS failed, linux-firmware failed" {
		t.Errorf("both down: %s, %v", statuses(res), err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("nothing to install, but the cache was created")
	}
	m.status = map[string]int{}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, manifestName), []byte("{broken"))
	if _, err := Update(context.Background(), m.options(dir)); !contains(err, "--allow-older") {
		t.Errorf("unreadable manifest: %v", err)
	}
	allow := m.options(dir)
	allow.AllowOlder = true
	if res, err := Update(context.Background(), allow); err != nil || statuses(res) != "LVFS updated, linux-firmware updated" {
		t.Errorf("--allow-older over an unreadable manifest: %s, %v", statuses(res), err)
	}

	// Edited after install: not trusted as the cached copy, so fetched
	// again in full (no ETag) and replaced.
	write(t, filepath.Join(dir, WhenceName), []byte("edited"))
	write(t, filepath.Join(dir, CatalogueName), []byte("edited"))
	m.took()
	res, err := Update(context.Background(), m.options(dir))
	if err != nil || statuses(res) != "LVFS updated, linux-firmware updated" {
		t.Errorf("edited cache: %s, %v", statuses(res), err)
	}
	if got := strings.Join(m.took(), " "); !strings.Contains(got, "/downloads/"+CatalogueName) || !strings.Contains(got, "WHENCE") {
		t.Errorf("edited cache, requests: %s", got)
	}
	// The same bytes again, without an ETag from the host: unchanged.
	m.mu.Lock()
	m.etag = ""
	m.mu.Unlock()
	if res, err := Update(context.Background(), m.options(dir)); err != nil || res[0].Status != Unchanged {
		t.Errorf("same catalogue without an ETag: %+v, %v", res, err)
	}

	if os.Geteuid() != 0 {
		ro := filepath.Join(t.TempDir(), "ro")
		if err := os.Mkdir(ro, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(ro, 0o700) })
		if _, err := Update(context.Background(), m.options(ro)); !contains(err, "installing into") {
			t.Errorf("read-only cache: %v", err)
		}
		if _, err := Update(context.Background(), m.options(filepath.Join(ro, "sub"))); !contains(err, "installing into") {
			t.Errorf("cache directory not creatable: %v", err)
		}
	}
}

// Redirects stay on the host first asked; responses are capped.
func TestFetch(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "elsewhere") }))
	t.Cleanup(other.Close)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/away":
			http.Redirect(w, r, other.URL+"/x", http.StatusFound)
		case "/here":
			http.Redirect(w, r, srv.URL+"/data", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/data":
			fmt.Fprint(w, "0123456789")
		case "/unchanged":
			w.WriteHeader(http.StatusNotModified)
		case "/hostile":
			// A reason phrase that would retitle and clear a terminal.
			conn, buf, _ := w.(http.Hijacker).Hijack()
			buf.WriteString("HTTP/1.1 404 \x1b]0;pwned\a\x1b[2J\r\nContent-Length: 0\r\n\r\n")
			buf.Flush()
			conn.Close()
		}
	}))
	t.Cleanup(srv.Close)
	get := httpGetter("")
	ctx := context.Background()
	if _, err := get(ctx, srv.URL+"/away", "", 100); !contains(err, "another host") {
		t.Errorf("redirect to another host: %v", err)
	}
	if r, err := get(ctx, srv.URL+"/here", "", 100); err != nil || string(r.body) != "0123456789" {
		t.Errorf("redirect on the same host: %v, %v", r, err)
	}
	if _, err := get(ctx, srv.URL+"/loop", "", 100); !contains(err, "too many redirects") {
		t.Errorf("redirect loop: %v", err)
	}
	if _, err := get(ctx, srv.URL+"/unchanged", "", 9); !contains(err, "to a request for the whole file") {
		t.Errorf("304 without an ETag asked: %v", err)
	}
	if _, err := get(ctx, srv.URL+"/hostile", "", 9); !contains(err, "HTTP 404") || strings.ContainsAny(err.Error(), "\x1b\a") {
		t.Errorf("the server's reason phrase: %q", err)
	}
	if _, err := get(ctx, srv.URL+"/data", "", 9); !contains(err, "larger than 9 bytes") {
		t.Errorf("over the cap: %v", err)
	}
	if _, err := get(ctx, "http://127.0.0.1:1/", "", 9); err == nil {
		t.Error("unreachable host: no error")
	}
	if _, err := get(ctx, "://bad", "", 9); err == nil {
		t.Error("bad URL: no error")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	srvBody := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		fmt.Fprint(w, "short")
	}))
	t.Cleanup(srvBody.Close)
	if _, err := get(ctx, srvBody.URL, "", 1000); err == nil {
		t.Error("truncated body: no error")
	}
	if _, err := get(cancelled, srv.URL+"/data", "", 100); err == nil {
		t.Error("cancelled: no error")
	}
}

// httpNames are the only net/http names fwindex may use: none of them can
// open a connection except through http.DefaultTransport.
var httpNames = map[string]bool{
	"Client": true, "NewRequestWithContext": true, "MethodGet": true,
	"StatusOK": true, "StatusNotModified": true, "Request": true,
}

// ADR 0012: fetch.go reaches the network only through
// http.DefaultTransport, as internal/ids does (see netrule).
func TestRequestsOnlyGoThroughTheDefaultTransport(t *testing.T) {
	problems, err := netrule.Check(".", httpNames)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

func TestCacheDir(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/xdg")
	if got := CacheDir(); got != "/xdg/hwspec/firmware" {
		t.Errorf("XDG_CACHE_HOME: %s", got)
	}
	t.Setenv("HOME", "/home/u")
	for _, xdg := range []string{"", "relative"} {
		t.Setenv("XDG_CACHE_HOME", xdg)
		if got := CacheDir(); got != "/home/u/.cache/hwspec/firmware" {
			t.Errorf("XDG_CACHE_HOME=%q: %s", xdg, got)
		}
	}
	t.Setenv("HOME", "")
	if got := CacheDir(); got != "" {
		t.Errorf("no home: %s", got)
	}
}

func TestReadManifest(t *testing.T) {
	dir := t.TempDir()
	if m, err := ReadManifest(dir); m != nil || err != nil {
		t.Errorf("nothing cached: %v, %v", m, err)
	}
	write(t, filepath.Join(dir, manifestName), []byte(`{"format":2}`))
	if _, err := ReadManifest(dir); !contains(err, "format 2") {
		t.Errorf("newer format: %v", err)
	}
	os.Remove(filepath.Join(dir, manifestName))
	os.Mkdir(filepath.Join(dir, manifestName), 0o700)
	if _, err := ReadManifest(dir); err == nil {
		t.Error("unreadable manifest: no error")
	}
	var nilSource *Source
	if nilSource.intact(dir) || (&Source{}).intact(dir) {
		t.Error("an empty source is intact")
	}
	if sameFiles(&Source{Files: map[string]string{"a": "1"}}, &Source{Files: map[string]string{"a": "1", "b": "2"}}) {
		t.Error("different file lists are the same")
	}
}

// Without test overrides, Update uses the real hosts, the built-in CA and
// the clock; a cancelled context shows that without a request leaving.
func TestUpdateDefaults(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := Update(ctx, Options{Dir: t.TempDir()})
	if err != nil || statuses(res) != "LVFS failed, linux-firmware failed" || !strings.Contains(res[0].Reason, LVFSBase) || !strings.Contains(res[1].Reason, LinuxFirmwareBase) {
		t.Errorf("%+v, %v", res, err)
	}
	saved := trustedCAs[0]
	t.Cleanup(func() { trustedCAs[0] = saved })
	trustedCAs[0].sha256 = ""
	if _, err := Update(ctx, Options{Dir: t.TempDir()}); !contains(err, "fingerprint") {
		t.Errorf("a broken built-in CA: %v", err)
	}
}

// LVFS signs a file's digest, not its name: another genuine catalogue
// (firmware-testing lists 934 components) passed off as this one is
// refused by its size, even with AllowOlder; a catalogue much smaller than
// the cached one needs AllowOlder.
func TestUpdateRefusesAnotherOrShrunkCatalogue(t *testing.T) {
	m := newMirror(t)
	m.publish(catalogueOf(934, "testing"), testNow.Add(-time.Hour))
	allow := m.options(t.TempDir())
	allow.AllowOlder = true
	res, err := Update(context.Background(), allow)
	if err != nil || res[0].Status != Refused || !strings.Contains(res[0].Reason, "lists 934 components, fewer than the 2000") {
		t.Errorf("firmware-testing as firmware.xml.zst: %+v, %v", res[0], err)
	}

	dir := t.TempDir()
	m.publish(catalogueOf(2400, "a"), testNow.Add(-3*time.Hour))
	if res, err := Update(context.Background(), m.options(dir)); err != nil || res[0].Status != Updated || res[0].Components != 2400 {
		t.Fatalf("2400 components: %+v, %v", res[0], err)
	}
	if man, _ := ReadManifest(dir); man.LVFS.Components != 2400 {
		t.Errorf("manifest components: %d", man.LVFS.Components)
	}
	m.publish(catalogueOf(2200, "b"), testNow.Add(-2*time.Hour))
	res, err = Update(context.Background(), m.options(dir))
	if err != nil || res[0].Status != Refused || !strings.Contains(res[0].Reason, "lists 2200 components, 200 fewer than the cached catalogue") {
		t.Errorf("8%% smaller: %+v, %v", res[0], err)
	}
	m.publish(catalogueOf(2350, "c"), testNow.Add(-time.Hour))
	if res, err := Update(context.Background(), m.options(dir)); err != nil || res[0].Status != Updated {
		t.Errorf("2%% smaller: %+v, %v", res[0], err)
	}
	m.publish(catalogueOf(2200, "d"), testNow)
	if res, err := Update(context.Background(), allowingOlder(m, dir)); err != nil || res[0].Status != Updated {
		t.Errorf("8%% smaller with AllowOlder: %+v, %v", res[0], err)
	}
}

func allowingOlder(m *mirror, dir string) Options {
	o := m.options(dir)
	o.AllowOlder = true
	return o
}

// The data's date is when LVFS signed it, not when it was fetched: a
// catalogue signed over a week ago is accepted with a warning, over 30
// days ago only with AllowOlder, and a CDN answering "unchanged" for days
// is warned about.
func TestUpdateJudgesTheCatalogueBySigningTime(t *testing.T) {
	m := newMirror(t)
	m.publish(catalogueOf(2100, "old"), testNow.Add(-31*24*time.Hour))
	res, err := Update(context.Background(), m.options(t.TempDir()))
	if err != nil || res[0].Status != Refused || !strings.Contains(res[0].Reason, "31 days ago") {
		t.Errorf("31 days old: %+v, %v", res[0], err)
	}
	if res, err := Update(context.Background(), allowingOlder(m, t.TempDir())); err != nil || res[0].Status != Updated || !strings.Contains(res[0].Warning, "31 days ago") {
		t.Errorf("31 days old with AllowOlder: %+v, %v", res[0], err)
	}
	m.publish(catalogueOf(2100, "week"), testNow.Add(-8*24*time.Hour))
	if res, err := Update(context.Background(), m.options(t.TempDir())); err != nil || res[0].Status != Updated || !strings.Contains(res[0].Warning, "8 days ago") {
		t.Errorf("8 days old: %+v, %v", res[0], err)
	}

	m.publish(catalogueOf(2100, "fresh"), testNow.Add(-time.Hour))
	dir := t.TempDir()
	if res, err := Update(context.Background(), m.options(dir)); err != nil || res[0].Warning != "" {
		t.Fatalf("fresh: %+v, %v", res[0], err)
	}
	frozen := m.options(dir)
	frozen.now = testNow.Add(10 * 24 * time.Hour)
	res, err = Update(context.Background(), frozen)
	if err != nil || res[0].Status != Unchanged || !strings.Contains(res[0].Warning, "10 days ago") || !res[0].Date.Equal(testNow.Add(-time.Hour)) {
		t.Errorf("a CDN answering 304 for 10 days: %+v, %v", res[0], err)
	}
}

// The cached WHENCE is read back only as the last update installed it.
func TestReadWhence(t *testing.T) {
	dir := t.TempDir()
	// Without a home there is no cache, not one in the working directory.
	t.Chdir(dir)
	write(t, manifestName, []byte("{"))
	if w, s, err := ReadWhence(""); w != nil || s != nil || err != nil {
		t.Errorf("no cache directory: %v %v %v", w, s, err)
	}
	dir = t.TempDir()
	if w, _, err := ReadWhence(dir); w != nil || err != nil {
		t.Errorf("nothing cached: %v %v", w, err)
	}
	m := newMirror(t)
	if _, err := Update(context.Background(), m.options(dir)); err != nil {
		t.Fatal(err)
	}
	w, src, err := ReadWhence(dir)
	if err != nil || src.Tag != "20260916" || w.Files["intel/fw-1199.bin"].Version != "77.563a6e92.0" {
		t.Fatalf("cached: %v, %+v, %v", w != nil, src, err)
	}
	write(t, filepath.Join(dir, WhenceName), whenceText(1201))
	if _, _, err := ReadWhence(dir); !contains(err, "isn't the file the last update installed") {
		t.Errorf("edited: %v", err)
	}
	short := whenceText(5)
	write(t, filepath.Join(dir, WhenceName), short)
	man, _ := ReadManifest(dir)
	man.LinuxFirmware.Files[WhenceName] = sha(short)
	js, _ := json.Marshal(man)
	write(t, filepath.Join(dir, manifestName), js)
	if _, _, err := ReadWhence(dir); !contains(err, "lists 5 files") {
		t.Errorf("unparsable: %v", err)
	}
	os.Remove(filepath.Join(dir, WhenceName))
	if _, _, err := ReadWhence(dir); err == nil {
		t.Error("missing file: no error")
	}
	write(t, filepath.Join(dir, manifestName), []byte("{"))
	if _, _, err := ReadWhence(dir); err == nil {
		t.Error("unreadable manifest: no error")
	}
}
