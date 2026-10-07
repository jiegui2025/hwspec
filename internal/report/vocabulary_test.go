package report

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

var update = flag.Bool("update", false, "add new values to testdata/vocabularies.json")

// published is the committed list of every vocabulary value a release
// may have written.
const published = "testdata/vocabularies.json"

// A published value stays (ADR 0003): a value renamed or removed fails
// here, and a new one must be added to the committed list (-update), so
// the change shows in review.
func TestVocabulariesOnlyGrow(t *testing.T) {
	b, err := os.ReadFile(published)
	if err != nil {
		t.Fatal(err)
	}
	var old map[string][]string
	if err := json.Unmarshal(b, &old); err != nil {
		t.Fatal(err)
	}
	for key, values := range old {
		for _, v := range values {
			if !slices.Contains(Vocabularies[key], v) {
				t.Errorf("%s: %q was published and is gone: a value stays, or the schema version changes (ADR 0003)", key, v)
			}
		}
	}
	for key, values := range Vocabularies {
		for i, v := range values {
			if slices.Contains(values[:i], v) {
				t.Errorf("%s: %q is listed twice", key, v)
			}
		}
	}
	if t.Failed() {
		return
	}
	if *update {
		b, err := json.MarshalIndent(Vocabularies, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(published, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	for key, values := range Vocabularies {
		for _, v := range values {
			if !slices.Contains(old[key], v) {
				t.Errorf("%s: %q is new: add it to %s (go test ./internal/report -update)", key, v, published)
			}
		}
	}
}

// vocabularyValues calls yield with every value v holds in a field with
// a vocabulary: a non-empty string, or a map's keys.
func vocabularyValues(v reflect.Value, yield func(key, value string)) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			vocabularyValues(v.Elem(), yield)
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			vocabularyValues(v.Index(i), yield)
		}
	case reflect.Map:
		for it := v.MapRange(); it.Next(); {
			vocabularyValues(it.Value(), yield)
		}
	case reflect.Struct:
		for i, f := range slices.Collect(v.Type().Fields()) {
			if !f.IsExported() {
				continue
			}
			fv := v.Field(i)
			if key := v.Type().Name() + "." + f.Name; Vocabularies[key] != nil {
				switch fv.Kind() {
				case reflect.String:
					if fv.String() != "" {
						yield(key, fv.String())
					}
				case reflect.Map:
					for it := fv.MapRange(); it.Next(); {
						yield(key, it.Key().String())
					}
				}
			}
			vocabularyValues(fv, yield)
		}
	}
}

// Every vocabulary value in a recorded machine's capture is listed: a
// collector writing a literal that isn't fails here.
func TestRecordedCapturesKeepToTheVocabularies(t *testing.T) {
	files, _ := filepath.Glob("../collect/testdata/machines/*/expected.json")
	if len(files) == 0 {
		t.Fatal("no recorded machines")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var r Report
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		seen := 0
		vocabularyValues(reflect.ValueOf(r), func(key, value string) {
			seen++
			if !slices.Contains(Vocabularies[key], value) {
				t.Errorf("%s: %s is %q, which report.Vocabularies doesn't list", f, key, value)
			}
		})
		if seen == 0 {
			t.Errorf("%s: no vocabulary values found", f)
		}
	}
}

func TestVocabularyValuesFindsStringsAndMapKeys(t *testing.T) {
	r := Report{
		CPU:     CPU{Health: &Health{Status: StatusOK, Metrics: map[string]float64{"made_up": 1}}},
		Storage: []Disk{{Firmware: &Firmware{Source: "nowhere"}}, {Firmware: &Firmware{}}},
	}
	var got []string
	vocabularyValues(reflect.ValueOf(&r), func(key, value string) { got = append(got, key+"="+value) })
	slices.Sort(got)
	if want := []string{"Firmware.Source=nowhere", "Health.Metrics=made_up", "Health.Status=ok"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
