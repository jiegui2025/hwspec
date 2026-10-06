package ids

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"
)

// The advisor's knowledge base travels in the signed bundle next to the ID
// databases (ADR 0009, #81), so it updates without a release. This package
// checks it only structurally, without importing internal/kb: the CLI
// hands the bytes to kb, which parses them and picks the newer copy.

// AdvisorFile is the knowledge base this build reads. A new format is a
// new file name, so a bundle can carry an older or newer one too: those
// are skipped here, like databases this build doesn't know.
const AdvisorFile = "advisor-v1.json.gz"

var advisorName = regexp.MustCompile(`^advisor-v([1-9][0-9]*)\.json\.gz$`)

// IsAdvisorFile says whether a bundle file is a knowledge base, of any
// format.
func IsAdvisorFile(name string) bool { return advisorName.MatchString(name) }

// maxAdvisorBytes bounds a decompressed knowledge base, as internal/kb does.
const maxAdvisorBytes = 4 << 20

// advisorVersion is a knowledge base's version: the UTC time its content
// last changed (internal/kb's VersionLayout).
const advisorVersion = "2006-01-02T15:04:05Z"

// CheckAdvisor checks a knowledge-base file as far as this package can: it
// decompresses within the size limit, and is a JSON object whose format is
// the one its name says, with a version and a list of rules. It returns
// the version (the date its content last changed) and the rule count, for
// the manifest.
func CheckAdvisor(name string, gz []byte) (version string, rules int, err error) {
	m := advisorName.FindStringSubmatch(name)
	if m == nil {
		return "", 0, fmt.Errorf("%s: not a knowledge-base file name", name)
	}
	format, _ := strconv.Atoi(m[1]) // the pattern makes it a number
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return "", 0, fmt.Errorf("%s: %w", name, err)
	}
	content, err := io.ReadAll(io.LimitReader(zr, maxAdvisorBytes+1))
	if err != nil {
		return "", 0, fmt.Errorf("%s: %w", name, err)
	}
	if len(content) > maxAdvisorBytes {
		return "", 0, fmt.Errorf("%s: larger than %d bytes", name, maxAdvisorBytes)
	}
	// Read by exact key: encoding/json would also take "Format" or
	// "VERSION" into those fields.
	var sections map[string]json.RawMessage
	if err := json.Unmarshal(content, &sections); err != nil {
		return "", 0, fmt.Errorf("%s: %w", name, err)
	}
	var fileFormat int
	var ruleList []json.RawMessage
	if err := errors.Join(json.Unmarshal(sections["format"], &fileFormat), json.Unmarshal(sections["version"], &version),
		json.Unmarshal(sections["rules"], &ruleList)); err != nil {
		return "", 0, fmt.Errorf("%s: format, version and rules: %w", name, err)
	}
	if t, err := time.Parse(advisorVersion, version); err != nil || t.Format(advisorVersion) != version {
		return "", 0, fmt.Errorf("%s: version %q isn't a UTC time such as 2026-10-06T14:03:05Z", name, version)
	}
	switch {
	case fileFormat != format:
		return "", 0, fmt.Errorf("%s: format %d, but the name says %d", name, fileFormat, format)
	case len(ruleList) == 0:
		return "", 0, fmt.Errorf("%s: no rules", name)
	}
	return version, len(ruleList), nil
}

// SyncedAdvisor returns the knowledge base `hwspec ids update` installed,
// and its version as the signed manifest gives it, or nil when there is
// none. Like the synced databases, it is used only with a readable synced
// manifest that lists it, and only while its size and SHA-256 are the
// manifest's: an edited copy can't pin newer-looking advice.
func SyncedAdvisor() (data []byte, version string, err error) {
	if syncedDir == "" {
		return nil, "", nil
	}
	m, err := syncedManifest()
	if err != nil {
		return nil, "", fmt.Errorf("synced manifest unreadable: %w", err)
	}
	if m == nil {
		return nil, "", nil
	}
	f, listed := m.Files[AdvisorFile]
	if !listed {
		return nil, "", nil
	}
	b, err := os.ReadFile(filepath.Join(syncedDir, AdvisorFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	if sha(b) != f.SHA256 {
		return nil, "", fmt.Errorf("%s differs from the synced manifest", AdvisorFile)
	}
	return b, f.Date, nil
}
