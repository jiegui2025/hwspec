// Package trust decides whether a file is safe to run as root: only root
// may be able to change it.
package trust

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// fuseSuperMagic identifies FUSE filesystems, whose daemon (possibly the
// user's) can claim any owner and mode for its files.
const fuseSuperMagic = 0x65735546

// RootOwned returns nil if path, after resolving symlinks, and every
// directory above it are owned by root and can't be modified by anyone
// else. A group- or world-writable directory is accepted only with the
// sticky bit (like /nix/store or /tmp): others may add entries there but
// can't rename or remove root's. Files on FUSE are refused.
func RootOwned(path string) error {
	return checkTree(path, func(uid uint32) bool { return uid == 0 })
}

// statfs reports a file's filesystem; tests replace it.
var statfs = unix.Statfs

// checkTree applies RootOwned's rules with trusted deciding which owners
// are acceptable (tests trust their own user to build trees without root).
func checkTree(path string, trusted func(uid uint32) bool) error {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	var fs unix.Statfs_t
	if err := statfs(real, &fs); err != nil {
		return err
	}
	if uint32(fs.Type) == fuseSuperMagic {
		return fmt.Errorf("%s is on a FUSE filesystem, which can misreport its owner", real)
	}
	for p := real; ; p = filepath.Dir(p) {
		st, err := os.Lstat(p) //nolint:gosec // G703: checking the path and its own parent directories is the point
		if err != nil {
			return err
		}
		sys, ok := st.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("can't check the owner of %s", p)
		}
		if !trusted(sys.Uid) {
			return fmt.Errorf("%s is owned by a non-root user", p)
		}
		writable := st.Mode().Perm()&0o022 != 0
		if writable && (p == real || st.Mode()&os.ModeSticky == 0) {
			return fmt.Errorf("%s is writable by non-root users", p)
		}
		if p == "/" {
			return nil
		}
	}
}
