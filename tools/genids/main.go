// Command genids builds the ID database files that hwspec embeds and that
// the sync bundle publishes. `make update-ids` and the ids workflow use it.
//
//	genids jedec <decode-dimms> <out.gz>    JEDEC JEP106 table from i2c-tools
//	genids oui   <oui.txt> <out.gz>         IEEE MA-L registry, compacted
//	genids gzip  <file> <out.gz>            any file as-is (pci/usb/pnp/amdgpu)
//	genids bluetooth <company_identifiers.yaml> <out.gz>
//	genids cpu <intel-family.h> <amd.c> <cpu-curated.ids> <out.gz>
//	genids manifest <dir> [previous.json]   write <dir>/manifest.json
//	genids verify <dir> [previous.json]     re-check a bundle before signing
//	genids sign <manifest.json>             write <manifest.json>.sig; key from
//	                                        $HWSPEC_IDS_SIGNING_KEY (base64 seed)
//	genids keygen <private-key-file>        new ed25519 key; prints public key
//
// All outputs are deterministic, so unchanged upstream data gives
// byte-identical files and the bundle isn't republished for nothing.
package main

import (
	"bufio"
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
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/jiegui2025/hwspec/internal/ids"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes one command (without the program name) and returns the exit
// status: 0 success, 1 failure (including a command's wrong arguments), 2
// no command, or a command without any arguments.
func run(argv []string, stdout, stderr io.Writer) int {
	if len(argv) < 2 {
		fmt.Fprintln(stderr, "usage: genids jedec|oui|gzip SRC OUT.gz | manifest DIR [PREV] | sign MANIFEST | keygen KEYFILE")
		return 2
	}
	var err error
	switch cmd, args := argv[0], argv[1:]; cmd {
	case "jedec", "oui", "gzip", "bluetooth":
		if len(args) != 2 {
			err = fmt.Errorf("usage: genids %s SRC OUT.gz", cmd)
			break
		}
		err = convert(cmd, args[0], args[1])
	case "cpu":
		if len(args) != 4 {
			err = fmt.Errorf("usage: genids cpu INTEL-FAMILY.H AMD.C CURATED OUT.gz")
			break
		}
		var data []byte
		if data, err = cpu(args[0], args[1], args[2]); err == nil {
			err = writeGzip(args[3], data)
		}
	case "manifest":
		prev := ""
		if len(args) > 1 {
			prev = args[1]
		}
		err = manifest(stdout, args[0], prev)
	case "verify":
		prev := ""
		if len(args) > 1 {
			prev = args[1]
		}
		err = verify(stdout, args[0], prev)
	case "sign":
		err = sign(args[0])
	case "keygen":
		err = keygen(stdout, args[0])
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(stderr, "genids:", err)
		return 1
	}
	return 0
}

func convert(kind, src, out string) error {
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	var data []byte
	switch kind {
	case "jedec":
		data, err = jedec(in)
	case "oui":
		data, err = oui(in)
	case "bluetooth":
		data, err = btcompany(in)
	default:
		data = in
	}
	if err != nil {
		return err
	}
	return writeGzip(out, data)
}

func writeGzip(out string, data []byte) error {
	// gzip.Writer leaves the header timestamp zero, so this is deterministic.
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if _, err := zw.Write(data); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return os.WriteFile(out, buf.Bytes(), 0o644)
}

var (
	perlPage   = regexp.MustCompile(`(?s)\[(.*?)\]`)
	perlString = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
)

// jedec extracts the @vendors array of arrays from decode-dimms (one array
// per JEP106 bank, 126 names each) into "BANK ID<TAB>Name" lines, with the
// bank 1-based in decimal and the ID in hex without the parity bit.
func jedec(src []byte) ([]byte, error) {
	s := string(src)
	start := strings.Index(s, "@vendors = (")
	if start < 0 {
		return nil, fmt.Errorf("no @vendors table found")
	}
	s = s[start:]
	end := strings.Index(s, ");")
	if end < 0 {
		return nil, fmt.Errorf("@vendors table is not terminated")
	}
	s = s[:end]
	var b strings.Builder
	b.WriteString("# JEDEC JEP106 manufacturer IDs, from i2c-tools decode-dimms\n")
	for bank, page := range perlPage.FindAllStringSubmatch(s, -1) {
		for i, m := range perlString.FindAllStringSubmatch(page[1], -1) {
			fmt.Fprintf(&b, "%d %02X\t%s\n", bank+1, i+1, ids.CleanName(strings.ReplaceAll(m[1], `\"`, `"`)))
		}
	}
	return []byte(b.String()), nil
}

// oui keeps the "(base 16)" lines of the IEEE registry as "XXXXXX<TAB>Name".
func oui(src []byte) ([]byte, error) {
	entries := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(src))
	for sc.Scan() {
		prefix, name, ok := strings.Cut(sc.Text(), "(base 16)")
		if !ok {
			continue
		}
		if key := strings.TrimSpace(prefix); len(key) == 6 {
			entries[strings.ToUpper(key)] = ids.CleanName(name)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# IEEE MA-L (OUI) assignments\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s\t%s\n", k, entries[k])
	}
	return []byte(b.String()), nil
}

// manifest validates every *.ids.gz in dir and writes dir/manifest.json.
// A file without a header date keeps the date recorded for the same
// content in the previous manifest, or gets today's date if it changed.
func manifest(stdout io.Writer, dir, prevPath string) error {
	prev, err := readPrevManifest(prevPath)
	if err != nil {
		return err
	}
	m := ids.Manifest{
		Format:      ids.ManifestFormat,
		GeneratedAt: time.Now().UTC().Truncate(time.Second),
		Files:       map[string]ids.ManifestFile{},
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.ids.gz"))
	for _, path := range paths {
		name := filepath.Base(path)
		kind, ok := ids.KindForFile(name)
		if !ok {
			return fmt.Errorf("%s: not a known database file", name)
		}
		gz, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		zr, err := gzip.NewReader(bytes.NewReader(gz))
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		content, err := io.ReadAll(zr)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		entries, err := ids.Validate(kind, content)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if d := rawHeaderDate(content); d != "" && d > time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02") {
			return fmt.Errorf("%s: header date %s is in the future; refusing to publish (tampered or broken upstream?)", name, d)
		}
		// A large drop in entries means a truncated or replaced upstream
		// file. Publishing it would remove names for every user, so it
		// needs a person to look (HWSPEC_ALLOW_SHRINK=1 to accept).
		if p, ok := prevFile(prev, name); ok && p.Entries > 0 && entries < p.Entries*95/100 && os.Getenv("HWSPEC_ALLOW_SHRINK") != "1" {
			return fmt.Errorf("%s: %d entries, down from %d in the previous bundle (more than 5%%); set HWSPEC_ALLOW_SHRINK=1 if this is expected", name, entries, p.Entries)
		}
		sum := sha256.Sum256(gz)
		f := ids.ManifestFile{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(gz)), Entries: entries}
		f.Date = ids.HeaderDate(content)
		if f.Date == "" {
			f.Date = m.GeneratedAt.Format("2006-01-02")
			if p, ok := prevFile(prev, name); ok && p.SHA256 == f.SHA256 {
				f.Date = p.Date
			}
		}
		m.Files[name] = f
		fmt.Fprintf(stdout, "%-16s %6d entries  %s\n", name, entries, f.Date)
	}
	if len(m.Files) != len(ids.Kinds) {
		return fmt.Errorf("found %d database files in %s, want %d", len(m.Files), dir, len(ids.Kinds))
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "manifest.json"), append(out, '\n'), 0o644)
}

var headerDate = regexp.MustCompile(`(?m)^#\s*(?:Version|Date):\s*(\d{4})[.-](\d{2})[.-](\d{2})`)

// rawHeaderDate is the file's own "# Version:"/"# Date:" header, unfiltered
// (ids.HeaderDate ignores future dates; here they are an error).
func rawHeaderDate(content []byte) string {
	head := content
	if len(head) > 4096 {
		head = head[:4096]
	}
	m := headerDate.FindSubmatch(head)
	if m == nil {
		return ""
	}
	return string(m[1]) + "-" + string(m[2]) + "-" + string(m[3])
}

// readPrevManifest reads the previous bundle's manifest; a missing file
// means there was no previous bundle.
func readPrevManifest(path string) (*ids.Manifest, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m, err := ids.ParseManifest(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

func prevFile(m *ids.Manifest, name string) (ids.ManifestFile, bool) {
	if m == nil {
		return ids.ManifestFile{}, false
	}
	f, ok := m.Files[name]
	return f, ok
}

// verify re-checks a built bundle before it is signed, independently of
// the job that built it: every file listed in the manifest exists with
// that size and hash, parses into a plausible database, contains no
// control characters, and isn't much smaller than in the previous bundle;
// nothing unlisted is present; no date is in the future.
func verify(stdout io.Writer, dir, prevPath string) error {
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return err
	}
	m, err := ids.ParseManifest(b)
	if err != nil {
		return err
	}
	prev, err := readPrevManifest(prevPath)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if m.GeneratedAt.After(now.Add(24*time.Hour)) || m.GeneratedAt.Before(now.Add(-24*time.Hour)) {
		return fmt.Errorf("manifest generated_at %s is not within a day of now", m.GeneratedAt.Format(time.RFC3339))
	}
	tomorrow := now.AddDate(0, 0, 1).Format("2006-01-02")
	paths, _ := filepath.Glob(filepath.Join(dir, "*.ids.gz"))
	if len(paths) != len(m.Files) {
		return fmt.Errorf("%d database files but %d in the manifest", len(paths), len(m.Files))
	}
	for name, f := range m.Files {
		kind, ok := ids.KindForFile(name)
		if !ok {
			return fmt.Errorf("%s: not a known database file", name)
		}
		gz, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(gz)
		if int64(len(gz)) != f.Size || hex.EncodeToString(sum[:]) != f.SHA256 {
			return fmt.Errorf("%s: size or SHA-256 differs from the manifest", name)
		}
		zr, err := gzip.NewReader(bytes.NewReader(gz))
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		content, err := io.ReadAll(zr)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		n, err := ids.Validate(kind, content)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if n != f.Entries {
			return fmt.Errorf("%s: %d entries, but the manifest says %d", name, n, f.Entries)
		}
		if f.Date > tomorrow {
			return fmt.Errorf("%s: date %s is in the future", name, f.Date)
		}
		if bytes.ContainsFunc(content, func(r rune) bool { return r != '\n' && r != '\t' && r != '\r' && unicode.IsControl(r) }) {
			return fmt.Errorf("%s: contains control characters", name)
		}
		if p, ok := prevFile(prev, name); ok && p.Entries > 0 && n < p.Entries*95/100 && os.Getenv("HWSPEC_ALLOW_SHRINK") != "1" {
			return fmt.Errorf("%s: %d entries, down from %d in the previous bundle", name, n, p.Entries)
		}
	}
	fmt.Fprintf(stdout, "verified %d databases\n", len(m.Files))
	return nil
}

func sign(manifestPath string) error {
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("HWSPEC_IDS_SIGNING_KEY")))
	if err != nil || len(seed) != ed25519.SeedSize {
		return fmt.Errorf("HWSPEC_IDS_SIGNING_KEY must be a base64 %d-byte ed25519 seed", ed25519.SeedSize)
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	key := ed25519.NewKeyFromSeed(seed)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(key, data))
	if err := os.WriteFile(manifestPath+".sig", []byte(sig+"\n"), 0o644); err != nil {
		return err
	}
	// Check against the keys this build trusts, to catch a wrong secret.
	return ids.VerifySignature(data, []byte(sig))
}

func keygen(stdout io.Writer, path string) error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	seed := base64.StdEncoding.EncodeToString(priv.Seed())
	if err := os.WriteFile(path, []byte(seed+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Fprintln(stdout, base64.StdEncoding.EncodeToString(pub))
	return nil
}
