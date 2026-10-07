package ids

import (
	"testing"

	"github.com/jiegui2025/hwspec/internal/netrule"
)

// httpNames are the only net/http names this package may use: none of
// them can open a connection except through http.DefaultTransport.
var httpNames = map[string]bool{
	"Client": true, "NewRequestWithContext": true, "NewRequest": true,
	"MethodGet": true, "StatusOK": true, "Response": true, "Request": true, "Header": true,
}

// Every network request leaves through http.DefaultTransport (see
// netrule); depguard lets only sync.go import net.
func TestRequestsOnlyGoThroughTheDefaultTransport(t *testing.T) {
	problems, err := netrule.Check(".", httpNames)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}
