package fwindex

import (
	"context"
	"crypto/x509"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A catalogue in LVFS's shape (made up: LVFS's own isn't committed).
const catalogueXML = `<?xml version="1.0" encoding="utf-8"?>
<components origin="lvfs" version="0.9">
<component id="3710" type="firmware"><id>com.lenovo.PM981.256GB.firmware</id><name>PM981</name><name xml:lang="de">PM981 (de)</name>
<summary>SSD firmware</summary>
<provides><firmware type="flashed">9657CE89-450F-58D2-ADE0-2C6AE667541A</firmware><firmware type="flashed">9657ce89-450f-58d2-ade0-2c6ae667541a</firmware><firmware type="flashed"> </firmware></provides>
<developer_name xml:lang="fr">Lenovo (fr)</developer_name><developer_name>Lenovo</developer_name>
<custom><value key="LVFS::UpdateProtocol">org.nvmexpress</value><value key="LVFS::VersionFormat"> triplet </value></custom>
<releases>
<release version="1.2.0" timestamp="1467946800" urgency="high"><description><p>old</p></description></release>
<release version="1.3.0" timestamp="1767946800" urgency="critical"><issues><issue type="cve">CVE-2026-0001</issue><issue type="cve">CVE-2026-0001</issue><issue type="dell">DSA-2026-1</issue><issue type="cve"> </issue></issues></release>
<release version=" " timestamp="1"/>
<release version="0.9"/>
<release version="1.10.0" timestamp="1167946800"/>
<release version="2.0.0" timestamp="1067946800"/>
</releases>
<requires><firmware compare="eq" version="NVME:0x144D">vendor-id</firmware><firmware compare="ge" version="1.0.0"/><id compare="ge" version="1.9.0">org.freedesktop.fwupd</id><hardware>6de5d951-d755-576b-bd09-c5cf66b27234</hardware></requires>
</component>
<component type="firmware"><id>org.example.nameless</id><developer_name xml:lang="de">Beispiel</developer_name><provides><firmware type="flashed">00000000-0000-0000-0000-000000000001</firmware></provides></component>
<component type="firmware"><id>org.example.noguid</id><provides/></component>
<component type="desktop-application"><id>org.example.app</id><provides><firmware type="flashed">00000000-0000-0000-0000-000000000002</firmware></provides></component>
</components>`

func TestParseCatalogue(t *testing.T) {
	c, err := ParseCatalogue([]byte(catalogueXML))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Components) != 2 || len(c.ByGUID("00000000-0000-0000-0000-000000000002")) != 0 {
		t.Fatalf("%d components", len(c.Components))
	}
	pm := c.ByGUID("9657CE89-450F-58D2-ADE0-2C6AE667541A")
	if len(pm) != 1 {
		t.Fatalf("by GUID: %d", len(pm))
	}
	p := pm[0]
	if p.ID != "com.lenovo.PM981.256GB.firmware" || p.Name != "PM981" || p.Developer != "Lenovo" || p.VersionFormat != "triplet" ||
		!slices.Equal(p.GUIDs, []string{"9657ce89-450f-58d2-ade0-2c6ae667541a"}) {
		t.Errorf("component %+v", p)
	}
	var versions []string
	for _, r := range p.Releases {
		versions = append(versions, r.Version)
	}
	// By date, newest first (1.10.0 is older than 1.2.0 here), undated last.
	if !slices.Equal(versions, []string{"1.3.0", "1.2.0", "1.10.0", "2.0.0", "0.9"}) || p.Releases[0].Urgency != "critical" ||
		!slices.Equal(p.Releases[0].CVEs, []string{"CVE-2026-0001"}) || !p.Releases[4].Date.IsZero() ||
		!p.Releases[1].Date.Equal(time.Unix(1467946800, 0)) {
		t.Errorf("releases %+v", p.Releases)
	}
	want := []Requirement{
		{Kind: "firmware", Compare: "eq", Version: "NVME:0x144D", Text: "vendor-id"},
		{Kind: "firmware", Compare: "ge", Version: "1.0.0"},
		{Kind: "id", Compare: "ge", Version: "1.9.0", Text: "org.freedesktop.fwupd"},
		{Kind: "hardware", Text: "6de5d951-d755-576b-bd09-c5cf66b27234"},
	}
	if !slices.Equal(p.Requires, want) {
		t.Errorf("requires %+v", p.Requires)
	}
	if n := c.ByGUID("00000000-0000-0000-0000-000000000001"); len(n) != 1 || n[0].Name != "" || n[0].Developer != "Beispiel" {
		t.Errorf("translated only: %+v", n)
	}

	for name, bad := range map[string]string{
		"not XML":       "<components><component type='firmware'>",
		"a bad number":  `<components><component type="firmware"><releases><release timestamp="soon"/></releases></component></components>`,
		"a broken name": `<components><component type="firmware"><name>x</nam></component></components>`,
		"broken before": `<components><x></y><component type="firmware"/></components>`,
	} {
		if _, err := ParseCatalogue([]byte(bad)); !contains(err, "catalogue:") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// loadCatalogue opens and parses, as advise does.
func loadCatalogue(path string, roots *x509.CertPool, now time.Time) (*Catalogue, time.Time, error) {
	f, err := openCatalogue(path, roots, now)
	if err != nil {
		return nil, time.Time{}, err
	}
	c, err := f.Parse()
	return c, f.Signed(), err
}

// From disk, a catalogue is used only as a download would be: verified,
// within the caps, and complete.
func TestLoadCatalogue(t *testing.T) {
	m := newMirror(t)
	dir := t.TempDir()
	path := filepath.Join(dir, CatalogueName)
	put := func(zst, jcat []byte) {
		write(t, path, zst)
		write(t, path+".jcat", jcat)
	}
	// catalogueOf's components provide no GUID, so only this one is kept.
	big := strings.Replace(string(catalogueOf(2100, "x")), "<component ", catalogueComponent+"<component ", 1)
	m.publish([]byte(big), testNow.Add(-time.Hour))
	put(m.zst, m.jcat)
	c, signedAt, err := loadCatalogue(path, m.pki.roots, testNow)
	if err != nil || !signedAt.Equal(testNow.Add(-time.Hour)) || len(c.Components) != 1 || len(c.ByGUID("9657ce89-450f-58d2-ade0-2c6ae667541a")) != 1 {
		t.Fatalf("good: %v, %d components, %v", signedAt, len(c.Components), err)
	}

	m.publish(catalogueOf(934, "testing"), testNow.Add(-time.Hour))
	put(m.zst, m.jcat)
	if _, _, err := loadCatalogue(path, m.pki.roots, testNow); !contains(err, "lists 934 components") {
		t.Errorf("another catalogue: %v", err)
	}
	m.publish(catalogueOf(minComponents, "floor"), testNow.Add(-time.Hour))
	put(m.zst, m.jcat)
	if _, _, err := loadCatalogue(path, m.pki.roots, testNow); err != nil {
		t.Errorf("exactly %d components: %v", minComponents, err)
	}
	m.publish([]byte(strings.Replace(string(catalogueOf(2100, "x")), "<component ", `<component type="firmware"><releases><release timestamp="soon"/></releases></component><component `, 1)), testNow)
	put(m.zst, m.jcat)
	if _, _, err := loadCatalogue(path, m.pki.roots, testNow); !contains(err, "catalogue:") {
		t.Errorf("unparsable: %v", err)
	}
	put(zstdOf(t, []byte("<html/>")), m.jcat)
	if _, _, err := loadCatalogue(path, m.pki.roots, testNow); !contains(err, "for another file") {
		t.Errorf("a file the signature isn't for: %v", err)
	}
	m.publish(append(catalogueOf(2100, "x"), " "...), testNow)
	write(t, path, m.zst)
	if _, err := OpenCatalogue(path, testNow); !contains(err, "unknown authority") {
		t.Errorf("the built-in CA: %v", err)
	}
	// Signed, but not zstd: refused after verification, before parsing.
	raw := []byte("not zstd")
	put(raw, jcatOf(t, CatalogueName, pkcs7Blob(cms{}.build(t, m.pki, raw))))
	if _, _, err := loadCatalogue(path, m.pki.roots, testNow); !contains(err, "catalogue:") {
		t.Errorf("not zstd: %v", err)
	}
	write(t, path, make([]byte, maxCatalogueBytes+1))
	if _, _, err := loadCatalogue(path, m.pki.roots, testNow); !contains(err, "larger than") {
		t.Errorf("too large: %v", err)
	}
	put(m.zst, nil)
	os.Remove(path + ".jcat")
	if _, _, err := loadCatalogue(path, m.pki.roots, testNow); err == nil {
		t.Error("no signature file: no error")
	}
	os.Remove(path)
	if _, _, err := loadCatalogue(path, m.pki.roots, testNow); err == nil {
		t.Error("no catalogue: no error")
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadCatalogue(path, m.pki.roots, testNow); err == nil {
		t.Error("a directory: no error")
	}
	saved := trustedCAs[0]
	t.Cleanup(func() { trustedCAs[0] = saved })
	trustedCAs[0].sha256 = ""
	if _, err := OpenCatalogue(path, testNow); !contains(err, "fingerprint") {
		t.Errorf("a broken built-in CA: %v", err)
	}
}

const catalogueComponent = `<component type="firmware"><id>com.lenovo.PM981.256GB.firmware</id><provides><firmware type="flashed">9657ce89-450f-58d2-ade0-2c6ae667541a</firmware></provides><releases><release version="1L2QEXD7" timestamp="1467946800"/></releases></component>`

// fwupd's cached catalogue loads as hwspec's would, and holds the
// reference machine's drive.
func TestHostLoadsFwupdsCatalogue(t *testing.T) {
	hostTest(t)
	if _, err := os.Stat(FwupdCatalogue); err != nil {
		t.Skip("fwupd hasn't cached LVFS's catalogue here")
	}
	f, err := OpenCatalogue(FwupdCatalogue, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.Parse()
	if err != nil {
		t.Fatal(err)
	}
	signedAt := f.Signed()
	t.Logf("signed %s, %d components", signedAt, len(c.Components))
	if len(c.Components) < minComponents {
		t.Errorf("%d components", len(c.Components))
	}
}

// The catalogue an update installed is the one its manifest describes:
// checksums and signing time.
func TestCatalogueMatchesTheManifest(t *testing.T) {
	m := newMirror(t)
	dir := t.TempDir()
	if _, err := Update(context.Background(), m.options(dir)); err != nil {
		t.Fatal(err)
	}
	man, _ := ReadManifest(dir)
	f, err := openCatalogue(filepath.Join(dir, CatalogueName), m.pki.roots, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Matches(man.LVFS); err != nil {
		t.Errorf("as installed: %v", err)
	}
	for name, edit := range map[string]func(s *Source){
		"another catalogue": func(s *Source) { s.Files[CatalogueName] = "x" },
		"another jcat":      func(s *Source) { s.Files[JcatName] = "x" },
		"signed later":      func(s *Source) { s.SignedAt = s.SignedAt.Add(time.Second) },
		"signed earlier":    func(s *Source) { s.SignedAt = s.SignedAt.Add(-time.Second) },
	} {
		s := *man.LVFS
		s.Files = maps.Clone(s.Files)
		edit(&s)
		if err := f.Matches(&s); !contains(err, "isn't the catalogue the last") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := f.Matches(nil); err == nil {
		t.Error("no manifest entry: no error")
	}
}

// The cache is the user's to change: a FIFO where a file should be gives
// an error at once rather than a hang, and a symlink isn't followed.
func TestCacheReadsOnlyRegularFiles(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, manifestName), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := ReadManifest(dir); done <- err }()
	select {
	case err := <-done:
		if !contains(err, "isn't a regular file") {
			t.Errorf("a FIFO manifest: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked on a FIFO")
	}
	other := t.TempDir()
	write(t, filepath.Join(other, CatalogueName), []byte("x"))
	if err := os.Symlink(filepath.Join(other, CatalogueName), filepath.Join(dir, CatalogueName)); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenCatalogue(filepath.Join(dir, CatalogueName), testNow); !contains(err, "is a symlink") {
		t.Errorf("a symlinked catalogue: %v", err)
	}
}
