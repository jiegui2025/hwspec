// Command genkb compiles the advisor's knowledge base (ADR 0009): every
// kb/**/*.yaml file, each one YAML document mapping "sources" (ID →
// source) and "rules" (a list), into one gzipped JSON file. It is strict
// where the binary is lenient: it refuses anything the binary would have
// to skip (unknown fields and values, uncited rules and claims, unknown or
// duplicate IDs, unknown checks, match keys or data a rule's check doesn't
// take, and placeholders it doesn't fill), an empty knowledge base, .yml files,
// and YAML values that don't survive as written (0403 read as octal, null).
//
//	genkb [-version YYYY-MM-DD] -o FILE DIR   compile DIR into FILE; the
//	                                          version is today (UTC), or
//	                                          FILE's own when nothing else
//	                                          changed
//	genkb check FILE DIR                      fail unless FILE is DIR compiled
//	                                          (with FILE's own version)
package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/jiegui2025/hwspec/internal/advisor"
	"github.com/jiegui2025/hwspec/internal/kb"
)

const usage = "usage: genkb [-version YYYY-MM-DD] -o FILE DIR | check FILE DIR"

func main() {
	os.Exit(run(os.Args[1:], time.Now(), os.Stdout, os.Stderr))
}

func run(args []string, now time.Time, stdout, stderr io.Writer) int {
	var err error
	switch {
	case len(args) == 3 && args[0] == "check":
		err = check(stdout, args[1], args[2])
	case len(args) == 3 && args[0] == "-o":
		err = write(args[1], args[2], "", now.UTC().Format("2006-01-02"))
	case len(args) == 5 && args[0] == "-version" && args[2] == "-o":
		err = write(args[3], args[4], args[1], "")
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "genkb:", err)
		return 1
	}
	return 0
}

// write compiles dir into out. Without an explicit version it is today's,
// unless out already holds the same sources and rules: then out keeps its
// version, so rebuilding unchanged sources changes no bytes.
func write(out, dir, version, today string) error {
	if version == "" {
		version = today
		if old, err := os.ReadFile(out); err == nil {
			if k, err := kb.Parse(old); err == nil {
				if same, err := compile(dir, k.Version); err == nil && sameContent(old, same) {
					return nil
				}
			}
		}
	}
	k, err := compile(dir, version)
	if err != nil {
		return err
	}
	data, err := kb.Encode(k)
	if err != nil {
		return err
	}
	return os.WriteFile(out, data, 0o644)
}

// check recompiles dir with file's version and compares the content: the
// embedded knowledge base must be what the sources say. It compares the
// JSON, not the gzip bytes, which differ between Go releases.
func check(stdout io.Writer, file, dir string) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	have, err := kb.Parse(data)
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	want, err := compile(dir, have.Version)
	if err != nil {
		return err
	}
	if !sameContent(data, want) {
		return fmt.Errorf("%s is out of date with %s: run make gen-kb", file, dir)
	}
	fmt.Fprintf(stdout, "%s: %d rules, version %s, up to date\n", file, len(have.Rules), have.Version)
	return nil
}

// sameContent says whether a compiled file holds exactly k.
func sameContent(gz []byte, k *kb.KB) bool {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return false
	}
	have, err := io.ReadAll(zr)
	if err != nil {
		return false
	}
	want, err := json.Marshal(k)
	return err == nil && bytes.Equal(have, want)
}

// compile reads every .yaml file under dir and validates the sources and
// rules together. Both are sorted by ID, so moving one between files
// doesn't change the output.
func compile(dir, version string) (*kb.KB, error) {
	k := &kb.KB{Format: kb.Format, Version: version, Sources: []kb.Source{}, Rules: []kb.Rule{}}
	var errs []error
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch filepath.Ext(path) {
		case ".yml":
			errs = append(errs, fmt.Errorf("%s: name knowledge-base files .yaml", path))
			return nil
		case ".yaml":
		default:
			return nil
		}
		f, err := readFile(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			return nil
		}
		for id, s := range f.Sources {
			if slices.ContainsFunc(k.Sources, func(o kb.Source) bool { return o.ID == id }) {
				errs = append(errs, fmt.Errorf("%s: source %q is defined twice", path, id))
				continue
			}
			s.ID = id
			k.Sources = append(k.Sources, s)
		}
		k.Rules = append(k.Rules, f.Rules...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	slices.SortFunc(k.Sources, func(a, b kb.Source) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(k.Rules, func(a, b kb.Rule) int { return strings.Compare(a.ID, b.ID) })
	errs = append(errs, k.Validate())
	// What only the check knows: its match keys, its data, its placeholders.
	for i := range k.Rules {
		r := &k.Rules[i]
		if r.Check == "" {
			continue // k.Validate says so
		}
		for _, err := range advisor.ValidateRule(r) {
			errs = append(errs, fmt.Errorf("%q: %w", r.ID, err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return k, nil
}

// file is one YAML source file. A source's ID is its key.
type file struct {
	Sources map[string]kb.Source `json:"sources"`
	Rules   []kb.Rule            `json:"rules"`
}

// readFile decodes one YAML file through JSON, so the field names are kb's
// JSON names and unknown fields are refused.
func readFile(path string) (*file, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); errors.Is(err, io.EOF) {
		return &file{}, nil
	} else if err != nil {
		return nil, err
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("more than one YAML document; one per file")
	}
	v, err := plain(&doc)
	if err != nil {
		return nil, err
	}
	js, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	jdec := json.NewDecoder(bytes.NewReader(js))
	jdec.DisallowUnknownFields()
	var f file
	if err := jdec.Decode(&f); err != nil {
		return nil, fmt.Errorf("want sources and rules: %w", err)
	}
	for id, s := range f.Sources {
		if s.ID != "" {
			return nil, fmt.Errorf("source %q: its id is its key; drop the id field", id)
		}
	}
	return &f, nil
}

// plain turns a YAML node into JSON-ready values, keeping every scalar as
// written: timestamps stay text, numbers keep their digits, and a value
// YAML would silently change (0403 as octal 259) or a null is refused.
func plain(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.DocumentNode:
		return plain(n.Content[0])
	case yaml.AliasNode:
		return plain(n.Alias)
	case yaml.MappingNode:
		m := make(map[string]any, len(n.Content)/2)
		for i := 0; i < len(n.Content); i += 2 {
			if _, dup := m[n.Content[i].Value]; dup {
				return nil, fmt.Errorf("line %d: %q appears twice in one mapping", n.Content[i].Line, n.Content[i].Value)
			}
			v, err := plain(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			m[n.Content[i].Value] = v
		}
		return m, nil
	case yaml.SequenceNode:
		l := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := plain(c)
			if err != nil {
				return nil, err
			}
			l = append(l, v)
		}
		return l, nil
	}
	switch n.ShortTag() {
	case "!!null":
		return nil, fmt.Errorf("line %d: a null value; leave the key out instead", n.Line)
	case "!!bool":
		return strconv.ParseBool(n.Value) // YAML's true, True, TRUE
	case "!!int", "!!float":
		if len(n.Value) > 1 && n.Value[0] == '0' && n.Value[1] != '.' || !json.Valid([]byte(n.Value)) {
			return nil, fmt.Errorf("line %d: %s isn't a plain number; quote it", n.Line, n.Value)
		}
		return json.Number(n.Value), nil
	}
	return n.Value, nil // strings and timestamps, as written
}
