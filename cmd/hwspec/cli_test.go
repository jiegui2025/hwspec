package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/jiegui2025/hwspec/internal/collect"
	"github.com/jiegui2025/hwspec/internal/ids"
	"github.com/jiegui2025/hwspec/internal/output"
	"github.com/jiegui2025/hwspec/internal/report"
	"github.com/jiegui2025/hwspec/schema"
)

// The command-line features, run in-process against a recorded machine:
// an HP EliteDesk 800 G5 Mini (see internal/collect/testdata/machines).
const machine = "../../internal/collect/testdata/machines/hp-elitedesk-800-g5-mini"

// setup isolates a test from this machine: captures come from the
// recording, and the synced databases and overrides from empty
// directories. It returns the overrides path and the synced directory.
func setup(t *testing.T) (overrides, synced string) {
	t.Helper()
	t.Cleanup(ids.UseEnvironment) // runs after the variables are restored
	data, config := t.TempDir(), t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("HWSPEC_IDS_URL", "")
	ids.UseEnvironment()
	// The distribution's databases differ between machines.
	ids.UseSystemDatabases(false)
	t.Cleanup(func() { ids.UseSystemDatabases(true) })

	old := collectReport
	t.Cleanup(func() { collectReport = old })
	collectReport = func(version string) *report.Report {
		r, err := collect.CollectRecorded(machine, version)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	return filepath.Join(config, "hwspec/overrides.ids"), filepath.Join(data, "hwspec/ids")
}

// hwspec runs one command line and returns its exit status and output.
func hwspec(t *testing.T, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(args, cli{stdin: strings.NewReader(stdin), stdout: &out, stderr: &errOut})
	return code, out.String(), errOut.String()
}

func mustRun(t *testing.T, stdin string, args ...string) (stdout, stderr string) {
	t.Helper()
	code, stdout, stderr := hwspec(t, stdin, args...)
	if code != 0 {
		t.Fatalf("hwspec %s: exit %d\nstderr: %s", strings.Join(args, " "), code, stderr)
	}
	return stdout, stderr
}

func TestUsageVersionAndUnknownCommands(t *testing.T) {
	setup(t)
	if code, _, stderr := hwspec(t, ""); code != 2 || !strings.Contains(stderr, "Usage:") {
		t.Errorf("no command: exit %d, %q", code, stderr)
	}
	if code, _, stderr := hwspec(t, "", "frobnicate"); code != 2 || !strings.Contains(stderr, `unknown command "frobnicate"`) {
		t.Errorf("unknown command: exit %d, %q", code, stderr)
	}
	for _, help := range [][]string{{"help"}, {"--help"}, {"capture", "-h"}, {"show", "--help"}} {
		if stdout, _ := mustRun(t, "", help...); !strings.Contains(stdout, "Usage:") {
			t.Errorf("%v: no usage on stdout", help)
		}
	}
	if stdout, _ := mustRun(t, "", "version"); !strings.HasPrefix(stdout, "hwspec ") {
		t.Errorf("version = %q", stdout)
	}
	if code, _, stderr := hwspec(t, "", "capture", "--bogus"); code != 1 || !strings.Contains(stderr, "bogus") {
		t.Errorf("unknown flag: exit %d, %q", code, stderr)
	}
}

func TestCaptureWritesTheMachineAsJSONByDefault(t *testing.T) {
	setup(t)
	stdout, _ := mustRun(t, "", "capture")
	r, err := output.Read([]byte(stdout))
	if err != nil {
		t.Fatal(err)
	}
	if r.SchemaVersion != report.SchemaVersion || r.Tool.Name != "hwspec" || r.Hostname != "recorded" {
		t.Errorf("header: schema %d, tool %+v, host %q", r.SchemaVersion, r.Tool, r.Hostname)
	}
	if r.CPU.Identity == nil || !strings.Contains(r.CPU.Identity.Model, "i5-9500T") {
		t.Errorf("cpu = %+v", r.CPU.Identity)
	}
	// Names come from the ID databases.
	if len(r.Memory.Modules) != 1 || r.Memory.Modules[0].Identity.Vendor != "Avant Technology" {
		t.Errorf("memory vendor not resolved: %+v", r.Memory.Modules)
	}
}

// `hwspec schema` prints the embedded JSON Schema, which captures name.
func TestSchemaPrintsTheCaptureFormat(t *testing.T) {
	setup(t)
	stdout, _ := mustRun(t, "", "schema")
	if stdout != string(schema.JSON) {
		t.Error("schema output differs from the embedded schema")
	}
	capture, _ := mustRun(t, "", "capture")
	var r struct {
		Schema string `json:"$schema"`
	}
	if err := json.Unmarshal([]byte(capture), &r); err != nil || r.Schema != schema.URL {
		t.Errorf("capture's $schema = %q (%v), want %q", r.Schema, err, schema.URL)
	}
}

func TestCaptureFormatComesFromTheFlagOrTheFileExtension(t *testing.T) {
	setup(t)
	dir := t.TempDir()
	stdout, _ := mustRun(t, "", "capture", "-f", "yaml")
	var y map[string]any
	if err := yaml.Unmarshal([]byte(stdout), &y); err != nil || y["schema_version"] == nil {
		t.Errorf("-f yaml: %v", err)
	}
	if stdout, _ := mustRun(t, "", "capture", "--format", "text"); !strings.Contains(stdout, "HP EliteDesk 800 G5 Desktop Mini") {
		t.Errorf("text output lacks the machine:\n%s", stdout)
	}
	for file, check := range map[string]func([]byte) bool{
		"spec.yml": func(b []byte) bool {
			return bytes.HasPrefix(b, []byte("$schema: ")) && bytes.Contains(b, []byte("\nschema_version: 1\n"))
		},
		"spec.txt":  func(b []byte) bool { return bytes.Contains(b, []byte("Machine")) },
		"spec.JSON": json.Valid,
	} {
		path := filepath.Join(dir, file)
		_, stderr := mustRun(t, "", "capture", "-o", path)
		if b, err := os.ReadFile(path); err != nil || !check(b) {
			t.Errorf("%s: wrong format (%v)", file, err)
		}
		if !strings.Contains(stderr, "wrote "+path) {
			t.Errorf("%s: no confirmation: %q", file, stderr)
		}
	}
	if code, _, stderr := hwspec(t, "", "capture", "-o", filepath.Join(dir, "spec.csv")); code != 1 || !strings.Contains(stderr, `can't tell the format from ".csv"`) {
		t.Errorf("unknown extension: exit %d, %q", code, stderr)
	}
	if code, _, stderr := hwspec(t, "", "capture", "-f", "xml"); code != 1 || !strings.Contains(stderr, `unknown format "xml"`) {
		t.Errorf("unknown format: exit %d, %q", code, stderr)
	}
	if code, _, stderr := hwspec(t, "", "capture", "spec.json"); code != 1 || !strings.Contains(stderr, "use -o") {
		t.Errorf("stray argument: exit %d, %q", code, stderr)
	}
}

// Captures hold serials and MACs: private files unless redacted.
func TestCaptureFilesArePrivateUnlessRedacted(t *testing.T) {
	setup(t)
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })
	dir := t.TempDir()
	private, shared := filepath.Join(dir, "private.json"), filepath.Join(dir, "shared.json")
	mustRun(t, "", "capture", "-o", private)
	mustRun(t, "", "capture", "--redact", "-o", shared)
	for path, want := range map[string]os.FileMode{private: 0o600, shared: 0o644} {
		if st, err := os.Stat(path); err != nil || st.Mode().Perm() != want {
			t.Errorf("%s: mode %v, want %v (%v)", filepath.Base(path), st.Mode().Perm(), want, err)
		}
	}
	data, _ := os.ReadFile(shared)
	r, err := output.Read(data)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Redacted || r.Hostname != "" || r.Displays[0].Identity.Serial != "" || r.Network[0].MAC != "" || r.Bluetooth[0].Address != "" {
		t.Errorf("redacted capture keeps identifiers: host %q, display %+v, mac %q", r.Hostname, r.Displays[0].Identity, r.Network[0].MAC)
	}
}

// --full re-runs hwspec as root through pkexec, but only a binary root
// alone can change; it never elevates one the user (or malware) can swap.
func TestFullCaptureElevatesOnlyARootOwnedBinary(t *testing.T) {
	setup(t)
	old, oldEuid := findPkexec, geteuid
	t.Cleanup(func() { findPkexec, geteuid = old, oldEuid })
	geteuid = func() int { return 1000 }

	findPkexec = func() (string, error) { return "", errors.New("not found") }
	if code, _, stderr := hwspec(t, "", "capture", "--full"); code != 1 || !strings.Contains(stderr, "--full needs pkexec") {
		t.Errorf("without pkexec: exit %d, %q", code, stderr)
	}
	// The test binary belongs to the user running the tests.
	if os.Geteuid() == 0 {
		t.Skip("run as root, the test binary is root-owned")
	}
	findPkexec = func() (string, error) { return "/usr/bin/pkexec", nil }
	if code, _, stderr := hwspec(t, "", "capture", "--full"); code != 1 || !strings.Contains(stderr, "Install hwspec somewhere only root can change") {
		t.Errorf("user-owned binary: exit %d, %q", code, stderr)
	}
}

func TestShowPrintsAndReexportsASavedCapture(t *testing.T) {
	setup(t)
	saved := filepath.Join(t.TempDir(), "saved.json")
	mustRun(t, "", "capture", "-o", saved)
	data, _ := os.ReadFile(saved)

	for _, args := range [][]string{{"show", saved}, {"show", "-f", "text", saved}, {"show", saved, "-f", "text"}} {
		if stdout, _ := mustRun(t, "", args...); !strings.Contains(stdout, "HP EliteDesk 800 G5 Desktop Mini") || !strings.Contains(stdout, "Avant Technology") {
			t.Errorf("%v:\n%s", args, stdout)
		}
	}
	if stdout, _ := mustRun(t, string(data), "show", "-"); !strings.Contains(stdout, "Machine") {
		t.Errorf("show from stdin:\n%s", stdout)
	}
	stdout, _ := mustRun(t, "", "show", saved, "-f", "json", "--redact")
	r, err := output.Read([]byte(stdout))
	if err != nil || !r.Redacted || r.Hostname != "" {
		t.Errorf("re-export: redacted %v, host %q, %v", r != nil && r.Redacted, r.Hostname, err)
	}
}

func TestShowExplainsWhatIsWrongWithItsInput(t *testing.T) {
	setup(t)
	dir := t.TempDir()
	garbage := filepath.Join(dir, "garbage.json")
	if err := os.WriteFile(garbage, []byte("not a capture"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := filepath.Join(dir, "future.json")
	if err := os.WriteFile(future, []byte(`{"schema_version": 99, "tool": {"name": "hwspec"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"show"}, "usage: hwspec show FILE"},
		{[]string{"show", "a.json", "b.json"}, "usage: hwspec show FILE"},
		{[]string{"show", filepath.Join(dir, "missing.json")}, "no such file"},
		{[]string{"show", garbage}, garbage},
	} {
		if code, _, stderr := hwspec(t, "", c.args...); code != 1 || !strings.Contains(stderr, c.want) {
			t.Errorf("%v: exit %d, %q (want %q)", c.args, code, stderr, c.want)
		}
	}
	if _, stderr := mustRun(t, "", "show", future); !strings.Contains(stderr, "uses schema 99, newer than this build") {
		t.Errorf("newer schema not flagged: %q", stderr)
	}
}

func TestIDsStatusListsEveryDatabaseAndItsSources(t *testing.T) {
	overrides, _ := setup(t)
	stdout, _ := mustRun(t, "", "ids")
	for _, k := range ids.Kinds {
		if !strings.Contains(stdout, "\n"+string(k)+" ") {
			t.Errorf("status lacks %s:\n%s", k, stdout)
		}
	}
	for _, want := range []string{"embedded", "Not synced yet", "No overrides file", overrides} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status lacks %q:\n%s", want, stdout)
		}
	}
}

func TestIDsLookupResolvesAndOverridesWin(t *testing.T) {
	overrides, _ := setup(t)
	if stdout, _ := mustRun(t, "", "ids", "lookup", "pci", "8086"); !strings.HasPrefix(stdout, "Intel") {
		t.Errorf("pci 8086 = %q", stdout)
	}
	if stdout, _ := mustRun(t, "", "ids", "lookup", "JEDEC", "F785"); !strings.HasPrefix(stdout, "Avant Technology") {
		t.Errorf("jedec F785 = %q", stdout)
	}
	if code, stdout, _ := hwspec(t, "", "ids", "lookup", "pci", "0000"); code != 1 || !strings.Contains(stdout, "not found") {
		t.Errorf("unknown id: exit %d, %q", code, stdout)
	}
	if code, _, stderr := hwspec(t, "", "ids", "lookup", "pci"); code != 1 || !strings.Contains(stderr, "usage: hwspec ids lookup") {
		t.Errorf("missing id: exit %d, %q", code, stderr)
	}
	if code, _, stderr := hwspec(t, "", "ids", "lookup", "floppy", "1"); code != 1 || stderr == "" {
		t.Errorf("unknown kind: exit %d, %q", code, stderr)
	}

	// The user's overrides win, also in captures; a bad line is reported
	// and the good ones still apply.
	if err := os.MkdirAll(filepath.Dir(overrides), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overrides, []byte("jedec F785 = Avant (corrected)\nnonsense line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ids.Reset()
	if stdout, _ := mustRun(t, "", "ids", "lookup", "jedec", "F785"); !strings.HasPrefix(stdout, "Avant (corrected)") {
		t.Errorf("override not applied: %q", stdout)
	}
	stdout, stderr := mustRun(t, "", "capture", "-f", "text")
	if !strings.Contains(stdout, "Avant (corrected)") || !strings.Contains(stderr, "other lines still apply") {
		t.Errorf("capture with overrides: stderr %q\n%s", stderr, stdout)
	}
	if stdout, _ := mustRun(t, "", "ids"); !strings.Contains(stdout, "overrides (1)") || !strings.Contains(stdout, "Overrides file has problems") {
		t.Errorf("status with overrides:\n%s", stdout)
	}
}

func TestIDsTemplateAndUnknownSubcommands(t *testing.T) {
	setup(t)
	if stdout, _ := mustRun(t, "", "ids", "template"); !strings.HasPrefix(stdout, "# hwspec name overrides") {
		t.Errorf("template = %q", stdout)
	}
	if code, _, stderr := hwspec(t, "", "ids", "frob"); code != 1 || !strings.Contains(stderr, `unknown ids subcommand "frob"`) {
		t.Errorf("unknown subcommand: exit %d, %q", code, stderr)
	}
}

// Synced databases whose manifest is unreadable are skipped, and both the
// status and every capture say so.
func TestUnreadableSyncedDatabasesAreSkippedAndReported(t *testing.T) {
	_, synced := setup(t)
	if err := os.MkdirAll(synced, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"pci.ids.gz": "x", "manifest.json": "{broken"} {
		if err := os.WriteFile(filepath.Join(synced, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ids.Reset()
	if stdout, _ := mustRun(t, "", "ids"); !strings.Contains(stdout, "Synced databases are not used") || !strings.Contains(stdout, "unusable") {
		t.Errorf("status:\n%s", stdout)
	}
	if _, stderr := mustRun(t, "", "capture"); !strings.Contains(stderr, "synced ID databases skipped") {
		t.Errorf("capture stderr: %q", stderr)
	}
}

func TestIDsUpdateReportsEveryOutcome(t *testing.T) {
	setup(t)
	old := updateIDs
	t.Cleanup(func() { updateIDs = old })
	fresh := time.Now()
	files := []ids.FileUpdate{{File: "pci.ids.gz", Status: "updated", Date: "2026-10-01", Entries: 9}, {File: "usb.ids.gz", Status: "unchanged"}}
	var got ids.UpdateOptions
	answer := func(res *ids.UpdateResult, err error) {
		updateIDs = func(_ context.Context, opt ids.UpdateOptions) (*ids.UpdateResult, error) {
			got = opt
			return res, err
		}
	}

	answer(&ids.UpdateResult{Files: files, BundleAt: fresh}, nil)
	if stdout, _ := mustRun(t, "", "ids", "update"); !strings.Contains(stdout, "Installed 1 databases") || got.BaseURL != ids.DefaultSyncURL || got.DryRun {
		t.Errorf("install: %q, options %+v", stdout, got)
	}
	if stdout, _ := mustRun(t, "", "ids", "update", "--check", "--url", "https://mirror.example/ids/"); !strings.Contains(stdout, "would update") ||
		!strings.Contains(stdout, "1 of 2 databases have updates") || !got.DryRun || got.BaseURL != "https://mirror.example/ids/" {
		t.Errorf("check: %q, options %+v", stdout, got)
	}
	t.Setenv("HWSPEC_IDS_URL", "https://env.example/")
	if _, stderr := mustRun(t, "", "ids", "update", "--allow-older"); got.BaseURL != "https://env.example/" || !got.AllowOlder || !strings.Contains(stderr, "Checking https://env.example/") {
		t.Errorf("env mirror: %+v, %q", got, stderr)
	}
	answer(&ids.UpdateResult{Files: files[1:], BundleAt: fresh}, nil)
	if stdout, _ := mustRun(t, "", "ids", "update"); !strings.Contains(stdout, "Already up to date.") {
		t.Errorf("nothing new: %q", stdout)
	}
	answer(&ids.UpdateResult{Files: files[1:], BundleAt: fresh.Add(-90 * 24 * time.Hour)}, nil)
	if _, stderr := mustRun(t, "", "ids", "update"); !strings.Contains(stderr, "may be stale") {
		t.Errorf("stale bundle not flagged: %q", stderr)
	}
	answer(&ids.UpdateResult{BundleAt: fresh}, ids.ErrBuiltInIsNewer)
	if stdout, _ := mustRun(t, "", "ids", "update"); !strings.Contains(stdout, "built into hwspec are newer") {
		t.Errorf("built-in newer: %q", stdout)
	}
	answer(nil, &ids.InstallError{Written: []string{"pci.ids.gz"}, Err: errors.New("disk full")})
	if code, _, stderr := hwspec(t, "", "ids", "update"); code != 1 || !strings.Contains(stderr, "partly installed") || !strings.Contains(stderr, "disk full") {
		t.Errorf("partial: exit %d, %q", code, stderr)
	}
	// Under sudo the databases would land in root's home, or (sudo -E)
	// make the user's own directory root's: refused before any request.
	oldEuid := geteuid
	t.Cleanup(func() { geteuid = oldEuid })
	geteuid = func() int { return 0 }
	t.Setenv("SUDO_UID", "1000")
	got = ids.UpdateOptions{}
	if code, _, stderr := hwspec(t, "", "ids", "update"); code != 1 || !strings.Contains(stderr, "without sudo") || got.BaseURL != "" {
		t.Errorf("under sudo: exit %d, %q, options %+v", code, stderr, got)
	}
	t.Setenv("SUDO_UID", "")
	answer(&ids.UpdateResult{Files: files, BundleAt: fresh}, nil)
	if code, _, _ := hwspec(t, "", "ids", "update"); code != 0 || got.BaseURL == "" {
		t.Errorf("root without sudo: exit %d", code)
	}
	geteuid = oldEuid
	answer(nil, errors.New("signature does not verify"))
	if code, _, stderr := hwspec(t, "", "ids", "update"); code != 1 || !strings.Contains(stderr, "nothing changed: signature does not verify") {
		t.Errorf("refused: exit %d, %q", code, stderr)
	}
}

// End to end: a mirror serving a bundle that isn't signed by the project's
// key changes nothing.
func TestIDsUpdateRefusesABundleNotSignedByTheProject(t *testing.T) {
	_, synced := setup(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case "manifest.json":
			_, _ = w.Write([]byte(`{"format":1,"generated_at":"2030-01-01T00:00:00Z","files":{}}`))
		case "manifest.json.sig":
			_, _ = w.Write([]byte("AAAA"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	if code, _, stderr := hwspec(t, "", "ids", "update", "--url", srv.URL+"/"); code != 1 || !strings.Contains(stderr, "nothing changed") {
		t.Errorf("exit %d, %q", code, stderr)
	}
	if _, err := os.Stat(synced); !os.IsNotExist(err) {
		t.Errorf("synced directory created: %v", err)
	}
}

// --full runs hwspec again as root through pkexec and takes the child's
// JSON; the user's overrides then rename devices as in any capture. A
// dismissed prompt and a failed child are explained.
func TestFullCaptureRunsTheRootChildThroughPkexec(t *testing.T) {
	overrides, _ := setup(t)
	oldFind, oldOwned, oldEuid := findPkexec, rootOwned, geteuid
	t.Cleanup(func() { findPkexec, rootOwned, geteuid = oldFind, oldOwned, oldEuid })
	geteuid = func() int { return 1000 }
	rootOwned = func(string) error { return nil }

	// What root would capture: the recorded machine, as JSON.
	child := filepath.Join(t.TempDir(), "child.json")
	mustRun(t, "", "capture", "-o", child)
	dir := t.TempDir()
	pkexec := filepath.Join(dir, "pkexec")
	script := `#!/bin/sh
[ "$2 $3 $4" = "capture -f json" ] || { echo "unexpected arguments: $*" >&2; exit 2; }
case "$FAKE_PKEXEC" in
dismiss) echo "Error executing command as another user: Request dismissed" >&2; exit 126 ;;
crash) exit 3 ;;
*) cat "` + child + `" ;;
esac
`
	if err := os.WriteFile(pkexec, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	findPkexec = func() (string, error) { return pkexec, nil }
	if err := os.MkdirAll(filepath.Dir(overrides), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overrides, []byte("jedec F785 = Avant (mine)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ids.Reset()

	t.Setenv("FAKE_PKEXEC", "")
	stdout, _ := mustRun(t, "", "capture", "--full", "-f", "text")
	if !strings.Contains(stdout, "HP EliteDesk") || !strings.Contains(stdout, "Avant (mine)") {
		t.Errorf("root capture:\n%s", stdout)
	}
	t.Setenv("FAKE_PKEXEC", "dismiss")
	if code, _, stderr := hwspec(t, "", "capture", "--full"); code != 1 || !strings.Contains(stderr, "Request dismissed") || !strings.Contains(stderr, "root access was not obtained") {
		t.Errorf("dismissed: exit %d, %q", code, stderr)
	}
	t.Setenv("FAKE_PKEXEC", "crash")
	if code, _, stderr := hwspec(t, "", "capture", "--full"); code != 1 || !strings.Contains(stderr, "privileged capture failed: exit status 3") {
		t.Errorf("crash: exit %d, %q", code, stderr)
	}
}

// Run with sudo, hwspec writes the capture as the invoking user, so it
// doesn't leave a root-owned file in their directory; a root capture
// doesn't elevate again either.
func TestUnderSudoTheFileBelongsToTheInvokingUser(t *testing.T) {
	setup(t)
	old := geteuid
	t.Cleanup(func() { geteuid = old })
	geteuid = func() int { return 0 }
	uid, gid := os.Getuid(), os.Getgid()
	t.Setenv("SUDO_UID", strconv.Itoa(uid))
	t.Setenv("SUDO_GID", strconv.Itoa(gid))
	if u, g, ok := sudoUser(); !ok || u != uid || g != gid {
		t.Fatalf("sudoUser = %d %d %v", u, g, ok)
	}
	path := filepath.Join(t.TempDir(), "spec.json")
	// --full as root captures directly instead of asking pkexec.
	mustRun(t, "", "capture", "--full", "-o", path)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if sys := st.Sys().(*syscall.Stat_t); int(sys.Uid) != uid || int(sys.Gid) != gid {
		t.Errorf("owner %d:%d, want %d:%d", sys.Uid, sys.Gid, uid, gid)
	}
	t.Setenv("SUDO_UID", "not a number")
	if _, _, ok := sudoUser(); ok {
		t.Error("a malformed SUDO_UID was accepted")
	}
}

// Where a capture can't be written, the reason is clear and nothing is
// left half-written: a device or socket in the way, a missing or
// unreadable directory.
func TestCaptureExplainsWhereItCantWrite(t *testing.T) {
	setup(t)
	dir := t.TempDir()
	sock := filepath.Join(dir, "sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	noSearch := filepath.Join(dir, "nosearch")
	if err := os.Mkdir(noSearch, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(noSearch, 0o700) })
	cases := map[string]string{
		filepath.Join(dir, "missing", "spec.json"): "no such file or directory",
		sock + ".json": "", // a fresh file next to the socket is fine
	}
	if os.Geteuid() != 0 { // root isn't stopped by permissions
		cases[filepath.Join(noSearch, "spec.json")] = "permission denied"
		if st, err := os.Stat("/dev/zero"); err == nil && st.Sys().(*syscall.Stat_t).Uid == 0 {
			cases["/dev/zero"] = "is a device that belongs to someone else"
		}
	}
	for path, want := range cases {
		code, _, stderr := hwspec(t, "", "capture", "-f", "json", "-o", path)
		if want == "" {
			if code != 0 {
				t.Errorf("%s: exit %d, %q", path, code, stderr)
			}
			continue
		}
		if code != 1 || !strings.Contains(stderr, want) {
			t.Errorf("%s: exit %d, %q (want %q)", path, code, stderr, want)
		}
	}
	// A socket of our own can't take a file's contents.
	if code, _, _ := hwspec(t, "", "capture", "-f", "json", "-o", sock); code != 1 {
		t.Errorf("socket: exit %d", code)
	}
	if kindOf(os.ModeSocket) != "socket" || kindOf(os.ModeNamedPipe) != "pipe" || kindOf(os.ModeDevice) != "device" || kindOf(os.ModeIrregular) != "special file" {
		t.Error("kindOf")
	}
}

// The version names the build: a release tag, or the commit for a
// development build.
func TestVersionNamesTheBuild(t *testing.T) {
	old := version
	t.Cleanup(func() { version = old })
	version = "v1.2.3"
	if got := fullVersion(); got != "v1.2.3" {
		t.Errorf("release = %q", got)
	}
	version = "dev"
	if got := fullVersion(); got != "dev" && !strings.HasPrefix(got, "dev-") {
		t.Errorf("dev build = %q", got)
	}
}

// Bad options to show and ids update are reported, not ignored.
func TestBadOptionsAreReported(t *testing.T) {
	setup(t)
	saved := filepath.Join(t.TempDir(), "saved.json")
	mustRun(t, "", "capture", "-o", saved)
	for args, want := range map[string]string{
		"show " + saved + " -o out.csv": `can't tell the format from ".csv"`,
		"show " + saved + " --bogus":    "bogus",
		"ids update --bogus":            "bogus",
	} {
		if code, _, stderr := hwspec(t, "", strings.Fields(args)...); code != 1 || !strings.Contains(stderr, want) {
			t.Errorf("%s: exit %d, %q", args, code, stderr)
		}
	}
}

// An output name whose extension says no format is refused before anything
// is written, for capture and show alike; a dotfile's name (".hwspec")
// isn't an extension and gets the default format.
func TestUnknownOutputExtensionsWriteNothing(t *testing.T) {
	setup(t)
	saved := filepath.Join(t.TempDir(), "saved.json")
	mustRun(t, "", "capture", "-o", saved)
	for name, args := range map[string]func(dir string) []string{
		"capture": func(dir string) []string { return []string{"capture", "-o", filepath.Join(dir, "spec.xml")} },
		"show":    func(dir string) []string { return []string{"show", saved, "-o", filepath.Join(dir, "x.xml")} },
	} {
		dir := t.TempDir()
		code, _, stderr := hwspec(t, "", args(dir)...)
		if code != 1 || !strings.Contains(stderr, ".xml") || !strings.Contains(stderr, "use -f json|yaml|text") {
			t.Errorf("%s: exit %d, %q", name, code, stderr)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("%s left %v in the output directory", name, entries)
		}
	}
	dot := filepath.Join(t.TempDir(), ".hwspec")
	mustRun(t, "", "capture", "-o", dot)
	if data, err := os.ReadFile(dot); err != nil || !json.Valid(data) {
		t.Errorf(".hwspec: %v, valid JSON %v", err, json.Valid(data))
	}
}
