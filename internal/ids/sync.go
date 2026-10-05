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
func Update(ctx context.Context, opt UpdateOptions) ([]FileUpdate, error) {
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
	local := syncedManifest()
	if local != nil && remote.GeneratedAt.Before(local.GeneratedAt) && !opt.AllowOlder {
		return nil, fmt.Errorf("published bundle (%s) is older than the synced one (%s); refusing to roll back",
			remote.GeneratedAt.Format(time.RFC3339), local.GeneratedAt.Format(time.RFC3339))
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
		if !ok {
			continue // a database this build doesn't know yet
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
		return results, nil
	}

	// Every file checked out; install them, the manifest last, so a crash
	// midway leaves the old manifest (and dates) describing older files at
	// worst, which only makes hwspec prefer other sources.
	if err := os.MkdirAll(syncedDir, 0o750); err != nil {
		return nil, err
	}
	for name, gz := range downloads {
		if err := writeAtomic(filepath.Join(syncedDir, name), gz); err != nil {
			return nil, err
		}
	}
	if err := writeAtomic(filepath.Join(syncedDir, "manifest.json.sig"), sig); err != nil {
		return nil, err
	}
	if err := writeAtomic(filepath.Join(syncedDir, "manifest.json"), manifestBytes); err != nil {
		return nil, err
	}
	Reset()
	return results, nil
}

// SyncedAt reports when the synced bundle was built, or zero if none.
func SyncedAt() time.Time {
	if m := syncedManifest(); m != nil {
		return m.GeneratedAt
	}
	return time.Time{}
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
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
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
