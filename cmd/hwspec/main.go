// Command hwspec captures a machine's hardware specification to a JSON,
// YAML or text file.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"

	"github.com/jiegui2025/hwspec/internal/collect"
	"github.com/jiegui2025/hwspec/internal/ids"
	"github.com/jiegui2025/hwspec/internal/output"
	"github.com/jiegui2025/hwspec/internal/report"
	"github.com/jiegui2025/hwspec/internal/resolve"
	"github.com/jiegui2025/hwspec/internal/trust"
)

// version is set at build time with -ldflags "-X main.version=v1.2.3".
var version = "dev"

const usage = `hwspec captures this machine's hardware specification.

Usage:
  hwspec capture [-o FILE] [-f json|yaml|text] [--full] [--redact]
  hwspec show FILE [-o FILE] [-f text|json|yaml] [--redact]
  hwspec ids [update [--check] | lookup KIND ID | template]
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

ids:
  Without arguments, lists the ID databases and where their names come
  from. "update" downloads the latest signed databases (--check only
  reports what would change; HWSPEC_IDS_URL or --url sets a mirror).
  "lookup" resolves one ID, e.g. "hwspec ids lookup pci 8086:3e92" or
  "hwspec ids lookup jedec F785". "template" prints a commented overrides
  file to start from.

Examples:
  hwspec capture -o myspec.json
  hwspec capture --full --redact -o spec.yaml
  hwspec show myspec.json
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
	case "ids":
		err = c.idsCmd(args[1:])
	case "version", "--version", "-v":
		fmt.Fprintln(c.stdout, "hwspec", fullVersion())
	case "help", "--help", "-h":
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
// else the default.
func pickFormat(format, outPath, def string) (string, error) {
	if format == "" {
		format = output.FormatFromPath(outPath)
		// An extension we don't know would otherwise silently get the default.
		// A dotfile's name (".hwspec") isn't an extension; "." still is refused.
		base := filepath.Base(outPath)
		if ext := filepath.Ext(outPath); format == "" && ext != "" && (ext != base || base == ".") {
			return "", fmt.Errorf("can't tell the format from %q; use -f %s", ext, strings.Join(output.Formats, "|"))
		}
	}
	if format == "" {
		format = def
	}
	for _, f := range output.Formats {
		if f == format {
			return format, nil
		}
	}
	return "", fmt.Errorf("unknown format %q (want one of %s)", format, strings.Join(output.Formats, ", "))
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
// /dev/null), and pipes or devices that belong to the caller, opened
// without following symlinks. Anything else that isn't a regular file is
// refused, so a pipe or device another user planted can't receive the
// capture. Under sudo, a new file is given to the invoking user.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if ownStream(path) {
		return writeInto(path, data, 0)
	}
	st, err := os.Lstat(path)
	switch {
	case err == nil && st.Mode()&os.ModeSymlink == 0 && !st.Mode().IsRegular():
		if err := ownedByCaller(path, st); err != nil {
			return err
		}
		return writeInto(path, data, syscall.O_NOFOLLOW)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
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
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q (use -o to name the output file)", fs.Arg(0))
	}
	format, err := pickFormat(format, outPath, "json")
	if err != nil {
		return err
	}

	var r *report.Report
	if full && geteuid() != 0 {
		if r, err = c.captureAsRoot(); err != nil {
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
func (c cli) captureAsRoot() (*report.Report, error) {
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
			"(e.g. `sudo install -m755 %s /usr/local/bin/`), or run `sudo %s capture`", err, self, self)
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
			return nil, errors.New("root access was not obtained (see pkexec's message above); run without --full for a capture without memory modules, serials and drive health")
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
	// Accept the file before or after the flags.
	var file string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") || len(args) > 0 && args[0] == "-" {
		file, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if file == "" && fs.NArg() == 1 {
		file = fs.Arg(0)
	} else if fs.NArg() > 0 || file == "" {
		return errors.New("usage: hwspec show FILE [-o FILE] [-f text|json|yaml] [--redact]")
	}
	format, err := pickFormat(format, outPath, "text")
	if err != nil {
		return err
	}

	var data []byte
	if file == "-" {
		data, err = io.ReadAll(c.stdin)
	} else {
		data, err = os.ReadFile(file) //nolint:gosec // G703: reading the file the user named is the point
	}
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
