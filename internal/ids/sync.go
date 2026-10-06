package ids

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DefaultSyncURL is the published bundle: a GitHub release whose assets are
// refreshed weekly by the repository's ids workflow.
const DefaultSyncURL = "https://github.com/jiegui2025/hwspec/releases/download/ids-latest/"

// SyncedDir is where `hwspec ids update` stores databases.
func SyncedDir() string { return syncedDir }

type UpdateOptions struct {
	BaseURL   string // defaults to DefaultSyncURL
	UserAgent string
	DryRun    bool // report what would change without writing
	// AllowOlder accepts a bundle older than the one already synced
	// (normally refused, so a stale or replayed bundle can't roll names back).
	AllowOlder bool
}

type FileUpdate struct {
	File    string
	Status  string // "updated", "new", "unchanged"
	Date    string
	Entries int
}

const (
	maxManifestBytes = 1 << 20
	maxFileBytes     = 64 << 20
)

// Update downloads the signed bundle manifest, verifies it, then fetches
// and verifies each database that differs from the synced copy. Nothing is
// written unless every changed file passes its checks.
// ErrBuiltInIsNewer means the published bundle is older than the
// databases built into this hwspec: there is nothing newer to install.
var ErrBuiltInIsNewer = errors.New("the databases built into hwspec are newer than the published bundle")

// UpdateResult describes an update (or, with DryRun, what it would do).
type UpdateResult struct {
	Files    []FileUpdate
	BundleAt time.Time // when the published bundle was built
}

func Update(ctx context.Context, opt UpdateOptions) (*UpdateResult, error) {
	if syncedDir == "" {
		return nil, errors.New("no home directory to store synced databases in")
	}
	base := opt.BaseURL
	if base == "" {
		base = DefaultSyncURL
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	fetch := func(name string, limit int64) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+name, nil)
		if err != nil {
			return nil, err
		}
		if opt.UserAgent != "" {
			req.Header.Set("User-Agent", opt.UserAgent)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: HTTP %s", name, resp.Status)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if int64(len(b)) > limit {
			return nil, fmt.Errorf("%s: larger than %d bytes", name, limit)
		}
		return b, nil
	}

	manifestBytes, err := fetch("manifest.json", maxManifestBytes)
	if err != nil {
		return nil, err
	}
	sig, err := fetch("manifest.json.sig", 4096)
	if err != nil {
		return nil, err
	}
	if err := VerifySignature(manifestBytes, sig); err != nil {
		return nil, err
	}
	remote, err := ParseManifest(manifestBytes)
	if err != nil {
		return nil, err
	}
	local, err := syncedManifest()
	if err != nil && !opt.AllowOlder {
		// Without the local manifest we can't tell whether this is a rollback.
		return nil, fmt.Errorf("synced manifest unreadable (%w); rerun with --allow-older to replace the synced databases", err)
	}
	if !opt.AllowOlder {
		if local != nil && remote.GeneratedAt.Before(local.GeneratedAt) {
			return nil, fmt.Errorf("published bundle (%s) is older than the synced one (%s); refusing to roll back",
				remote.GeneratedAt.Format(time.RFC3339), local.GeneratedAt.Format(time.RFC3339))
		}
		if emb := embeddedManifest(); emb != nil && remote.GeneratedAt.Before(emb.GeneratedAt) {
			return &UpdateResult{BundleAt: remote.GeneratedAt}, ErrBuiltInIsNewer
		}
	}

	names := make([]string, 0, len(remote.Files))
	for name := range remote.Files {
		names = append(names, name)
	}
	sort.Strings(names)

	var results []FileUpdate
	downloads := map[string][]byte{}
	for _, name := range names {
		want := remote.Files[name]
		kind, ok := KindForFile(name)
		if !ok && name != AdvisorFile {
			continue // a database or knowledge-base format this build doesn't know
		}
		res := FileUpdate{File: name, Date: want.Date, Entries: want.Entries, Status: "new"}
		path := filepath.Join(syncedDir, name)
		if cur, err := os.ReadFile(path); err == nil {
			res.Status = "updated"
			if sha(cur) == want.SHA256 {
				res.Status = "unchanged"
				results = append(results, res)
				continue
			}
		}
		results = append(results, res)
		if opt.DryRun {
			continue
		}
		gz, err := fetch(name, maxFileBytes)
		if err != nil {
			return nil, err
		}
		if int64(len(gz)) != want.Size || sha(gz) != want.SHA256 {
			return nil, fmt.Errorf("%s: checksum mismatch, not installing", name)
		}
		if name == AdvisorFile {
			// Checked as far as this package can; the CLI parses it, and
			// falls back to the built-in copy if it doesn't.
			version, rules, err := CheckAdvisor(name, gz)
			if err != nil {
				return nil, err
			}
			// The signed manifest says what it is: a file that disagrees,
			// or claims a version from the future, isn't installed.
			switch {
			case version != want.Date || rules != want.Entries:
				return nil, fmt.Errorf("%s: version %s with %d rules, but the manifest says %s with %d", name, version, rules, want.Date, want.Entries)
			case version > time.Now().UTC().Add(24*time.Hour).Format(advisorVersion):
				return nil, fmt.Errorf("%s: version %s is in the future", name, version)
			}
			downloads[name] = gz
			continue
		}
		zr, err := gzip.NewReader(bytes.NewReader(gz))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		content, err := io.ReadAll(io.LimitReader(zr, 4*maxFileBytes))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if _, err := Validate(kind, content); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		downloads[name] = gz
	}
	if opt.DryRun {
		return &UpdateResult{Files: results, BundleAt: remote.GeneratedAt}, nil
	}

	// Every file checked out; install them, the manifest last, so a crash
	// midway leaves the old manifest (and dates) describing older files at
	// worst, which only makes hwspec prefer other sources.
	if err := os.MkdirAll(syncedDir, 0o750); err != nil {
		return nil, err
	}
	var written []string
	install := func(name string, data []byte) error {
		if err := writeAtomic(filepath.Join(syncedDir, name), data); err != nil {
			return &InstallError{Written: written, Err: err}
		}
		written = append(written, name)
		return nil
	}
	sorted := make([]string, 0, len(downloads))
	for name := range downloads {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)
	for _, name := range sorted {
		if err := install(name, downloads[name]); err != nil {
			return nil, err
		}
	}
	if err := install("manifest.json.sig", sig); err != nil {
		return nil, err
	}
	if err := install("manifest.json", manifestBytes); err != nil {
		return nil, err
	}
	// The renames themselves are on disk once the directory is synced.
	if err := syncDir(syncedDir); err != nil {
		return nil, &InstallError{Written: written, Err: err}
	}
	Reset()
	return &UpdateResult{Files: results, BundleAt: remote.GeneratedAt}, nil
}

// InstallError is returned when installing failed after verification, so
// some files may already have been replaced. Until a later update succeeds,
// the previous manifest keeps describing them; hwspec then dates them by it.
type InstallError struct {
	Written []string
	Err     error
}

func (e *InstallError) Error() string {
	if len(e.Written) == 0 {
		return fmt.Sprintf("installing failed before any file was replaced: %v", e.Err)
	}
	return fmt.Sprintf("installing failed after replacing %s: %v", strings.Join(e.Written, ", "), e.Err)
}

func (e *InstallError) Unwrap() error { return e.Err }

// SyncedAt reports when the synced bundle was built: zero if nothing has
// been synced, or an error if the saved manifest can't be read.
func SyncedAt() (time.Time, error) {
	m, err := syncedManifest()
	if err != nil || m == nil {
		return time.Time{}, err
	}
	return m.GeneratedAt, nil
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	// On disk before the rename: after a power loss the manifest is the
	// old one or the new one, never empty.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
