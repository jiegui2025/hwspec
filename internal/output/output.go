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

	"gopkg.in/yaml.v3"

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
	blockStyle(&node)
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
func blockStyle(n *yaml.Node) {
	n.Style = 0
	if n.Kind == yaml.ScalarNode && n.Tag == "!!str" && needsQuotes(n.Value) {
		n.Style = yaml.DoubleQuotedStyle
	}
	for _, c := range n.Content {
		blockStyle(c)
	}
	// Keep empty collections readable as [] / {}.
	if (n.Kind == yaml.SequenceNode || n.Kind == yaml.MappingNode) && len(n.Content) == 0 {
		n.Style = yaml.FlowStyle
	}
}

// needsQuotes reports whether s, written plain, would read back as
// something other than that string in YAML 1.1 or 1.2. yaml.v3 leaves
// YAML 1.1's value ("=") and merge ("<<") keys plain; PyYAML refuses them.
func needsQuotes(s string) bool {
	if s == "=" || s == "<<" {
		return true
	}
	b, err := yaml.Marshal(s)
	return err == nil && len(b) > 0 && (b[0] == '"' || b[0] == '\'')
}

// ErrNotCapture is returned for valid JSON/YAML that isn't a hwspec capture.
var ErrNotCapture = errors.New("not a hwspec capture (no tool.name \"hwspec\" and schema_version)")

// Read loads a report previously written as JSON or YAML.
func Read(data []byte) (*report.Report, error) {
	r, err := decode(data)
	if err != nil {
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
