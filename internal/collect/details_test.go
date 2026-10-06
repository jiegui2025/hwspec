package collect

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jiegui2025/hwspec/internal/report"
	"github.com/jiegui2025/hwspec/internal/spd"
)

// fakeRoot points the collectors at an empty temporary tree and returns
// helpers to populate it.
func fakeRoot(t *testing.T) (file func(path, content string), link func(path, target string)) {
	t.Helper()
	t.Cleanup(saveHooks())
	root = t.TempDir()
	file = func(path, content string) {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link = func(path, target string) {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, full); err != nil {
			t.Fatal(err)
		}
	}
	return file, link
}

func TestDriversReportTheirModuleAndTaint(t *testing.T) {
	file, link := fakeRoot(t)
	file("/sys/module/module/parameters/sig_enforce", "N\n")
	// An out-of-tree, proprietary, unsigned module (e.g. a vendor GPU driver).
	link("/sys/devices/pci0000:00/0000:01:00.0/driver", "../../../bus/pci/drivers/nvidia")
	link("/sys/bus/pci/drivers/nvidia/module", "../../../../module/nvidia")
	file("/sys/module/nvidia/version", "580.95.05\n")
	file("/sys/module/nvidia/srcversion", "ABCDEF\n")
	file("/sys/module/nvidia/taint", "POE\n")
	file("/sys/module/nvidia/initstate", "live\n")
	// A clean in-tree module.
	link("/sys/devices/pci0000:00/0000:00:1f.6/driver", "../../../bus/pci/drivers/e1000e")
	link("/sys/bus/pci/drivers/e1000e/module", "../../../../module/e1000e")
	file("/sys/module/e1000e/taint", "\n")
	file("/sys/module/e1000e/initstate", "live\n")
	// A built-in driver without a module directory.
	link("/sys/devices/pci0000:00/0000:00:14.0/driver", "../../../bus/pci/drivers/xhci_hcd")
	file("/sys/bus/pci/drivers/xhci_hcd/bind", "")
	// A built-in driver that still links to its module directory (ahci with
	// CONFIG_SATA_AHCI=y): no initstate, as only loaded modules have one.
	link("/sys/devices/pci0000:00/0000:00:17.0/driver", "../../../bus/pci/drivers/ahci")
	link("/sys/bus/pci/drivers/ahci/module", "../../../../module/ahci")
	file("/sys/module/ahci/version", "3.0\n")

	t.Run("signing", func(t *testing.T) {
		c := &collector{moduleSigning: moduleSigningSupported()}
		nv := c.driverAt("/sys/devices/pci0000:00/0000:01:00.0")
		if nv == nil || nv.Name != "nvidia" || nv.Module != "nvidia" || nv.Version != "580.95.05" || *nv.InTree || !*nv.Proprietary || !*nv.Unsigned {
			t.Errorf("nvidia driver = %+v", nv)
		}
		e := c.driverAt("/sys/devices/pci0000:00/0000:00:1f.6")
		if e == nil || !*e.InTree || *e.Proprietary || *e.Unsigned || e.Builtin {
			t.Errorf("e1000e driver = %+v", e)
		}
		out, err := json.Marshal(&report.Report{PCI: []report.PCIDevice{{Driver: e}}})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), `"unsigned":false`) {
			t.Errorf("unsigned state with module signing = %s", out)
		}
		if x := c.driverAt("/sys/devices/pci0000:00/0000:00:14.0"); x == nil || !x.Builtin || x.Module != "" {
			t.Errorf("built-in driver = %+v", x)
		}
		if a := c.driverAt("/sys/devices/pci0000:00/0000:00:17.0"); a == nil || !a.Builtin || a.Module != "" || a.Version != "3.0" {
			t.Errorf("built-in ahci = %+v", a)
		}
		if d := c.driverAt("/sys/devices/pci0000:00/0000:00:00.0"); d != nil {
			t.Errorf("unbound device has driver %+v", d)
		}
	})

	if err := os.Remove(filepath.Join(root, "sys/module/module/parameters/sig_enforce")); err != nil {
		t.Fatal(err)
	}
	t.Run("no signing", func(t *testing.T) {
		withoutSigning := (&collector{moduleSigning: moduleSigningSupported()}).driverAt("/sys/devices/pci0000:00/0000:01:00.0")
		if withoutSigning == nil || withoutSigning.Unsigned != nil || withoutSigning.InTree == nil || *withoutSigning.InTree || withoutSigning.Proprietary == nil || !*withoutSigning.Proprietary {
			t.Errorf("driver without module signing = %+v", withoutSigning)
		}
		out, err := json.Marshal(&report.Report{PCI: []report.PCIDevice{{Driver: withoutSigning}}})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), `"unsigned"`) {
			t.Errorf("unsigned state without module signing = %s", out)
		}
	})
}

// A disk's driver is its controller's: nvme for an NVMe namespace, not
// the class device above it.
func TestDiskDriverIsTheControllers(t *testing.T) {
	_, link := fakeRoot(t)
	pci := "/sys/devices/pci0000:00/0000:00:1b.0/0000:01:00.0"
	link(pci+"/driver", "../../../../bus/pci/drivers/nvme")
	link(pci+"/subsystem", "../../../../bus/pci")
	link("/sys/bus/pci/drivers/nvme/module", "../../../../module/nvme")
	if err := os.MkdirAll(filepath.Join(root, pci, "nvme/nvme0"), 0o755); err != nil {
		t.Fatal(err)
	}
	link("/sys/block/nvme0n1/device", "../../devices/pci0000:00/0000:00:1b.0/0000:01:00.0/nvme/nvme0")
	if d := (&collector{}).controllerDriver("/sys/block/nvme0n1/device"); d == nil || d.Name != "nvme" {
		t.Errorf("controller driver = %+v", d)
	}
}

func TestNetworkErrorsAboveOnePerThousandNeedAttention(t *testing.T) {
	file, _ := fakeRoot(t)
	stats := func(dir string, rx, tx, rxErr string) {
		file(dir+"rx_packets", rx)
		file(dir+"tx_packets", tx)
		file(dir+"rx_errors", rxErr)
		file(dir+"tx_errors", "0")
		file(dir+"rx_dropped", "3")
		file(dir+"tx_dropped", "0")
	}
	stats("/a/", "100000", "100000", "10")
	stats("/b/", "100000", "100000", "500")
	if h := nicHealth("/a/"); h.Status != report.StatusOK || h.Metrics[report.MetricRxErrors] != 10 {
		t.Errorf("clean link: %+v", h)
	}
	if h := nicHealth("/b/"); h.Status != report.StatusWarning || len(h.Reasons) != 1 {
		t.Errorf("noisy link: %+v", h)
	}
	if h := nicHealth("/none/"); h != nil {
		t.Errorf("no statistics: %+v", h)
	}
}

func TestBatteryWearAndCyclesToEightyPercent(t *testing.T) {
	file, _ := fakeRoot(t)
	// 90% of design capacity after 200 cycles: half the 100% → 80% range
	// used, 0.05% per cycle, so 200 more cycles until 80%.
	d := "/sys/class/power_supply/BAT0/"
	file(d+"energy_full_design", "50000000")
	file(d+"energy_full", "45000000")
	file(d+"cycle_count", "200")
	h := batteryHealth(d)
	if h.Status != report.StatusOK || *h.LifeUsedPercent != 50 || *h.LifeRemainingPercent != 50 || h.Metrics[report.MetricCapacityPercent] != 90 {
		t.Errorf("health = %+v", h)
	}
	if h.Estimate == nil || h.Estimate.Value != 200 || !strings.Contains(h.Estimate.Method, "200 cycles") {
		t.Errorf("estimate = %+v", h.Estimate)
	}
	// Worn below 80%, reported through charge and design voltage.
	w := "/sys/class/power_supply/BAT1/"
	file(w+"charge_full_design", "4000000")
	file(w+"charge_full", "3000000")
	file(w+"voltage_min_design", "11400000")
	if h := batteryHealth(w); h.Status != report.StatusWarning || h.Metrics[report.MetricDesignWh] != 45.6 {
		t.Errorf("worn battery = %+v", h)
	}
	// The driver's own verdict wins.
	f := "/sys/class/power_supply/BAT2/"
	file(f+"health", "Dead")
	if h := batteryHealth(f); h == nil || h.Status != report.StatusFailing {
		t.Errorf("dead battery = %+v", h)
	}
	// A cold battery is a passing condition, not a failed cell.
	c := "/sys/class/power_supply/BAT3/"
	file(c+"health", "Cold")
	if h := batteryHealth(c); h == nil || h.Status != report.StatusWarning || !strings.Contains(h.Reasons[0], "temperature") {
		t.Errorf("cold battery = %+v", h)
	}
	// A new battery above its design capacity: capacity kept, no wear.
	n := "/sys/class/power_supply/BAT4/"
	file(n+"energy_full_design", "50000000")
	file(n+"energy_full", "51000000")
	if h := batteryHealth(n); *h.LifeUsedPercent != 0 || *h.LifeRemainingPercent != 100 || h.Metrics[report.MetricCapacityPercent] != 102 {
		t.Errorf("new battery = %+v", h)
	}
	if h := batteryHealth("/sys/class/power_supply/none/"); h != nil {
		t.Errorf("no data: %+v", h)
	}
}

func TestNVMeWarningBitsAndEndurance(t *testing.T) {
	log := make([]byte, 512)
	log[0] = 0x01 | 0x04 // spare below threshold, reliability degraded
	log[5] = 12
	h := parseNVMeSMART(log)
	if h.Status != report.StatusFailing || len(h.Reasons) != 2 {
		t.Errorf("status = %s, reasons %v", h.Status, h.Reasons)
	}
	if *h.LifeUsedPercent != 12 || *h.LifeRemainingPercent != 88 {
		t.Errorf("life = %v/%v", *h.LifeUsedPercent, *h.LifeRemainingPercent)
	}
	worn := make([]byte, 512)
	worn[5] = 105
	if h := parseNVMeSMART(worn); h.Status != report.StatusWarning || *h.LifeRemainingPercent != 0 {
		t.Errorf("past rated endurance: %+v", h)
	}
}

func TestSmartctlVerdictsAndEndurance(t *testing.T) {
	oldFind, oldRun := findSmartctl, runCommand
	t.Cleanup(func() { findSmartctl, runCommand = oldFind, oldRun })
	findSmartctl = func() string { return "/usr/sbin/smartctl" }
	reply := ""
	runCommand = func(time.Duration, string, ...string) ([]byte, error) { return []byte(reply), nil }

	reply = `{"smart_status":{"passed":true},"power_on_time":{"hours":1200},
	  "ata_smart_attributes":{"table":[{"id":5,"raw":{"value":8}},{"id":197,"raw":{"value":0}}]},
	  "ata_device_statistics":{"pages":[{"table":[{"name":"Percentage Used Endurance Indicator","value":17}]}]}}`
	h, err := smartctlHealth("/dev/sda")
	if err != nil || h.Status != report.StatusWarning || *h.LifeUsedPercent != 17 || h.Metrics[report.MetricPowerOnHours] != 1200 {
		t.Errorf("reallocating SSD: %+v, %v", h, err)
	}
	reply = `{"smart_status":{"passed":false}}`
	if h, _ := smartctlHealth("/dev/sdb"); h.Status != report.StatusFailing {
		t.Errorf("failed drive: %+v", h)
	}
	reply = `{"smartctl":{"messages":[{"string":"Unknown USB bridge"}]}}`
	if _, err := smartctlHealth("/dev/sdc"); err == nil || !strings.Contains(err.Error(), "Unknown USB bridge") {
		t.Errorf("no data: %v", err)
	}
	reply = "not json"
	if _, err := smartctlHealth("/dev/sdd"); err == nil {
		t.Error("garbage accepted")
	}
	findSmartctl = func() string { return "" }
	if _, err := smartctlHealth("/dev/sde"); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("missing smartctl: %v", err)
	}
}

// Counters repeat on every logical CPU that shares them: a core's on its
// hyper-thread sibling, a package's on all its CPUs.
func TestThrottlingIsCountedOncePerCoreAndPackage(t *testing.T) {
	file, _ := fakeRoot(t)
	for i, cpu := range []string{"cpu0", "cpu1", "cpu2", "cpu3", "cpu4", "cpu5"} {
		pkg, core := "0", strconv.Itoa(i/2) // cpu0/1, 2/3, 4/5 are siblings
		if i >= 4 {
			pkg = "1"
		}
		d := cpuDir + cpu + "/"
		file(d+"topology/physical_package_id", pkg)
		file(d+"topology/core_id", core)
		file(d+"thermal_throttle/core_throttle_count", "1")
		file(d+"thermal_throttle/package_throttle_count", "10")
	}
	// 3 cores × 1 + 2 packages × 10. Throttling alone isn't a fault.
	h := cpuHealth()
	if h == nil || h.Metrics[report.MetricThrottleEvents] != 23 || h.Status != report.StatusOK || len(h.Reasons) != 1 {
		t.Errorf("health = %+v", h)
	}
}

func TestSPDFillsWhatTheFirmwareLeftOut(t *testing.T) {
	file, _ := fakeRoot(t)
	spd := make([]byte, 512)
	spd[2], spd[3], spd[4], spd[12], spd[13] = 0x0C, 0x03, 0x86, 0x01, 0x03
	spd[320], spd[321] = 0x85, 0xF7
	spd[323], spd[324] = 0x21, 0x10
	copy(spd[325:329], []byte{0x00, 0x77, 0x27, 0x03})
	copy(spd[329:349], "J642GU44J2320NL     ")
	file("/sys/bus/i2c/drivers/ee1004/7-0050/eeprom", string(spd))

	// Without SMBIOS (no root) the SPD module is listed on its own.
	c := &collector{r: &report.Report{}}
	c.spdModules()
	if len(c.r.Memory.Modules) != 1 {
		t.Fatalf("modules = %+v", c.r.Memory.Modules)
	}
	m := c.r.Memory.Modules[0]
	if m.SizeBytes != 16<<30 || m.Identity.Vendor != "85F7" || m.Identity.PartNumber != "J642GU44J2320NL" ||
		m.Identity.ManufactureDate != "2021-W10" || m.Identity.ManufactureDateSource != "spd" {
		t.Errorf("SPD-only module = %+v, %+v", m, m.Identity)
	}

	// With SMBIOS, the SPD is matched by serial and only fills gaps.
	c = &collector{r: &report.Report{}}
	c.r.Memory.Modules = []report.MemoryModule{
		{Locator: "DIMM1", Identity: &report.Identity{Vendor: "Unknown - [0xF785]", Serial: "0772703"}},
		{Locator: "DIMM3", Identity: &report.Identity{Serial: "99999999"}},
	}
	c.spdModules()
	if len(c.r.Memory.Modules) != 2 || c.r.Memory.Modules[0].Identity.Vendor != "Unknown - [0xF785]" ||
		c.r.Memory.Modules[0].Identity.ManufactureDate != "2021-W10" || c.r.Memory.Modules[1].Identity.ManufactureDate != "" {
		t.Errorf("matched modules = %+v / %+v", c.r.Memory.Modules[0].Identity, c.r.Memory.Modules[1].Identity)
	}

	// An SPD the firmware's list doesn't match is reported, not dropped
	// silently.
	c = &collector{r: &report.Report{}}
	c.r.Memory.Modules = []report.MemoryModule{{Locator: "DIMM1", Identity: &report.Identity{Serial: "99999999"}}}
	c.spdModules()
	if len(c.r.Memory.Modules) != 1 || len(c.r.Warnings) != 1 || !strings.Contains(c.r.Warnings[0], "J642GU44J2320NL") {
		t.Errorf("unmatched SPD: modules %+v, warnings %v", c.r.Memory.Modules, c.r.Warnings)
	}
}

func TestECCErrorsMarkTheModule(t *testing.T) {
	file, _ := fakeRoot(t)
	d := "/sys/devices/system/edac/mc/mc0/dimm0/"
	file(d+"dimm_label", "CPU_SrcID#0_MC#0_Chan#0_DIMM#0 DIMM_A1")
	file(d+"dimm_ce_count", "4")
	file(d+"dimm_ue_count", "0")
	u := "/sys/devices/system/edac/mc/mc0/dimm1/"
	file(u+"dimm_label", "DIMM_B1")
	file(u+"dimm_ce_count", "0")
	file(u+"dimm_ue_count", "1")
	o := "/sys/devices/system/edac/mc/mc0/dimm2/"
	file(o+"dimm_label", "Nowhere")
	file(o+"dimm_ce_count", "0")
	file(o+"dimm_ue_count", "0")
	c := &collector{r: &report.Report{}}
	c.r.Memory.Modules = []report.MemoryModule{{Locator: "DIMM_A1"}, {Locator: "DIMM B1"}}
	c.edac()
	if h := c.r.Memory.Modules[0].Health; h == nil || h.Status != report.StatusWarning || h.Metrics[report.MetricECCCorrected] != 4 {
		t.Errorf("A1 = %+v", h)
	}
	if h := c.r.Memory.Modules[1].Health; h == nil || h.Status != report.StatusFailing {
		t.Errorf("B1 = %+v", h)
	}
	if len(c.r.Warnings) != 1 || !strings.Contains(c.r.Warnings[0], "dimm2") {
		t.Errorf("unmatched DIMM not reported: %v", c.r.Warnings)
	}
}

func TestFirmwareVersionsAndDates(t *testing.T) {
	for in, want := range map[string]string{"0543": "5.43", "0001": "0.01", "5002": "50.02", "12": "12"} {
		if got := usbRelease(in); got != want {
			t.Errorf("usbRelease(%q) = %q, want %q", in, got, want)
		}
	}
	for _, v := range []string{"", "N/A", "0x0", " none "} {
		if fw := firmwareVersion(v, "x"); fw != nil {
			t.Errorf("placeholder %q gave %+v", v, fw)
		}
	}
	if fw := firmwareVersion(" 0.5-4 ", "ethtool"); fw == nil || fw.Version != "0.5-4" || fw.Source != "ethtool" {
		t.Errorf("firmware = %+v", fw)
	}
	cases := map[string]string{isoWeek(2020, 38): "2020-W38", isoWeek(2020, 0): "2020", isoWeek(0, 5): "",
		isoDate(2023, 4, 9): "2023-04-09", isoDate(2023, 4, 0): "2023-04", isoDate(2023, 0, 0): "2023"}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestEthtoolFailureLeavesFirmwareEmpty(t *testing.T) {
	old := ethtoolDrvinfo
	t.Cleanup(func() { ethtoolDrvinfo = old })
	ethtoolDrvinfo = func(string) (string, error) { return "", errors.New("operation not supported") }
	file, link := fakeRoot(t)
	link("/sys/class/net/eth0/device", "../../../devices/pci0000:00/0000:00:1f.6")
	file("/sys/devices/pci0000:00/0000:00:1f.6/vendor", "0x8086")
	file("/sys/class/net/eth0/operstate", "up")
	c := &collector{r: &report.Report{}}
	c.network()
	if len(c.r.Network) != 1 || c.r.Network[0].Firmware != nil {
		t.Errorf("network = %+v", c.r.Network)
	}
}

// Boards that repeat a locator in every channel ("DIMM 0" on channels A
// and B) are told apart by the bank locator ghes_edac puts in the label;
// errors must never land on the wrong module, whose replacement would
// leave the failing one in place.
func TestECCErrorsNeverLandOnTheWrongModule(t *testing.T) {
	file, _ := fakeRoot(t)
	dimm := func(name, label, ce, ue string) {
		d := "/sys/devices/system/edac/mc/mc0/" + name + "/"
		file(d+"dimm_label", label)
		file(d+"dimm_ce_count", ce)
		file(d+"dimm_ue_count", ue)
	}
	dimm("dimm0", "P0 CHANNEL A DIMM 0", "0", "0")
	dimm("dimm1", "P0 CHANNEL B DIMM 0", "0", "3")
	dimm("rank2", "A1", "2", "0") // two ranks of one module add up
	dimm("rank3", "A1", "5", "0")
	dimm("dimm4", "DIMM 0", "1", "0") // no bank: could be either channel
	c := &collector{r: &report.Report{}}
	c.r.Memory.Modules = []report.MemoryModule{
		{Locator: "DIMM 0", BankLocator: "P0 CHANNEL A"},
		{Locator: "DIMM 0", BankLocator: "P0 CHANNEL B"},
		{Locator: "A10"},
		{Locator: "A1"},
	}
	c.edac()
	mods := c.r.Memory.Modules
	if h := mods[0].Health; h == nil || h.Status != report.StatusOK {
		t.Errorf("channel A = %+v", h)
	}
	if h := mods[1].Health; h == nil || h.Status != report.StatusFailing || h.Metrics[report.MetricECCUncorrected] != 3 {
		t.Errorf("channel B = %+v", h)
	}
	if mods[2].Health != nil {
		t.Errorf("A10 took A1's errors: %+v", mods[2].Health)
	}
	if h := mods[3].Health; h == nil || h.Metrics[report.MetricECCCorrected] != 7 || h.Status != report.StatusWarning {
		t.Errorf("A1 = %+v", h)
	}
	if len(c.r.Warnings) != 1 || !strings.Contains(c.r.Warnings[0], "several modules") {
		t.Errorf("ambiguous label not reported: %v", c.r.Warnings)
	}
}

// GPU clocks come from the driver's own files, whichever it has: i915's
// hardware range (gt_RPn/RP0) and measured clock (gt_act), or amdgpu's
// pp_dpm_sclk levels with the active one starred. Missing files: nothing,
// silently. Unreadable or inconsistent ones: nothing, and a warning.
func TestGPUClocksFromTheDriversFiles(t *testing.T) {
	const i915, amd = "/sys/class/drm/card0/", "/sys/bus/pci/devices/0000:03:00.0/pp_dpm_sclk"
	mhz := func(v int) *int { return &v }
	cases := []struct {
		name  string
		files map[string]string
		want  *report.GPUClocks
		warn  string
	}{
		{"i915", map[string]string{i915 + "gt_RPn_freq_mhz": "350\n", i915 + "gt_RP0_freq_mhz": "1100\n", i915 + "gt_act_freq_mhz": "700\n",
			i915 + "gt_min_freq_mhz": "500\n", i915 + "gt_cur_freq_mhz": "1000\n"}, // soft limit and request: ignored
			&report.GPUClocks{MinFreqMHz: 350, MaxFreqMHz: 1100, ActualFreqMHz: mhz(700), Source: "i915"}, ""},
		{"i915 idle in RC6", map[string]string{i915 + "gt_RPn_freq_mhz": "350", i915 + "gt_RP0_freq_mhz": "1100", i915 + "gt_act_freq_mhz": "0"},
			&report.GPUClocks{MinFreqMHz: 350, MaxFreqMHz: 1100, ActualFreqMHz: mhz(0), Source: "i915"}, ""},
		{"i915 without an actual clock", map[string]string{i915 + "gt_RPn_freq_mhz": "350", i915 + "gt_RP0_freq_mhz": "1100"},
			&report.GPUClocks{MinFreqMHz: 350, MaxFreqMHz: 1100, Source: "i915"}, ""},
		{"i915 locked", map[string]string{i915 + "gt_RPn_freq_mhz": "1000", i915 + "gt_RP0_freq_mhz": "1000"},
			&report.GPUClocks{MinFreqMHz: 1000, MaxFreqMHz: 1000, Source: "i915"}, ""},
		{"i915 actual below the range", map[string]string{i915 + "gt_RPn_freq_mhz": "350", i915 + "gt_RP0_freq_mhz": "1100", i915 + "gt_act_freq_mhz": "300"},
			&report.GPUClocks{MinFreqMHz: 350, MaxFreqMHz: 1100, Source: "i915"}, "actual clock 300 MHz is outside 350–1100 MHz"},
		{"i915 actual negative", map[string]string{i915 + "gt_RPn_freq_mhz": "350", i915 + "gt_RP0_freq_mhz": "1100", i915 + "gt_act_freq_mhz": "-1"},
			&report.GPUClocks{MinFreqMHz: 350, MaxFreqMHz: 1100, Source: "i915"}, "actual clock -1 MHz is outside 350–1100 MHz"},
		{"i915 actual above the range", map[string]string{i915 + "gt_RPn_freq_mhz": "350", i915 + "gt_RP0_freq_mhz": "1100", i915 + "gt_act_freq_mhz": "1200"},
			&report.GPUClocks{MinFreqMHz: 350, MaxFreqMHz: 1100, Source: "i915"}, "actual clock 1200 MHz is outside 350–1100 MHz"},
		{"i915 actual unreadable", map[string]string{i915 + "gt_RPn_freq_mhz": "350", i915 + "gt_RP0_freq_mhz": "1100", i915 + "gt_act_freq_mhz": "fast"},
			&report.GPUClocks{MinFreqMHz: 350, MaxFreqMHz: 1100, Source: "i915"}, "actual clock: gt_act_freq_mhz: strconv.Atoi"},
		{"i915 maximum only", map[string]string{i915 + "gt_RP0_freq_mhz": "1100"}, nil, "has gt_RP0_freq_mhz but no gt_RPn_freq_mhz"},
		{"i915 minimum only", map[string]string{i915 + "gt_RPn_freq_mhz": "350"}, nil, "has gt_RPn_freq_mhz but no gt_RP0_freq_mhz"},
		{"i915 inverted", map[string]string{i915 + "gt_RPn_freq_mhz": "1100", i915 + "gt_RP0_freq_mhz": "350"}, nil, "1100–350 MHz, not a range"},
		{"i915 zero", map[string]string{i915 + "gt_RPn_freq_mhz": "0", i915 + "gt_RP0_freq_mhz": "1100"}, nil, "0–1100 MHz, not a range"},
		{"i915 unreadable", map[string]string{i915 + "gt_RPn_freq_mhz": "350", i915 + "gt_RP0_freq_mhz/x": ""}, // a directory: EISDIR
			nil, "gt_RP0_freq_mhz: is a directory"},
		{"i915 garbage", map[string]string{i915 + "gt_RPn_freq_mhz": "low", i915 + "gt_RP0_freq_mhz": "1100"}, nil, "gt_RPn_freq_mhz: strconv.Atoi"},
		{"amdgpu", map[string]string{amd: "0: 300Mhz\n1: 1000Mhz *\n2: 2100Mhz\n"},
			&report.GPUClocks{MinFreqMHz: 300, MaxFreqMHz: 2100, ActualFreqMHz: mhz(1000), Source: "amdgpu"}, ""},
		{"amdgpu deep sleep", map[string]string{amd: "S: 19Mhz *\n0: 500Mhz\n1: 2100Mhz\n"}, // smu_cmn.c's real format
			&report.GPUClocks{MinFreqMHz: 500, MaxFreqMHz: 2100, ActualFreqMHz: mhz(19), Source: "amdgpu"}, ""},
		{"amdgpu, none active, any case", map[string]string{amd: "0: 500MHZ\n1: 2100mhz\n"},
			&report.GPUClocks{MinFreqMHz: 500, MaxFreqMHz: 2100, Source: "amdgpu"}, ""},
		{"amdgpu out of order", map[string]string{amd: "0: 800Mhz\n1: 300Mhz *\n"},
			&report.GPUClocks{MinFreqMHz: 300, MaxFreqMHz: 800, ActualFreqMHz: mhz(300), Source: "amdgpu"}, ""},
		{"amdgpu locked", map[string]string{amd: "0: 1000Mhz *\n"},
			&report.GPUClocks{MinFreqMHz: 1000, MaxFreqMHz: 1000, ActualFreqMHz: mhz(1000), Source: "amdgpu"}, ""},
		{"amdgpu half-parsed", map[string]string{amd: "0: 300Mhz\n1: 1000Mhz *\n2: garbage\n"}, nil, `unexpected pp_dpm_sclk line "2: garbage"`},
		{"amdgpu trailing text", map[string]string{amd: "0: 300Mhz\n1: 2100Mhz (boost)\n"}, nil, `unexpected pp_dpm_sclk line "1: 2100Mhz (boost)"`},
		{"amdgpu leading text", map[string]string{amd: "x0: 300Mhz\n1: 2100Mhz\n"}, nil, `unexpected pp_dpm_sclk line "x0: 300Mhz"`},
		{"amdgpu overflow", map[string]string{amd: "0: 300Mhz\n1: 99999999999999999999Mhz\n"}, nil, "value out of range"},
		{"amdgpu two active", map[string]string{amd: "0: 300Mhz *\n1: 1000Mhz *\n"}, nil, "marks two levels active"},
		{"amdgpu zero level", map[string]string{amd: "0: 0Mhz\n1: 1000Mhz\n"}, nil, "no usable levels"},
		{"amdgpu deep sleep above the range", map[string]string{amd: "S: 3000Mhz *\n0: 500Mhz\n1: 2100Mhz\n"},
			&report.GPUClocks{MinFreqMHz: 500, MaxFreqMHz: 2100, Source: "amdgpu"}, "active level 3000 MHz is above 2100 MHz"},
		{"amdgpu empty", map[string]string{amd: "\n"}, nil, "no usable levels"},
		{"amdgpu unreadable", map[string]string{amd + "/x": ""}, nil, "pp_dpm_sclk: is a directory"},
		{"amdgpu sleep only", map[string]string{amd: "S: 19Mhz *\n"}, nil, "no usable levels"},
		{"neither", map[string]string{}, nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			file, _ := fakeRoot(t)
			for p, v := range c.files {
				file(p, v)
			}
			col := &collector{r: &report.Report{}}
			got := col.gpuClocks("card0", "0000:03:00.0")
			if !reflect.DeepEqual(got, c.want) {
				g, _ := json.Marshal(got)
				w, _ := json.Marshal(c.want)
				t.Errorf("clocks %s, want %s", g, w)
			}
			w := strings.Join(col.r.Warnings, "\n")
			if c.warn == "" && w != "" || c.warn != "" && !strings.Contains(w, c.warn) {
				t.Errorf("warnings %q, want %q", w, c.warn)
			}
		})
	}
}

// EPERM (amdgpu while the GPU is runtime-suspended) is the driver's
// refusal, not a missing privilege: a warning that says so, and no
// suggestion to run with --full.
func TestGPUClocksPermissionErrorDoesNotAskForRoot(t *testing.T) {
	file, _ := fakeRoot(t)
	const amd = "/sys/bus/pci/devices/0000:03:00.0/pp_dpm_sclk"
	file(amd, "0: 300Mhz *\n")
	unreadable = map[string]error{amd: syscall.EPERM}
	col := &collector{r: &report.Report{}}
	if got := col.gpuClocks("card0", "0000:03:00.0"); got != nil {
		t.Errorf("clocks %+v, want none", got)
	}
	w := strings.Join(col.r.Warnings, "\n")
	if !strings.Contains(w, "pp_dpm_sclk: operation not permitted") || strings.Contains(w, "--full") {
		t.Errorf("warnings %q", w)
	}
}

// Through gpus(): a GPU without clock files gets no clocks block and no
// warning; with an i915 and an amdgpu card, each with a render node, each
// GPU gets its own clocks, every time.
func TestEachGPUGetsItsOwnClocks(t *testing.T) {
	for range 20 {
		file, link := fakeRoot(t)
		col := &collector{r: &report.Report{PCI: []report.PCIDevice{
			{Address: "0000:00:02.0", ClassCode: "030000"},
			{Address: "0000:03:00.0", ClassCode: "030000"},
			{Address: "0000:04:00.0", ClassCode: "030200"}, // NVIDIA-like: no clock files
		}}}
		for _, card := range []struct{ name, addr string }{{"card0", "0000:00:02.0"}, {"renderD128", "0000:00:02.0"}, {"card1", "0000:03:00.0"},
			{"renderD129", "0000:03:00.0"}, {"card2", "0000:04:00.0"}} {
			file("/sys/devices/pci0000:00/"+card.addr+"/drm/"+card.name+"/uevent", "")
			link("/sys/class/drm/"+card.name, "../../devices/pci0000:00/"+card.addr+"/drm/"+card.name)
			link("/sys/devices/pci0000:00/"+card.addr+"/drm/"+card.name+"/device", "../..")
		}
		file("/sys/bus/pci/.keep", "")
		for _, addr := range []string{"0000:00:02.0", "0000:03:00.0", "0000:04:00.0"} {
			link("/sys/devices/pci0000:00/"+addr+"/subsystem", "../../../bus/pci")
		}
		file("/sys/class/drm/card0/gt_RPn_freq_mhz", "350")
		file("/sys/class/drm/card0/gt_RP0_freq_mhz", "1100")
		file("/sys/bus/pci/devices/0000:03:00.0/pp_dpm_sclk", "0: 300Mhz\n1: 2100Mhz\n")
		col.gpus()
		got := map[string]*report.GPUClocks{}
		for _, g := range col.r.GPUs {
			got[g.PCIAddress] = g.Clocks
		}
		if c := got["0000:00:02.0"]; c == nil || c.Source != "i915" || c.MaxFreqMHz != 1100 {
			t.Fatalf("i915: %+v", c)
		}
		if c := got["0000:03:00.0"]; c == nil || c.Source != "amdgpu" || c.MaxFreqMHz != 2100 || c.ActualFreqMHz != nil {
			t.Fatalf("amdgpu: %+v", c)
		}
		if got["0000:04:00.0"] != nil || len(col.r.Warnings) != 0 {
			t.Fatalf("no clock files: %+v, warnings %q", got["0000:04:00.0"], col.r.Warnings)
		}
		js, _ := json.Marshal(col.r.GPUs[2])
		if strings.Contains(string(js), "clocks") {
			t.Fatalf("a GPU without clock files has %s", js)
		}
		js, _ = json.Marshal(col.r.GPUs[1])
		if strings.Contains(string(js), "actual_freq_mhz") {
			t.Fatalf("an unstarred amdgpu has %s", js)
		}
	}
}

// Two cards on one device: the first in sorted order is the GPU's card
// and gives its clocks, every time (map order would pick either). A
// display GPU with only a render node gets no card and no clocks.
func TestTheFirstCardOfADeviceGivesItsClocks(t *testing.T) {
	for range 20 {
		file, link := fakeRoot(t)
		col := &collector{r: &report.Report{PCI: []report.PCIDevice{
			{Address: "0000:00:02.0", ClassCode: "030000"},
			{Address: "0000:05:00.0", ClassCode: "030000"},
		}}}
		for _, card := range []struct{ name, addr string }{{"card0", "0000:00:02.0"}, {"card1", "0000:00:02.0"}, {"renderD130", "0000:05:00.0"}} {
			file("/sys/devices/pci0000:00/"+card.addr+"/drm/"+card.name+"/uevent", "")
			link("/sys/class/drm/"+card.name, "../../devices/pci0000:00/"+card.addr+"/drm/"+card.name)
			link("/sys/devices/pci0000:00/"+card.addr+"/drm/"+card.name+"/device", "../..")
		}
		file("/sys/bus/pci/.keep", "")
		for _, addr := range []string{"0000:00:02.0", "0000:05:00.0"} {
			link("/sys/devices/pci0000:00/"+addr+"/subsystem", "../../../bus/pci")
		}
		file("/sys/class/drm/card0/gt_RPn_freq_mhz", "350")
		file("/sys/class/drm/card0/gt_RP0_freq_mhz", "1100")
		col.gpus()
		if g := col.r.GPUs[0]; g.DRMCard != "card0" || g.Clocks == nil || g.Clocks.MaxFreqMHz != 1100 {
			t.Fatalf("two cards: card %q, clocks %+v", g.DRMCard, g.Clocks)
		}
		if g := col.r.GPUs[1]; g.DRMCard != "" || g.Clocks != nil {
			t.Fatalf("render node only: card %q, clocks %+v", g.DRMCard, g.Clocks)
		}
	}
}

// A device behind a switch behind a root port names its own bridge, not
// the root port; behind Intel VMD (a 5-digit domain on a bus the VMD
// controller hosts) the chain reaches the controller. Root-bus devices
// have no parent and no warning; an unresolvable path has no parent and
// a warning.
func TestPCIDevicesNameTheBridgeTheySitBehind(t *testing.T) {
	file, link := fakeRoot(t)
	for _, d := range []struct{ addr, path string }{
		{"0000:00:1c.0", "pci0000:00/0000:00:1c.0"},
		{"0000:03:00.0", "pci0000:00/0000:00:1c.0/0000:03:00.0"},
		{"0000:04:01.0", "pci0000:00/0000:00:1c.0/0000:03:00.0/0000:04:01.0"},
		{"0000:05:00.0", "pci0000:00/0000:00:1c.0/0000:03:00.0/0000:04:01.0/0000:05:00.0"},
		{"0000:00:0e.0", "pci0000:00/0000:00:0e.0"},
		{"10000:e0:06.0", "pci0000:00/0000:00:0e.0/pci10000:e0/10000:e0:06.0"},
		{"10000:e1:00.0", "pci0000:00/0000:00:0e.0/pci10000:e0/10000:e0:06.0/10000:e1:00.0"},
		{"0000:07:00.0", "platform/pcie-controller/0000:07:00.0"}, // a parent named like, but not, a bus
	} {
		file("/sys/devices/"+d.path+"/vendor", "0x8086")
		link("/sys/bus/pci/devices/"+d.addr, "../../../devices/"+d.path)
	}
	file("/sys/bus/pci/devices/0000:06:00.0/vendor", "0x8086")                                 // a plain directory, not a link into /sys/devices
	link("/sys/bus/pci/devices/0000:08:00.0", "../../../devices/pci0000:00/gone/0000:08:00.0") // dangling, as in a partial recording
	col := &collector{r: &report.Report{}}
	col.pci()
	got := map[string]string{}
	for _, d := range col.r.PCI {
		got[d.Address] = d.Parent
	}
	want := map[string]string{"0000:00:1c.0": "", "0000:03:00.0": "0000:00:1c.0", "0000:04:01.0": "0000:03:00.0",
		"0000:05:00.0": "0000:04:01.0", "0000:06:00.0": "", "0000:00:0e.0": "",
		"10000:e0:06.0": "0000:00:0e.0", "10000:e1:00.0": "10000:e0:06.0", "0000:07:00.0": "", "0000:08:00.0": ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parents %v, want %v", got, want)
	}
	if w := col.r.Warnings; !reflect.DeepEqual(w, []string{
		"pci 0000:06:00.0: parent unknown: its sysfs path isn't under a PCI bus",
		"pci 0000:07:00.0: parent unknown: its sysfs path isn't under a PCI bus",
		"pci 0000:08:00.0: parent unknown: its sysfs path isn't under a PCI bus",
	}) {
		t.Errorf("warnings %q, want one each for 0000:06:00.0, 0000:07:00.0 and 0000:08:00.0", w)
	}
}

// meDevice adds a mei device to a fake root: its kind ("" for a kernel
// before 5.8, which has none), its parent's PCI class and vendor, and
// its fw_ver.
func meDevice(file func(path, content string), link func(path, target string), name, addr, kind, class, fwVer string) {
	dir := "/sys/devices/pci0000:00/" + addr
	file(dir+"/class", class)
	file(dir+"/vendor", "0x8086")
	file(dir+"/mei/"+name+"/fw_ver", fwVer)
	if kind != "" {
		file(dir+"/mei/"+name+"/kind", kind)
	}
	link(dir+"/mei/"+name+"/device", "../..")
	link("/sys/class/mei/"+name, "../../devices/pci0000:00/"+addr+"/mei/"+name)
}

// The platform ME's first fw_ver block (its running code) is recorded;
// other mei devices, all-zero blocks and kernels without kind are
// handled; a machine without mei, an EC release or a TPM gets none of
// them and no warning; a read error or an unexpected format is a warning.
func TestPlatformFirmware(t *testing.T) {
	const three = "0:16.1.30.2307\n0:16.1.30.2307\n0:16.0.15.1810\n"
	for _, c := range []struct {
		name  string
		setup func(file func(string, string), link func(string, string))
		want  string
		warn  string
	}{
		{"the code block", func(f func(string, string), l func(string, string)) {
			meDevice(f, l, "mei0", "0000:00:16.0", "mei", "0x078000", three)
		}, "16.1.30.2307", ""},
		{"a GPU's GSC only", func(f func(string, string), l func(string, string)) {
			meDevice(f, l, "mei0", "0000:03:00.0", "gsc", "0x030000", "0:1.2.3.4\n")
		}, "", ""},
		{"IVSC first, the CSME second", func(f func(string, string), l func(string, string)) {
			meDevice(f, l, "mei0", "0000:00:05.0", "ivsc", "0x048000", "0:1.0.0.1\n")
			meDevice(f, l, "mei1", "0000:00:16.0", "mei", "0x078000", three)
		}, "16.1.30.2307", ""},
		{"no version yet, then one", func(f func(string, string), l func(string, string)) {
			meDevice(f, l, "mei0", "0000:00:16.0", "mei", "0x078000", "0:0.0.0.0\n0:0.0.0.0\n0:0.0.0.0\n")
			meDevice(f, l, "mei1", "0000:00:16.4", "mei", "0x078000", three)
		}, "16.1.30.2307", ""},
		{"before 5.8: an Intel communication controller", func(f func(string, string), l func(string, string)) {
			meDevice(f, l, "mei0", "0000:00:16.0", "", "0x078000", three)
		}, "16.1.30.2307", ""},
		{"before 5.8: another parent", func(f func(string, string), l func(string, string)) {
			meDevice(f, l, "mei0", "0000:03:00.0", "", "0x030000", three)
		}, "", ""},
		{"before 5.8: another vendor's communication controller", func(f func(string, string), l func(string, string)) {
			meDevice(f, l, "mei0", "0000:00:16.0", "", "0x078000", three)
			f("/sys/devices/pci0000:00/0000:00:16.0/vendor", "0x1022")
		}, "", ""},
		{"trailing text", func(f func(string, string), l func(string, string)) {
			meDevice(f, l, "mei0", "0000:00:16.0", "mei", "0x078000", "0:12.0.45.1509x\n")
		}, "", `unexpected fw_ver "0:12.0.45.1509x"`},
		{"no platform prefix", func(f func(string, string), l func(string, string)) {
			meDevice(f, l, "mei0", "0000:00:16.0", "mei", "0x078000", "x:12.0.45.1509\n")
		}, "", `unexpected fw_ver "x:12.0.45.1509"`},
		{"garbage", func(f func(string, string), l func(string, string)) {
			meDevice(f, l, "mei0", "0000:00:16.0", "mei", "0x078000", "0:12.0.45\n")
		}, "", `me firmware: unexpected fw_ver "0:12.0.45"`},
		{"unreadable", func(f func(string, string), l func(string, string)) {
			meDevice(f, l, "mei0", "0000:00:16.0", "mei", "0x078000", three)
			unreadable = map[string]error{"/sys/class/mei/mei0/fw_ver": syscall.EIO}
		}, "", "me firmware: open"},
		{"unreadable first, the CSME second", func(f func(string, string), l func(string, string)) {
			meDevice(f, l, "mei0", "0000:00:16.0", "mei", "0x078000", three)
			meDevice(f, l, "mei1", "0000:00:16.4", "mei", "0x078000", "0:15.0.10.1000\n")
			unreadable = map[string]error{"/sys/class/mei/mei0/fw_ver": syscall.EIO}
		}, "15.0.10.1000", "me firmware: open"},
	} {
		t.Run(c.name, func(t *testing.T) {
			file, link := fakeRoot(t)
			c.setup(file, link)
			col := &collector{r: &report.Report{}}
			col.platformFirmware()
			got := ""
			if fw := col.r.System.MEFirmware; fw != nil {
				if fw.Vendor != "Intel" || fw.Source != "mei" {
					t.Errorf("me %+v", fw)
				}
				got = fw.Version
			}
			w := strings.Join(col.r.Warnings, "\n")
			if got != c.want || c.warn == "" && w != "" || c.warn != "" && !strings.Contains(w, c.warn) {
				t.Errorf("version %q, warnings %q; want %q, %q", got, w, c.want, c.warn)
			}
		})
	}
}

// The TPM's spec version, and what's wrong when it can't be read.
func TestTPMSpecVersion(t *testing.T) {
	for _, c := range []struct {
		content string
		want    int
		warn    string
	}{{"2\n", 2, ""}, {"1", 1, ""}, {"two", 0, `tpm: unexpected tpm_version_major "two"`},
		{"0", 0, `unexpected tpm_version_major "0"`}, {"/x", 0, "tpm_version_major: is a directory"}, {"", -1, ""},
		{"/x then 2", 2, "tpm_version_major: is a directory"}} {
		file, _ := fakeRoot(t)
		switch {
		case c.content == "/x":
			file("/sys/class/tpm/tpm0/tpm_version_major/x", "")
		case c.content == "/x then 2":
			file("/sys/class/tpm/tpm0/tpm_version_major/x", "")
			file("/sys/class/tpm/tpm1/tpm_version_major", "2")
		case c.want >= 0:
			file("/sys/class/tpm/tpm0/tpm_version_major", c.content)
		}
		col := &collector{r: &report.Report{}}
		col.platformFirmware()
		got := 0
		if col.r.TPM != nil {
			got = col.r.TPM.SpecVersionMajor
		}
		w := strings.Join(col.r.Warnings, "\n")
		if c.want < 0 && (col.r.TPM != nil || w != "") || c.want >= 0 && got != c.want || c.warn != "" && !strings.Contains(w, c.warn) || c.warn == "" && w != "" {
			t.Errorf("%q: tpm %+v, warnings %q", c.content, col.r.TPM, w)
		}
	}
}

// The EC's release comes from DMI as the kernel prints it ("%u.%u"):
// 0.0 is kept as written, anything else is a warning.
func TestECFirmware(t *testing.T) {
	for _, c := range []struct{ content, want, warn string }{
		{"8.9\n", "8.9", ""}, {"0.0", "0.0", ""}, {"", "", ""}, {"8.9.1", "", `dmi ec_firmware_release: unexpected "8.9.1"`},
	} {
		file, _ := fakeRoot(t)
		file(dmiDir+"sys_vendor", "HP\n")
		if c.content != "" {
			file(dmiDir+"ec_firmware_release", c.content)
		}
		col := &collector{r: &report.Report{}}
		col.dmi()
		got := ""
		if fw := col.r.System.ECFirmware; fw != nil {
			if fw.Source != "dmi" {
				t.Errorf("%+v", fw)
			}
			got = fw.Version
		}
		w := strings.Join(col.r.Warnings, "\n")
		if got != c.want || c.warn == "" && w != "" || c.warn != "" && !strings.Contains(w, c.warn) {
			t.Errorf("%q: ec %q, warnings %q", c.content, got, w)
		}
	}
}

// A machine without ME, EC or TPM writes none of the keys.
func TestAbsentPlatformFirmwareIsLeftOut(t *testing.T) {
	js, _ := json.Marshal(&report.Report{})
	for _, key := range []string{"me_firmware", "ec_firmware", `"tpm"`} {
		if strings.Contains(string(js), key) {
			t.Errorf("%s in %s", key, js)
		}
	}
}

// SPD EEPROMs answer over SMBus at about 0.16 ms a byte: only the bytes the
// parser uses are read, and the result decodes the same as the whole image.
func TestSPDReadsOnlyTheBytesTheParserUses(t *testing.T) {
	file, _ := fakeRoot(t)
	full := make([]byte, 512)
	for i := range full {
		full[i] = 0xAA // bytes the parser never looks at
	}
	full[2], full[3], full[4], full[6], full[12], full[13] = 0x0C, 0x03, 0x86, 0x00, 0x01, 0x03
	full[320], full[321], full[323], full[324] = 0x85, 0xF7, 0x21, 0x10
	copy(full[325:329], []byte{0x00, 0x77, 0x27, 0x03})
	copy(full[329:349], "J642GU44J2320NL     ")
	full[349], full[350], full[351] = 0x00, 0x00, 0x00
	file("/sys/bus/i2c/drivers/ee1004/7-0050/eeprom", string(full))

	got, err := readSPD("/sys/bus/i2c/drivers/ee1004/7-0050/eeprom")
	if err != nil {
		t.Fatal(err)
	}
	_, spans, _ := spd.Layout(0x0C)
	want := make([]byte, 512)
	for _, s := range spans {
		copy(want[s.Off:s.Off+s.Len], full[s.Off:s.Off+s.Len])
	}
	if string(got) != string(want) {
		t.Fatal("readSPD read bytes outside the parser's spans, or missed some")
	}
	a, _ := spd.Parse(full)
	b, _ := spd.Parse(got)
	if a == nil || b == nil || *a != *b {
		t.Errorf("partial image decodes as %+v, whole image as %+v", b, a)
	}

	// A short image is still reported as short, an unknown type as unknown.
	for name, content := range map[string]string{
		"7-0051": string(full[:400]),
		"7-0052": "\x00\x00\x0B",
		"7-0053": "x",
	} {
		file("/sys/bus/i2c/drivers/ee1004/"+name+"/eeprom", content)
	}
	c := &collector{r: &report.Report{}}
	c.spdModules()
	for _, w := range []string{"7-0051: spd: DDR4 image shorter than 512 bytes", "7-0052: spd: memory type 0x0b not supported", "7-0053: spd: too short"} {
		if !strings.Contains(strings.Join(c.r.Warnings, "\n"), w) {
			t.Errorf("no warning %q in %q", w, c.r.Warnings)
		}
	}
}
