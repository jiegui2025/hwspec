package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The format comes from -f, else the output file's extension, else the
// default; an extension hwspec doesn't know is refused with the fix to use.
func TestFormatComesFromTheFlagOrTheOutputExtension(t *testing.T) {
	cases := []struct{ format, path, want string }{
		{"", "spec.json", "json"}, {"", "spec.yml", "yaml"}, {"", "spec.txt", "text"},
		{"", "", "json"}, {"yaml", "spec.xml", "yaml"}, {"", "-", "json"},
		{"", "spec.yaml", "yaml"}, {"", "myspec", "json"},
		{"", "SPEC.JSON", "json"}, {"", "out.d/spec", "json"},
		// A dotfile's name isn't an extension.
		{"", ".hwspec", "json"}, {"", "out/.hwspec", "json"},
	}
	for _, c := range cases {
		if got, err := pickFormat(c.format, c.path, "json"); err != nil || got != c.want {
			t.Errorf("pickFormat(%q, %q) = %q, %v; want %q", c.format, c.path, got, err, c.want)
		}
	}
	for path, ext := range map[string]string{"spec.xml": ".xml", ".hwspec.xml": ".xml"} {
		_, err := pickFormat("", path, "json")
		if err == nil || !strings.Contains(err.Error(), ext) || !strings.Contains(err.Error(), "use -f json|yaml|text") {
			t.Errorf("%s: err = %v, want an error naming %s and the -f fix", path, err, ext)
		}
	}
	// A directory isn't a file to write: refuse it before capturing.
	for _, dir := range []string{".", "dir/.", ".."} {
		if _, err := pickFormat("", dir, "json"); err == nil {
			t.Errorf("%s: accepted as an output file", dir)
		}
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

// Under sudo, hwspec writes only where the user who ran sudo could have
// written: it never replaces a file that isn't theirs (a typo such as
// -o /etc/hosts), and creates a new file only in their own directory or a
// sticky world-writable one. The refusal says how to proceed.
func TestUnderSudoNeverReplaceOthersFiles(t *testing.T) {
	old := geteuid
	t.Cleanup(func() { geteuid = old })
	geteuid = func() int { return 0 }
	t.Setenv("SUDO_UID", strconv.Itoa(os.Getuid()))
	t.Setenv("SUDO_GID", strconv.Itoa(os.Getgid()))

	before, err := os.ReadFile("/etc/passwd") // owned by root
	if err != nil {
		t.Skip("no /etc/passwd")
	}
	if os.Getuid() == 0 {
		t.Skip("running as root: root owns /etc/passwd")
	}
	err = writeFileAtomic("/etc/passwd", []byte("{}"), 0o600)
	if err == nil || !strings.Contains(err.Error(), "/etc/passwd isn't yours") || !strings.Contains(err.Error(), "run hwspec without sudo") {
		t.Errorf("replacing a root file: %v", err)
	}
	if after, _ := os.ReadFile("/etc/passwd"); string(after) != string(before) {
		t.Fatal("/etc/passwd changed")
	}

	err = writeFileAtomic("/etc/hwspec-test-capture.json", []byte("{}"), 0o600)
	if err == nil || !strings.Contains(err.Error(), "/etc isn't your directory") {
		t.Errorf("creating in /etc: %v", err)
	}
	if _, err := os.Lstat("/etc/hwspec-test-capture.json"); err == nil {
		t.Fatal("created in /etc")
	}

	mine := filepath.Join(t.TempDir(), "spec.json")
	if err := writeFileAtomic(mine, []byte("{}"), 0o600); err != nil {
		t.Fatalf("a new file in my directory: %v", err)
	}
	if err := writeFileAtomic(mine, []byte("{\"x\":1}"), 0o600); err != nil {
		t.Fatalf("replacing my own file: %v", err)
	}
	if b, _ := os.ReadFile(mine); string(b) != `{"x":1}` {
		t.Errorf("my file holds %q", b)
	}

	tmp, err := os.Stat("/tmp")
	if err != nil || tmp.Mode()&os.ModeSticky == 0 || tmp.Mode().Perm()&0o002 == 0 {
		return // no sticky world-writable /tmp here
	}
	if sys, ok := tmp.Sys().(*syscall.Stat_t); ok && int(sys.Uid) == os.Getuid() {
		return // /tmp is ours: the sticky rule isn't what lets this through
	}
	shared := filepath.Join("/tmp", fmt.Sprintf("hwspec-sudo-test-%d.json", os.Getpid()))
	t.Cleanup(func() { os.Remove(shared) })
	if err := writeFileAtomic(shared, []byte("{}"), 0o600); err != nil {
		t.Errorf("a new file in sticky /tmp: %v", err)
	}
}

// A special file's owner is checked before it is opened: someone else's
// pipe with no reader is refused at once instead of blocking, and their
// device is never opened (opening a watchdog arms it). The opened
// descriptor must then be the file that was checked.
func TestSpecialFilesAreCheckedBeforeAndAfterOpening(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "out")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := os.Lstat(fifo)
	if err != nil {
		t.Fatal(err)
	}
	r, err := os.OpenFile(fifo, os.O_RDONLY|syscall.O_NONBLOCK, 0) // a reader already there: the open doesn't block
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := special(t, fifo, st, []byte("{}")); err != nil {
		t.Fatalf("own pipe with a reader: %v", err)
	}
	buf := make([]byte, 8)
	if n, _ := r.Read(buf); string(buf[:n]) != "{}" {
		t.Errorf("pipe received %q", buf[:n])
	}

	// Swapped after the check: the path now names another file.
	other := filepath.Join(dir, "other")
	if err := os.WriteFile(other, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := special(t, other, st, []byte("{}")); err == nil || !strings.Contains(err.Error(), "changed while") {
		t.Errorf("swapped file: %v", err)
	}
	if b, _ := os.ReadFile(other); string(b) != "keep" {
		t.Errorf("the swapped-in file holds %q", b)
	}
	if err := special(t, filepath.Join(dir, "missing"), st, nil); err == nil {
		t.Error("a missing special file opened")
	}

	// Someone else's pipe with no reader: refused at once, not blocked on.
	lonely := filepath.Join(dir, "lonely")
	if err := syscall.Mkfifo(lonely, 0o600); err != nil {
		t.Fatal(err)
	}
	old := geteuid
	t.Cleanup(func() { geteuid = old })
	geteuid = func() int { return os.Getuid() + 1 } // as if another user ran it
	done := make(chan error, 1)
	go func() { done <- writeFileAtomic(lonely, []byte("{}"), 0o600) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "pipe that belongs to someone else") {
			t.Errorf("someone else's pipe: %v", err)
		}
	case <-time.After(2 * time.Second):
		os.OpenFile(lonely, os.O_RDONLY|syscall.O_NONBLOCK, 0) // release the blocked open
		t.Fatal("blocked opening someone else's pipe")
	}
}

type dirInfo struct {
	os.FileInfo
	mode os.FileMode
	uid  uint32
}

func (d dirInfo) Mode() os.FileMode { return d.mode }
func (d dirInfo) Sys() any          { return &syscall.Stat_t{Uid: d.uid} }

// A new file may go where the user could have created it: their own
// directory, or a sticky world-writable one; not a world-writable one
// without the sticky bit (anyone could replace it), nor a sticky one
// only its owner can write.
func TestUserMayCreateIn(t *testing.T) {
	for _, c := range []struct {
		mode os.FileMode
		uid  uint32
		want bool
	}{
		{os.ModeDir | 0o700, 1000, true},
		{os.ModeDir | 0o755, 0, false},
		{os.ModeDir | os.ModeSticky | 0o777, 0, true},
		{os.ModeDir | 0o777, 0, false},
		{os.ModeDir | os.ModeSticky | 0o755, 0, false},
	} {
		if got := userMayCreateIn(dirInfo{mode: c.mode, uid: c.uid}, 1000); got != c.want {
			t.Errorf("%v owned by %d: %v, want %v", c.mode, c.uid, got, c.want)
		}
	}
}

// A symlink swapped in for a special file after the check can't redirect
// the write: not out of the directory (to a device), nor to another file
// in it.
func TestSwappedInSymlinksAreRefused(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := os.Lstat(fifo)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "other")
	if err := os.WriteFile(other, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	for target, want := range map[string]string{"/dev/null": "escapes", "other": "changed while"} {
		link := filepath.Join(dir, "out")
		os.Remove(link)
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if err := special(t, link, st, []byte("{}")); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("a link to %s: %v, want %q", target, err, want)
		}
	}
	if b, _ := os.ReadFile(other); string(b) != "keep" {
		t.Errorf("%s holds %q", other, b)
	}
}

// Under sudo, even the user's own file is written only in a directory
// they could write in: replacing it puts a new file in the directory.
func TestSudoMayWriteOnlyInTheUsersDirectories(t *testing.T) {
	const way = "run hwspec without sudo"
	mine := dirInfo{mode: 0o600, uid: 1000}
	theirs := dirInfo{mode: 0o644, uid: 0}
	myDir := dirInfo{mode: os.ModeDir | 0o700, uid: 1000}
	rootDir := dirInfo{mode: os.ModeDir | 0o755, uid: 0}
	for _, c := range []struct {
		name     string
		existing os.FileInfo
		dir      os.FileInfo
		want     string
	}{
		{"new in mine", nil, myDir, ""},
		{"mine in mine", mine, myDir, ""},
		{"theirs in mine", theirs, myDir, "/d/f isn't yours"},
		{"new in root's", nil, rootDir, "/d isn't your directory: as root under sudo, hwspec won't write f there"},
		{"mine in root's", mine, rootDir, "/d isn't your directory"},
	} {
		err := sudoMayWrite("/d/f", c.existing, c.dir, 1000)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), way)):
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}

// The directory is opened once and everything after is relative to it:
// swapping it for a symlink after the checks can't redirect the write.
func TestASwappedDirectoryCantRedirectTheWrite(t *testing.T) {
	base := t.TempDir()
	dir, victim, moved := filepath.Join(base, "out"), filepath.Join(base, "victim"), filepath.Join(base, "moved")
	for _, d := range []string{dir, victim} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(victim, "spec.json"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := afterChecks
	t.Cleanup(func() { afterChecks = old })
	afterChecks = func() {
		if err := os.Rename(dir, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, dir); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeFileAtomic(filepath.Join(dir, "spec.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(victim, "spec.json")); string(b) != "keep" {
		t.Errorf("the write followed the swapped-in symlink: victim holds %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(moved, "spec.json")); string(b) != "{}" {
		t.Errorf("the directory that was checked holds %q", b)
	}
}

// Errors from inside the opened directory name the whole path.
func TestErrorsNameTheWholePath(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root isn't stopped by permissions")
	}
	dir := filepath.Join(t.TempDir(), "nosearch")
	if err := os.Mkdir(dir, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	path := filepath.Join(dir, "spec.json")
	if err := writeFileAtomic(path, []byte("{}"), 0o600); err == nil || !strings.Contains(err.Error(), path+": permission denied") {
		t.Errorf("err = %v, want it to name %s", err, path)
	}
	if err := withPath(io.EOF, dir); !errors.Is(err, io.EOF) {
		t.Errorf("a non-path error changed: %v", err)
	}
}

// A write that fails part way leaves no temporary file behind: here
// handing the file to a group the user isn't in fails, as for a
// non-root process.
func TestAFailedWriteLeavesNothingBehind(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root may give a file to any group")
	}
	groups, _ := os.Getgroups()
	if os.Getgid() == 0 || slices.Contains(groups, 0) {
		t.Skip("in group 0")
	}
	old := geteuid
	t.Cleanup(func() { geteuid = old })
	geteuid = func() int { return 0 }
	t.Setenv("SUDO_UID", strconv.Itoa(os.Getuid()))
	t.Setenv("SUDO_GID", "0")
	dir := t.TempDir()
	if err := writeFileAtomic(filepath.Join(dir, "spec.json"), []byte("{}"), 0o600); err == nil {
		t.Fatal("a chown to group 0 succeeded")
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("left behind: %v", left[0].Name())
	}
}

// special calls writeSpecial as writeFileAtomic does, with path's
// directory opened.
func special(t *testing.T, path string, st os.FileInfo, data []byte) error {
	t.Helper()
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	return writeSpecial(root, filepath.Base(path), path, st, data)
}

// A special file is opened inside the directory already opened: a
// directory swapped for a symlink afterwards doesn't lead the open
// elsewhere (to someone else's device).
func TestSpecialFilesAreOpenedInTheOpenedDirectory(t *testing.T) {
	base := t.TempDir()
	dir, decoy, moved := filepath.Join(base, "out"), filepath.Join(base, "decoy"), filepath.Join(base, "moved")
	for _, d := range []string{dir, decoy} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	fifo := filepath.Join(dir, "spec")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(decoy, "spec"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := os.Lstat(fifo)
	if err != nil {
		t.Fatal(err)
	}
	r, err := os.OpenFile(fifo, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(decoy, dir); err != nil {
		t.Fatal(err)
	}
	if err := writeSpecial(root, "spec", fifo, st, []byte("{}")); err != nil {
		t.Fatalf("the pipe in the opened directory: %v", err)
	}
	buf := make([]byte, 8)
	if n, _ := r.Read(buf); string(buf[:n]) != "{}" {
		t.Errorf("pipe received %q", buf[:n])
	}
}

// A path ending in a slash names a directory: refused, never written
// inside it ("out/" isn't "out/out").
func TestATrailingSlashIsADirectory(t *testing.T) {
	dir := t.TempDir()
	for _, path := range []string{dir + "/", filepath.Join(dir, "missing") + "/", dir + "/."} {
		err := writeFileAtomic(path, []byte("{}"), 0o600)
		if err == nil || !strings.Contains(err.Error(), "is a directory") {
			t.Errorf("%s: %v", path, err)
		}
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("wrote %s inside the directory", left[0].Name())
	}
}
