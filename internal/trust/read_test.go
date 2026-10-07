package trust

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Only a regular file within the limit is read: a symlink, a FIFO (which
// would block), a directory or a larger file are refused, without hanging.
func TestReadRegular(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, err := ReadRegular(file, 5); err != nil || string(b) != "12345" {
		t.Errorf("regular: %q %v", b, err)
	}
	if _, err := ReadRegular(file, 4); err == nil || !strings.Contains(err.Error(), "larger than 4 bytes") {
		t.Errorf("too large: %v", err)
	}
	link := filepath.Join(dir, "l")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRegular(link, 5); err == nil || !strings.Contains(err.Error(), "is a symlink") {
		t.Errorf("symlink: %v", err)
	}
	fifo := filepath.Join(dir, "p")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := ReadRegular(fifo, 5); done <- err }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "isn't a regular file") {
			t.Errorf("fifo: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked on a FIFO")
	}
	if _, err := ReadRegular(dir, 5); err == nil || !strings.Contains(err.Error(), "isn't a regular file") {
		t.Errorf("directory: %v", err)
	}
	if _, err := ReadRegular(filepath.Join(dir, "none"), 5); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
}
