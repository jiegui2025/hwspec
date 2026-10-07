package output

import (
	"bytes"
	"os"
	"testing"
)

// Captures read by show and advise may come from anyone, so Read must
// handle any bytes without panicking, and what it accepts must write and
// read back.
func FuzzRead(f *testing.F) {
	if b, err := os.ReadFile("../collect/testdata/machines/hp-elitedesk-800-g5-mini/expected.json"); err == nil {
		f.Add(b)
		if r, err := Read(b); err == nil {
			var y bytes.Buffer
			if YAML(&y, r) == nil {
				f.Add(y.Bytes())
			}
		}
	}
	f.Add([]byte(`{"tool":{"name":"hwspec"},"schema_version":1}`))
	f.Add([]byte("tool: {name: hwspec}\nschema_version: 1\n"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		r, err := Read(b)
		if err != nil {
			return
		}
		var out bytes.Buffer
		if err := Write(&out, r, "json"); err != nil {
			t.Fatalf("an accepted capture doesn't write: %v", err)
		}
		if _, err := Read(out.Bytes()); err != nil {
			t.Fatalf("a written capture doesn't read back: %v", err)
		}
	})
}
