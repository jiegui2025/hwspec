package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jiegui2025/hwspec/internal/advisor"
	"github.com/jiegui2025/hwspec/internal/fwindex"
	"github.com/jiegui2025/hwspec/internal/ids"
	"github.com/jiegui2025/hwspec/internal/kb"
	"github.com/jiegui2025/hwspec/internal/output"
	"github.com/jiegui2025/hwspec/internal/report"
	"github.com/jiegui2025/hwspec/internal/resolve"
)

// Seams for tests.
var (
	machineIDPath = "/etc/machine-id"
	now           = time.Now
)

// advise prints advice about a saved capture, or about this machine when
// no file is given (ADR 0009). Facts about the machine are in the capture;
// only its maintenance state is read here, and only for this machine: a
// saved capture doesn't say which machine runs advise.
func (c cli) advise(args []string) error {
	fs := newFlags("advise")
	var outPath, format string
	var full, redact bool
	outputFlags(fs, &outPath, &format)
	fs.BoolVar(&full, "full", false, "")
	fs.BoolVar(&redact, "redact", false, "")
	file, err := fileArg(fs, args, false)
	if errors.Is(err, errArgs) || err == nil && full && file != "" {
		return errors.New("usage: hwspec advise [FILE] [--full] [--redact] [-o FILE] [-f text|json|yaml] (--full only without FILE)")
	}
	if err != nil {
		return err
	}
	if format, err = pickFormat(format, outPath, "text"); err != nil {
		return err
	}
	k, _, warnings, err := knowledgeBase()
	if err != nil {
		return err
	}

	in := advisor.Input{KB: k, Now: now()}
	switch {
	case file != "":
		data, err := c.readInput(file)
		if err != nil {
			return err
		}
		if in.Report, err = output.Read(data); err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
		// A newer format may have renamed what the checks read: advice
		// from it could be wrong, not just incomplete.
		if in.Report.SchemaVersion > report.SchemaVersion {
			return fmt.Errorf("%s uses schema %d, newer than this build understands (%d); update hwspec to advise on it",
				file, in.Report.SchemaVersion, report.SchemaVersion)
		}
		warnings = append(warnings, "advice about a saved capture doesn't use a maintenance record: that belongs to the machine running advise")
	case full && geteuid() != 0:
		if in.Report, err = c.captureAsRoot("advise"); err != nil {
			return err
		}
		resolve.Names(in.Report)
	default:
		in.Report = collectReport(fullVersion())
	}
	c.warnIDSources()
	var warn string
	if in.LinuxFirmware, warn = linuxFirmwareIndex(); warn != "" {
		warnings = append(warnings, warn)
	}
	var lvfsWarnings []string
	in.LVFS, lvfsWarnings = lvfsIndex(in.Now)
	warnings = append(warnings, lvfsWarnings...)
	if file == "" {
		in.Live = true
		var warn string
		if in.State, warn = loadState(redact); warn != "" {
			warnings = append(warnings, warn)
		}
	}
	if redact {
		in.Report.Redact()
	}

	a := advisor.Advise(in)
	a.Warnings = append(warnings, a.Warnings...)

	var buf bytes.Buffer
	switch format {
	case "json":
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")
		err = enc.Encode(a)
	case "yaml":
		err = output.YAML(&buf, a)
	default:
		err = advisor.WriteText(&buf, a)
	}
	if err != nil {
		return err
	}
	if outPath == "" || outPath == "-" {
		_, err = c.stdout.Write(buf.Bytes())
		return err
	}
	// Advice names the machine's devices: as private as an unredacted
	// capture, even when redacted (the umask can only tighten this).
	if err := writeFileAtomic(outPath, buf.Bytes(), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(c.stderr, "hwspec: wrote %s (%s, %s, %s)\n", outPath, format, count(len(a.Findings), "finding"), count(len(a.Warnings), "warning"))
	return nil
}

// linuxFirmwareIndex is linux-firmware's WHENCE from `hwspec firmware
// update`'s cache, as the advisor takes it: each file's and link's
// version. Without one, the advisor says how to get it; one that can't be
// used gets a warning saying why.
func linuxFirmwareIndex() (*advisor.LinuxFirmware, string) {
	w, src, err := fwindex.ReadWhence(fwindex.CacheDir())
	if err != nil {
		return nil, fmt.Sprintf("the firmware index can't be used (%v); run `hwspec firmware update` (with --allow-older if it asks)", err)
	}
	if w == nil {
		return nil, ""
	}
	versions := map[string]string{}
	for path, f := range w.Files {
		if f.Version != "" {
			versions[path] = f.Version
		}
	}
	for link, target := range w.Links {
		if v := w.Files[target].Version; v != "" {
			versions[link] = v
		}
	}
	return &advisor.LinuxFirmware{Tag: src.Tag, FetchedAt: src.FetchedAt, Versions: versions}, ""
}

// catalogueFile is a verified catalogue on disk (fwindex.CatalogueFile).
type catalogueFile interface {
	Signed() time.Time
	Matches(*fwindex.Source) error
	Parse() (*fwindex.Catalogue, error)
}

// fwupd's copy of LVFS's catalogue, and the opener; tests replace them.
var (
	fwupdCatalogue = fwindex.FwupdCatalogue
	openCatalogue  = func(path string, now time.Time) (catalogueFile, error) { return fwindex.OpenCatalogue(path, now) }
)

// lvfsIndex is LVFS's catalogue for the advisor (ADR 0012): hwspec's own
// copy or fwupd's, whichever was signed later, each verified as a
// download would be. hwspec's must be the one its last update installed
// (the manifest's checksums and signing time), so an older copy put back
// isn't used. A catalogue signed over 30 days ago isn't used, as `hwspec
// firmware update` wouldn't install it; over 7 days, it is used with a
// warning. Without one, the advisor says how to get one.
func lvfsIndex(now time.Time) (*advisor.LVFS, []string) {
	var warnings []string
	type candidate struct {
		f            catalogueFile
		source, name string
	}
	var found []candidate
	open := func(path, source, name string) {
		f, err := openCatalogue(path, now)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s LVFS catalogue can't be used (%v); run `hwspec firmware update`", name, err))
			return
		}
		found = append(found, candidate{f, source, name})
	}
	if dir := fwindex.CacheDir(); dir != "" {
		m, err := fwindex.ReadManifest(dir)
		switch {
		case err != nil:
			warnings = append(warnings, fmt.Sprintf("the firmware index can't be used (%v); run `hwspec firmware update --allow-older`", err))
		case m != nil && m.LVFS != nil:
			if open(filepath.Join(dir, fwindex.CatalogueName), "hwspec", "hwspec's"); len(found) > 0 {
				if err := found[0].f.Matches(m.LVFS); err != nil {
					warnings = append(warnings, fmt.Sprintf("hwspec's LVFS catalogue can't be used (%v); run `hwspec firmware update`", err))
					found = nil
				}
			}
		}
	}
	switch _, err := os.Stat(fwupdCatalogue); {
	case err == nil:
		open(fwupdCatalogue, "fwupd", "fwupd's")
	case !errors.Is(err, fs.ErrNotExist):
		warnings = append(warnings, fmt.Sprintf("fwupd's LVFS catalogue can't be used (%v)", err))
	}
	// The latest signed first; an equal one from hwspec's own cache.
	slices.SortStableFunc(found, func(a, b candidate) int { return b.f.Signed().Compare(a.f.Signed()) })
	for _, c := range found {
		age := now.Sub(c.f.Signed())
		if age > fwindex.TooOld {
			warnings = append(warnings, fmt.Sprintf("%s LVFS catalogue was signed %d days ago, over 30: run `hwspec firmware update` for a current one", c.name, int(age.Hours()/24)))
			return nil, warnings
		}
		cat, err := c.f.Parse()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s LVFS catalogue can't be used (%v); run `hwspec firmware update`", c.name, err))
			continue
		}
		if age > fwindex.StaleAfter {
			warnings = append(warnings, fmt.Sprintf("%s LVFS catalogue was signed %d days ago; run `hwspec firmware update` for a current one", c.name, int(age.Hours()/24)))
		}
		return lvfsInput(cat, c.source, c.f.Signed()), warnings
	}
	return nil, warnings
}

// lvfsInput hands the catalogue to the advisor by GUID.
func lvfsInput(c *fwindex.Catalogue, source string, signedAt time.Time) *advisor.LVFS {
	lv := &advisor.LVFS{Source: source, SignedAt: signedAt, Components: map[string][]advisor.LVFSComponent{}}
	for _, comp := range c.Components {
		a := advisor.LVFSComponent{ID: comp.ID, Name: comp.Name, Developer: comp.Developer, VersionFormat: comp.VersionFormat}
		var requires []advisor.LVFSRequirement
		for _, q := range comp.Requires {
			requires = append(requires, advisor.LVFSRequirement{Kind: q.Kind, Compare: q.Compare, Version: q.Version, Text: q.Text})
		}
		for _, r := range comp.Releases {
			a.Releases = append(a.Releases, advisor.LVFSRelease{Version: r.Version, Date: r.Date, Urgency: r.Urgency, CVEs: r.CVEs, Requires: requires})
		}
		for _, g := range comp.GUIDs {
			lv.Components[g] = append(lv.Components[g], a)
		}
	}
	return lv
}

// count says "1 finding", "2 findings".
func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// errArgs is a command's arguments, not its flags, being wrong.
var errArgs = errors.New("wrong arguments")

// fileArg takes the one FILE argument a command accepts, before or after
// its flags. required says whether it may be left out. Flag errors come
// back as they are; a missing or extra argument is errArgs.
func fileArg(flags *flag.FlagSet, args []string, required bool) (string, error) {
	var file string
	if len(args) > 0 && (!strings.HasPrefix(args[0], "-") || args[0] == "-") {
		file, args = args[0], args[1:]
	}
	if err := flags.Parse(args); err != nil {
		return "", err
	}
	if file == "" && flags.NArg() == 1 {
		file = flags.Arg(0)
	} else if flags.NArg() > 0 {
		return "", errArgs
	}
	if required && file == "" {
		return "", errArgs
	}
	return file, nil
}

// readInput reads the file a command was given; "-" is stdin.
func (c cli) readInput(file string) ([]byte, error) {
	in := c.stdin
	if file != "-" {
		f, err := os.Open(file) //nolint:gosec // G703: reading the file the user named is the point
		if err != nil {
			return nil, err
		}
		defer f.Close()
		in = f
	}
	// Bounded: /dev/zero or a huge wrong file must fail, not exhaust memory.
	data, err := io.ReadAll(io.LimitReader(in, output.MaxCaptureSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > output.MaxCaptureSize {
		return nil, fmt.Errorf("%s: larger than %d MiB: not a hwspec capture", file, output.MaxCaptureSize>>20)
	}
	return data, nil
}

// appID is hwspec's application ID for systemd's app-specific machine IDs.
// It is public and fixed: changing it would orphan every state file.
var appID = [16]byte{0x32, 0x86, 0x82, 0x0f, 0x74, 0x15, 0x4a, 0x5d, 0xb0, 0x6c, 0x16, 0xd2, 0xc1, 0xd8, 0x58, 0xfa}

// stateKey names a machine's state file without revealing its machine-id,
// as machine-id(5) asks ("hashed with a cryptographic, keyed hash function,
// using a fixed, application-specific key"), the way systemd's
// sd_id128_get_machine_app_specific() does: HMAC-SHA256 keyed with the
// machine ID's 16 bytes over the application ID, the first 16 bytes made a
// v4 UUID, as 32 hex digits. `systemd-id128 -a
// 3286820f74154a5db06c16d2c1d858fa machine-id` prints the same. machineID
// is the 32 hex digits of /etc/machine-id.
func stateKey(machineID string) (string, error) {
	id, err := hex.DecodeString(machineID)
	if err != nil || len(id) != 16 {
		return "", fmt.Errorf("%s isn't 32 hex digits", machineIDPath)
	}
	mac := hmac.New(sha256.New, id)
	mac.Write(appID[:])
	key := mac.Sum(nil)[:16]
	key[6] = key[6]&0x0f | 0x40 // version 4
	key[8] = key[8]&0x3f | 0x80 // RFC 4122 variant
	return hex.EncodeToString(key), nil
}

// lookupUser finds a user's home; a seam for tests.
var lookupUser = user.LookupId

// stateOwner is whose state advise reads: the user running it, or under
// sudo the invoking user, as output files are theirs (ADR 0009).
func stateOwner() (uid int, home string, sudo bool, err error) {
	if u, _, ok := sudoUser(); ok {
		usr, err := lookupUser(strconv.Itoa(u))
		if err != nil {
			return 0, "", true, fmt.Errorf("can't find the home of the user running sudo: %w", err)
		}
		return u, usr.HomeDir, true, nil
	}
	home, err = os.UserHomeDir()
	return os.Getuid(), home, false, err
}

// statePath is $XDG_STATE_HOME/hwspec/machines/<key>.json (default
// ~/.local/state; under sudo, the invoking user's ~/.local/state). An
// unreadable or malformed machine-id is an error: the state couldn't be
// told apart from another machine's.
func statePath() (path string, uid int, err error) {
	data, err := os.ReadFile(machineIDPath)
	if err != nil {
		return "", 0, err
	}
	key, err := stateKey(strings.TrimSpace(string(data)))
	if err != nil {
		return "", 0, err
	}
	uid, home, sudo, err := stateOwner()
	if err != nil {
		return "", 0, err
	}
	base := os.Getenv("XDG_STATE_HOME")
	if sudo || !filepath.IsAbs(base) { // root's environment isn't the user's; relative paths are ignored (XDG)
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "hwspec", "machines", key+".json"), uid, nil
}

// maxStateSize bounds the state file: a record of maintenance tasks, not a
// place for anything large.
const maxStateSize = 1 << 20

// loadState reads this machine's maintenance state. No state file yet is
// an empty state; anything that stops it being read gives no state and a
// warning, and advice goes on without it. Under --redact the warning
// doesn't name the file: its path holds the user's home and the machine's
// key.
func loadState(redact bool) (*advisor.State, string) {
	path, uid, err := statePath()
	if err != nil {
		if redact {
			return nil, "maintenance record unavailable"
		}
		return nil, "maintenance record unavailable: " + err.Error()
	}
	name := path
	if redact {
		name = "it" // "maintenance record unavailable: it isn't valid JSON"
	}
	data, err := readStateFile(path, uid)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return &advisor.State{Format: advisor.StateFormat}, ""
	case err != nil:
		return nil, fmt.Sprintf("maintenance record unavailable: %s %s", name, err)
	}
	var st advisor.State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Sprintf("maintenance record unavailable: %s isn't valid JSON", name) // the parser's message could quote the file
	}
	if st.Format != advisor.StateFormat {
		return nil, fmt.Sprintf("maintenance record unavailable: %s has format %d, this build reads %d", name, st.Format, advisor.StateFormat)
	}
	var kept []advisor.Done
	for _, d := range st.Done {
		if _, err := time.Parse("2006-01-02", d.Date); err == nil && d.Task != "" {
			kept = append(kept, d)
		}
	}
	var warn string
	if dropped := len(st.Done) - len(kept); dropped > 0 {
		warn = fmt.Sprintf("maintenance record: %d entries without a task or a YYYY-MM-DD date were ignored", dropped)
	}
	st.Done = kept
	return &st, warn
}

// readStateFile reads the state only if it's a regular file the user owns,
// opened without following a symlink or blocking on a FIFO, and at most
// maxStateSize bytes. Its errors never quote the file.
func readStateFile(path string, uid int) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	switch {
	case errors.Is(err, syscall.ELOOP):
		return nil, errors.New("is a symlink")
	case errors.Is(err, os.ErrNotExist):
		return nil, err
	case err != nil:
		return nil, errors.New("can't be opened")
	}
	defer f.Close()
	st, err := f.Stat()
	switch {
	case err != nil:
		return nil, errors.New("can't be read")
	case !st.Mode().IsRegular():
		return nil, errors.New("isn't a regular file")
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && int(sys.Uid) != uid {
		return nil, errors.New("belongs to another user")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxStateSize+1))
	switch {
	case err != nil:
		return nil, errors.New("can't be read")
	case len(data) > maxStateSize:
		return nil, fmt.Errorf("is larger than %d bytes", maxStateSize)
	}
	return data, nil
}

// syncedAdvisor is the knowledge base `hwspec ids update` installed;
// tests replace it.
var syncedAdvisor = ids.SyncedAdvisor

// knowledgeBase is the newer of the built-in knowledge base and the one
// `hwspec ids update` installed, by version (the UTC time their content
// last changed, unique per content); the built-in one wins a tie, which
// is then the same content. A synced copy that can't be read or parsed falls back to
// the built-in one, with a warning. source names the copy used.
func knowledgeBase() (k *kb.KB, source string, warnings []string, err error) {
	k, err = kb.Embedded()
	if err != nil {
		return nil, "", nil, err
	}
	b, signed, err := syncedAdvisor()
	switch {
	case err != nil:
		return k, "embedded", []string{"knowledge base: the synced copy isn't used (" + err.Error() + "); the built-in one is"}, nil
	case b == nil:
		return k, "embedded", nil, nil
	}
	synced, err := kb.Parse(b)
	if err != nil {
		return k, "embedded", []string{"knowledge base: the synced copy can't be read (" + err.Error() + "); the built-in one is used"}, nil
	}
	// The version decides which copy is newer, so it must be the one the
	// signed manifest gives, not only the file's own word.
	if synced.Version != signed {
		return k, "embedded", []string{fmt.Sprintf("knowledge base: the synced copy's version %s isn't the signed manifest's %s; the built-in one is used", synced.Version, signed)}, nil
	}
	if synced.Version > k.Version {
		return synced, filepath.Join(ids.SyncedDir(), ids.AdvisorFile), nil, nil
	}
	return k, "embedded", nil, nil
}
