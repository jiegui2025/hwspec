package edid

import "testing"

// EDID bytes come from a monitor, so Parse must handle anything without
// panicking.
func FuzzParse(f *testing.F) {
	f.Add(testEDID())
	f.Add(append(testEDID(), make([]byte, 128)...)) // with an extension block
	f.Add(header)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		info, err := Parse(b)
		if err == nil && info == nil {
			t.Fatal("no error and no result")
		}
	})
}
