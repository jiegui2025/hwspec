package fwindex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Files in the cache directory.
const (
	JcatName     = CatalogueName + ".jcat"
	WhenceName   = "WHENCE"
	manifestName = "manifest.json"
)

const manifestFormat = 1

// Manifest describes what `hwspec firmware update` cached, per source. It
// is written last, so after a crash it describes older files, which a
// reader notices by their checksums.
type Manifest struct {
	Format        int     `json:"format"`
	LVFS          *Source `json:"lvfs,omitempty"`
	LinuxFirmware *Source `json:"linux_firmware,omitempty"`
}

// Source is one source's cached state.
type Source struct {
	URL       string    `json:"url"`
	FetchedAt time.Time `json:"fetched_at"`         // when the source was last asked; not the data's date
	SignedAt  time.Time `json:"signed_at,omitzero"` // LVFS: the signature's signing time, the data's date
	// LVFS: how many components the catalogue lists, so a much smaller
	// one is noticed.
	Components int               `json:"components,omitempty"`
	Tag        string            `json:"tag,omitempty"`    // linux-firmware: the release tag
	Commit     string            `json:"commit,omitempty"` // linux-firmware: the tag's commit
	ETag       string            `json:"etag,omitempty"`   // for a conditional request next time
	Files      map[string]string `json:"files"`            // cached file → its SHA-256
}

// CacheDir is where the index is kept: $XDG_CACHE_HOME/hwspec/firmware,
// or ~/.cache/hwspec/firmware. It is empty without a home directory.
func CacheDir() string {
	base := os.Getenv("XDG_CACHE_HOME")
	if !filepath.IsAbs(base) { // unset, or relative, which XDG says to ignore
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "hwspec", "firmware")
}

// ReadManifest reads dir's manifest: nil without an error when nothing has
// been cached.
func ReadManifest(dir string) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, manifestName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", manifestName, err)
	}
	if m.Format != manifestFormat {
		return nil, fmt.Errorf("%s: format %d, want %d", manifestName, m.Format, manifestFormat)
	}
	return &m, nil
}

// ReadWhence reads linux-firmware's WHENCE from the cache in dir, with
// its manifest entry: nil without an error when none has been fetched.
// WHENCE comes over TLS without a signature (ADR 0012), so the check here
// is that the file is the one the update installed.
func ReadWhence(dir string) (*Whence, *Source, error) {
	if dir == "" {
		return nil, nil, nil // no home directory, so no cache
	}
	m, err := ReadManifest(dir)
	if err != nil || m == nil || m.LinuxFirmware == nil {
		return nil, nil, err
	}
	b, err := os.ReadFile(filepath.Join(dir, WhenceName))
	if err != nil {
		return nil, nil, err
	}
	if sha(b) != m.LinuxFirmware.Files[WhenceName] {
		return nil, nil, fmt.Errorf("%s isn't the file the last update installed", WhenceName)
	}
	w, err := ParseWhence(b)
	if err != nil {
		return nil, nil, err
	}
	return w, m.LinuxFirmware, nil
}

// intact reports whether every file s lists is in dir with its checksum.
func (s *Source) intact(dir string) bool {
	if s == nil || len(s.Files) == 0 {
		return false
	}
	for name, sum := range s.Files {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || sha(b) != sum {
			return false
		}
	}
	return true
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// install writes files, then the manifest, each atomically.
func install(dir string, files map[string][]byte, m *Manifest) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	for _, name := range []string{CatalogueName, JcatName, WhenceName} {
		if b, ok := files[name]; ok {
			if err := writeAtomic(filepath.Join(dir, name), b); err != nil {
				return err
			}
		}
	}
	js, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(dir, manifestName), append(js, '\n')); err != nil {
		return err
	}
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
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
