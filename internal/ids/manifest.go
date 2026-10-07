package ids

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// ManifestFormat is bumped if the manifest layout changes incompatibly.
const ManifestFormat = 1

// Manifest describes a set of ID database files: the embedded copies, a
// published sync bundle, or the synced copies on disk.
type Manifest struct {
	Format      int                     `json:"format"`
	GeneratedAt time.Time               `json:"generated_at"`
	Files       map[string]ManifestFile `json:"files"` // keyed by file name, e.g. "pci.ids.gz"
}

type ManifestFile struct {
	SHA256 string `json:"sha256"` // of the .gz file
	Size   int64  `json:"size"`
	// Date is when the content last changed upstream: the file's own
	// "Version"/"Date" header where it has one, otherwise the first time
	// the bundle builder saw this exact content.
	Date    string `json:"date"`
	Entries int    `json:"entries"`
}

func ParseManifest(b []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if m.Format != ManifestFormat {
		return nil, fmt.Errorf("manifest format %d not supported (want %d); update hwspec", m.Format, ManifestFormat)
	}
	return &m, nil
}

// trustedKeys are the ed25519 public keys (base64) whose signatures
// `hwspec ids update` accepts. To rotate, add the new key here, ship a
// release, then switch the signing secret; remove the old key later.
var trustedKeys = []string{
	SigningPublicKey,
}

// VerifySignature checks a base64 ed25519 signature over the manifest bytes.
func VerifySignature(manifest, sig []byte) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return errors.New("signature: malformed")
	}
	for _, k := range trustedKeys {
		pub, err := base64.StdEncoding.DecodeString(k)
		if err != nil || len(pub) != ed25519.PublicKeySize {
			continue
		}
		if ed25519.Verify(pub, manifest, raw) {
			return nil
		}
	}
	return errors.New("signature: not signed by a trusted key")
}

// KindForFile maps a bundle file name ("pci.ids.gz") to its database.
func KindForFile(name string) (Kind, bool) {
	for k, s := range specs {
		if s.file+".gz" == name {
			return k, true
		}
	}
	return "", false
}

// minEntries guards against truncated or wrong files: a database smaller
// than this is rejected. Thresholds are well below current sizes.
var minEntries = map[Kind]int{
	PCI: 20000, USB: 10000, PNP: 1000, OUI: 20000, JEDEC: 1000, AMDGPU: 200,
	BT: 2000, CPU: 300,
}

// Names parses an uncompressed database into its names by the keys
// lookups use ("8086", "6:77", "amd:19:21").
func Names(k Kind, content []byte) (map[string]string, error) {
	s, ok := specs[k]
	if !ok {
		return nil, fmt.Errorf("unknown database %q", k)
	}
	m := map[string]string{}
	if err := s.parse(bytes.NewReader(content), m); err != nil {
		return nil, err
	}
	return m, nil
}

// Validate parses an uncompressed database and checks it is plausibly
// complete. It returns the number of entries.
func Validate(k Kind, content []byte) (int, error) {
	m, err := Names(k, content)
	if err != nil {
		return 0, err
	}
	if len(m) < minEntries[k] {
		return len(m), fmt.Errorf("%s: only %d entries (expected at least %d)", k, len(m), minEntries[k])
	}
	return len(m), nil
}

// HeaderDate returns the date in a database's "# Version:"/"# Date:"
// header, or "".
func HeaderDate(content []byte) string {
	return sourceDate(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(content)), nil
	})
}
