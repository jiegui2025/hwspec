package collect

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// root is prefixed to every path (and passed to ghw), so tests can point
// collectors at a fixture tree instead of the running machine.
var root = "/"

// Seams for things that aren't files; tests replace them.
var (
	geteuid  = os.Geteuid
	hostname = os.Hostname
)

// unreadable lists paths whose reads fail with the given error: a recording
// replays the files it couldn't copy (root-only DMI serials) this way.
var unreadable map[string]error

// traceRead, when set, is told every path a collector touches; the
// snapshot tool uses it to record a machine as a test fixture.
var traceRead func(path string)

// traceMu serialises traceRead: the sensors collector runs alongside the
// others, and recorders keep the paths in a plain map.
var traceMu sync.Mutex

func p(path string) string {
	if traceRead != nil {
		traceMu.Lock()
		traceRead(path)
		traceMu.Unlock()
	}
	return filepath.Join(root, path)
}

// under gives a path's place under root, or the error a recording
// replays for it. Every collector read goes through it (readFile,
// openFile, readWithin), so recordings can replay files they couldn't
// copy.
func under(path string) (string, error) {
	full := p(path)
	if err, ok := unreadable[path]; ok {
		return "", &fs.PathError{Op: "open", Path: full, Err: err}
	}
	return full, nil
}

// readFile reads a file under root.
func readFile(path string) ([]byte, error) {
	full, err := under(path)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(full)
}

// openFile opens a file under root for streaming reads.
func openFile(path string) (*os.File, error) {
	full, err := under(path)
	if err != nil {
		return nil, err
	}
	return os.Open(full)
}

// readWithin is readFile for a file the kernel may answer slowly or
// never, given up on after slowAnswer. Only the read runs apart, so one
// left behind touches nothing of the capture's.
func readWithin(path string) ([]byte, error) {
	full, err := under(path)
	if err != nil {
		return nil, err
	}
	return within(slowAnswer, func() ([]byte, error) { return os.ReadFile(full) })
}

// readStr returns the trimmed file contents, or "" if it can't be read.
func readStr(path string) string {
	s, _ := readStrErr(path)
	return s
}

// readStrErr is readStr but keeps the error, for fields where a failure is
// worth reporting (usually permission denied on root-only files).
func readStrErr(path string) (string, error) {
	b, err := readFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// parseInt reads a sysfs number (decimal, or 0x hex).
func parseInt(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseInt(s, 0, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// readInt32 is readInt for values that fit in 32 bits (speeds, counts,
// widths, kHz), so converting to int can't truncate on any platform.
func readInt32(path string) (int, bool) {
	v, err := strconv.ParseInt(readStr(path), 0, 32)
	if err != nil {
		return 0, false
	}
	return int(v), true
}

func readUint(path string) uint64 {
	v, err := strconv.ParseUint(readStr(path), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// linkBase returns the last element of a symlink target, e.g. the driver
// name from .../device/driver.
func linkBase(path string) string {
	t, err := os.Readlink(p(path))
	if err != nil {
		return ""
	}
	return filepath.Base(t)
}

// list returns the sorted entry names of a directory (nil if missing).
func list(dir string) []string {
	entries, err := os.ReadDir(p(dir))
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// realPath resolves all symlinks, returning "" on error.
func realPath(path string) string {
	r, err := filepath.EvalSymlinks(p(path))
	if err != nil {
		return ""
	}
	return r
}

func exists(path string) bool {
	_, err := os.Stat(p(path))
	return err == nil
}

// hex4 normalises "0x8086" to "8086".
func hex4(s string) string {
	return strings.ToLower(strings.TrimPrefix(s, "0x"))
}

// busOf resolves a sysfs device symlink (e.g. /sys/class/net/eth0/device)
// to its bus ("pci", "usb", ...) and address.
func busOf(deviceLink string) (bus, addr string) {
	target, err := filepath.EvalSymlinks(p(deviceLink))
	if err != nil {
		return "", ""
	}
	sub, err := filepath.EvalSymlinks(p(deviceLink + "/subsystem"))
	if err != nil {
		return "", filepath.Base(target)
	}
	bus = filepath.Base(sub)
	addr = filepath.Base(target)
	// A USB interface (1-2:1.0) belongs to the USB device one level up (1-2).
	if bus == "usb" && strings.Contains(addr, ":") {
		addr = filepath.Base(filepath.Dir(target))
	}
	return bus, addr
}
