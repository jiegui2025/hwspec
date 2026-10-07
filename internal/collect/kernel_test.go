package collect

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The thin wrappers around kernel interfaces, run against the real kernel.
// Each test accepts what any Linux machine (a CI runner, a container, a
// laptop) may answer, and checks that answer is handled.

func TestUnameNamesTheKernelAndArchitecture(t *testing.T) {
	var u syscall.Utsname
	if err := syscall.Uname(&u); err != nil {
		t.Skip(err)
	}
	release, machine := uname()
	if release == "" || release != utsString(u.Release[:]) || machine == "" || machine != utsString(u.Machine[:]) {
		t.Errorf("uname() = %q, %q", release, machine)
	}
	if got := utsString([]int8{'x', '8', '6', 0, 'z'}); got != "x86" {
		t.Errorf("utsString stops at NUL: %q", got)
	}
}

func TestCommandsRunWithATimeLimit(t *testing.T) {
	if out, err := runCommand(5*time.Second, "sh", "-c", "echo ok"); err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Errorf("sh: %q, %v", out, err)
	}
	start := time.Now()
	if _, err := runCommand(100*time.Millisecond, "sleep", "5"); err == nil || time.Since(start) > 3*time.Second {
		t.Errorf("a hung command wasn't stopped: %v after %v", err, time.Since(start))
	}
}

// ethtool answers for real network drivers; the loopback has none.
func TestHostEthtoolAsksTheNetworkDriver(t *testing.T) {
	hostTest(t)
	if _, err := ethtoolDrvinfo("lo"); err == nil {
		t.Log("the loopback answered ethtool (some kernels do)")
	}
	if _, err := ethtoolDrvinfo("no-such-if0"); err == nil {
		t.Error("a missing interface answered")
	}
	entries, _ := os.ReadDir("/sys/class/net")
	for _, e := range entries {
		if _, err := os.Stat("/sys/class/net/" + e.Name() + "/device"); err != nil {
			continue // virtual
		}
		fw, err := ethtoolDrvinfo(e.Name())
		if err != nil && !errors.Is(err, unix.EOPNOTSUPP) {
			t.Errorf("%s: %v", e.Name(), err)
		}
		t.Logf("%s firmware %q", e.Name(), fw)
		return
	}
	t.Log("no hardware network interface here")
}

func TestCStringStopsAtNUL(t *testing.T) {
	if got := cString([]byte("77.8dbafb52.0 \x00junk")); got != "77.8dbafb52.0" {
		t.Errorf("cString = %q", got)
	}
	if got := cString([]byte(" 1.2 ")); got != "1.2" {
		t.Errorf("without NUL: %q", got)
	}
}

// The Bluetooth management socket answers for a controller, refuses one
// that doesn't exist, or isn't there at all (no Bluetooth in the kernel, a
// sandbox without the socket family).
func TestHostBluetoothManagementSocket(t *testing.T) {
	hostTest(t)
	info, err := mgmtReadInfo(0)
	switch {
	case err == nil:
		if len(info.address) != 17 || strings.HasPrefix(btVersion(info.version), "unknown") {
			t.Errorf("hci0 answered %+v", info)
		}
	case errors.Is(err, unix.EAFNOSUPPORT), errors.Is(err, unix.EPROTONOSUPPORT), errors.Is(err, unix.EPERM), errors.Is(err, unix.EACCES):
		t.Logf("no Bluetooth here: %v", err)
	case err.Error() == "management status 17":
		t.Log("no controller 0") // 0x11: invalid index
	default:
		t.Errorf("unexpected error: %v", err)
	}
	if _, err := mgmtReadInfo(0xFFFE); err == nil {
		t.Error("a missing controller answered")
	}
}

// A raw HCI socket answers Read Local Version for a controller without
// privilege (#202), or the controller is down, missing, or the socket
// family isn't there at all.
func TestHostBluetoothHCISocket(t *testing.T) {
	hostTest(t)
	v, err := hciReadLocalVersion(0)
	switch {
	case err == nil:
		if v.hciVersion == 0 && v.lmpSubver == 0 && v.hciRevision == 0 {
			t.Errorf("hci0 answered %+v", v)
		}
	case errors.Is(err, unix.EAFNOSUPPORT), errors.Is(err, unix.EPROTONOSUPPORT), errors.Is(err, unix.ENODEV),
		errors.Is(err, unix.ENETDOWN), errors.Is(err, unix.EPERM), errors.Is(err, unix.EACCES):
		t.Logf("no usable controller 0 here: %v", err)
	default:
		t.Errorf("unexpected error: %v", err)
	}
	if _, err := hciReadLocalVersion(0xFFFE); err == nil {
		t.Error("a missing controller answered")
	}
}

// Reading the NVMe health log needs the device node, and root.
func TestNVMeHealthNeedsTheDevice(t *testing.T) {
	if _, err := nvmeHealth("/dev/no-such-nvme"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing device: %v", err)
	}
	// Not an NVMe controller: the ioctl is refused.
	if _, err := nvmeHealth("/dev/null"); err == nil || !strings.Contains(err.Error(), "NVMe get-log-page") {
		t.Errorf("/dev/null: %v", err)
	}
}

// Paths lists what a capture reads, for tools/snapshot.
func TestPathsListsWhatACaptureReads(t *testing.T) {
	file, _ := fakeRoot(t)
	asMachine(t, "x86_64", 1000)
	file("/etc/os-release", "ID=test\n")
	paths := Paths("test")
	has := map[string]bool{}
	for _, p := range paths {
		has[p] = true
	}
	for _, want := range []string{"/etc/os-release", "/proc/cpuinfo", "/sys/class/dmi/id/", "/proc/meminfo"} {
		if !has[want] {
			t.Errorf("Paths lacks %s (got %d paths)", want, len(paths))
		}
	}
	if traceRead != nil {
		t.Error("tracing left on")
	}
}

// hostTest marks a test whose coverage depends on this machine's hardware
// (a Bluetooth controller, a network card). CI's coverage run sets
// HWSPEC_SKIP_HOST_TESTS so the gate doesn't depend on the runner; another
// step runs them.
func hostTest(t *testing.T) {
	t.Helper()
	if !strings.HasPrefix(t.Name(), "TestHost") {
		t.Fatalf("%s depends on this machine, so it must be named TestHost*: CI runs host tests by that prefix (-run '^TestHost')", t.Name())
	}
	if os.Getenv("HWSPEC_SKIP_HOST_TESTS") != "" {
		t.Skip("depends on this machine's hardware (HWSPEC_SKIP_HOST_TESTS is set)")
	}
}
