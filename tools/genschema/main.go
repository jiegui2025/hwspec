// Command genschema generates the JSON Schemas of hwspec's two document
// formats from their Go types: the capture (internal/report) and the
// advice document (internal/advisor, ADR 0009). It checks that a change to
// either is compatible (ADR 0003: fields may be added; renaming, removing or
// retyping one needs a new version).
//
//	genschema [-o FILE]                write the capture schema (stdout by
//	                                   default)
//	genschema -dir DIR                 write every format's schema, each to
//	                                   DIR/<its URL's file name>
//	genschema compat OLD NEW           fail if NEW breaks documents OLD accepts
//	genschema check BASEDIR HEADDIR    compat for every <format>-vN.json, each
//	                                   format apart: the newest may grow, older
//	                                   ones are frozen
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/jiegui2025/hwspec/internal/advisor"
	"github.com/jiegui2025/hwspec/internal/report"
	"github.com/jiegui2025/hwspec/schema"
)

// family is one document format: its schema files are <name>-vN.json, the
// newest generated from Go types for version.
type family struct {
	name     string
	url      string
	version  int
	generate func() ([]byte, error)
}

var families = []family{
	{"capture", schema.URL, report.SchemaVersion, generate},
	{"advice", schema.AdviceURL, advisor.Version, generateAdvice},
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	var err error
	switch {
	case len(args) == 0:
		err = write(stdout, "")
	case len(args) == 2 && args[0] == "-o":
		err = write(stdout, args[1])
	case len(args) == 2 && args[0] == "-dir":
		err = writeAll(args[1])
	case len(args) == 3 && args[0] == "compat":
		err = compatFiles(stdout, args[1], args[2])
	case len(args) == 3 && args[0] == "check":
		err = check(stdout, args[1], args[2])
	default:
		fmt.Fprintln(stderr, "usage: genschema [-o FILE | -dir DIR] | compat OLD NEW | check BASEDIR HEADDIR")
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "genschema:", err)
		return 1
	}
	return 0
}

// writeAll writes every format's schema to dir.
func writeAll(dir string) error {
	for _, f := range families {
		b, err := f.generate()
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, path.Base(f.url)), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func write(stdout io.Writer, file string) error {
	b, err := generate()
	if err != nil {
		return err
	}
	if file == "" {
		_, err = stdout.Write(b)
		return err
	}
	return os.WriteFile(file, b, 0o644)
}

// generate returns the schema for report.Report, adjusted to the format's
// compatibility rules (ADR 0003):
//   - objects allow unknown properties: a v1 reader must accept files from
//     newer v1 builds, which may add fields;
//   - only schema_version, tool and tool.name are required, as output.Read
//     checks; everything else may be missing ("never guess");
//   - maps may be null, as Go writes nil maps (slices and pointers already
//     are).
func generate() ([]byte, error) {
	s, err := jsonschema.For[report.Report](nil)
	if err != nil {
		return nil, err
	}
	walk(s, func(n *jsonschema.Schema) {
		switch {
		case n.Properties != nil: // a struct
			n.AdditionalProperties, n.Required = nil, nil
		case n.AdditionalProperties != nil && n.Type == "object": // a map
			n.Type, n.Types = "", []string{"null", "object"}
		}
	})
	s.Required = []string{"schema_version", "tool"}
	s.Properties["tool"].Required = []string{"name"}
	s.Schema = "https://json-schema.org/draft/2020-12/schema"
	s.ID = schema.URL
	s.Title = "hwspec capture, schema_version " + strconv.Itoa(report.SchemaVersion)
	s.Description = "Generated from internal/report by tools/genschema: don't edit by hand."
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// evidencePath is the grammar of an evidence path: the capture's JSON keys
// from its root, dot-separated, each optionally indexed into a list, e.g.
// "pci[16].class_code" or "system.esrt.entries[0].fw_version".
const evidencePath = `^[a-z][a-z0-9_]*(\[[0-9]+\])?(\.[a-z][a-z0-9_]*(\[[0-9]+\])?)*$`

// generateAdvice returns the schema for advisor.Advice (ADR 0009), with the
// same compatibility rules as captures: objects allow unknown properties,
// maps may be null. Go always writes the fields it requires here, so they
// stay required; a requirement may be dropped later, never added. Evidence,
// written by its own MarshalJSON, is exactly {path, value} or {path,
// absent: true}. category, severity and confidence are plain strings:
// newer builds may add values, which readers rank last.
func generateAdvice() ([]byte, error) {
	evidence := &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"path":   {Type: "string", Pattern: evidencePath},
			"value":  {},
			"absent": {Type: "boolean", Const: jsonschema.Ptr[any](true)},
		},
		Required: []string{"path"},
		OneOf: []*jsonschema.Schema{
			{Required: []string{"value"}, Not: &jsonschema.Schema{Required: []string{"absent"}}},
			{Required: []string{"absent"}, Not: &jsonschema.Schema{Required: []string{"value"}}},
		},
	}
	s, err := jsonschema.For[advisor.Advice](&jsonschema.ForOptions{TypeSchemas: map[reflect.Type]*jsonschema.Schema{
		reflect.TypeFor[advisor.Evidence](): evidence,
	}})
	if err != nil {
		return nil, err
	}
	walk(s, func(n *jsonschema.Schema) {
		switch {
		case n.Properties != nil: // a struct
			n.AdditionalProperties = nil
		case n.AdditionalProperties != nil && n.Type == "object": // a map
			n.Type, n.Types = "", []string{"null", "object"}
		}
	})
	s.Schema = "https://json-schema.org/draft/2020-12/schema"
	s.ID = schema.AdviceURL
	s.Title = "hwspec advice, advice_version " + strconv.Itoa(advisor.Version)
	s.Description = "Generated from internal/advisor by tools/genschema: don't edit by hand."
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// walk calls fn on s and every schema below it, parents first.
func walk(s *jsonschema.Schema, fn func(*jsonschema.Schema)) {
	if s == nil {
		return
	}
	fn(s)
	for _, k := range sortedKeys(s.Properties) {
		walk(s.Properties[k], fn)
	}
	walk(s.Items, fn)
	walk(s.AdditionalProperties, fn)
}

func sortedKeys(m map[string]*jsonschema.Schema) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func compatFiles(stdout io.Writer, oldPath, newPath string) error {
	old, err := load(oldPath)
	if err != nil {
		return err
	}
	cur, err := load(newPath)
	if err != nil {
		return err
	}
	if p := compat(old, cur); len(p) > 0 {
		return fmt.Errorf("%s breaks %s (anything but an added property or a dropped requirement needs a new schema_version, ADR 0003):\n  %s",
			newPath, oldPath, strings.Join(p, "\n  "))
	}
	fmt.Fprintf(stdout, "%s is compatible with %s\n", newPath, oldPath)
	return nil
}

// load reads a schema as plain JSON, so compat sees every keyword, even
// ones this program doesn't know.
func load(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s map[string]any
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// annotations don't constrain documents, so they may change freely.
var annotations = map[string]bool{"$schema": true, "$id": true, "title": true, "description": true}

// compat lists every way cur differs from old other than the two changes
// ADR 0003 allows within a schema_version: a property added, or a
// requirement dropped (missing data is always allowed). Readers rely on
// the rest, so any other difference is a break: a removed property, a
// type set that grew or shrank (an int that may now be null breaks a
// reader as much as one that may be a string), a newly required
// property, and any other keyword added, removed or changed (minimum,
// enum, format, ...), including ones not understood here.
func compat(old, cur map[string]any) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	var visit func(path string, o, c map[string]any)
	visit = func(path string, o, c map[string]any) {
		keys := map[string]bool{}
		for k := range o {
			keys[k] = true
		}
		for k := range c {
			keys[k] = true
		}
		for _, k := range slices.Sorted(maps.Keys(keys)) {
			ov, inOld := o[k]
			cv, inCur := c[k]
			switch {
			case annotations[k]:
			case k == "type":
				if a, b := typeSet(ov), typeSet(cv); !slices.Equal(a, b) {
					add("%s: type %s is now %s", path, strings.Join(a, "|"), strings.Join(b, "|"))
				}
			case k == "required":
				for _, r := range strings_(cv) {
					if !slices.Contains(strings_(ov), r) {
						add("%s.%s: newly required", path, r)
					}
				}
			case k == "properties":
				op, cp := object(ov), object(cv)
				for _, name := range slices.Sorted(maps.Keys(op)) {
					sub, ok := cp[name]
					if !ok {
						add("%s.%s: removed", path, name)
						continue
					}
					visit(path+"."+name, object(op[name]), object(sub))
				}
			case (k == "items" || k == "additionalProperties") && isObject(ov) && isObject(cv):
				suffix := "[]"
				if k == "additionalProperties" {
					suffix = "{}"
				}
				visit(path+suffix, object(ov), object(cv))
			case !inOld:
				add("%s: %s added (%v)", path, k, jsonText(cv))
			case !inCur:
				add("%s: %s removed (was %v)", path, k, jsonText(ov))
			case !reflect.DeepEqual(ov, cv):
				add("%s: %s changed from %v to %v", path, k, jsonText(ov), jsonText(cv))
			}
		}
	}
	visit("$", old, cur)
	return problems
}

func object(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func isObject(v any) bool {
	_, ok := v.(map[string]any)
	return ok
}

// strings_ returns a JSON array of strings ([] for anything else).
func strings_(v any) []string {
	var out []string
	a, _ := v.([]any)
	for _, x := range a {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// typeSet is a "type" keyword's value as a sorted set: "integer" or
// ["null", "integer"].
func typeSet(v any) []string {
	if s, ok := v.(string); ok {
		return []string{s}
	}
	out := strings_(v)
	slices.Sort(out)
	return out
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

var versioned = regexp.MustCompile(`^([a-z]+)-v([0-9]+)\.json$`)

// versions maps N to dir/<name>-vN.json.
func versions(dir, name string) (map[int]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	out := map[int]string{}
	for _, e := range entries {
		if m := versioned.FindStringSubmatch(e.Name()); m != nil && m[1] == name {
			n, _ := strconv.Atoi(m[2])
			out[n] = filepath.Join(dir, e.Name())
		}
	}
	return out, nil
}

// check compares the schema files in base (the schema directory before a
// change, possibly absent) with head, each format apart: none may
// disappear, the newest may grow compatibly, and older ones may not change
// at all.
func check(stdout io.Writer, base, head string) error {
	for _, f := range families {
		if err := checkFamily(stdout, f.name, base, head); err != nil {
			return err
		}
	}
	return nil
}

func checkFamily(stdout io.Writer, name, base, head string) error {
	before, err := versions(base, name)
	if err != nil {
		return err
	}
	after, err := versions(head, name)
	if err != nil {
		return err
	}
	newest := 0
	for n := range after {
		newest = max(newest, n)
	}
	if len(before) == 0 {
		fmt.Fprintf(stdout, "no %s schema before this change: nothing to compare\n", name)
	}
	for _, n := range slices.Sorted(func(yield func(int) bool) {
		for n := range before {
			if !yield(n) {
				return
			}
		}
	}) {
		cur, ok := after[n]
		switch {
		case !ok:
			return fmt.Errorf("%s-v%d.json was removed: published schemas stay", name, n)
		case n == newest:
			if err := compatFiles(stdout, before[n], cur); err != nil {
				return err
			}
		default:
			a, err1 := os.ReadFile(before[n])
			b, err2 := os.ReadFile(cur)
			if err := errors.Join(err1, err2); err != nil {
				return err
			}
			if !bytes.Equal(a, b) {
				return fmt.Errorf("%s-v%d.json changed, but v%d is newer: older schemas are frozen", name, n, newest)
			}
			fmt.Fprintf(stdout, "%s-v%d.json is unchanged\n", name, n)
		}
	}
	return nil
}
