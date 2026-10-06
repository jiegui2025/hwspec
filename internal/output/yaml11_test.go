package output

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/jiegui2025/hwspec/internal/report"
)

// yaml11 matches a plain scalar that a YAML 1.1 parser (PyYAML, Ruby's
// Psych) reads as something other than a string: the implicit types of
// https://yaml.org/type/ (bool, int, float, null, timestamp, merge, value).
// Floats follow PyYAML's resolver rather than the spec's `\.[0-9.]*`, which
// no parser implements: "1.1.1" stays a string, "1.2" doesn't.
var yaml11 = regexp.MustCompile(`^(?:` +
	`y|Y|yes|Yes|YES|n|N|no|No|NO|true|True|TRUE|false|False|FALSE|on|On|ON|off|Off|OFF` + // bool
	`|[-+]?0b[0-1_]+|[-+]?0[0-7_]+|[-+]?(?:0|[1-9][0-9_]*)|[-+]?0x[0-9a-fA-F_]+|[-+]?[1-9][0-9_]*(?::[0-5]?[0-9])+` + // int
	`|[-+]?[0-9][0-9_]*\.[0-9_]*(?:[eE][-+][0-9]+)?|\.[0-9][0-9_]*(?:[eE][-+][0-9]+)?|[-+]?[0-9][0-9_]*(?::[0-5]?[0-9])+\.[0-9_]*|[-+]?\.(?:inf|Inf|INF)|\.(?:nan|NaN|NAN)` + // float
	`|~|null|Null|NULL|` + // null, including the empty string
	`|[0-9]{4}-[0-9]{2}-[0-9]{2}|[0-9]{4}-[0-9]{1,2}-[0-9]{1,2}(?:[Tt]|[ \t]+)[0-9]{1,2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]*)?(?:[ \t]*(?:Z|[-+][0-9]{1,2}(?::[0-9]{2})?))?` + // timestamp
	`|<<|=)$`) // merge, value

// plainNonStrings lists the strings in a JSON document that its YAML
// output writes plain although YAML 1.1 reads them as another type. The
// two trees have the same shape: the YAML is generated from the JSON.
func plainNonStrings(t *testing.T, js []byte, out []byte) []string {
	t.Helper()
	var from, to yaml.Node
	if err := yaml.Unmarshal(js, &from); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(out, &to); err != nil {
		t.Fatal(err)
	}
	var bad []string
	var walk func(a, b *yaml.Node)
	walk = func(a, b *yaml.Node) {
		if a.Kind == yaml.ScalarNode && a.Tag == "!!str" && b.Style == 0 && yaml11.MatchString(b.Value) {
			bad = append(bad, b.Value)
		}
		for i := range a.Content {
			if i < len(b.Content) {
				walk(a.Content[i], b.Content[i])
			}
		}
	}
	walk(&from, &to)
	return bad
}

// Strings stay strings for YAML 1.1 parsers too: PCI addresses
// (0000:02:00.0 is a base-60 float in YAML 1.1), all-digit MAC addresses,
// "on", "NO", octal-looking serials, dates. Safe strings stay plain.
func TestYAMLQuotesStringsThatYAML11ReadsAsSomethingElse(t *testing.T) {
	r := sample()
	tricky := []string{"=", "<<", "1.2", "0000:02:00.0", "0000:00:14.0", "12:34:56:00:11:22", "on", "NO", "017", "1_000", ".5", "~", "",
		"2026-10-05", "2026-10-05T12:00:00Z", "0x10", "1:30"}
	for _, s := range tricky {
		r.PCI = append(r.PCI, report.PCIDevice{Address: s, Identity: &report.Identity{Model: "HP EliteDesk 800", Serial: s}})
	}
	js, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var y bytes.Buffer
	if err := Write(&y, r, "yaml"); err != nil {
		t.Fatal(err)
	}
	if bad := plainNonStrings(t, js, y.Bytes()); len(bad) > 0 {
		t.Errorf("written plain, read as another type by YAML 1.1: %q", bad)
	}
	if !bytes.Contains(y.Bytes(), []byte("model: HP EliteDesk 800\n")) {
		t.Error("a safe string was quoted (or the model is missing)")
	}
	back, err := Read(y.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.PCI, r.PCI) {
		t.Errorf("YAML round trip changed the devices:\n got %+v\nwant %+v", back.PCI, r.PCI)
	}
}

// The same holds for every recorded machine's capture.
func TestRecordedCapturesAreYAML11Safe(t *testing.T) {
	files, _ := filepath.Glob("../collect/testdata/machines/*/expected.json")
	if len(files) == 0 {
		t.Fatal("no recorded machines")
	}
	for _, f := range files {
		js, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		r, err := Read(js)
		if err != nil {
			t.Fatal(err)
		}
		var y bytes.Buffer
		if err := Write(&y, r, "yaml"); err != nil {
			t.Fatal(err)
		}
		canon, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if bad := plainNonStrings(t, canon, y.Bytes()); len(bad) > 0 {
			t.Errorf("%s: written plain, read as another type by YAML 1.1: %q", f, bad)
		}
	}
}

// The YAML 1.1 pattern itself: it must match what PyYAML reads as another
// type, or the tests above prove nothing.
func TestYAML11Pattern(t *testing.T) {
	for s, other := range map[string]bool{
		"0000:02:00.0": true, "0000:00:14.0": true, "12:34:56:00:11:22": true, "on": true, "NO": true, "017": true,
		"1_000": true, ".5": true, "~": true, "": true, "2026-10-05": true, "2026-10-05T12:00:00Z": true, "0x10": true, "1:30": true,
		"=": true, "<<": true, "1.2": true, "1.1.1": false, "0000:00:1f.6": false, "60:F2:62:15:AB:5C": false, "HP EliteDesk 800": false, "2020-W38": false, "Intel(R)": false,
	} {
		if got := yaml11.MatchString(s); got != other {
			t.Errorf("%q: matches %v, want %v", s, got, other)
		}
	}
}

// Captures written before strings kept their quotes still read: YAML 1.2
// (yaml.v3) takes a plain 0000:02:00.0 or 123456001122 as a string, and
// the fields are strings.
func TestOldUnquotedYAMLCapturesStillRead(t *testing.T) {
	old := []byte(`schema_version: 1
tool:
  name: hwspec
  version: v0.0.0
captured_at: 2026-10-05T12:00:00Z
pci:
  - address: 0000:02:00.0
    vendor_id: "8086"
  - address: 0000:00:14.0
network:
  - name: eth0
    mac: 12:34:56:00:11:22
`)
	r, err := Read(old)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.PCI) != 2 || r.PCI[0].Address != "0000:02:00.0" || r.PCI[1].Address != "0000:00:14.0" {
		t.Errorf("pci = %+v", r.PCI)
	}
	if len(r.Network) != 1 || r.Network[0].MAC != "12:34:56:00:11:22" {
		t.Errorf("network = %+v", r.Network)
	}
	if want := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC); !r.CapturedAt.Equal(want) {
		t.Errorf("captured_at = %v", r.CapturedAt)
	}
}

// plainWord lets needsQuotes skip encoding a string; it must only ever
// say "plain" for strings that YAML 1.1 and the encoder both read as
// strings. Every case spelling of the boolean and null words is checked.
func TestPlainWordShortcutNeverSkipsAQuote(t *testing.T) {
	words := []string{"pci", "Intel", "x86_64", "e", "E1", "nan", "inf", "Nil", "_x", "a_b_9", "NVMe", "i915", "eno1", "ondemand", "offline", "yesterday"}
	// YAML 1.1's boolean and null words (https://yaml.org/type/bool.html,
	// null.html), listed here independently of yaml11Words.
	for _, w := range []string{"y", "n", "yes", "no", "on", "off", "true", "false", "null"} {
		for mask := 0; mask < 1<<len(w); mask++ {
			b := []byte(w)
			for i := range b {
				if mask&(1<<i) != 0 {
					b[i] -= 'a' - 'A'
				}
			}
			words = append(words, string(b))
		}
	}
	for _, w := range words {
		if !plainWord(w) {
			continue
		}
		out, err := yaml.Marshal(w)
		if err != nil {
			t.Fatal(err)
		}
		if yaml11.MatchString(w) || out[0] == '"' || out[0] == '\'' {
			t.Errorf("plainWord(%q) = true, but YAML 1.1 or the encoder doesn't read it as a plain string", w)
		}
	}
}
