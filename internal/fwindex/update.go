// Package fwindex keeps a verified index of available firmware: LVFS's
// signed catalogue and linux-firmware's WHENCE at its latest release, in
// the user's cache (ADR 0012). Only `hwspec firmware update` fills it, and
// only fetch.go reaches the network; the parsers and checks are pure.
package fwindex

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"maps"
	"time"
)

// Where the sources are, fixed (ADR 0012): the update never asks another
// host, and follows no redirect to one.
const (
	LVFSBase          = "https://cdn.fwupd.org/downloads/"
	LinuxFirmwareBase = "https://git.kernel.org/pub/scm/linux/kernel/git/firmware/linux-firmware.git/"
)

// Statuses of a source after an update.
const (
	Updated   = "updated"   // verified, and installed (or, with DryRun, would be)
	Unchanged = "unchanged" // the cached copy is the latest
	Refused   = "refused"   // fetched, but failed a check; nothing installed
	Failed    = "failed"    // couldn't be fetched
)

type Options struct {
	Dir        string // the cache directory, usually CacheDir()
	UserAgent  string
	DryRun     bool // fetch and verify, but write nothing
	AllowOlder bool // accept data older than the cached copy

	// Tests point these elsewhere.
	lvfsBase, kernelBase string
	roots                *x509.CertPool
	get                  getter
	now                  time.Time
}

// Result is one source's outcome.
type Result struct {
	Source  string    // "LVFS" or "linux-firmware"
	Status  string    // Updated, Unchanged, Refused or Failed
	Reason  string    // why it was refused or failed
	Warning string    // about data that was accepted (LVFS: signed long ago)
	Date    time.Time // the data's date: LVFS: when it was signed; linux-firmware: the release date
	Tag     string    // linux-firmware: the release tag
	// LVFS: how many components the catalogue lists.
	Components int
}

// Update fetches and verifies each source, then installs the ones that
// changed and passed every check. A source that fails leaves its cached
// copy as it was; the other source is still updated.
func Update(ctx context.Context, opt Options) ([]Result, error) {
	if opt.Dir == "" {
		return nil, errors.New("no home directory to keep the firmware index in")
	}
	if opt.lvfsBase == "" {
		opt.lvfsBase = LVFSBase
	}
	if opt.kernelBase == "" {
		opt.kernelBase = LinuxFirmwareBase
	}
	if opt.roots == nil {
		roots, err := lvfsRoots()
		if err != nil {
			return nil, err
		}
		opt.roots = roots
	}
	if opt.get == nil {
		opt.get = httpGetter(opt.UserAgent)
	}
	if opt.now.IsZero() {
		opt.now = time.Now().UTC()
	}
	old, err := ReadManifest(opt.Dir)
	if err != nil && !opt.AllowOlder {
		// Without it there is no telling a rollback.
		return nil, fmt.Errorf("cached manifest unreadable (%w); rerun with --allow-older to replace the cache", err)
	}
	if old == nil {
		old = &Manifest{}
	}
	u := updater{opt: opt, old: old}
	next := Manifest{Format: manifestFormat}
	files := map[string][]byte{}
	var results []Result
	for _, s := range []struct {
		run  func(context.Context) (Result, *Source, map[string][]byte)
		old  *Source
		next **Source
	}{{u.lvfs, old.LVFS, &next.LVFS}, {u.linuxFirmware, old.LinuxFirmware, &next.LinuxFirmware}} {
		r, src, got := s.run(ctx)
		results = append(results, r)
		*s.next = s.old
		if src != nil {
			*s.next = src
		}
		maps.Copy(files, got)
	}
	if opt.DryRun || next.LVFS == nil && next.LinuxFirmware == nil {
		return results, nil
	}
	if err := install(opt.Dir, files, &next); err != nil {
		return results, fmt.Errorf("installing into %s: %w", opt.Dir, err)
	}
	return results, nil
}

type updater struct {
	opt Options
	old *Manifest
}

// How old a signed catalogue may be. LVFS publishes several a day, so an
// older one means a CDN serving a stale copy, or a replay.
const (
	staleAfter = 7 * 24 * time.Hour  // warned about
	tooOld     = 30 * 24 * time.Hour // refused without AllowOlder
)

func (u updater) lvfs(ctx context.Context) (Result, *Source, map[string][]byte) {
	r := Result{Source: "LVFS"}
	cached := u.old.LVFS
	intact := cached.intact(u.opt.Dir)
	etag := ""
	if intact {
		etag = cached.ETag
	}
	jcat, err := u.opt.get(ctx, u.opt.lvfsBase+JcatName, etag, maxJcatBytes)
	if err != nil {
		return failed(r, err), nil, nil
	}
	if jcat.notModified {
		r.Status, r.Date, r.Components = Unchanged, cached.SignedAt, cached.Components
		r.Warning = u.staleness(cached.SignedAt)
		fresh := *cached
		fresh.FetchedAt = u.opt.now
		return r, &fresh, nil
	}
	zst, err := u.opt.get(ctx, u.opt.lvfsBase+CatalogueName, "", maxCatalogueBytes)
	if err != nil {
		return failed(r, err), nil, nil
	}
	signedAt, err := verifyCatalogue(zst.body, jcat.body, u.opt.roots, u.opt.now)
	if err != nil {
		return refused(r, err), nil, nil
	}
	r.Date = signedAt
	switch {
	case cached != nil && signedAt.Before(cached.SignedAt) && !u.opt.AllowOlder:
		return refused(r, fmt.Errorf("signed %s, before the cached catalogue (%s); --allow-older replaces it",
			stamp(signedAt), stamp(cached.SignedAt))), nil, nil
	case u.opt.now.Sub(signedAt) > tooOld && !u.opt.AllowOlder:
		return refused(r, fmt.Errorf("signed %s, %d days ago; LVFS publishes several a day, so this is a stale or replayed copy; --allow-older accepts it",
			stamp(signedAt), days(u.opt.now.Sub(signedAt)))), nil, nil
	}
	_, n, err := DecodeCatalogue(zst.body)
	if err != nil {
		return refused(r, err), nil, nil
	}
	r.Components = n
	switch {
	case n < minComponents:
		return refused(r, fmt.Errorf("lists %d components, fewer than the %d of LVFS's complete catalogue (another LVFS catalogue, such as firmware-testing, passed off as this one?)",
			n, minComponents)), nil, nil
	case cached != nil && float64(n) < float64(cached.Components)*(1-maxShrink) && !u.opt.AllowOlder:
		return refused(r, fmt.Errorf("lists %d components, %d fewer than the cached catalogue; --allow-older accepts it",
			n, cached.Components-n)), nil, nil
	}
	r.Warning = u.staleness(signedAt)
	src := &Source{
		URL: u.opt.lvfsBase + CatalogueName, FetchedAt: u.opt.now, SignedAt: signedAt, Components: n, ETag: jcat.etag,
		Files: map[string]string{CatalogueName: sha(zst.body), JcatName: sha(jcat.body)},
	}
	if intact && sameFiles(cached, src) {
		r.Status = Unchanged
		return r, src, nil
	}
	r.Status = Updated
	return r, src, map[string][]byte{CatalogueName: zst.body, JcatName: jcat.body}
}

// staleness warns about a catalogue signed more than a week ago.
func (u updater) staleness(signedAt time.Time) string {
	age := u.opt.now.Sub(signedAt)
	if age <= staleAfter {
		return ""
	}
	return fmt.Sprintf("the newest catalogue offered was signed %d days ago, though LVFS publishes several a day: the CDN may be serving a stale copy", days(age))
}

func days(d time.Duration) int { return int(d / (24 * time.Hour)) }

func (u updater) linuxFirmware(ctx context.Context) (Result, *Source, map[string][]byte) {
	r := Result{Source: "linux-firmware"}
	cached := u.old.LinuxFirmware
	refs, err := u.opt.get(ctx, u.opt.kernelBase+"info/refs", "", maxRefsBytes)
	if err != nil {
		return failed(r, err), nil, nil
	}
	tag, commit, err := latestRelease(refs.body, u.opt.now)
	if err != nil {
		return refused(r, err), nil, nil
	}
	r.Tag = tag
	r.Date, _ = time.Parse("20060102", tag)
	if cached != nil && tag < cached.Tag && !u.opt.AllowOlder {
		return refused(r, fmt.Errorf("latest release %s is older than the cached %s; --allow-older replaces it", tag, cached.Tag)), nil, nil
	}
	if cached != nil && cached.Commit == commit && cached.intact(u.opt.Dir) {
		r.Status = Unchanged
		fresh := *cached
		fresh.FetchedAt = u.opt.now
		return r, &fresh, nil
	}
	url := u.opt.kernelBase + "plain/" + WhenceName + "?id=" + commit
	whence, err := u.opt.get(ctx, url, "", maxWhenceBytes)
	if err != nil {
		return failed(r, err), nil, nil
	}
	if _, err := ParseWhence(whence.body); err != nil {
		return refused(r, err), nil, nil
	}
	r.Status = Updated
	src := &Source{URL: url, FetchedAt: u.opt.now, Tag: tag, Commit: commit,
		Files: map[string]string{WhenceName: sha(whence.body)}}
	return r, src, map[string][]byte{WhenceName: whence.body}
}

func failed(r Result, err error) Result {
	r.Status, r.Reason = Failed, err.Error()
	return r
}

func refused(r Result, err error) Result {
	r.Status, r.Reason = Refused, err.Error()
	return r
}

func sameFiles(a, b *Source) bool {
	if len(a.Files) != len(b.Files) {
		return false
	}
	for name, sum := range b.Files {
		if a.Files[name] != sum {
			return false
		}
	}
	return true
}

func stamp(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") }
