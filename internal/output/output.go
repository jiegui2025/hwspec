// Package output writes a report as JSON, YAML or a human-readable summary.
package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/jiegui2025/hwspec/internal/report"
)

var Formats = []string{"json", "yaml", "text"}

// FormatFromPath guesses the format from a file extension ("" if unknown).
func FormatFromPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".txt":
		return "text"
	}
	return ""
}

func Write(w io.Writer, r *report.Report, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	case "yaml":
		return YAML(w, r)
	case "text":
		return writeText(w, r)
	}
	return fmt.Errorf("unknown format %q (want one of %s)", format, strings.Join(Formats, ", "))
}

// YAML writes v as block-style YAML. It goes through JSON so the YAML uses
// the same field names and order as the JSON (JSON is valid YAML, and
// yaml.Node keeps key order), and quotes the strings YAML 1.1 parsers
// would misread. Captures and advice documents both use it.
func YAML(w io.Writer, v any) error {
	js, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var node yaml.Node
	if err := yaml.Unmarshal(js, &node); err != nil {
		return err
	}
	blockStyle(&node, map[string]bool{})
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&node); err != nil {
		return err
	}
	_, err = w.Write(buf.Bytes())
	return err
}

// blockStyle drops the flow ({...}, "...") styling that parsing JSON leaves
// on every node, except on strings that need their quotes: an encoder
// writes a plain style node as is, and YAML 1.1 parsers (PyYAML, Psych) read
// a plain 0000:02:00.0 as the number 120.0 or "on" as true. Which strings
// those are, yaml.v3 knows: it's what it quotes when encoding a string.
//
// quoted remembers needsQuotes' answer per string: a capture repeats the
// same keys and values many times, and each answer costs an encode.
func blockStyle(n *yaml.Node, quoted map[string]bool) {
	n.Style = 0
	if n.Kind == yaml.ScalarNode && n.Tag == "!!str" {
		q, ok := quoted[n.Value]
		if !ok {
			q = needsQuotes(n.Value)
			quoted[n.Value] = q
		}
		if q {
			n.Style = yaml.DoubleQuotedStyle
		}
	}
	for _, c := range n.Content {
		blockStyle(c, quoted)
	}
	// Keep empty collections readable as [] / {}.
	if (n.Kind == yaml.SequenceNode || n.Kind == yaml.MappingNode) && len(n.Content) == 0 {
		n.Style = yaml.FlowStyle
	}
}

// yaml11Words are the words YAML 1.1 reads as booleans or null, in any
// case: the only letters-and-digits strings that need quotes.
var yaml11Words = map[string]bool{
	"y": true, "n": true, "yes": true, "no": true, "on": true, "off": true,
	"true": true, "false": true, "null": true,
}

// plainWord reports whether s is a letter followed by letters, digits and
// underscores, and not a YAML 1.1 boolean or null: such a string reads back
// as itself, so needsQuotes can answer without encoding it.
func plainWord(s string) bool {
	if s == "" || !isLetter(s[0]) || yaml11Words[strings.ToLower(s)] {
		return false
	}
	for i := 1; i < len(s); i++ {
		if c := s[i]; !isLetter(c) && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}

func isLetter(c byte) bool { return c|0x20 >= 'a' && c|0x20 <= 'z' }

// needsQuotes reports whether s, written plain, would read back as
// something other than that string in YAML 1.1 or 1.2. yaml.v3 leaves
// YAML 1.1's value ("=") and merge ("<<") keys plain; PyYAML refuses them.
func needsQuotes(s string) bool {
	if s == "=" || s == "<<" {
		return true
	}
	if plainWord(s) {
		return false
	}
	b, err := yaml.Marshal(s)
	return err == nil && len(b) > 0 && (b[0] == '"' || b[0] == '\'')
}

// ErrNotCapture is returned for valid JSON/YAML that isn't a hwspec capture.
var ErrNotCapture = errors.New("not a hwspec capture (no tool.name \"hwspec\" and schema_version)")

// MaxCaptureSize bounds what readers accept: a real capture is tens of
// kilobytes, so anything this large isn't one, and reading it whole would
// only exhaust memory (#145).
const MaxCaptureSize = 64 << 20

// NewerSchemaError is returned when a capture from a newer schema version
// doesn't decode: the reason to give is "update hwspec", not the decoder's.
type NewerSchemaError struct {
	Version int
	Err     error
}

func (e *NewerSchemaError) Error() string {
	return fmt.Sprintf("uses schema %d, newer than this build understands (%d): update hwspec to read it (%v)",
		e.Version, report.SchemaVersion, e.Err)
}

func (e *NewerSchemaError) Unwrap() error { return e.Err }

// Read loads a report previously written as JSON or YAML.
func Read(data []byte) (*report.Report, error) {
	r, err := decode(data)
	if err != nil {
		if v := schemaVersion(data); v > report.SchemaVersion {
			return nil, &NewerSchemaError{Version: v, Err: err}
		}
		return nil, err
	}
	if r.Tool.Name != "hwspec" || r.SchemaVersion < 1 {
		return nil, ErrNotCapture
	}
	r.Sanitize() // a shared capture is untrusted input
	return r, nil
}

func decode(data []byte) (*report.Report, error) {
	var r report.Report
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		if err := json.Unmarshal(trimmed, &r); err != nil {
			return nil, err
		}
		return &r, nil
	}
	// YAML: decode generically, then reuse the JSON field names.
	var v any
	if err := yaml.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	js, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(js, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// schemaVersion reads only a capture's schema_version, so a capture whose
// other fields don't decode can still say which version it is (0 if even
// that can't be read).
func schemaVersion(data []byte) int {
	var h struct {
		SchemaVersion int `json:"schema_version" yaml:"schema_version"`
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		_ = json.Unmarshal(trimmed, &h)
	} else {
		_ = yaml.Unmarshal(data, &h)
	}
	return h.SchemaVersion
}

// UnknownField returns the name of a field in a capture that this build
// doesn't know (the first one found), or "". A capture from a newer v1
// build may add fields; Read ignores them, so re-exporting it drops them,
// and the caller should say so (#145, owner: warn, don't refuse).
func UnknownField(data []byte) string {
	js := bytes.TrimSpace(data)
	if len(js) == 0 || js[0] != '{' {
		var v any
		if yaml.Unmarshal(data, &v) != nil {
			return ""
		}
		var err error
		if js, err = json.Marshal(v); err != nil {
			return ""
		}
	}
	dec := json.NewDecoder(bytes.NewReader(js))
	dec.DisallowUnknownFields()
	var r report.Report
	err := dec.Decode(&r)
	const prefix = `json: unknown field "`
	if err == nil || !strings.HasPrefix(err.Error(), prefix) {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(err.Error(), prefix), `"`)
}
