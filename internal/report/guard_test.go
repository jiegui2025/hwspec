package report

import (
	"bufio"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Guards for the invariants a new field could skip without anything
// failing (#149): every string a report can hold is classified for
// redaction, and sanitising reaches into every map.

// jsonName is a struct field's name in the JSON, "" for one left out.
func jsonName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	switch name {
	case "-":
		return ""
	case "":
		return f.Name
	}
	return name
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// stringPaths lists every string a value of type t can hold, by JSON
// path: a field "system.uuid", a list element "cpu.flags[]", a map's key
// "health.metrics{key}" and value "id_databases{}".
func stringPaths(t *testing.T, typ reflect.Type, path string, out *[]string) {
	t.Helper()
	switch typ.Kind() {
	case reflect.String:
		*out = append(*out, path)
	case reflect.Pointer:
		stringPaths(t, typ.Elem(), path, out)
	case reflect.Slice, reflect.Array:
		stringPaths(t, typ.Elem(), path+"[]", out)
	case reflect.Map:
		stringPaths(t, typ.Key(), path+"{key}", out)
		stringPaths(t, typ.Elem(), path+"{}", out)
	case reflect.Struct:
		for f := range typ.Fields() {
			if f.IsExported() && jsonName(f) != "" {
				stringPaths(t, f.Type, join(path, jsonName(f)), out)
			}
		}
	case reflect.Interface:
		t.Errorf("%s: an interface can hold anything; give it a concrete type", path)
	}
}

// fill gives every string reachable from v a value: its own path, or a
// day-precise date for a manufacture date.
func fill(v reflect.Value, path string) {
	switch v.Kind() {
	case reflect.String:
		if strings.HasSuffix(path, "manufacture_date") {
			v.SetString("2021-03-17")
		} else {
			v.SetString("marker:" + path)
		}
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem(), path)
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fill(v.Index(0), path+"[]")
	case reflect.Array:
		for i := range v.Len() {
			fill(v.Index(i), path+"[]")
		}
	case reflect.Map:
		k, e := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		fill(k, path+"{key}")
		fill(e, path+"{}")
		v.Set(reflect.MakeMap(v.Type()))
		v.SetMapIndex(k, e)
	case reflect.Struct:
		for i := range v.NumField() {
			if f := v.Type().Field(i); f.IsExported() && jsonName(f) != "" {
				fill(v.Field(i), join(path, jsonName(f)))
			}
		}
	}
}

// stringsAt collects every string reachable from v, by path.
func stringsAt(v reflect.Value, path string, out map[string][]string) {
	switch v.Kind() {
	case reflect.String:
		out[path] = append(out[path], v.String())
	case reflect.Pointer:
		if !v.IsNil() {
			stringsAt(v.Elem(), path, out)
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			stringsAt(v.Index(i), path+"[]", out)
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			stringsAt(k, path+"{key}", out)
			stringsAt(v.MapIndex(k), path+"{}", out)
		}
	case reflect.Struct:
		for i := range v.NumField() {
			if f := v.Type().Field(i); f.IsExported() && jsonName(f) != "" {
				stringsAt(v.Field(i), join(path, jsonName(f)), out)
			}
		}
	}
}

// readClassification reads testdata/redaction.txt: "<path> <class>" per
// line, # comments.
func readClassification(t *testing.T) map[string]string {
	t.Helper()
	f, err := os.Open("testdata/redaction.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	classes := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line, _, _ := strings.Cut(sc.Text(), "#")
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 || !slices.Contains([]string{"kept", "redacted", "month"}, fields[1]) {
			t.Fatalf("testdata/redaction.txt: %q isn't \"<path> kept|redacted|month\"", sc.Text())
		}
		if _, dup := classes[fields[0]]; dup {
			t.Errorf("testdata/redaction.txt: %s twice", fields[0])
		}
		classes[fields[0]] = fields[1]
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return classes
}

// Every string a report can hold is classified in testdata/redaction.txt,
// and Redact does what the classification says: a new field fails here
// until someone decides whether it identifies the machine or its owner.
func TestRedactionClassifiesEveryString(t *testing.T) {
	classes := readClassification(t)
	var paths []string
	stringPaths(t, reflect.TypeFor[Report](), "", &paths)
	for _, p := range paths {
		if classes[p] == "" {
			t.Errorf("%s isn't classified: add it to testdata/redaction.txt as kept, redacted or month (and to Redact if redacted)", p)
		}
	}
	for p := range classes {
		if !slices.Contains(paths, p) {
			t.Errorf("testdata/redaction.txt: %s is no longer in the report", p)
		}
	}

	r := &Report{}
	fill(reflect.ValueOf(r).Elem(), "")
	r.Redact()
	got := map[string][]string{}
	stringsAt(reflect.ValueOf(r).Elem(), "", got)
	for _, p := range paths {
		for _, v := range got[p] {
			var want string
			switch classes[p] {
			case "kept":
				want = "marker:" + p
				if strings.HasSuffix(p, "manufacture_date") {
					want = "2021-03-17"
				}
			case "month":
				want = "2021-03"
			}
			if v != want {
				t.Errorf("%s (%s) is %q after Redact, want %q", p, classes[p], v, want)
			}
		}
	}
}

// Sanitising reaches every string, whatever holds it: map keys and the
// strings inside map values of any type.
func TestSanitizeEntersEveryMap(t *testing.T) {
	type identities struct {
		Lists   map[string][]string
		IDs     map[string]Identity
		Indexed map[int]string
		Nested  map[string]map[string]string
		Ptrs    map[string]*Identity
	}
	const bad = "a\x1b[2Jb"
	v := identities{
		Lists:   map[string][]string{bad: {bad}},
		IDs:     map[string]Identity{"x": {Model: bad}},
		Indexed: map[int]string{1: bad},
		Nested:  map[string]map[string]string{"x": {bad: bad}},
		Ptrs:    map[string]*Identity{"x": {Serial: bad}},
	}
	sanitizeValue(reflect.ValueOf(&v).Elem())
	got := map[string][]string{}
	stringsAt(reflect.ValueOf(v), "", got)
	for _, p := range []string{"Lists{key}", "Lists{}[]", "IDs{}.model", "Indexed{}", "Nested{}{key}", "Nested{}{}", "Ptrs{}.serial"} {
		if !slices.Equal(got[p], []string{"a[2Jb"}) {
			t.Errorf("%s = %q, want the escape removed", p, got[p])
		}
	}
}
