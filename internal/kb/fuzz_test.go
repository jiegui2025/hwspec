package kb

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"testing"
)

// The knowledge base also arrives in the downloaded bundle (#185), so Parse
// must handle any content without panicking, and what it accepts must
// encode and parse back. The fuzzer mutates the JSON, which is compressed
// before Parse: mutating gzip bytes would rarely get past the decompressor.
func FuzzParse(f *testing.F) {
	if b, err := os.ReadFile("data/advisor-v1.json.gz"); err == nil {
		if zr, err := gzip.NewReader(bytes.NewReader(b)); err == nil {
			if raw, err := io.ReadAll(zr); err == nil {
				f.Add(raw)
			}
		}
	}
	f.Add([]byte(`{"format":1,"version":"2026-10-06T00:00:00Z","sources":[],"rules":[]}`))
	f.Add([]byte(`{"format":2}`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write(raw)
		_ = zw.Close()
		k, err := Parse(buf.Bytes())
		if err != nil {
			return
		}
		enc, err := Encode(k)
		if err != nil {
			t.Fatalf("an accepted knowledge base doesn't encode: %v", err)
		}
		if _, err := Parse(enc); err != nil {
			t.Fatalf("an encoded knowledge base doesn't parse back: %v", err)
		}
	})
}
