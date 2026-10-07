package collect

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jiegui2025/hwspec/internal/report"
)

func TestWithin(t *testing.T) {
	if v, err := within(time.Second, func() (int, error) { return 7, nil }); v != 7 || err != nil {
		t.Errorf("answered: %v, %v", v, err)
	}
	failed := errors.New("failed")
	if _, err := within(time.Second, func() (int, error) { return 0, failed }); !errors.Is(err, failed) {
		t.Errorf("failed: %v", err)
	}
	start := time.Now()
	v, err := within(50*time.Millisecond, func() (int, error) { select {} })
	if v != 0 || err == nil || err.Error() != "didn't answer within 50ms; left out" || time.Since(start) > time.Second {
		t.Errorf("never answers: %v, %v after %v", v, err, time.Since(start))
	}
}

// The real runCommand: a program is killed at its timeout, and output a
// child of it holds open is closed commandGrace later (WaitDelay).
func TestRunCommandIsBounded(t *testing.T) {
	sh, err := os.Stat("/bin/sh")
	if err != nil || !sh.Mode().IsRegular() {
		t.Skip("no /bin/sh")
	}
	start := time.Now()
	if _, err := runCommand(100*time.Millisecond, "/bin/sh", "-c", "exec sleep 10"); err == nil || time.Since(start) > time.Second {
		t.Errorf("killed: %v after %v", err, time.Since(start))
	}
	start = time.Now()
	// WaitDelay ends it, not the hard bound (which would say noAnswer).
	if _, err := runCommand(100*time.Millisecond, "/bin/sh", "-c", "sleep 10 & sleep 10"); err == nil || errors.As(err, new(noAnswer)) || time.Since(start) > commandGrace+2*time.Second {
		t.Errorf("child holds the output: %v after %v", err, time.Since(start))
	}
	if out, err := runCommand(time.Second, "/bin/sh", "-c", "echo ok"); err != nil || string(out) != "ok\n" {
		t.Errorf("answers: %q, %v", out, err)
	}
}

// #151's acceptance: smartctl hanging on two disks costs about one
// timeout, not two.
func TestDisksHungInSmartctlAreCheckedAtOnce(t *testing.T) {
	file, link := fakeRoot(t)
	asMachine(t, "x86_64", 0)
	old := smartctlTimeout
	t.Cleanup(func() { smartctlTimeout = old })
	smartctlTimeout = 300 * time.Millisecond
	findSmartctl = func() string { return "/usr/sbin/smartctl" }
	runCommand = func(timeout time.Duration, _ string, _ ...string) ([]byte, error) {
		time.Sleep(timeout)
		return nil, errors.New("signal: killed")
	}
	for i, name := range []string{"sda", "sdb"} {
		target := "../devices/pci0000:00/0000:00:17.0/ata1/host0/target0:0:" + string(rune('0'+i)) + "/0:0:" + string(rune('0'+i)) + ":0/block/" + name
		dev := "/sys/" + strings.TrimPrefix(target, "../")
		file(dev+"/size", "2000")
		link("/sys/block/"+name, target)
		link(dev+"/device", "../..")
	}
	c := &collector{r: &report.Report{}, privileged: true}
	start := time.Now()
	c.storage()
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Errorf("took %v for two hung disks (timeout 300ms)", took)
	}
	if len(c.r.Storage) != 2 || !hasWarning(c.r, "drive health sda: ") || !hasWarning(c.r, "drive health sdb: ") {
		t.Errorf("disks %+v, warnings %q", c.r.Storage, c.r.Warnings)
	}
}

// fifo makes a file under the fake root whose reads never return: opening
// a FIFO waits for a writer. The reader is left blocked when the test
// ends, as a stuck kernel read would be.
func fifo(t *testing.T, path string) {
	t.Helper()
	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(full, 0o644); err != nil {
		t.Fatal(err)
	}
}

func shortAnswer(t *testing.T) {
	old := slowAnswer
	t.Cleanup(func() { slowAnswer = old })
	slowAnswer = 50 * time.Millisecond
}

// #151's acceptance: a read that never returns is reported and left out
// within slowAnswer: a hwmon chip, a module's SPD, a codec's proc file,
// the ethtool query. The rest is still read.
func TestReadsThatNeverReturn(t *testing.T) {
	t.Run("hwmon", func(t *testing.T) {
		file, _ := fakeRoot(t)
		shortAnswer(t)
		file("/sys/class/hwmon/hwmon0/name", "coretemp")
		file("/sys/class/hwmon/hwmon0/temp1_input", "40000")
		file("/sys/class/hwmon/hwmon1/name", "nct6775")
		fifo(t, "/sys/class/hwmon/hwmon1/temp1_input")
		fifo(t, "/sys/class/hwmon/hwmon1/temp2_input")
		var read []string // the chip's other files aren't tried once one is stuck
		traceRead = func(path string) { read = append(read, path) }
		c := &collector{r: &report.Report{}}
		start := time.Now()
		warnings := c.sensors()
		traceRead = nil
		if slices.Contains(read, "/sys/class/hwmon/hwmon1/temp2_input") {
			t.Errorf("read on after a stuck file: %q", read)
		}
		if time.Since(start) > time.Second || len(c.r.Sensors) != 1 || c.r.Sensors[0].Chip != "coretemp" ||
			len(warnings) != 1 || warnings[0] != "sensors hwmon1: didn't answer within 50ms; left out" {
			t.Errorf("sensors %+v, warnings %q after %v", c.r.Sensors, warnings, time.Since(start))
		}
	})
	t.Run("spd", func(t *testing.T) {
		file, _ := fakeRoot(t)
		shortAnswer(t)
		file("/sys/bus/i2c/drivers/ee1004/bind", "")
		fifo(t, "/sys/bus/i2c/drivers/ee1004/0-0050/eeprom")
		c := &collector{r: &report.Report{}}
		start := time.Now()
		c.spdModules()
		if time.Since(start) > time.Second || !hasWarning(c.r, "memory module SPD 0-0050: didn't answer within 50ms; left out") {
			t.Errorf("warnings %q after %v", c.r.Warnings, time.Since(start))
		}
	})
	t.Run("codec", func(t *testing.T) {
		file, _ := fakeRoot(t)
		shortAnswer(t)
		file("/proc/asound/cards", " 0 [PCH            ]: HDA-Intel - HDA Intel PCH\n                      HDA Intel PCH at 0xb1210000 irq 136\n")
		fifo(t, "/proc/asound/card0/codec#0")
		c := &collector{r: &report.Report{}}
		start := time.Now()
		c.audio()
		if time.Since(start) > time.Second || len(c.r.Audio) != 1 || c.r.Audio[0].Codecs != nil ||
			!hasWarning(c.r, "audio card0 codecs: didn't answer within 50ms; left out") {
			t.Errorf("audio %+v, warnings %q after %v", c.r.Audio, c.r.Warnings, time.Since(start))
		}
	})
	t.Run("ethtool", func(t *testing.T) {
		file, link := fakeRoot(t)
		shortAnswer(t)
		ethtoolDrvinfo = func(string) (string, error) { select {} }
		link("/sys/class/net/eth0/device", "../../../devices/pci0000:00/0000:00:1f.6")
		file("/sys/devices/pci0000:00/0000:00:1f.6/vendor", "0x8086")
		file("/sys/class/net/eth0/operstate", "up")
		c := &collector{r: &report.Report{}}
		start := time.Now()
		c.network()
		if time.Since(start) > time.Second || len(c.r.Network) != 1 || c.r.Network[0].Firmware.Known() ||
			!hasWarning(c.r, "network eth0: ethtool: didn't answer within 50ms; left out") {
			t.Errorf("network %+v, warnings %q after %v", c.r.Network, c.r.Warnings, time.Since(start))
		}
	})
}

// An NVMe health command stuck past its timeout (a controller reset) is
// given up on; at most maxHealthChecks disks are checked at once.
func TestDiskHealthBounds(t *testing.T) {
	t.Cleanup(saveHooks())
	oldTimeout := nvmeTimeout
	t.Cleanup(func() { nvmeTimeout = oldTimeout })
	nvmeTimeout = 25 * time.Millisecond
	nvmeHealthFn = func(string) (*report.Health, error) { select {} }
	start := time.Now()
	if h, err := diskHealth("nvme0n1", "nvme"); h != nil || err == nil || err.Error() != "didn't answer within 50ms; left out" || time.Since(start) > time.Second {
		t.Errorf("stuck NVMe: %+v, %v after %v", h, err, time.Since(start))
	}

	var mu sync.Mutex
	running, most := 0, 0
	findSmartctl = func() string { return "/usr/sbin/smartctl" }
	runCommand = func(time.Duration, string, ...string) ([]byte, error) {
		mu.Lock()
		running++
		most = max(most, running)
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		running--
		mu.Unlock()
		return nil, errors.New("no SMART")
	}
	c := &collector{r: &report.Report{}, privileged: true}
	var all []int
	for i := range 3 * maxHealthChecks {
		c.r.Storage = append(c.r.Storage, report.Disk{Name: fmt.Sprintf("sd%c", 'a'+i), Transport: "sata"})
		all = append(all, i)
	}
	c.diskHealths(all)
	if most != 8 || len(c.r.Warnings) != len(all) {
		t.Errorf("%d at once, %d warnings", most, len(c.r.Warnings))
	}
}

// A stuck sensor chip's warning reaches the capture's report.
func TestCaptureReportsAStuckSensorChip(t *testing.T) {
	file, _ := fakeRoot(t)
	asMachine(t, "x86_64", 1000)
	shortAnswer(t)
	file("/proc/sys/kernel/osrelease", "6.12.0-test")
	file("/sys/class/hwmon/hwmon0/name", "nct6775")
	fifo(t, "/sys/class/hwmon/hwmon0/temp1_input")
	if r := Collect("test"); !hasWarning(r, "sensors hwmon0: didn't answer within 50ms; left out") {
		t.Errorf("warnings %q", r.Warnings)
	}
}
