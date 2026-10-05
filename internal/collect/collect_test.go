package collect

import (
	"testing"

	"github.com/jiegui2025/hwspec/internal/report"
)

func TestCountCPUList(t *testing.T) {
	for in, want := range map[string]int{"0-11": 12, "0-3,8-11": 8, "5": 1, "0,2,4-5\n": 4, "": 0} {
		if got := countCPUList(in); got != want {
			t.Errorf("countCPUList(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]uint64{"32K": 32 << 10, "16M": 16 << 20, "1G": 1 << 30, "512": 512, "": 0, "x": 0} {
		if got := parseSize(in); got != want {
			t.Errorf("parseSize(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestParseOSRelease(t *testing.T) {
	m := parseOSRelease("# comment\nNAME=\"Linux Mint\"\nID=linuxmint\nID_LIKE='ubuntu debian'\nVERSION_ID=\"22.1\"\nPRETTY_NAME=\"Linux Mint 22.1\"\n")
	want := map[string]string{"NAME": "Linux Mint", "ID": "linuxmint", "ID_LIKE": "ubuntu debian", "VERSION_ID": "22.1", "PRETTY_NAME": "Linux Mint 22.1"}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %q, want %q", k, m[k], v)
		}
	}
}

func TestSkipDisk(t *testing.T) {
	for name, skip := range map[string]bool{"loop0": true, "zram0": true, "dm-1": true, "nvme0n1": false, "sda": false, "mmcblk0": false} {
		if skipDisk(name) != skip {
			t.Errorf("skipDisk(%q) = %v", name, !skip)
		}
	}
}

// A drive reporting impossible counters (all bits set) must saturate, not
// wrap around to a small, plausible-looking number.
func TestNVMeCountersSaturate(t *testing.T) {
	log := make([]byte, 512)
	for i := 32; i < 64; i++ {
		log[i] = 0xFF // data units read and written
	}
	log[32+16*5] = 7 // power cycles = 7 (offset 112)
	h := parseNVMeSMART(log)
	if h.Metrics[report.MetricDataReadBytes] != float64(^uint64(0)) || h.Metrics[report.MetricDataWrittenBytes] != float64(^uint64(0)) {
		t.Errorf("read/written = %v/%v, want saturation", h.Metrics[report.MetricDataReadBytes], h.Metrics[report.MetricDataWrittenBytes])
	}
	if h.Metrics[report.MetricPowerCycles] != 7 {
		t.Errorf("power cycles = %v, want 7", h.Metrics[report.MetricPowerCycles])
	}
	if _, ok := h.Metrics[report.MetricTemperatureC]; ok {
		t.Error("temperature reported for a zero (unsupported) reading")
	}
}

func TestVMsAreRecognisedByTheirFirmwareIdentity(t *testing.T) {
	for _, c := range []struct {
		vendor, product string
		vm              bool
	}{
		{"QEMU", "Standard PC (Q35 + ICH9, 2009)", true},
		{"Microsoft Corporation", "Virtual Machine", true},
		{"innotek GmbH", "VirtualBox", true},
		{"Amazon EC2", "m7g.large", true},
		{"HP", "HP EliteDesk 800 G5 Desktop Mini", false},
		{"", "", false},
	} {
		if got := isVMVendor(c.vendor, c.product); got != c.vm {
			t.Errorf("isVMVendor(%q, %q) = %v, want %v", c.vendor, c.product, got, c.vm)
		}
	}
}
