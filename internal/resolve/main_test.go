package resolve

import (
	"os"
	"testing"

	"github.com/jiegui2025/hwspec/internal/ids"
)

// Names come from the databases built into hwspec only, so the tests
// don't depend on the machine's hwdata, synced databases or overrides.
func TestMain(m *testing.M) {
	ids.UseEmbeddedOnly()
	os.Exit(m.Run())
}
