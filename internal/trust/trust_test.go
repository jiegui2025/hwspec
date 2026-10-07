package trust

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestUserOwnedFilesAreNotTrusted(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: every file is root-owned")
	}
	f := filepath.Join(t.TempDir(), "hwspec")
	if err := os.WriteFile(f, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := RootOwned(f); err == nil {
		t.Error("a user-owned file was trusted")
	}
	// A symlink to it doesn't launder it.
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(f, link); err != nil {
		t.Fatal(err)
	}
	if err := RootOwned(link); err == nil {
		t.Error("a symlink to a user-owned file was trusted")
	}
}

func TestHostSystemFilesAreTrusted(t *testing.T) {
	hostTest(t)
	for _, sys := range []string{"/usr/bin/env", "/bin/sh"} {
		st, err := os.Stat(sys)
		if err != nil {
			continue
		}
		// Build sandboxes (Nix) give system files to the build user.
		if owner, ok := st.Sys().(*syscall.Stat_t); !ok || owner.Uid != 0 {
			t.Skipf("%s isn't root-owned here (build sandbox)", sys)
		}
		if err := RootOwned(sys); err != nil {
			t.Errorf("%s: %v", sys, err)
		}
		return
	}
}

// /nix/store is root-owned, group-writable and sticky; files under it
// can't be replaced by the group, so Nix-installed hwspec must be trusted.
func TestHostStickyWritableDirectoriesAreTrusted(t *testing.T) {
	hostTest(t)
	if _, err := os.Stat("/nix/store"); err != nil {
		t.Skip("no /nix/store on this system")
	}
	entries, _ := filepath.Glob("/nix/store/*-coreutils-*/bin/env")
	if len(entries) == 0 {
		t.Skip("no coreutils in /nix/store")
	}
	if err := RootOwned(entries[0]); err != nil {
		t.Errorf("%s: %v", entries[0], err)
	}
}

// A root-owned file anyone can write isn't trusted, nor is a path that
// doesn't exist.
func TestWritableOrMissingFilesAreNotTrusted(t *testing.T) {
	if st, err := os.Stat("/dev/null"); err == nil {
		if owner, ok := st.Sys().(*syscall.Stat_t); ok && owner.Uid == 0 {
			if err := RootOwned("/dev/null"); err == nil || !strings.Contains(err.Error(), "writable by non-root users") {
				t.Errorf("/dev/null: %v", err)
			}
		}
	}
	if err := RootOwned("/nonexistent/hwspec"); err == nil {
		t.Error("a missing path was trusted")
	}
}

// A FUSE filesystem's daemon can claim any owner, so nothing on one is
// trusted.
func TestHostFilesOnFUSEAreNotTrusted(t *testing.T) {
	hostTest(t)
	mounts, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		t.Skip(err)
	}
	for line := range strings.SplitSeq(string(mounts), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || f[2] != "fuse" && !strings.HasPrefix(f[2], "fuse.") {
			continue
		}
		if _, err := os.Stat(f[1]); err != nil {
			continue
		}
		if err := RootOwned(f[1]); err == nil || !strings.Contains(err.Error(), "FUSE") {
			t.Errorf("%s (%s): %v", f[1], f[2], err)
		}
		return
	}
	t.Skip("no readable FUSE mount here")
}

// hostTest marks a test whose coverage depends on this machine (Nix, FUSE
// mounts, who owns system files); CI's coverage run skips them.
func hostTest(t *testing.T) {
	t.Helper()
	if !strings.HasPrefix(t.Name(), "TestHost") {
		t.Fatalf("%s depends on this machine, so it must be named TestHost*: CI runs host tests by that prefix (-run '^TestHost')", t.Name())
	}
	if os.Getenv("HWSPEC_SKIP_HOST_TESTS") != "" {
		t.Skip("depends on this machine (HWSPEC_SKIP_HOST_TESTS is set)")
	}
}

// The rules on a tree built without root, trusting this user as root is
// trusted: a sticky world-writable directory (like /tmp or /nix/store)
// may hold a trusted file, but the file itself must not be writable by
// others, and a non-sticky world-writable directory taints everything
// under it.
func TestTheOwnershipRulesOnATree(t *testing.T) {
	me := uint32(os.Getuid())
	trusted := func(uid uint32) bool { return uid == me || uid == 0 }
	dir := t.TempDir()
	sticky := filepath.Join(dir, "sticky")
	open := filepath.Join(dir, "open")
	for d, mode := range map[string]os.FileMode{sticky: 0o777 | os.ModeSticky, open: 0o777} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(d, mode); err != nil {
			t.Fatal(err)
		}
	}
	file := func(path string, mode os.FileMode) string {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for path, want := range map[string]string{
		file(filepath.Join(sticky, "hwspec"), 0o755):   "",
		file(filepath.Join(sticky, "writable"), 0o777): "writable by non-root users",
		// The sticky bit protects a directory's entries, not a file's bytes.
		file(filepath.Join(sticky, "sticky-file"), 0o777|os.ModeSticky): "writable by non-root users",
		file(filepath.Join(open, "hwspec"), 0o755):                      "writable by non-root users",
	} {
		err := checkTree(path, trusted)
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: %v (want %q)", strings.TrimPrefix(path, dir), err, want)
		}
	}
	// Someone else's file is refused.
	if err := checkTree(filepath.Join(sticky, "hwspec"), func(uint32) bool { return false }); err == nil || !strings.Contains(err.Error(), "owned by a non-root user") {
		t.Errorf("untrusted owner: %v", err)
	}
}

// A FUSE filesystem's daemon can claim any owner, so nothing on one is
// trusted; a filesystem that can't be identified isn't either.
func TestFUSEAndUnknownFilesystemsAreRefused(t *testing.T) {
	old := statfs
	t.Cleanup(func() { statfs = old })
	path := filepath.Join(t.TempDir(), "hwspec")
	if err := os.WriteFile(path, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	statfs = func(_ string, fs *unix.Statfs_t) error { fs.Type = fuseSuperMagic; return nil }
	if err := checkTree(path, func(uint32) bool { return true }); err == nil || !strings.Contains(err.Error(), "FUSE") {
		t.Errorf("FUSE: %v", err)
	}
	statfs = func(string, *unix.Statfs_t) error { return unix.EIO }
	if err := checkTree(path, func(uint32) bool { return true }); err == nil {
		t.Error("an unidentifiable filesystem was trusted")
	}
}
