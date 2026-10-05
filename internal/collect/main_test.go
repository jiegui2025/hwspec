package collect

import (
	"os"
	"testing"

	"github.com/jiegui2025/hwspec/internal/ids"
)

// Captures resolve names from the ID databases; tests use only the ones
// built into hwspec, so results don't depend on the machine running them
// (its distribution's hwdata, synced databases or overrides).
func TestMain(m *testing.M) {
	ids.UseEmbeddedOnly()
	os.Exit(m.Run())
}
