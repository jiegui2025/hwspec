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
	"runtime/debug"
	"strings"

	"github.com/jiegui2025/hwspec/internal/collect"
	"github.com/jiegui2025/hwspec/internal/output"
	"github.com/jiegui2025/hwspec/internal/report"
	"github.com/jiegui2025/hwspec/internal/resolve"
)

// version is set at build time with -ldflags "-X main.version=v1.2.3".
var version = "dev"

const usage = `hwspec captures this machine's hardware specification.

Usage:
  hwspec capture [-o FILE] [-f json|yaml|text] [--full] [--redact]
  hwspec show FILE [-o FILE] [-f text|json|yaml]
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
  -f json/yaml it re-exports the capture with the refreshed names.

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
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "capture":
		err = capture(os.Args[2:])
	case "show":
		err = show(os.Args[2:])
	case "ids":
		err = idsCmd(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("hwspec", fullVersion())
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "hwspec: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "hwspec:", err)
		os.Exit(1)
	}
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
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

func writeReport(r *report.Report, outPath, format string) error {
	var buf bytes.Buffer
	if err := output.Write(&buf, r, format); err != nil {
		return err
	}
	if outPath == "" || outPath == "-" {
		_, err := os.Stdout.Write(buf.Bytes())
		return err
	}
	if err := os.WriteFile(outPath, buf.Bytes(), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "hwspec: wrote %s (%s, %d warnings)\n", outPath, format, len(r.Warnings))
	return nil
}

func capture(args []string) error {
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
	if full && os.Geteuid() != 0 {
		if r, err = captureAsRoot(); err != nil {
			return err
		}
		// The root child named devices with root's overrides (if any);
		// redo it with this user's.
		resolve.Names(r)
	} else {
		r = collect.Collect(fullVersion())
	}
	if redact {
		r.Redact()
	}
	return writeReport(r, outPath, format)
}

// captureAsRoot re-runs this binary through pkexec and reads its JSON from
// stdout. The parent (running as the user) writes the output file, so the
// file isn't owned by root.
func captureAsRoot() (*report.Report, error) {
	pkexec, err := exec.LookPath("pkexec")
	if err != nil {
		return nil, errors.New("--full needs pkexec (polkit); alternatively run hwspec with sudo")
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(pkexec, self, "capture", "-f", "json")
	cmd.Stdin = os.Stdin // lets pkexec fall back to a terminal prompt
	cmd.Stderr = os.Stderr
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && (exitErr.ExitCode() == 126 || exitErr.ExitCode() == 127) {
			return nil, errors.New("root access was not granted; run without --full for a capture without memory modules, serials and drive health")
		}
		return nil, fmt.Errorf("privileged capture failed: %w", err)
	}
	return output.Read(stdout.Bytes())
}

func show(args []string) error {
	fs := newFlags("show")
	var outPath, format string
	outputFlags(fs, &outPath, &format)
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
		return errors.New("usage: hwspec show FILE [-o FILE] [-f text|json|yaml]")
	}
	format, err := pickFormat(format, outPath, "text")
	if err != nil {
		return err
	}

	var data []byte
	if file == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(file) //nolint:gosec // G703: reading the file the user named is the point
	}
	if err != nil {
		return err
	}
	r, err := output.Read(data)
	if err != nil {
		return fmt.Errorf("%s: not a hwspec capture: %w", file, err)
	}
	if r.SchemaVersion > report.SchemaVersion {
		fmt.Fprintf(os.Stderr, "hwspec: %s uses schema %d, newer than this build understands (%d); some fields may be missing\n",
			file, r.SchemaVersion, report.SchemaVersion)
	}
	resolve.Names(r)
	return writeReport(r, outPath, format)
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
