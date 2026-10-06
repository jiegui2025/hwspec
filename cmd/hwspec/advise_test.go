package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jiegui2025/hwspec/internal/advisor"
	"github.com/jiegui2025/hwspec/internal/collect"
	"github.com/jiegui2025/hwspec/internal/ids"
	"github.com/jiegui2025/hwspec/internal/report"
)

const testMachineID = "0123456789abcdef0123456789abcdef"

// testStateKey is systemd's app-specific ID of testMachineID for hwspec's
// app ID, computed independently: HMAC-SHA256 keyed with the machine-id's
// bytes over 3286820f74154a5db06c16d2c1d858fa, the first 16 bytes made a
// v4 UUID (the same computation gives `systemd-id128 -a … machine-id` on a
// real machine).
const testStateKey = "3c863efdc0534d4e8044fb4568fa844e"

// setupAdvise isolates advise from this machine: the recorded machine plus
// a Wi-Fi card no driver is bound to, its own machine-id and state
// directory. It returns the machine-id file and the state file's path.
func setupAdvise(t *testing.T) (machineID, statePath string) {
	t.Helper()
	setup(t)
	collectReport = func(version string) *report.Report {
		r, err := collect.CollectRecorded(machine, version)
		if err != nil {
			t.Fatal(err)
		}
		r.PCI = append(r.PCI, report.PCIDevice{Address: "0000:02:00.0", VendorID: "8086", DeviceID: "2723",
			SubVendorID: "8086", SubDeviceID: "0084", ClassCode: "028000", Class: "Network controller"})
		return r
	}
	dir := t.TempDir()
	machineID, stateHome := filepath.Join(dir, "machine-id"), filepath.Join(dir, "state")
	must(t, os.WriteFile(machineID, []byte(testMachineID+"\n"), 0o644))
	old := machineIDPath
	t.Cleanup(func() { machineIDPath = old })
	machineIDPath = machineID
	t.Setenv("XDG_STATE_HOME", stateHome)
	return machineID, filepath.Join(stateHome, "hwspec/machines", testStateKey+".json")
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func readAdvice(t *testing.T, js string) advisor.Advice {
	t.Helper()
	var a advisor.Advice
	if err := json.Unmarshal([]byte(js), &a); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, js)
	}
	return a
}

func out(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	stdout, _ := mustRun(t, stdin, args...)
	return stdout
}

// #80's acceptance: advice about a saved capture is a JSON document with
// its versions and findings, and says the maintenance record wasn't used.
func TestAdviseASavedCaptureAsJSON(t *testing.T) {
	setupAdvise(t)
	capture := out(t, "", "capture")
	file := filepath.Join(t.TempDir(), "spec.json")
	must(t, os.WriteFile(file, []byte(capture), 0o600))

	stdout := out(t, "", "advise", file, "-f", "json")
	var raw map[string]any
	must(t, json.Unmarshal([]byte(stdout), &raw))
	for _, key := range []string{"advice_version", "kb_version", "capture_sha256", "live", "redacted", "rules_applied", "rules_skipped", "warnings", "findings"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("no %q in %s", key, stdout)
		}
	}
	// Absent evidence is {path, absent: true}, with no value key.
	if !strings.Contains(stdout, `"absent": true`) || strings.Contains(stdout, `"value": null`) {
		t.Errorf("evidence shape in %s", stdout)
	}
	a := readAdvice(t, stdout)
	if a.AdviceVersion != advisor.Version || a.Live || a.Redacted || len(a.CaptureSHA256) != 64 {
		t.Errorf("header: version %d, live %v, redacted %v, sha %s", a.AdviceVersion, a.Live, a.Redacted, a.CaptureSHA256)
	}
	if len(a.Warnings) == 0 || !strings.Contains(a.Warnings[0], "doesn't use a maintenance record") {
		t.Errorf("warnings %q", a.Warnings)
	}
	if len(a.Findings) != 1 || a.Findings[0].ID != "pci.no-driver" || a.Findings[0].Device.Key != "0000:02:00.0" {
		t.Errorf("findings %+v", a.Findings)
	}
	// The same capture from stdin, as YAML: the same content, the same hash.
	var y map[string]any
	if err := yaml.Unmarshal([]byte(out(t, capture, "advise", "-", "-f", "yaml")), &y); err != nil ||
		y["advice_version"] != 1 || y["capture_sha256"] != a.CaptureSHA256 {
		t.Errorf("yaml: %v %v", err, y)
	}
	// --redact advises on the redacted capture, and hashes that.
	r := readAdvice(t, out(t, "", "advise", file, "--redact", "-f", "json"))
	if !r.Redacted || r.CaptureSHA256 == a.CaptureSHA256 || len(r.Findings) != 1 {
		t.Errorf("redacted: %v, sha %s, %d findings", r.Redacted, r.CaptureSHA256, len(r.Findings))
	}
}

// A newer schema may have renamed what the checks read: no advice from it,
// rather than wrong advice.
func TestAdviseRefusesANewerSchema(t *testing.T) {
	setupAdvise(t)
	capture := out(t, "", "capture")
	if !strings.Contains(capture, `"schema_version": 1`) {
		t.Fatal("capture has no schema_version 1 to change")
	}
	file := filepath.Join(t.TempDir(), "spec.json")
	must(t, os.WriteFile(file, []byte(strings.Replace(capture, `"schema_version": 1`, `"schema_version": 2`, 1)), 0o600))
	if code, stdout, stderr := hwspec(t, "", "advise", file); code != 1 || stdout != "" || !strings.Contains(stderr, "uses schema 2, newer than this build understands (1)") {
		t.Errorf("exit %d, %q, %q", code, stdout, stderr)
	}
}

func TestAdviseThisMachine(t *testing.T) {
	setupAdvise(t)
	stdout := out(t, "", "advise")
	for _, want := range []string{"this machine", "No kernel driver is bound to this device", "pci 0000:02:00.0",
		"modprobe -R pci:v00008086d00002723sv00008086sd00000084bc02sc80i00"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in\n%s", want, stdout)
		}
	}
	a := readAdvice(t, out(t, "", "advise", "-f", "json"))
	if !a.Live || len(a.Findings) != 1 || a.RulesApplied != 1 {
		t.Errorf("live %v, %d findings, applied %d", a.Live, len(a.Findings), a.RulesApplied)
	}
	// The capture's own warnings are carried over: what it couldn't read
	// can hide findings.
	for _, w := range a.Warnings {
		if !strings.HasPrefix(w, "capture: ") {
			t.Errorf("unexpected warning %q", w)
		}
	}
}

// #80's acceptance: the state file's name is systemd's app-specific ID,
// never the machine-id.
func TestTheStateFileNameHidesTheMachineID(t *testing.T) {
	_, want := setupAdvise(t)
	path, uid, err := statePath()
	if err != nil || path != want || uid != os.Getuid() || strings.Contains(path, testMachineID) {
		t.Errorf("state path %s (uid %d, %v), want %s", path, uid, err, want)
	}
	if _, err := stateKey("not-hex"); err == nil {
		t.Error("a malformed machine-id gives a key")
	}
	// A relative XDG_STATE_HOME is ignored, as the XDG spec says.
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", "relative")
	t.Setenv("HOME", home)
	if path, _, _ := statePath(); path != filepath.Join(home, ".local/state/hwspec/machines", testStateKey+".json") {
		t.Errorf("relative XDG_STATE_HOME: %s", path)
	}
}

// Under sudo the state is the invoking user's, as output files are, not
// root's.
func TestUnderSudoTheStateIsTheInvokingUsers(t *testing.T) {
	setupAdvise(t)
	oldEuid, oldLookup := geteuid, lookupUser
	t.Cleanup(func() { geteuid, lookupUser = oldEuid, oldLookup })
	geteuid = func() int { return 0 }
	t.Setenv("SUDO_UID", "1234")
	t.Setenv("SUDO_GID", "1234")
	lookupUser = func(id string) (*user.User, error) {
		if id != "1234" {
			return nil, errors.New("no such user")
		}
		return &user.User{Uid: id, HomeDir: "/home/someone"}, nil
	}
	path, uid, err := statePath()
	if err != nil || uid != 1234 || path != "/home/someone/.local/state/hwspec/machines/"+testStateKey+".json" {
		t.Errorf("sudo: %s, uid %d, %v", path, uid, err)
	}
	t.Setenv("SUDO_UID", "99")
	if _, _, err := statePath(); err == nil || !strings.Contains(err.Error(), "can't find the home of the user running sudo") {
		t.Errorf("unknown sudo user: %v", err)
	}
}

// #80's acceptance: without a usable machine-id or state file, advice
// still comes, without state, and says why; the state is read only from a
// regular file the user owns, never through a symlink or from a FIFO.
func TestAdviceWithoutStateSaysWhy(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(t *testing.T, machineID, state string)
		want    string
	}{
		{"no state yet", func(*testing.T, string, string) {}, ""},
		{"valid state", func(t *testing.T, _, state string) {
			must(t, os.WriteFile(state, []byte(`{"format":1,"done":[{"task":"repaste","date":"2026-01-02","material":"ptm7950"}]}`), 0o600))
		}, ""},
		{"bad entries", func(t *testing.T, _, state string) {
			must(t, os.WriteFile(state, []byte(`{"format":1,"done":[{"task":"clean","date":"2026-01-02"},{"task":"x","date":"last week"},{"date":"2026-01-02"}]}`), 0o600))
		}, "2 entries without a task or a YYYY-MM-DD date were ignored"},
		{"corrupt state", func(t *testing.T, _, state string) { must(t, os.WriteFile(state, []byte(`{"secret`), 0o600)) }, "isn't valid JSON"},
		{"newer format", func(t *testing.T, _, state string) { must(t, os.WriteFile(state, []byte(`{"format":9}`), 0o600)) }, "has format 9, this build reads 1"},
		{"directory", func(t *testing.T, _, state string) { must(t, os.Mkdir(state, 0o700)) }, "isn't a regular file"},
		{"symlink", func(t *testing.T, _, state string) { must(t, os.Symlink("/etc/passwd", state)) }, "is a symlink"},
		{"fifo", func(t *testing.T, _, state string) { must(t, syscall.Mkfifo(state, 0o600)) }, "isn't a regular file"}, // the run below has a deadline
		{"too large", func(t *testing.T, _, state string) {
			must(t, os.WriteFile(state, append([]byte(`{"format":1,"note":"`), make([]byte, maxStateSize)...), 0o600))
		}, "is larger than"},
		{"empty machine-id", func(t *testing.T, machineID, _ string) { must(t, os.WriteFile(machineID, nil, 0o644)) }, "machine-id isn't 32 hex digits"},
		{"malformed machine-id", func(t *testing.T, machineID, _ string) {
			must(t, os.WriteFile(machineID, []byte("uninitialized\n"), 0o644))
		}, "isn't 32 hex digits"},
		{"no machine-id", func(t *testing.T, machineID, _ string) { must(t, os.Remove(machineID)) }, "maintenance record unavailable: open "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			machineID, state := setupAdvise(t)
			must(t, os.MkdirAll(filepath.Dir(state), 0o700))
			c.prepare(t, machineID, state)
			// A regression that blocks on a FIFO fails here, not at the
			// test binary's timeout.
			done := make(chan string, 1)
			go func() {
				_, stdout, _ := hwspec(t, "", "advise", "-f", "json")
				done <- stdout
			}()
			var stdout string
			select {
			case stdout = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("advise hung reading the maintenance record")
			}
			a := readAdvice(t, stdout)
			var got []string
			for _, w := range a.Warnings {
				if !strings.HasPrefix(w, "capture: ") {
					got = append(got, w)
				}
			}
			joined := strings.Join(got, "\n")
			if c.want == "" && len(got) != 0 || c.want != "" && !strings.Contains(joined, c.want) {
				t.Errorf("warnings %q, want %q", got, c.want)
			}
			if strings.Contains(joined, "root:") || strings.Contains(joined, "secret") {
				t.Error("the state warning quotes the file it read")
			}
			if len(a.Findings) != 1 {
				t.Errorf("%d findings, want the advice anyway", len(a.Findings))
			}
		})
	}
}

// The record is read only if it belongs to the user whose record it is.
func TestStateOfAnotherUserIsRefused(t *testing.T) {
	_, state := setupAdvise(t)
	must(t, os.MkdirAll(filepath.Dir(state), 0o700))
	must(t, os.WriteFile(state, []byte(`{"format":1}`), 0o600))
	if _, err := readStateFile(state, os.Getuid()); err != nil {
		t.Fatalf("own file: %v", err)
	}
	if _, err := readStateFile(state, os.Getuid()+1); err == nil || err.Error() != "belongs to another user" {
		t.Errorf("another user's file: %v", err)
	}
}

// advise reports problems with the user's own ID sources, as capture does.
func TestAdviseReportsBrokenOverrides(t *testing.T) {
	setupAdvise(t)
	overrides := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "hwspec/overrides.ids")
	must(t, os.MkdirAll(filepath.Dir(overrides), 0o700))
	must(t, os.WriteFile(overrides, []byte("not an override line\n"), 0o600))
	ids.Reset()
	if _, stderr := mustRun(t, "", "advise", "-f", "json"); !strings.Contains(stderr, "overrides") {
		t.Errorf("stderr %q", stderr)
	}
}

func TestLoadStateKeepsTheRecord(t *testing.T) {
	_, state := setupAdvise(t)
	must(t, os.MkdirAll(filepath.Dir(state), 0o700))
	must(t, os.WriteFile(state, []byte(`{"format":1,"done":[{"task":"repaste","date":"2026-01-02","material":"ptm7950","note":"both fans"}]}`), 0o600))
	st, warn := loadState(false)
	if warn != "" || st == nil || len(st.Done) != 1 || st.Done[0] != (advisor.Done{Task: "repaste", Date: "2026-01-02", Material: "ptm7950", Note: "both fans"}) {
		t.Errorf("state %+v, %q", st, warn)
	}
}

// --redact advice names neither the user's home nor the machine's key, in
// any format, even when the maintenance record can't be read.
func TestRedactedAdviceDoesntNameTheStateFile(t *testing.T) {
	machineID, state := setupAdvise(t)
	must(t, os.MkdirAll(filepath.Dir(state), 0o700))
	must(t, os.WriteFile(state, []byte(`{`), 0o600))
	stateHome := filepath.Dir(filepath.Dir(filepath.Dir(state)))
	for _, f := range []string{"json", "text", "yaml"} {
		got := out(t, "", "advise", "--redact", "-f", f)
		if strings.Contains(got, stateHome) || strings.Contains(got, testStateKey) || !strings.Contains(got, "maintenance record unavailable: it isn't valid JSON") {
			t.Errorf("-f %s:\n%s", f, got)
		}
	}
	must(t, os.Remove(machineID))
	if got := out(t, "", "advise", "--redact", "-f", "json"); strings.Contains(got, machineID) || !strings.Contains(got, `"maintenance record unavailable"`) {
		t.Errorf("redacted advice names the machine-id path:\n%s", got)
	}
}

func TestAdviseWritesPrivateFilesAndExplainsMistakes(t *testing.T) {
	setupAdvise(t)
	old := syscall.Umask(0) // so only advise's own mode decides
	t.Cleanup(func() { syscall.Umask(old) })
	file := filepath.Join(t.TempDir(), "advice.json")
	_, stderr := mustRun(t, "", "advise", "--redact", "-o", file)
	st, err := os.Stat(file)
	if err != nil || st.Mode().Perm() != 0o600 || !strings.Contains(stderr, "(json, 1 finding, ") {
		t.Errorf("-o: %v, mode %v, %q", err, st, stderr)
	}
	if data, _ := os.ReadFile(file); readAdvice(t, string(data)).AdviceVersion != 1 {
		t.Error("-o advice.json isn't JSON")
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"advise", "a.json", "b.json"}, "usage: hwspec advise"},
		{[]string{"advise", "a.json", "--full"}, "--full only without FILE"},
		{[]string{"advise", "-f", "xml"}, `unknown format "xml"`},
		{[]string{"advise", "--bogus"}, "bogus"},
		{[]string{"advise", filepath.Join(t.TempDir(), "missing.json")}, "no such file"},
		{[]string{"advise", "-"}, "not a hwspec capture"},
		{[]string{"advise", "-o", filepath.Join(t.TempDir(), "no/dir/a.json")}, "no such file"},
		{[]string{"show", "a.json", "b.json"}, "usage: hwspec show"},
		{[]string{"show"}, "usage: hwspec show"},
	} {
		code, _, stderr := hwspec(t, `{"tool":{}}`, c.args...)
		if code != 1 || !strings.Contains(stderr, c.want) {
			t.Errorf("%v: exit %d, %q; want %q", c.args, code, stderr, c.want)
		}
	}
	if stdout, _ := mustRun(t, "", "advise", "-h"); !strings.Contains(stdout, "hwspec advise [FILE]") {
		t.Error("advise -h doesn't print the usage")
	}
}

// advise --full captures as root through pkexec, like capture --full; its
// messages name advise, and the state stays the unprivileged user's.
func TestAdviseFullCapturesAsRoot(t *testing.T) {
	setupAdvise(t)
	oldFind, oldOwned, oldEuid := findPkexec, rootOwned, geteuid
	t.Cleanup(func() { findPkexec, rootOwned, geteuid = oldFind, oldOwned, oldEuid })
	geteuid = func() int { return 1000 }
	rootOwned = func(string) error { return nil }
	child := filepath.Join(t.TempDir(), "child.json")
	mustRun(t, "", "capture", "-o", child)
	pkexec := filepath.Join(t.TempDir(), "pkexec")
	script := "#!/bin/sh\n[ \"$2 $3 $4\" = \"capture -f json\" ] || exit 2\n" +
		"[ \"$FAKE_PKEXEC\" = dismiss ] && exit 126\ncat \"" + child + "\"\n"
	must(t, os.WriteFile(pkexec, []byte(script), 0o755))
	findPkexec = func() (string, error) { return pkexec, nil }

	t.Setenv("FAKE_PKEXEC", "")
	a := readAdvice(t, out(t, "", "advise", "--full", "-f", "json"))
	if !a.Live || len(a.Findings) != 1 {
		t.Errorf("live %v, %d findings", a.Live, len(a.Findings))
	}
	t.Setenv("FAKE_PKEXEC", "dismiss")
	if code, _, stderr := hwspec(t, "", "advise", "--full"); code != 1 || !strings.Contains(stderr, "run hwspec advise without --full") {
		t.Errorf("dismissed: exit %d, %q", code, stderr)
	}
	findPkexec = func() (string, error) { return "", errors.New("none") }
	if code, _, stderr := hwspec(t, "", "advise", "--full"); code != 1 || !strings.Contains(stderr, "needs pkexec") {
		t.Errorf("no pkexec: exit %d, %q", code, stderr)
	}
}
