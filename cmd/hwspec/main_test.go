package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestUnknownOutputExtensionIsAnError(t *testing.T) {
	cases := []struct{ format, path, want string }{
		{"", "spec.json", "json"}, {"", "spec.yml", "yaml"}, {"", "spec.txt", "text"},
		{"", "", "json"}, {"yaml", "spec.xml", "yaml"}, {"", "-", "json"},
		{"", "spec.yaml", "yaml"}, {"", "myspec", "json"},
	}
	for _, c := range cases {
		if got, err := pickFormat(c.format, c.path, "json"); err != nil || got != c.want {
			t.Errorf("pickFormat(%q, %q) = %q, %v; want %q", c.format, c.path, got, err, c.want)
		}
	}
	if _, err := pickFormat("", "spec.xml", "json"); err == nil || !strings.Contains(err.Error(), ".xml") {
		t.Errorf("spec.xml: err = %v, want an error naming .xml", err)
	}
	if _, err := pickFormat("csv", "", "json"); err == nil {
		t.Error("unknown -f accepted")
	}
}

// The process's own streams (-o /dev/stdout, >(cmd)) are written into.
func TestOwnStreamsAreWrittenInto(t *testing.T) {
	for _, p := range []string{"/dev/null", "/dev/stdout", "/dev/fd/1", "/proc/self/fd/1"} {
		if !ownStream(p) {
			t.Errorf("ownStream(%q) = false", p)
		}
	}
	for _, p := range []string{"/dev/shm/spec.json", "/dev/sda", "spec.json"} {
		if ownStream(p) {
			t.Errorf("ownStream(%q) = true", p)
		}
	}
	if err := writeFileAtomic("/dev/null", []byte("x"), 0o600); err != nil {
		t.Fatalf("writing to /dev/null: %v", err)
	}
	if st, err := os.Lstat("/dev/null"); err != nil || st.Mode()&os.ModeCharDevice == 0 {
		t.Fatalf("/dev/null is no longer a character device: %v", err)
	}
}

// A symlink someone planted at the output path is replaced, never
// followed, even when it points at a device.
func TestPlantedSymlinksAreReplacedNotFollowed(t *testing.T) {
	p := filepath.Join(t.TempDir(), "spec.json")
	if err := os.Symlink("/dev/null", p); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(p, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Lstat(p); !st.Mode().IsRegular() {
		t.Errorf("%s is %v, want a regular file replacing the link", p, st.Mode())
	}
}

// The caller's own pipe is written into; a pipe owned by someone else is
// refused, so a FIFO planted in /tmp can't collect the capture.
func TestPipesAreWrittenOnlyIfTheCallerOwnsThem(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "spec.json")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	got := make(chan []byte)
	go func() {
		b, _ := os.ReadFile(fifo)
		got <- b
	}()
	if err := writeFileAtomic(fifo, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b := <-got; string(b) != "{}" {
		t.Errorf("pipe received %q", b)
	}
	st, err := os.Lstat(fifo)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkOwner(fifo, st, os.Geteuid()+1); err == nil || !strings.Contains(err.Error(), "pipe that belongs to someone else") {
		t.Errorf("someone else's pipe: err = %v", err)
	}
}

func TestTheUmaskCanOnlyTightenModes(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	p := filepath.Join(t.TempDir(), "spec.json")
	if err := writeFileAtomic(p, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v under umask 077, want 0600", st.Mode().Perm())
	}
}

func TestWritesAreAtomicAndPrivateWhenTheyHoldSerials(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "spec.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(link, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Errorf("write followed the symlink and clobbered %s", target)
	}
	st, err := os.Lstat(link)
	if err != nil || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm() != 0o600 {
		t.Errorf("spec.json: mode %v, err %v; want a regular 0600 file", st.Mode(), err)
	}
}
