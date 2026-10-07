package main

import (
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiegui2025/hwspec/internal/ids"
)

// genids builds the databases hwspec ships and the signed weekly bundle.
// Each test runs a command the Makefile or the ids workflow runs.

func runOK(t *testing.T, args ...string) string {
	t.Helper()
	var buf bytes.Buffer
	if code := run(args, &buf, io.Discard); code != 0 {
		t.Fatalf("genids %s: exit %d", strings.Join(args, " "), code)
	}
	return buf.String()
}

func write(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func gunzip(t *testing.T, path string) string {
	t.Helper()
	gz, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(zr)
	return string(b)
}

func TestUsageAndUnknownCommands(t *testing.T) {
	for _, c := range []struct {
		args []string
		code int
	}{
		{nil, 2}, {[]string{"jedec"}, 2}, {[]string{"frob", "x"}, 1}, {[]string{"jedec", "a"}, 1},
		{[]string{"cpu", "a", "b"}, 1}, {[]string{"jedec", "/nonexistent", "out.gz"}, 1},
	} {
		if code := run(c.args, io.Discard, io.Discard); code != c.code {
			t.Errorf("%v: exit %d, want %d", c.args, code, c.code)
		}
	}
}

// The converters turn each upstream format into hwspec's "KEY<TAB>Name"
// lines, deterministically.
func TestConvertersProduceIDFiles(t *testing.T) {
	dir := t.TempDir()
	jedecSrc := write(t, filepath.Join(dir, "decode-dimms"), `my @vendors = (
["AMD", "AMI", "Fairchild", `+fillers(123)+`],
["Cdk \"quoted\"", "Hynix"]);
`)
	out := filepath.Join(dir, "jedec.ids.gz")
	runOK(t, "jedec", jedecSrc, out)
	if got := gunzip(t, out); !strings.Contains(got, "1 01\tAMD\n1 02\tAMI\n1 03\tFairchild\n1 04\tMaker 0\n") ||
		!strings.Contains(got, "1 7E\tMaker 122\n2 01\tCdk \"quoted\"\n2 02\tHynix\n") {
		t.Errorf("jedec:\n%s", got)
	}
	first, _ := os.ReadFile(out)
	runOK(t, "jedec", jedecSrc, out)
	if again, _ := os.ReadFile(out); !bytes.Equal(first, again) {
		t.Error("output isn't deterministic")
	}

	ouiSrc := write(t, filepath.Join(dir, "oui.txt"), "OUI/MA-L\n\n00-00-0C   (hex)\t\tCisco Systems, Inc\n00000C     (base 16)\t\tCisco Systems, Inc\n\nfcfbfb     (base 16)\t\tCisco\n12345      (base 16)\t\tToo short\n")
	runOK(t, "oui", ouiSrc, filepath.Join(dir, "oui.ids.gz"))
	if got := gunzip(t, filepath.Join(dir, "oui.ids.gz")); !strings.Contains(got, "00000C\tCisco Systems, Inc\nFCFBFB\tCisco\n") || strings.Contains(got, "Too short") {
		t.Errorf("oui:\n%s", got)
	}

	btSrc := write(t, filepath.Join(dir, "bt.yaml"), "company_identifiers:\n  - value: 0x0002\n    name: 'Intel Corp.'\n  - value: 0x0000\n    name: 'Ericsson AB'\n")
	runOK(t, "bluetooth", btSrc, filepath.Join(dir, "bt.ids.gz"))
	if got := gunzip(t, filepath.Join(dir, "bt.ids.gz")); !strings.Contains(got, "0000\tEricsson AB\n0002\tIntel Corp.\n") {
		t.Errorf("bluetooth:\n%s", got)
	}

	runOK(t, "gzip", ouiSrc, filepath.Join(dir, "copy.gz"))
	if got := gunzip(t, filepath.Join(dir, "copy.gz")); !strings.Contains(got, "(base 16)") {
		t.Error("gzip changed the content")
	}

	for name, src := range map[string]string{"jedec, no table": "nothing", "jedec, unterminated": "@vendors = ([\"A\"]", "bluetooth, not yaml": "{",
		// a "]" in a name ends bank 1 early, shifting every later ID
		"jedec, short bank": `@vendors = (["A", "B [x]", ` + fillers(124) + `], ["C"]);`,
		"jedec, long bank":  `@vendors = ([` + fillers(127) + `]);`,
		"jedec, empty bank": `@vendors = ([` + fillers(126) + `], []);`,
	} {
		path := write(t, filepath.Join(dir, "bad"), src)
		kind, _, _ := strings.Cut(name, ",")
		if code := run([]string{kind, path, filepath.Join(dir, "x.gz")}, io.Discard, io.Discard); code != 1 {
			t.Errorf("%s: exit %d", name, code)
		}
	}
}

// Kernel sources shaped like intel-family.h and amd.c.
func intelFamily() string {
	var b strings.Builder
	b.WriteString(`#define INTEL_ANY			IFM(X86_FAMILY_ANY, X86_MODEL_ANY)
#define INTEL_PENTIUM_PRO		IFM(6, 0x01)
#define INTEL_CORE2_MEROM		IFM(6, 0x0F)
#define INTEL_NEHALEM			IFM(6, 0x1E) /* Auburndale / Havendale */
#define INTEL_SKYLAKE			IFM(6, 0x5E) /* Sky Lake */
#define INTEL_SKYLAKE_X			IFM(6, 0x55)
#define INTEL_KABYLAKE			IFM(6, 0x9E)
#define INTEL_ICELAKE_X			IFM(6, 0x6A) /* Sunny Cove */
#define INTEL_ALDERLAKE_N		IFM(6, 0xBE) /* Alderlake N */
#define INTEL_ATOM_GOLDMONT		IFM(6, 0x5C) /* Apollo Lake */
#define INTEL_SAPPHIRERAPIDS_X		IFM(6, 0x8F) /* Golden Cove */
#define INTEL_FAM6_LAST			IFM(6, 0xFF)
`)
	for i := range 45 {
		fmt.Fprintf(&b, "#define INTEL_FILLER%d IFM(15, 0x%02X)\n", i, i)
	}
	return b.String()
}

const amdC = `
	/* Figure out Zen generations: */
	switch (c->x86) {
	case 0x17:
		switch (c->x86_model) {
		case 0x00 ... 0x2f:
		case 0x50 ... 0x5f:
			setup_force_cpu_cap(X86_FEATURE_ZEN1);
			break;
		case 0x30 ... 0x4f:
			setup_force_cpu_cap(X86_FEATURE_ZEN2);
			break;
		default:
			goto warn;
		}
		break;

	case 0x19:
		/* the model switch follows */
		switch (c->x86_model) {
		case 0x00 ... 0x0f:
			setup_force_cpu_cap(X86_FEATURE_ZEN3);
			break;
		case 0x10 ... 0x1f:
		case 0x61:
			setup_force_cpu_cap(X86_FEATURE_ZEN4);
			break;
		default:
			goto warn;
		}
		break;

	case 0x1a:
		switch (c->x86_model) {
		case 0x00 ... 0x2f:
			setup_force_cpu_cap(X86_FEATURE_ZEN5);
			break;
		default:
			goto warn;
		}
		break;
	default:
		break;
	}
`

func TestCPUCodenamesFromKernelSourcesAndTheCuratedList(t *testing.T) {
	dir := t.TempDir()
	intel := write(t, filepath.Join(dir, "intel-family.h"), intelFamily())
	amd := write(t, filepath.Join(dir, "amd.c"), amdC)
	curated := write(t, filepath.Join(dir, "curated.ids"), "# curated\n\nintel 6 9E 10-13\tCoffee Lake\tSkylake\tkernel:intel-family.h\namd 19 61\tRaphael\t\tinstlat:a.txt url:https://example.org\n")
	out := filepath.Join(dir, "cpu.ids.gz")
	runOK(t, "cpu", intel, amd, curated, out)
	got := gunzip(t, out)
	for _, want := range []string{
		"intel:6:01\tPentium Pro\tPentium Pro",
		"intel:6:0f\tCore 2 Merom\tCore 2 Merom",
		"intel:6:1e\tAuburndale / Havendale\tNehalem",
		"intel:6:5e\tSkylake\tSkylake",
		"intel:6:55\tSkylake-X\tSkylake",
		"intel:6:9e\tKaby Lake\tKaby Lake",
		"intel:6:9e:12\tCoffee Lake\tSkylake",
		"intel:6:6a\tIce Lake-X\tSunny Cove",
		"intel:6:be\tAlder Lake-N\tAlder Lake",
		"intel:6:5c\tApollo Lake\tGoldmont",
		"intel:6:8f\tSapphire Rapids-X\tGolden Cove",
		"amd:17:00\t\tZen",
		"amd:17:5a\t\tZen",
		"amd:17:31\t\tZen 2",
		"amd:19:61\tRaphael\tZen 4",
		"amd:1a:2f\t\tZen 5",
	} {
		if !strings.Contains(got, want+"\n") {
			t.Errorf("cpu.ids lacks %q", want)
		}
	}
	if strings.Contains(got, "intel:6:ff") || strings.Contains(got, "ANY") {
		t.Error("range markers were kept")
	}

	for name, files := range map[string][3]string{
		"too few Intel models":  {write(t, filepath.Join(dir, "few.h"), "#define INTEL_X IFM(6, 0x01)\n"), amd, curated},
		"no Zen table":          {intel, write(t, filepath.Join(dir, "nozen.c"), "int x;\n"), curated},
		"too few Zen":           {intel, write(t, filepath.Join(dir, "fewzen.c"), "Figure out Zen generations\ncase 0x17:\nswitch (c->x86_model) {\ncase 0x01:\nX86_FEATURE_ZEN1\n"), curated},
		"malformed curated":     {intel, amd, write(t, filepath.Join(dir, "bad1.ids"), "intel 6\n")},
		"bad stepping range":    {intel, amd, write(t, filepath.Join(dir, "bad2.ids"), "intel 6 9e 5-2\tX\t\tkernel:x\n")},
		"no source":             {intel, amd, write(t, filepath.Join(dir, "bad3.ids"), "intel 6 9e 5\tX\tY\n")},
		"empty source":          {intel, amd, write(t, filepath.Join(dir, "bad4.ids"), "intel 6 9e 5\tX\tY\t \n")},
		"unknown source kind":   {intel, amd, write(t, filepath.Join(dir, "bad5.ids"), "intel 6 9e 5\tX\tY\tkernel:x wikipedia:Y\n")},
		"bare source prefix":    {intel, amd, write(t, filepath.Join(dir, "bad6.ids"), "intel 6 9e 5\tX\tY\tinstlat:\n")},
		"a fifth field":         {intel, amd, write(t, filepath.Join(dir, "bad7.ids"), "intel 6 9e 5\tX\tY\tkernel:x\tkernel:y\n")},
		"vendor source no URL":  {intel, amd, write(t, filepath.Join(dir, "bad8.ids"), "intel 6 9e 5\tX\tY\tamd:55449\n")},
		"plain http source":     {intel, amd, write(t, filepath.Join(dir, "bad9.ids"), "intel 6 9e 5\tX\tY\turl:http://example.org\n")},
		"missing Intel source":  {filepath.Join(dir, "none.h"), amd, curated},
		"missing AMD source":    {intel, filepath.Join(dir, "none.c"), curated},
		"missing curated list":  {intel, amd, filepath.Join(dir, "none.ids")},
		"family before its Zen": {intel, write(t, filepath.Join(dir, "order.c"), strings.Replace(amdC, "setup_force_cpu_cap(X86_FEATURE_ZEN2);", "", 1)), curated},
	} {
		if _, err := cpu(files[0], files[1], files[2], io.Discard); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A curated line that changes nothing is reported, not fatal: kernel
// updates make lines redundant.
func TestCPUWarnsAboutCuratedLinesThatChangeNothing(t *testing.T) {
	dir := t.TempDir()
	intel := write(t, filepath.Join(dir, "intel-family.h"), intelFamily())
	amd := write(t, filepath.Join(dir, "amd.c"), amdC)
	curated := write(t, filepath.Join(dir, "curated.ids"),
		"intel 6 9e 9\tKaby Lake\tKaby Lake\tkernel:x\n"+ // 1: what the model says already
			"intel 6 9e 11\tCoffee Lake\t\tkernel:x\n"+ // 2: changes the codename
			"amd 17 31\t\tZen 2\tinstlat:x\n"+ // 3: the kernel's Zen generation
			"amd 19 61\t\tZen 4c\tinstlat:x\n"+ // 4: changes only the microarchitecture
			"intel 6 9e 10-11\tCoffee Lake\t\tkernel:x\n"+ // 5: changes stepping 10, not 11 (line 2)
			"intel 6 9e 11\tCoffee Lake\t\tkernel:x\n") // 6: repeats line 2
	var warn bytes.Buffer
	if _, err := cpu(intel, amd, curated, &warn); err != nil {
		t.Fatal(err)
	}
	// Exactly lines 1, 3 and 6, as GitHub Actions annotations.
	var lines []string
	for l := range strings.SplitSeq(strings.TrimSpace(warn.String()), "\n") {
		if !strings.HasPrefix(l, "::warning file="+curated+",line=") || !strings.HasSuffix(l, ": the kernel or an earlier line already says this; remove it") {
			t.Errorf("warning %q isn't an annotation with the expected message", l)
		}
		_, n, _ := strings.Cut(strings.TrimPrefix(l, "::warning file="+curated+",line="), "::")
		lines = append(lines, strings.TrimPrefix(strings.Split(n, ": ")[0], curated+":"))
	}
	if got := strings.Join(lines, " "); got != "1 3 6" {
		t.Errorf("warned about lines %q, want 1 3 6:\n%s", got, warn.String())
	}
}

// The real curated list parses, and every line names a source.
func TestTheCuratedListIsSourced(t *testing.T) {
	dir := t.TempDir()
	intel := write(t, filepath.Join(dir, "intel-family.h"), intelFamily())
	amd := write(t, filepath.Join(dir, "amd.c"), amdC)
	if _, err := cpu(intel, amd, "cpu-curated.ids", io.Discard); err != nil {
		t.Fatal(err)
	}
}

func TestMarketingNames(t *testing.T) {
	for in, want := range map[string]string{"KABYLAKE": "Kaby Lake", "SAPPHIRERAPIDS": "Sapphire Rapids", "XEON PHI KNL": "Xeon Phi KNL",
		"PENTIUM III": "Pentium III", "LAKEFIELD": "Lakefield", "GOLDMONT PLUS": "Goldmont Plus"} {
		if got := pretty(in); got != want {
			t.Errorf("pretty(%q) = %q, want %q", in, got, want)
		}
	}
	if b, s := splitSuffix("HASWELL"); b != "HASWELL" || s != "" {
		t.Errorf("splitSuffix = %q %q", b, s)
	}
	if got := title("PRO"); got != "Pro" {
		t.Errorf("title(PRO) = %q", got)
	}
}

// A bundle directory built from the databases hwspec embeds.
func bundle(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src, _ := filepath.Glob("../../internal/ids/data/*.ids.gz")
	if len(src) != len(ids.Kinds) {
		t.Fatalf("embedded databases: %v", src)
	}
	for _, s := range src {
		b, err := os.ReadFile(s)
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, filepath.Base(s)), string(b))
	}
	return dir
}

func gz(t *testing.T, content string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write([]byte(content))
	zw.Close()
	return buf.String()
}

// The workflow builds a manifest, re-verifies the bundle independently,
// and only then signs it.
func TestManifestThenVerify(t *testing.T) {
	dir := bundle(t)
	if out := runOK(t, "manifest", dir); !strings.Contains(out, "pci.ids.gz") {
		t.Errorf("manifest output: %s", out)
	}
	if out := runOK(t, "verify", dir); !strings.Contains(out, fmt.Sprintf("verified %d databases", len(ids.Kinds))) {
		t.Errorf("verify output: %s", out)
	}
	// With the same content, a file without a header date keeps the
	// previous bundle's date.
	prev := filepath.Join(t.TempDir(), "prev.json")
	m := readManifest(t, dir)
	f := m.Files["jedec.ids.gz"]
	f.Date = "2025-01-01"
	m.Files["jedec.ids.gz"] = f
	writeManifest(t, prev, m)
	runOK(t, "manifest", dir, prev)
	if got := readManifest(t, dir).Files["jedec.ids.gz"].Date; got != "2025-01-01" {
		t.Errorf("unchanged file's date = %s", got)
	}
	runOK(t, "verify", dir, prev)
	runOK(t, "manifest", dir, filepath.Join(t.TempDir(), "no-previous-bundle.json"))
}

func readManifest(t *testing.T, dir string) *ids.Manifest {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := ids.ParseManifest(b)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func writeManifest(t *testing.T, path string, m *ids.Manifest) {
	t.Helper()
	b, _ := json.Marshal(m)
	write(t, path, string(b))
}

// What the manifest step refuses to publish.
func TestManifestRefusesBadDatabases(t *testing.T) {
	for name, mutate := range map[string]func(t *testing.T, dir string) string{
		"unknown file": func(t *testing.T, dir string) string { write(t, filepath.Join(dir, "x.ids.gz"), gz(t, "x")); return "" },
		"not gzip":     func(t *testing.T, dir string) string { write(t, filepath.Join(dir, "pci.ids.gz"), "plain"); return "" },
		"too few names": func(t *testing.T, dir string) string {
			write(t, filepath.Join(dir, "pci.ids.gz"), gz(t, "8086  Intel\n"))
			return ""
		},
		"future date": func(t *testing.T, dir string) string {
			content := gunzip(t, filepath.Join(dir, "pci.ids.gz"))
			future := time.Now().AddDate(1, 0, 0).Format("2006.01.02")
			write(t, filepath.Join(dir, "pci.ids.gz"), gz(t, "# Version: "+future+"\n"+content))
			return ""
		},
		"missing database": func(t *testing.T, dir string) string { os.Remove(filepath.Join(dir, "oui.ids.gz")); return "" },
		"shrank": func(t *testing.T, dir string) string {
			prev := filepath.Join(t.TempDir(), "prev.json")
			writeManifest(t, prev, &ids.Manifest{Format: ids.ManifestFormat, GeneratedAt: time.Now(),
				Files: map[string]ids.ManifestFile{"pci.ids.gz": {Entries: 10_000_000, SHA256: strings.Repeat("0", 64), Size: 1, Date: "2026-01-01"}}})
			return prev
		},
		"unreadable previous manifest": func(t *testing.T, dir string) string { return write(t, filepath.Join(t.TempDir(), "p.json"), "{") },
	} {
		dir := bundle(t)
		prev := mutate(t, dir)
		if err := manifest(io.Discard, dir, prev); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	t.Setenv("HWSPEC_ALLOW_SHRINK", "1")
	dir := bundle(t)
	prev := filepath.Join(t.TempDir(), "prev.json")
	writeManifest(t, prev, &ids.Manifest{Format: ids.ManifestFormat, GeneratedAt: time.Now(),
		Files: map[string]ids.ManifestFile{"pci.ids.gz": {Entries: 10_000_000, SHA256: strings.Repeat("0", 64), Size: 1, Date: "2026-01-01"}}})
	if err := manifest(io.Discard, dir, prev); err != nil {
		t.Errorf("HWSPEC_ALLOW_SHRINK=1: %v", err)
	}
}

// What the independent verification catches between building and signing.
func TestVerifyCatchesTamperedBundles(t *testing.T) {
	for name, mutate := range map[string]func(t *testing.T, dir string) string{
		"no manifest":     func(t *testing.T, dir string) string { os.Remove(filepath.Join(dir, "manifest.json")); return "" },
		"broken manifest": func(t *testing.T, dir string) string { write(t, filepath.Join(dir, "manifest.json"), "{"); return "" },
		"file swapped": func(t *testing.T, dir string) string {
			write(t, filepath.Join(dir, "pnp.ids.gz"), gz(t, "XXX\tY\n"))
			return ""
		},
		"file added": func(t *testing.T, dir string) string { write(t, filepath.Join(dir, "extra.ids.gz"), "x"); return "" },
		"stale manifest": func(t *testing.T, dir string) string {
			m := readManifest(t, dir)
			m.GeneratedAt = time.Now().AddDate(0, 0, -3)
			writeManifest(t, filepath.Join(dir, "manifest.json"), m)
			return ""
		},
		"date in future": func(t *testing.T, dir string) string {
			return withEntry(t, dir, "pci.ids.gz", func(f *ids.ManifestFile) { f.Date = "2999-01-01" })
		},
		"wrong entry count": func(t *testing.T, dir string) string {
			return withEntry(t, dir, "pci.ids.gz", func(f *ids.ManifestFile) { f.Entries++ })
		},
		"unknown file listed": func(t *testing.T, dir string) string {
			m := readManifest(t, dir)
			m.Files["x.ids.gz"] = m.Files["pci.ids.gz"]
			delete(m.Files, "pci.ids.gz")
			writeManifest(t, filepath.Join(dir, "manifest.json"), m)
			os.Rename(filepath.Join(dir, "pci.ids.gz"), filepath.Join(dir, "x.ids.gz"))
			return ""
		},
		"shrank": func(t *testing.T, dir string) string {
			prev := filepath.Join(t.TempDir(), "prev.json")
			writeManifest(t, prev, &ids.Manifest{Format: ids.ManifestFormat, GeneratedAt: time.Now(),
				Files: map[string]ids.ManifestFile{"usb.ids.gz": {Entries: 10_000_000, SHA256: strings.Repeat("0", 64), Size: 1, Date: "2026-01-01"}}})
			return prev
		},
		"unreadable previous manifest": func(t *testing.T, dir string) string { return write(t, filepath.Join(t.TempDir(), "p.json"), "{") },
	} {
		dir := bundle(t)
		if err := manifest(io.Discard, dir, ""); err != nil {
			t.Fatal(err)
		}
		prev := mutate(t, dir)
		if err := verify(io.Discard, dir, prev); err == nil {
			t.Errorf("%s: verified", name)
		}
	}
}

// withEntry rewrites one manifest entry, keeping its file's real hash.
func withEntry(t *testing.T, dir, name string, change func(*ids.ManifestFile)) string {
	t.Helper()
	m := readManifest(t, dir)
	f := m.Files[name]
	change(&f)
	m.Files[name] = f
	writeManifest(t, filepath.Join(dir, "manifest.json"), m)
	return ""
}

// Control characters in a database (an escape sequence planted upstream)
// stop the bundle.
func TestVerifyRefusesControlCharacters(t *testing.T) {
	dir := bundle(t)
	content := gunzip(t, filepath.Join(dir, "pnp.ids.gz"))
	write(t, filepath.Join(dir, "pnp.ids.gz"), gz(t, content+"ZZZ\tEvil\x1b[2J Corp\n"))
	if err := manifest(io.Discard, dir, ""); err != nil {
		t.Skipf("Validate already refuses it: %v", err)
	}
	if err := verify(io.Discard, dir, ""); err == nil || !strings.Contains(err.Error(), "control characters") {
		t.Errorf("verify: %v", err)
	}
}

// Signing checks the result against the keys hwspec trusts, so a wrong
// secret in the workflow fails the job instead of publishing a bundle no
// one can verify.
func TestSignCatchesAWrongKey(t *testing.T) {
	dir := t.TempDir()
	path := write(t, filepath.Join(dir, "manifest.json"), `{"format":1}`)
	t.Setenv("HWSPEC_IDS_SIGNING_KEY", "not base64!")
	if err := sign(path); err == nil || !strings.Contains(err.Error(), "base64") {
		t.Errorf("garbage key: %v", err)
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	t.Setenv("HWSPEC_IDS_SIGNING_KEY", base64.StdEncoding.EncodeToString(priv.Seed()))
	if err := sign(path); err == nil || !strings.Contains(err.Error(), "trusted") {
		t.Errorf("untrusted key: %v", err)
	}
	if _, err := os.Stat(path + ".sig"); err != nil {
		t.Errorf("no signature written: %v", err)
	}
	if err := sign(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("signed a missing manifest")
	}
}

func TestKeygenWritesAPrivateSeedAndPrintsThePublicKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	pub := strings.TrimSpace(runOK(t, "keygen", path))
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", st, err)
	}
	seedText, _ := os.ReadFile(path)
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(seedText)))
	if err != nil || len(seed) != ed25519.SeedSize {
		t.Fatalf("seed: %v", err)
	}
	want := base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
	if pub != want {
		t.Errorf("printed %q, want %q", pub, want)
	}
	if code := run([]string{"keygen", filepath.Join(t.TempDir(), "missing", "key")}, io.Discard, io.Discard); code != 1 {
		t.Errorf("unwritable path: exit %d", code)
	}
}

// Output and input failures stop the build with the reason.
func TestReadAndWriteFailures(t *testing.T) {
	dir := t.TempDir()
	src := write(t, filepath.Join(dir, "src"), "data")
	if code := run([]string{"gzip", src, filepath.Join(dir, "missing", "out.gz")}, io.Discard, io.Discard); code != 1 {
		t.Errorf("unwritable output: exit %d", code)
	}
	if code := run([]string{"cpu", src, src, src, filepath.Join(dir, "out.gz")}, io.Discard, io.Discard); code != 1 {
		t.Errorf("bad cpu sources: exit %d", code)
	}
	if _, err := readPrevManifest(dir); err == nil {
		t.Error("a directory read as the previous manifest")
	}
	if err := sign(filepath.Join(dir, "missing", "manifest.json")); err == nil {
		t.Error("signed a missing manifest")
	}
	if err := verify(io.Discard, dir, filepath.Join(dir, "nothing.json")); err == nil {
		t.Error("verified a bundle without a manifest")
	}
	b := bundle(t)
	if err := manifest(io.Discard, b, ""); err != nil {
		t.Fatal(err)
	}
	if err := verify(io.Discard, b, dir); err == nil {
		t.Error("a directory read as the previous manifest")
	}
	os.Chmod(filepath.Join(b, "pci.ids.gz"), 0)
	t.Cleanup(func() { os.Chmod(filepath.Join(b, "pci.ids.gz"), 0o644) })
	if os.Geteuid() != 0 {
		if err := manifest(io.Discard, b, ""); err == nil {
			t.Error("an unreadable database was listed")
		}
		if err := verify(io.Discard, b, ""); err == nil {
			t.Error("an unreadable database was verified")
		}
	}
}

// The workflow's steps, through the command line: manifest and verify with
// a previous bundle, keygen, and sign with the secret from the environment.
func TestWorkflowStepsThroughTheCommandLine(t *testing.T) {
	dir := bundle(t)
	prev := filepath.Join(t.TempDir(), "prev.json")
	runOK(t, "manifest", dir, prev)
	runOK(t, "verify", dir, prev)
	t.Setenv("HWSPEC_IDS_SIGNING_KEY", "")
	if code := run([]string{"sign", filepath.Join(dir, "manifest.json")}, io.Discard, io.Discard); code != 1 {
		t.Errorf("sign without a key: exit %d", code)
	}
	for _, args := range [][]string{{"jedec", "a", "b", "c"}, {"cpu", "a"}} {
		if code := run(args, io.Discard, io.Discard); code != 1 {
			t.Errorf("%v: exit %d", args, code)
		}
	}
}

// kbBundle is bundle(t) plus the committed knowledge base.
func kbBundle(t *testing.T) string {
	t.Helper()
	dir := bundle(t)
	b, err := os.ReadFile("../../internal/kb/data/" + ids.AdvisorFile)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, ids.AdvisorFile), string(b))
	return dir
}

// #81: the bundle carries the knowledge base (every format present),
// dated by its version and counted by its rules, and verification checks
// it like the databases; a knowledge base that shrank more than 5% needs
// HWSPEC_ALLOW_SHRINK=1.
func TestBundlesCarryTheKnowledgeBase(t *testing.T) {
	dir := kbBundle(t)
	write(t, filepath.Join(dir, "advisor-v2.json.gz"), gz(t, `{"format":2,"version":"2026-01-01T00:00:00Z","rules":[{},{},{}]}`))
	if out := runOK(t, "manifest", dir); !strings.Contains(out, ids.AdvisorFile) {
		t.Errorf("manifest output: %s", out)
	}
	m := readManifest(t, dir)
	b, _ := os.ReadFile(filepath.Join(dir, ids.AdvisorFile))
	version, rules, err := ids.CheckAdvisor(ids.AdvisorFile, b)
	if f := m.Files[ids.AdvisorFile]; err != nil || f.Date != version || f.Entries != rules || f.Size != int64(len(b)) {
		t.Errorf("manifest entry %+v (version %s, %d rules, %v)", f, version, rules, err)
	}
	if f := m.Files["advisor-v2.json.gz"]; f.Date != "2026-01-01T00:00:00Z" || f.Entries != 3 {
		t.Errorf("another format's entry %+v", f)
	}
	if out := runOK(t, "verify", dir); !strings.Contains(out, fmt.Sprintf("verified %d databases and knowledge bases", len(ids.Kinds)+2)) {
		t.Errorf("verify output: %s", out)
	}

	shrank := func(t *testing.T) string {
		prev := filepath.Join(t.TempDir(), "prev.json")
		writeManifest(t, prev, &ids.Manifest{Format: ids.ManifestFormat, GeneratedAt: time.Now(),
			Files: map[string]ids.ManifestFile{ids.AdvisorFile: {Entries: 1000, SHA256: strings.Repeat("0", 64), Size: 1, Date: "2026-01-01"}}})
		return prev
	}
	dir = kbBundle(t)
	if err := manifest(io.Discard, dir, shrank(t)); err == nil || !strings.Contains(err.Error(), "rules, down from 1000") {
		t.Errorf("manifest of a shrunken knowledge base: %v", err)
	}
	if err := manifest(io.Discard, dir, ""); err != nil {
		t.Fatal(err)
	}
	if err := verify(io.Discard, dir, shrank(t)); err == nil || !strings.Contains(err.Error(), "rules, down from 1000") {
		t.Errorf("verify of a shrunken knowledge base: %v", err)
	}
	t.Setenv("HWSPEC_ALLOW_SHRINK", "1")
	if err := manifest(io.Discard, dir, shrank(t)); err != nil {
		t.Errorf("HWSPEC_ALLOW_SHRINK=1: %v", err)
	}
	if err := verify(io.Discard, dir, shrank(t)); err != nil {
		t.Errorf("verify with HWSPEC_ALLOW_SHRINK=1: %v", err)
	}
}

// What manifest refuses and verification catches in a knowledge base.
func TestKnowledgeBaseTampering(t *testing.T) {
	dir := kbBundle(t)
	write(t, filepath.Join(dir, ids.AdvisorFile), gz(t, `{"format":2,"version":"2026-10-06T12:00:00Z","rules":[{}]}`))
	if err := manifest(io.Discard, dir, ""); err == nil || !strings.Contains(err.Error(), "format 2, but the name says 1") {
		t.Errorf("manifest of a mislabelled knowledge base: %v", err)
	}
	for name, mutate := range map[string]func(t *testing.T, dir string){
		"swapped": func(t *testing.T, dir string) { write(t, filepath.Join(dir, ids.AdvisorFile), gz(t, "{}")) },
		"swapped, still valid": func(t *testing.T, dir string) {
			// The same rules and version, other bytes: only the hash tells.
			write(t, filepath.Join(dir, ids.AdvisorFile), gz(t, gunzip(t, filepath.Join(dir, ids.AdvisorFile))+"\n"))
		},
		"rule count": func(t *testing.T, dir string) {
			withEntry(t, dir, ids.AdvisorFile, func(f *ids.ManifestFile) { f.Entries++ })
		},
		"version": func(t *testing.T, dir string) {
			withEntry(t, dir, ids.AdvisorFile, func(f *ids.ManifestFile) { f.Date = "2020-01-01T00:00:00Z" })
		},
		"unlisted file": func(t *testing.T, dir string) { write(t, filepath.Join(dir, "advisor-v9.json.gz"), "x") },
		"structure": func(t *testing.T, dir string) {
			bad := gz(t, `{"format":1,"version":"2026-10-06T12:00:00Z","rules":[]}`)
			write(t, filepath.Join(dir, ids.AdvisorFile), bad)
			withEntry(t, dir, ids.AdvisorFile, func(f *ids.ManifestFile) {
				sum := sha256.Sum256([]byte(bad))
				f.SHA256, f.Size = hex.EncodeToString(sum[:]), int64(len(bad))
			})
		},
		"missing": func(t *testing.T, dir string) { os.Remove(filepath.Join(dir, ids.AdvisorFile)) },
	} {
		dir := kbBundle(t)
		if err := manifest(io.Discard, dir, ""); err != nil {
			t.Fatal(err)
		}
		mutate(t, dir)
		if err := verify(io.Discard, dir, ""); err == nil {
			t.Errorf("%s: verified", name)
		}
	}
	// A knowledge base dated in the future, consistently in its file and
	// the manifest, is refused too.
	dir = bundle(t)
	write(t, filepath.Join(dir, ids.AdvisorFile), gz(t, `{"format":1,"version":"2999-01-01T00:00:00Z","rules":[{}]}`))
	if err := manifest(io.Discard, dir, ""); err != nil {
		t.Fatal(err)
	}
	if err := verify(io.Discard, dir, ""); err == nil || !strings.Contains(err.Error(), "in the future") {
		t.Errorf("a future knowledge base: %v", err)
	}
}

// The shrink guard compares exactly: 2 → 1, 10 → 9 and 19 → 18 are drops
// of more than 5% (integer division let them through); 20 → 19 is 5%.
func TestShrinkIsExact(t *testing.T) {
	t.Setenv("HWSPEC_ALLOW_SHRINK", "")
	for _, c := range []struct {
		n, was int
		want   bool
	}{{1, 2, true}, {9, 10, true}, {18, 19, true}, {19, 20, false}, {95, 100, false}, {94, 100, true}, {5, 0, false}, {0, 1, true}} {
		if got := shrank(c.n, c.was); got != c.want {
			t.Errorf("%d after %d: %v", c.n, c.was, got)
		}
	}
	t.Setenv("HWSPEC_ALLOW_SHRINK", "1")
	if shrank(1, 2) {
		t.Error("HWSPEC_ALLOW_SHRINK=1 ignored")
	}
}

// Before signing, the bundle's knowledge bases must be the committed
// ones byte for byte: a build job can't publish rules of its own.
func TestVerifyComparesTheKnowledgeBaseWithTheCommittedOne(t *testing.T) {
	committed := "../../internal/kb/data"
	dir := kbBundle(t)
	if err := manifest(io.Discard, dir, ""); err != nil {
		t.Fatal(err)
	}
	if out := runOK(t, "verify", dir, "", committed); !strings.Contains(out, "verified") {
		t.Errorf("verify: %s", out)
	}
	// A knowledge base of the build job's own, made consistent with the
	// manifest: everything else verifies, the comparison doesn't.
	own := gz(t, `{"format":1,"version":"2026-10-06T23:00:00Z","sources":[],"rules":[{"id":"x"}]}`)
	write(t, filepath.Join(dir, ids.AdvisorFile), own)
	if err := manifest(io.Discard, dir, ""); err != nil {
		t.Fatal(err)
	}
	if err := verify(io.Discard, dir, ""); err != nil {
		t.Fatalf("the swapped bundle should pass the other checks: %v", err)
	}
	if err := sameAsCommitted(dir, committed); err == nil || !strings.Contains(err.Error(), "differs from the committed") {
		t.Errorf("swapped: %v", err)
	}
	var stderr bytes.Buffer
	if code := run([]string{"verify", dir, "", committed}, &bytes.Buffer{}, &stderr); code != 1 {
		t.Errorf("genids verify with the committed dir: exit %d %s", code, stderr.String())
	}
	other := t.TempDir()
	write(t, filepath.Join(other, "advisor-v2.json.gz"), "x")
	if err := sameAsCommitted(dir, other); err == nil || !strings.Contains(err.Error(), "aren't the committed") {
		t.Errorf("another set: %v", err)
	}
	if err := sameAsCommitted(t.TempDir(), committed); err == nil {
		t.Error("a bundle without the knowledge base passed")
	}
	unreadable := t.TempDir()
	if err := os.Mkdir(filepath.Join(unreadable, ids.AdvisorFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := sameAsCommitted(unreadable, committed); err == nil {
		t.Error("an unreadable bundle file passed")
	}
	if err := sameAsCommitted(committed, unreadable); err == nil {
		t.Error("an unreadable committed file passed")
	}
}

// fillers is n quoted JEDEC names, for full banks.
func fillers(n int) string {
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("%q", fmt.Sprintf("Maker %d", i))
	}
	return strings.Join(names, ", ")
}

// Every database has known answers, today's pass, and a bundle whose
// parser misread upstream fails verify (#155): a JEDEC bank shifted by
// one, AMD's Zen generations swapped.
func TestKnownAnswers(t *testing.T) {
	for _, k := range ids.Kinds {
		if len(knownAnswers[k]) == 0 {
			t.Errorf("%s has no known answers", k)
		}
	}
	for name, c := range map[string]struct {
		file   string
		tamper func(string) string
		want   string
	}{
		"JEDEC bank 6 shifted": {"jedec.ids.gz", func(s string) string {
			var out []string
			for line := range strings.SplitSeq(s, "\n") {
				var id int
				if k, v, ok := strings.Cut(line, "\t"); ok && strings.HasPrefix(k, "6 ") {
					if _, err := fmt.Sscanf(k, "6 %x", &id); err == nil {
						line = fmt.Sprintf("6 %02X\t%s", id+1, v)
					}
				}
				out = append(out, line)
			}
			return strings.Join(out, "\n")
		}, `known answer jedec 6:77 is "InterDigital Communications", want "Avant Technology"`},
		"Zen 3 and Zen 4 swapped": {"cpu.ids.gz", func(s string) string {
			return strings.NewReplacer("\tZen 3\n", "\tZen 4\n", "\tZen 4\n", "\tZen 3\n").Replace(s)
		}, `known answer cpu amd:19:22 is "\tZen 4", want "\tZen 3"`},
	} {
		dir := bundle(t)
		path := filepath.Join(dir, c.file)
		write(t, path, gz(t, c.tamper(gunzip(t, path))))
		if err := manifest(io.Discard, dir, ""); err != nil {
			t.Fatalf("%s: manifest: %v", name, err)
		}
		if err := verify(io.Discard, dir, ""); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: verify: %v", name, err)
		}
	}
	if err := checkKnownAnswers("nope", nil); err == nil {
		t.Error("an unknown kind passed")
	}
}

// The release guard (#17): embedded databases at most DAYS old, and not
// from the future.
func TestFresh(t *testing.T) {
	old := clock
	t.Cleanup(func() { clock = old })
	dir := t.TempDir()
	at := time.Date(2026, 10, 7, 13, 0, 0, 0, time.UTC)
	writeManifest(t, filepath.Join(dir, "manifest.json"), &ids.Manifest{Format: ids.ManifestFormat, GeneratedAt: at, Files: map[string]ids.ManifestFile{}})
	for _, c := range []struct {
		now  time.Time
		days string
		want string // "" for success
	}{
		{at.AddDate(0, 0, 45), "45", ""},
		{at.AddDate(0, 0, 45).Add(time.Minute), "45", "generated 2026-10-07, 45 days ago (at most 45): run make update-ids"},
		{at.Add(-23 * time.Hour), "45", ""},
		{at.Add(-25 * time.Hour), "45", "in the future"},
		{at, "0", `days "0" isn't a positive number`},
		{at, "x", `days "x" isn't a positive number`},
	} {
		clock = func() time.Time { return c.now }
		var out bytes.Buffer
		err := fresh(&out, dir, c.days)
		if c.want == "" && (err != nil || !strings.Contains(out.String(), "within 45 days")) || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("now %s, %s days: %v (%q)", c.now, c.days, err, out.String())
		}
	}
	clock = func() time.Time { return at }
	if err := fresh(io.Discard, t.TempDir(), "45"); err == nil {
		t.Error("a missing manifest passed")
	}
	write(t, filepath.Join(dir, "manifest.json"), "{")
	if err := fresh(io.Discard, dir, "45"); err == nil {
		t.Error("a broken manifest passed")
	}
	if code := run([]string{"fresh", dir}, io.Discard, io.Discard); code != 1 {
		t.Errorf("fresh with one argument: exit %d", code)
	}
	// Through the command line, as the release workflow runs it.
	writeManifest(t, filepath.Join(dir, "manifest.json"), &ids.Manifest{Format: ids.ManifestFormat, GeneratedAt: at, Files: map[string]ids.ManifestFile{}})
	clock = func() time.Time { return at.AddDate(0, 0, 60) }
	var stderr bytes.Buffer
	if code := run([]string{"fresh", dir, "45"}, io.Discard, &stderr); code != 1 || !strings.Contains(stderr.String(), "60 days ago") {
		t.Errorf("stale data through run: exit %d, %q", code, stderr.String())
	}
}

// A regenerated file with unchanged content keeps its bytes, so its hash
// and manifest date don't change (compress/flate's output can differ
// between Go versions); changed content is written.
func TestWriteGzipKeepsUnchangedFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.ids.gz")
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.NoCompression) // other bytes than writeGzip's
	zw.Write([]byte("same\n"))
	zw.Close()
	write(t, path, buf.String())
	if err := writeGzip(path, []byte("same\n")); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, buf.Bytes()) {
		t.Error("unchanged content was rewritten")
	}
	if err := writeGzip(path, []byte("changed\n")); err != nil {
		t.Fatal(err)
	}
	if got := gunzip(t, path); got != "changed\n" {
		t.Errorf("changed content: %q", got)
	}
	write(t, path, "not gzip")
	if err := writeGzip(path, []byte("x\n")); err != nil || gunzip(t, path) != "x\n" {
		t.Errorf("a broken file wasn't replaced: %v", err)
	}
}
