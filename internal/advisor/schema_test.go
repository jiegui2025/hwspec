package advisor

import (
	"encoding/json"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/jiegui2025/hwspec/schema"
)

// Every check's example advice validates against the published advice
// schema (#83), and names it in $schema: what `hwspec advise -f json`
// prints is what the schema promises.
func TestAdviceValidatesAgainstItsSchema(t *testing.T) {
	var s jsonschema.Schema
	if err := json.Unmarshal(schema.AdviceJSON, &s); err != nil {
		t.Fatal(err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	findings := 0
	for _, name := range Checks() {
		rule, r := checks[name].example()
		a := Advise(exampleInput(name, rule, r))
		findings += len(a.Findings)
		if a.Schema != schema.AdviceURL {
			t.Errorf("%s: $schema = %q", name, a.Schema)
		}
		b, err := json.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		var doc any
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatal(err)
		}
		if err := rs.Validate(doc); err != nil {
			t.Errorf("%s: %v\n%s", name, err, b)
		}
	}
	if findings < len(checks) {
		t.Errorf("only %d findings from %d checks' examples", findings, len(checks))
	}
}
