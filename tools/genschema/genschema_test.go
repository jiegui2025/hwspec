package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/jiegui2025/hwspec/internal/output"
	"github.com/jiegui2025/hwspec/internal/report"
	"github.com/jiegui2025/hwspec/schema"
)

var update = flag.Bool("update", false, "rewrite schema/capture-v1.json")

// committed is the schema file schema.URL names.
var committed = "../../schema/" + path.Base(schema.URL)

// The committed schema is what the structs generate: a changed json tag
// fails here until `go generate ./schema` (or -update) is run.
func TestCommittedSchemaIsCurrent(t *testing.T) {
	want, err := generate()
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.WriteFile(committed, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(committed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s is stale: run go generate ./schema (or go test ./tools/genschema -update)", committed)
	}
	if !bytes.Equal(schema.JSON, got) {
		t.Error("package schema doesn't embed the committed file")
	}
}

// The newest capture-vN.json is the one for report.SchemaVersion: bumping
// the version needs a new file, so older ones can stay frozen.
func TestSchemaVersionHasTheNewestFile(t *testing.T) {
	v, err := versions("../../schema")
	if err != nil {
		t.Fatal(err)
	}
	newest := 0
	for n := range v {
		newest = max(newest, n)
	}
	if newest != report.SchemaVersion || path.Base(schema.URL) != filepath.Base(v[newest]) {
		t.Errorf("newest schema file is v%d (%s), SchemaVersion is %d, schema.URL is %s", newest, v[newest], report.SchemaVersion, schema.URL)
	}
}

func resolved(t *testing.T) *jsonschema.Resolved {
	t.Helper()
	var s jsonschema.Schema
	if err := json.Unmarshal(schema.JSON, &s); err != nil {
		t.Fatal(err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func validate(t *testing.T, rs *jsonschema.Resolved, name string, doc []byte) error {
	t.Helper()
	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return rs.Validate(v)
}

// Every recorded machine's capture validates, as captured, redacted, and
// after a YAML round trip (what `show` reads back).
func TestCapturesValidate(t *testing.T) {
	rs := resolved(t)
	files, _ := filepath.Glob("../../internal/collect/testdata/machines/*/expected.json")
	if len(files) == 0 {
		t.Fatal("no recorded machines")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := validate(t, rs, f, b); err != nil {
			t.Errorf("%s: %v", f, err)
		}
		r, err := output.Read(b)
		if err != nil {
			t.Fatal(err)
		}
		if r.Schema != schema.URL {
			t.Errorf("%s: $schema = %q, want %q", f, r.Schema, schema.URL)
		}
		r.Redact()
		var y bytes.Buffer
		if err := output.Write(&y, r, "yaml"); err != nil {
			t.Fatal(err)
		}
		back, err := output.Read(y.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		js, err := json.Marshal(back)
		if err != nil {
			t.Fatal(err)
		}
		if err := validate(t, rs, f+" (redacted, via YAML)", js); err != nil {
			t.Errorf("%s redacted, via YAML: %v", f, err)
		}
	}
}

// The schema accepts what ADR 0003 allows (fields added by newer v1
// builds, missing data, null maps) and rejects what readers reject.
func TestSchemaFollowsTheCompatibilityRules(t *testing.T) {
	rs := resolved(t)
	for doc, ok := range map[string]bool{
		`{"schema_version": 1, "tool": {"name": "hwspec"}}`:                                            true,
		`{"schema_version": 1, "tool": {"name": "hwspec", "id_databases": null}, "a_new_field": {}}`:   true,
		`{"schema_version": 1, "tool": {"name": "hwspec"}, "cpu": {"a_new_field": 1}}`:                 true,
		`{"schema_version": 1, "tool": {"name": "hwspec"}, "pci": null, "system": {"identity": null}}`: true,
		`{"schema_version": 1}`:                                        false,
		`{"tool": {"name": "hwspec"}}`:                                 false,
		`{"schema_version": 1, "tool": {}}`:                            false,
		`{"schema_version": "1", "tool": {"name": "hwspec"}}`:          false,
		`{"schema_version": 1, "tool": {"name": "hwspec"}, "pci": {}}`: false,
	} {
		if err := validate(t, rs, doc, []byte(doc)); (err == nil) != ok {
			t.Errorf("%s: valid = %v, want %v (%v)", doc, err == nil, ok, err)
		}
	}
}

func parse(t *testing.T, doc string) map[string]any {
	t.Helper()
	var s map[string]any
	if err := json.Unmarshal([]byte(doc), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCompat(t *testing.T) {
	old := `{"type": "object", "title": "v1", "required": ["a"], "properties": {
		"a": {"type": "integer", "minimum": 0},
		"b": {"type": ["null", "array"], "items": {"type": "object", "properties": {"c": {"type": "string"}}}},
		"m": {"type": ["null", "object"], "additionalProperties": {"type": "number"}}}}`
	for name, c := range map[string]struct {
		cur  string
		want string // a substring of the only problem; "" means compatible
	}{
		"unchanged": {old, ""},
		"field added": {`{"type": "object", "title": "v1", "required": ["a"], "properties": {"a": {"type": "integer", "minimum": 0}, "z": {"type": "string"},
			"b": {"type": ["null", "array"], "items": {"type": "object", "properties": {"c": {"type": "string"}, "d": {"type": "boolean"}}}},
			"m": {"type": ["null", "object"], "additionalProperties": {"type": "number"}}}}`, ""},
		"requirement dropped":    {strings.Replace(old, `"required": ["a"], `, "", 1), ""},
		"annotations changed":    {strings.Replace(old, `"title": "v1"`, `"title": "v1, again", "description": "x"`, 1), ""},
		"type order only":        {strings.Replace(old, `"type": ["null", "array"]`, `"type": ["array", "null"]`, 1), ""},
		"field removed":          {strings.Replace(old, `"a": {"type": "integer", "minimum": 0},`, "", 1), "$.a: removed"},
		"nested field removed":   {strings.Replace(old, `"properties": {"c": {"type": "string"}}`, `"properties": {}`, 1), "$.b[].c: removed"},
		"retyped":                {strings.Replace(old, `"a": {"type": "integer"`, `"a": {"type": "string"`, 1), "$.a: type integer is now string"},
		"may now be null":        {strings.Replace(old, `"a": {"type": "integer"`, `"a": {"type": ["null", "integer"]`, 1), "$.a: type integer is now integer|null"},
		"null no longer allowed": {strings.Replace(old, `"b": {"type": ["null", "array"]`, `"b": {"type": "array"`, 1), "$.b: type array|null is now array"},
		"map values retyped":     {strings.Replace(old, `{"type": "number"}`, `{"type": "string"}`, 1), "$.m{}: type number is now string"},
		"newly required":         {strings.Replace(old, `"required": ["a"]`, `"required": ["a", "m"]`, 1), "$.m: newly required"},
		"minimum dropped":        {strings.Replace(old, `, "minimum": 0`, "", 1), "$.a: minimum removed (was 0)"},
		"minimum changed":        {strings.Replace(old, `"minimum": 0`, `"minimum": 1`, 1), "$.a: minimum changed from 0 to 1"},
		"enum added":             {strings.Replace(old, `"c": {"type": "string"}`, `"c": {"type": "string", "enum": ["x"]}`, 1), `$.b[].c: enum added (["x"])`},
		"format added":           {strings.Replace(old, `"c": {"type": "string"}`, `"c": {"type": "string", "format": "date"}`, 1), "$.b[].c: format added"},
		"unknown keyword added":  {strings.Replace(old, `"c": {"type": "string"}`, `"c": {"type": "string", "x-new": true}`, 1), "$.b[].c: x-new added"},
		"no more unknown fields": {strings.Replace(old, `"type": "object", "title"`, `"type": "object", "additionalProperties": false, "title"`, 1), "$: additionalProperties added (false)"},
		"items became a boolean": {strings.Replace(old, `"items": {"type": "object", "properties": {"c": {"type": "string"}}}`, `"items": false`, 1), "$.b: items changed"},
	} {
		p := compat(parse(t, old), parse(t, c.cur))
		switch {
		case c.want == "" && len(p) > 0:
			t.Errorf("%s: %v", name, p)
		case c.want != "" && (len(p) != 1 || !strings.Contains(p[0], c.want)):
			t.Errorf("%s: problems %v, want one containing %q", name, p, c.want)
		}
	}
}

// writeSchemas creates dir with capture-vN.json files (N → content).
func writeSchemas(t *testing.T, files map[int]string) string {
	t.Helper()
	dir := t.TempDir()
	for n, s := range files {
		if err := os.WriteFile(filepath.Join(dir, "capture-v"+string(rune('0'+n))+".json"), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestCheck(t *testing.T) {
	v1 := `{"type": "object", "properties": {"a": {"type": "integer"}}}`
	v1grown := `{"type": "object", "properties": {"a": {"type": "integer"}, "b": {"type": "string"}}}`
	v1broken := `{"type": "object", "properties": {}}`
	for name, c := range map[string]struct {
		base, head map[int]string
		want       string // a substring of the error; "" means accepted
	}{
		"first schema":            {nil, map[int]string{1: v1}, ""},
		"v1 grows":                {map[int]string{1: v1}, map[int]string{1: v1grown}, ""},
		"v1 breaks":               {map[int]string{1: v1}, map[int]string{1: v1broken}, "$.a: removed"},
		"bumped to v2":            {map[int]string{1: v1}, map[int]string{1: v1, 2: v1broken}, ""},
		"bumped, v1 still edited": {map[int]string{1: v1}, map[int]string{1: v1grown, 2: v1broken}, "older schemas are frozen"},
		"v1 deleted":              {map[int]string{1: v1}, map[int]string{2: v1}, "was removed"},
	} {
		base := filepath.Join(t.TempDir(), "absent")
		if c.base != nil {
			base = writeSchemas(t, c.base)
		}
		err := check(&bytes.Buffer{}, base, writeSchemas(t, c.head))
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: error %v, want one containing %q", name, err, c.want)
		}
	}
}

func TestRun(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "s.json")
	for _, c := range []struct {
		args []string
		code int
		want string // on stdout or stderr
	}{
		{nil, 0, `"$id": "` + schema.URL},
		{[]string{"-o", out}, 0, ""},
		{[]string{"-dir", dir}, 0, ""},
		{[]string{"compat", committed, out}, 0, "is compatible"},
		{[]string{"compat", committed, filepath.Join(dir, "missing.json")}, 1, "no such file"},
		{[]string{"compat", filepath.Join(dir, "missing.json"), committed}, 1, "no such file"},
		{[]string{"check", "../../schema", "../../schema"}, 0, "is compatible"},
		{[]string{"check", dir, filepath.Join(dir, "missing")}, 1, "was removed"},
		{[]string{"-o", filepath.Join(dir, "no", "such", "dir.json")}, 1, "genschema:"},
		{[]string{"bogus"}, 2, "usage:"},
	} {
		if c.args != nil && c.args[0] == "check" && c.code == 1 {
			// dir holds a capture-v1.json once -o ran, as dir/s.json does not:
			// give it one so the missing head loses it.
			if err := os.WriteFile(filepath.Join(dir, "capture-v1.json"), schema.JSON, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		var stdout, stderr bytes.Buffer
		code := run(c.args, &stdout, &stderr)
		if code != c.code || !strings.Contains(stdout.String()+stderr.String(), c.want) {
			t.Errorf("run(%q) = %d, output %q; want %d and %q", c.args, code, stdout.String()+stderr.String(), c.code, c.want)
		}
	}
	for _, f := range []string{out, filepath.Join(dir, path.Base(schema.URL))} {
		if b, err := os.ReadFile(f); err != nil || !bytes.Equal(b, schema.JSON) {
			t.Errorf("%s isn't the committed schema (%v)", f, err)
		}
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"compat", bad, committed}, &bytes.Buffer{}, &bytes.Buffer{}); code != 1 {
		t.Errorf("compat with malformed JSON exited %d", code)
	}
}
