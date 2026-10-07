// Command hwspec captures a machine's hardware specification to a JSON,
// YAML or text file.
package main

import (
	"bytes"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/jiegui2025/hwspec/internal/collect"
	"github.com/jiegui2025/hwspec/internal/ids"
	"github.com/jiegui2025/hwspec/internal/output"
	"github.com/jiegui2025/hwspec/internal/report"
	"github.com/jiegui2025/hwspec/internal/resolve"
	"github.com/jiegui2025/hwspec/internal/trust"
	"github.com/jiegui2025/hwspec/schema"
)

// version is set at build time with -ldflags "-X main.version=v1.2.3".
var version = "dev"

const usage = `hwspec captures this machine's hardware specification.

Usage:
  hwspec capture [-o FILE] [-f json|yaml|text] [--full] [--redact]
  hwspec show FILE [-o FILE] [-f text|json|yaml] [--redact]
  hwspec advise [FILE] [--full] [--redact] [-o FILE] [-f text|json|yaml]
  hwspec ids [update [--check] | lookup KIND ID | template]
  hwspec firmware update [--dry-run] [--allow-older]
  hwspec schema [capture | advice]
  hwspec version

capture:
  -o, --output FILE   write to FILE instead of stdout (format from extension)
  -f, --format FMT    json (default), yaml or text
      --full          ask for root through pkexec to also capture memory
                      modules, serial numbers and drive health
      --redact        remove serial numbers, UUIDs, MAC addresses and the
                      hostname, for sharing the file publicly

show:
  Reads a saved JSON/YAML capture, refreshes device names from the current
  ID databases and your overrides, and prints it (text by default). With
  -f json/yaml it re-exports the capture with the refreshed names;
  --redact removes identifiers before sharing an existing capture.

advise:
  Prints advice (text by default) about this machine, or about a saved
  capture: devices that need attention, with what to do and the sources the
  advice rests on. Advice about this machine also uses its maintenance
  record. --full captures this machine as root first (not with FILE);
  --redact works as for capture. It makes no network calls; the knowledge
  base is built in, and firmware is compared with the index "hwspec
  firmware update" last fetched (or fwupd's copy of LVFS's catalogue).

ids:
  Without arguments, lists the ID databases and where their names come
  from. "update" downloads the latest signed databases (--check only
  reports what would change; HWSPEC_IDS_URL or --url sets a mirror).
  "lookup" resolves one ID, e.g. "hwspec ids lookup pci 8086:3e92" or
  "hwspec ids lookup jedec F785". "template" prints a commented overrides
  file to start from.

firmware:
  "update" downloads LVFS's signed firmware catalogue (cdn.fwupd.org) and
  linux-firmware's WHENCE at its latest release (git.kernel.org), checks
  them, and keeps them in ~/.cache/hwspec/firmware. Data older than the
  cached copy is refused unless --allow-older; --dry-run checks without
  writing. Only this and "ids update" use the network.

schema:
  Prints the JSON Schema (draft 2020-12) of the capture format (the default),
  which every capture names in its "$schema" key, or of the advice document
  "advise -f json" writes, which names it the same way.

Examples:
  hwspec capture -o myspec.json
  hwspec capture --full --redact -o spec.yaml
  hwspec show myspec.json
  hwspec advise
  hwspec advise myspec.json -f json -o advice.json
`

func main() {
	os.Exit(run(os.Args[1:], cli{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr}))
}

// cli holds the streams a command reads and writes, so commands can run
// in tests.
type cli struct {
	stdin          io.Reader
	stdout, stderr io.Writer
}

// Replaceable in tests: who is running, what a capture collects, how
// pkexec is found, and the check that only a root-owned binary is elevated.
var (
	geteuid       = os.Geteuid
	rootOwned     = trust.RootOwned
	collectReport = collect.Collect
	findPkexec    = func() (string, error) {
		if isFile("/usr/bin/pkexec") {
			return "/usr/bin/pkexec", nil
		}
		return exec.LookPath("pkexec")
	}
)

// afterChecks runs between writeFileAtomic's checks and its write; tests
// use it to swap the path in between.
var afterChecks = func() {}

// errNotFound ends a command with exit status 1 after it printed why.
var errNotFound = errors.New("not found")

// run executes one command line (without the program name) and returns
// the exit status: 0 success, 1 failure (including a command's bad flags
// or arguments), 2 no command or an unknown one.
func run(args []string, c cli) int {
	if len(args) == 0 {
		fmt.Fprint(c.stderr, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "capture":
		err = c.capture(args[1:])
	case "show":
		err = c.show(args[1:])
	case "advise":
		err = c.advise(args[1:])
	case "ids":
		err = c.idsCmd(args[1:])
	case "firmware":
		err = c.firmwareCmd(args[1:])
	case "schema":
		switch {
		case len(args) == 1 || len(args) == 2 && args[1] == "capture":
			_, err = c.stdout.Write(schema.JSON)
		case len(args) == 2 && args[1] == "advice":
			_, err = c.stdout.Write(schema.AdviceJSON)
		default:
			fmt.Fprintf(c.stderr, "hwspec: usage: hwspec schema [capture | advice]\n")
			return 2
		}
	case "version", "--version", "-v":
		if len(args) > 1 {
			err = errors.New("usage: hwspec version")
			break
		}
		fmt.Fprintln(c.stdout, "hwspec", fullVersion())
	case "help", "--help", "-h":
		if len(args) > 1 {
			err = errors.New("usage: hwspec help")
			break
		}
		fmt.Fprint(c.stdout, usage)
	default:
		fmt.Fprintf(c.stderr, "hwspec: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
	switch {
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprint(c.stdout, usage)
	case errors.Is(err, errNotFound):
		return 1
	case err != nil:
		fmt.Fprintln(c.stderr, "hwspec:", err)
		return 1
	}
	return 0
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard) // errors are reported once, by main
	fs.Usage = func() {}
	return fs
}

// outputFlags registers -o/--output and -f/--format.
func outputFlags(fs *flag.FlagSet, outPath, format *string) {
	fs.StringVar(outPath, "o", "", "")
	fs.StringVar(outPath, "output", "", "")
	fs.StringVar(format, "f", "", "")
	fs.StringVar(format, "format", "", "")
}

// pickFormat applies the explicit format, else the output file's extension,
// else the default, and accepts only the formats this command can write.
func pickFormat(format, outPath, def string, allowed []string) (string, error) {
	if format == "" {
		format = output.FormatFromPath(outPath)
		// An extension we don't know would otherwise silently get the default.
		// A dotfile's name (".hwspec") isn't an extension; "." still is refused.
		base := filepath.Base(outPath)
		if ext := filepath.Ext(outPath); format == "" && ext != "" && (ext != base || base == ".") {
			return "", fmt.Errorf("can't tell the format from %q; use -f %s", ext, strings.Join(allowed, "|"))
		}
	}
	if format == "" {
		format = def
	}
	if slices.Contains(allowed, format) {
		return format, nil
	}
	return "", fmt.Errorf("unknown format %q (want one of %s)", format, strings.Join(allowed, ", "))
}

func (c cli) writeReport(r *report.Report, outPath, format string) error {
	var buf bytes.Buffer
	if err := output.Write(&buf, r, format); err != nil {
		return err
	}
	if outPath == "" || outPath == "-" {
		_, err := c.stdout.Write(buf.Bytes())
		return err
	}
	// Captures hold serial numbers, MAC addresses and the hostname: private
	// unless redacted for sharing. The umask can only tighten this.
	mode := os.FileMode(0o600)
	if r.Redacted {
		mode = 0o644
	}
	if err := writeFileAtomic(outPath, buf.Bytes(), mode); err != nil {
		return err
	}
	fmt.Fprintf(c.stderr, "hwspec: wrote %s (%s, %d warnings)\n", outPath, format, len(r.Warnings))
	return nil
}

// writeFileAtomic writes to a temporary file next to path and renames it
// into place: a full disk can't leave a truncated capture, and a symlink
// at path is replaced, never followed. The exceptions are written into:
// the process's own streams (-o /dev/stdout, >(cmd) as /dev/fd/N,
// /dev/null), and pipes or devices that belong to the caller. Anything else that isn't a regular file is
// refused, so a pipe or device another user planted can't receive the
// capture. Under sudo, a new file is given to the invoking user.
//
// Everything after the directory is opened is relative to it, so swapping
// the directory or a path component for a symlink between the checks and
// the rename can't redirect the write.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if ownStream(path) {
		return writeInto(path, data, 0)
	}
	dir, name := filepath.Dir(path), filepath.Base(path)
	// "out/" would otherwise name out/out: Base drops the slash.
	if strings.HasSuffix(path, string(filepath.Separator)) {
		return &fs.PathError{Op: "write", Path: path, Err: syscall.EISDIR}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	st, err := root.Lstat(name)
	switch {
	case err == nil && st.Mode()&os.ModeSymlink == 0 && !st.Mode().IsRegular():
		return writeSpecial(root, name, path, st, data)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return withPath(err, dir)
	}
	if uid, _, ok := sudoUser(); ok {
		dirSt, err := root.Stat(".")
		if err != nil {
			return withPath(err, dir)
		}
		if err := sudoMayWrite(path, st, dirSt, uid); err != nil {
			return err
		}
	}
	afterChecks()
	tmpName := "." + name + "." + rand.Text()
	tmp, err := root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return withPath(err, dir)
	}
	defer func() { _ = root.Remove(tmpName) }() // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode &^ umask()); err != nil {
		tmp.Close()
		return err
	}
	if uid, gid, ok := sudoUser(); ok {
		if err := tmp.Chown(uid, gid); err != nil {
			tmp.Close()
			return err
		}
	}
	// On disk before the rename, so a power loss can't leave an empty file
	// in place of the old one.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return withPath(root.Rename(tmpName, name), dir)
}

// withPath gives an error from an os.Root, which names only the path
// inside the root, the directory the user named.
func withPath(err error, dir string) error {
	if pe, ok := errors.AsType[*fs.PathError](err); ok {
		pe.Path = filepath.Join(dir, pe.Path)
	}
	return err
}

// sudoMayWrite decides whether a root process started by sudo may write
// path for the user who ran sudo (uid), only where that user could have
// written it themselves: never replacing a file they don't own (a typo
// such as -o /etc/hosts would otherwise hand a system file to them), and
// only in a directory they own, or a sticky world-writable one such as
// /tmp. existing is path's Lstat, or nil when it doesn't exist; dir is
// the Stat of the directory as opened.
func sudoMayWrite(path string, existing, dir os.FileInfo, uid int) error {
	const way = "run hwspec without sudo, or write into a directory of your own"
	if existing != nil {
		if sys, ok := existing.Sys().(*syscall.Stat_t); !ok || int(sys.Uid) != uid {
			return fmt.Errorf("%s isn't yours: as root under sudo, hwspec won't replace it (%s)", path, way)
		}
	}
	if !userMayCreateIn(dir, uid) {
		return fmt.Errorf("%s isn't your directory: as root under sudo, hwspec won't write %s there (%s)", filepath.Dir(path), filepath.Base(path), way)
	}
	return nil
}

// userMayCreateIn is true for a directory the user owns, or a sticky
// world-writable one (/tmp), where they could create the file themselves.
func userMayCreateIn(dir os.FileInfo, uid int) bool {
	if sys, ok := dir.Sys().(*syscall.Stat_t); ok && int(sys.Uid) == uid {
		return true
	}
	return dir.Mode()&os.ModeSticky != 0 && dir.Mode().Perm()&0o002 != 0
}

// writeSpecial writes into a device or pipe the caller owns, never
// replacing it. The owner is checked on st (path's Lstat) before opening,
// since opening someone else's device can have effects of its own (a
// watchdog arms, a tape rewinds) and their pipe could block forever. It
// is opened as name inside root, the directory already opened: a symlink
// swapped in can't lead outside it (os.Root refuses), and the opened file
// must be the one checked, so a swap inside it is refused too. The open
// blocks only for the caller's own pipe with no reader yet.
func writeSpecial(root *os.Root, name, path string, st os.FileInfo, data []byte) error {
	if err := ownedByCaller(path, st); err != nil {
		return err
	}
	f, err := root.OpenFile(name, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ENXIO) {
		f, err = root.OpenFile(name, os.O_WRONLY, 0)
	}
	if err != nil {
		return withPath(err, filepath.Dir(path))
	}
	opened, err := f.Stat()
	if err == nil && !os.SameFile(st, opened) {
		err = fmt.Errorf("%s changed while hwspec was writing to it; refusing to write the capture into it", path)
	}
	if err == nil {
		err = syscall.SetNonblock(int(f.Fd()), false)
	}
	if err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ownStream reports whether path names one of this process's own output
// streams, which are always written into (following the /dev/stdout link).
func ownStream(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	switch abs {
	case "/dev/stdout", "/dev/stderr", "/dev/null":
		return true
	}
	return strings.HasPrefix(abs, "/dev/fd/") || strings.HasPrefix(abs, "/proc/self/fd/")
}

func writeInto(path string, data []byte, flags int) error {
	f, err := os.OpenFile(path, os.O_WRONLY|flags, 0)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ownedByCaller refuses to write into a pipe or device that belongs to
// someone else (under sudo, the caller is the invoking user).
func ownedByCaller(path string, st os.FileInfo) error {
	uid := geteuid()
	if u, _, ok := sudoUser(); ok {
		uid = u
	}
	return checkOwner(path, st, uid)
}

func checkOwner(path string, st os.FileInfo, uid int) error {
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || int(sys.Uid) != uid {
		return fmt.Errorf("%s is a %s that belongs to someone else; refusing to write the capture into it", path, kindOf(st.Mode()))
	}
	return nil
}

func kindOf(m os.FileMode) string {
	switch {
	case m&os.ModeNamedPipe != 0:
		return "pipe"
	case m&os.ModeDevice != 0:
		return "device"
	case m&os.ModeSocket != 0:
		return "socket"
	}
	return "special file"
}

// umask returns the process umask (reading it requires setting it).
func umask() os.FileMode {
	m := syscall.Umask(0o022)
	syscall.Umask(m)
	return os.FileMode(m)
}

// sudoUser returns the invoking user when running as root under sudo.
func sudoUser() (uid, gid int, ok bool) {
	if geteuid() != 0 {
		return 0, 0, false
	}
	u, err1 := strconv.Atoi(os.Getenv("SUDO_UID"))
	g, err2 := strconv.Atoi(os.Getenv("SUDO_GID"))
	return u, g, err1 == nil && err2 == nil
}

// warnIDSources tells the user about problems with their own ID sources:
// mistakes in the overrides file (valid lines still apply), and synced
// databases that are skipped because their manifest can't be read.
func (c cli) warnIDSources() {
	if err := ids.OverridesError(); err != nil {
		fmt.Fprintf(c.stderr, "hwspec: %s: %v (other lines still apply)\n", ids.OverridesPath(), err)
	}
	if _, err := ids.SyncedAt(); err != nil {
		fmt.Fprintf(c.stderr, "hwspec: synced ID databases skipped, their manifest is unreadable (%v); run `hwspec ids update --allow-older`\n", err)
	}
}

func (c cli) capture(args []string) error {
	fs := newFlags("capture")
	var outPath, format string
	var full, redact bool
	outputFlags(fs, &outPath, &format)
	fs.BoolVar(&full, "full", false, "")
	fs.BoolVar(&redact, "redact", false, "")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return fmt.Errorf("unexpected argument %q (use -o to name the output file)", pos[0])
	}
	format, err = pickFormat(format, outPath, "json", output.Formats)
	if err != nil {
		return err
	}

	var r *report.Report
	if full && geteuid() != 0 {
		if r, err = c.captureAsRoot("capture"); err != nil {
			return err
		}
		// The root child named devices with root's overrides (if any);
		// redo it with this user's.
		resolve.Names(r)
	} else {
		r = collectReport(fullVersion())
	}
	c.warnIDSources()
	if redact {
		r.Redact()
	}
	return c.writeReport(r, outPath, format)
}

// captureAsRoot re-runs this binary through pkexec and reads its JSON from
// stdout. The parent (running as the user) writes the output file, so the
// file isn't owned by root.
func (c cli) captureAsRoot(command string) (*report.Report, error) {
	pkexec, err := findPkexec()
	if err != nil {
		return nil, errors.New("--full needs pkexec (polkit); alternatively run hwspec with sudo")
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return nil, err
	}
	// Malware running as the user must not be able to swap the binary
	// just before the user approves the root prompt.
	if err := rootOwned(self); err != nil {
		return nil, fmt.Errorf("--full runs this binary as root, but %w. Install hwspec somewhere only root can change "+
			"(e.g. `sudo install -m755 %s /usr/local/bin/`), or run `sudo %s %s`", err, self, self, command)
	}
	cmd := exec.Command(pkexec, self, "capture", "-f", "json")
	cmd.Stdin = c.stdin // lets pkexec fall back to a terminal prompt
	cmd.Stderr = c.stderr
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && (exitErr.ExitCode() == 126 || exitErr.ExitCode() == 127) {
			// 126: authorization dismissed or refused; 127: not authorized,
			// or pkexec couldn't ask (no authentication agent). pkexec's
			// own message is on stderr above.
			return nil, fmt.Errorf("root access was not obtained (see pkexec's message above); run hwspec %s without --full to go without memory modules, serials and drive health", command)
		}
		return nil, fmt.Errorf("privileged capture failed: %w", err)
	}
	return output.Read(stdout.Bytes())
}

func isFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

func (c cli) show(args []string) error {
	fs := newFlags("show")
	var outPath, format string
	var redact bool
	outputFlags(fs, &outPath, &format)
	fs.BoolVar(&redact, "redact", false, "")
	file, err := fileArg(fs, args, true)
	if errors.Is(err, errArgs) {
		return errors.New("usage: hwspec show FILE [-o FILE] [-f text|json|yaml] [--redact]")
	}
	if err != nil {
		return err
	}
	format, err = pickFormat(format, outPath, "text", output.Formats)
	if err != nil {
		return err
	}
	data, err := c.readInput(file)
	if err != nil {
		return err
	}
	r, err := output.Read(data)
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	if r.SchemaVersion > report.SchemaVersion {
		fmt.Fprintf(c.stderr, "hwspec: %s uses schema %d, newer than this build understands (%d); some fields may be missing\n",
			file, r.SchemaVersion, report.SchemaVersion)
	}
	// Re-exporting writes only the fields this build knows (owner, #145:
	// warn, don't refuse).
	if format != "text" {
		if f := output.UnknownField(data); f != "" {
			fmt.Fprintf(c.stderr, "hwspec: %s has fields this build doesn't know (such as %q); the %s output leaves them out: update hwspec to keep them\n",
				file, f, format)
		}
	}
	resolve.Names(r)
	c.warnIDSources()
	if redact {
		r.Redact()
	}
	return c.writeReport(r, outPath, format)
}

func fullVersion() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 7 {
				return "dev-" + s.Value[:7]
			}
		}
	}
	return version
}
