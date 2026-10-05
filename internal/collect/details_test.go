package collect

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiegui2025/hwspec/internal/report"
)

// fakeRoot points the collectors at an empty temporary tree and returns
// helpers to populate it.
func fakeRoot(t *testing.T) (file func(path, content string), link func(path, target string)) {
	t.Helper()
	old := root
	root = t.TempDir()
	t.Cleanup(func() { root = old })
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
	// An out-of-tree, proprietary, unsigned module (e.g. a vendor GPU driver).
	link("/sys/devices/pci0000:00/0000:01:00.0/driver", "../../../bus/pci/drivers/nvidia")
	link("/sys/bus/pci/drivers/nvidia/module", "../../../../module/nvidia")
	file("/sys/module/nvidia/version", "580.95.05\n")
	file("/sys/module/nvidia/srcversion", "ABCDEF\n")
	file("/sys/module/nvidia/taint", "POE\n")
	// A clean in-tree module.
	link("/sys/devices/pci0000:00/0000:00:1f.6/driver", "../../../bus/pci/drivers/e1000e")
	link("/sys/bus/pci/drivers/e1000e/module", "../../../../module/e1000e")
	file("/sys/module/e1000e/taint", "\n")
	// A built-in driver.
	link("/sys/devices/pci0000:00/0000:00:14.0/driver", "../../../bus/pci/drivers/xhci_hcd")
	file("/sys/bus/pci/drivers/xhci_hcd/bind", "")

	nv := driverAt("/sys/devices/pci0000:00/0000:01:00.0")
	if nv == nil || nv.Name != "nvidia" || nv.Module != "nvidia" || nv.Version != "580.95.05" || *nv.InTree || !*nv.Proprietary || !*nv.Unsigned {
		t.Errorf("nvidia driver = %+v", nv)
	}
	e := driverAt("/sys/devices/pci0000:00/0000:00:1f.6")
	if e == nil || !*e.InTree || *e.Proprietary || *e.Unsigned || e.Builtin {
		t.Errorf("e1000e driver = %+v", e)
	}
	if x := driverAt("/sys/devices/pci0000:00/0000:00:14.0"); x == nil || !x.Builtin || x.Module != "" {
		t.Errorf("built-in driver = %+v", x)
	}
	if d := driverAt("/sys/devices/pci0000:00/0000:00:00.0"); d != nil {
		t.Errorf("unbound device has driver %+v", d)
	}
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
	if d := controllerDriver("/sys/block/nvme0n1/device"); d == nil || d.Name != "nvme" {
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
	// 90% of design capacity after 200 cycles: 0.05% per cycle, so 200
	// more cycles until 80%.
	d := "/sys/class/power_supply/BAT0/"
	file(d+"energy_full_design", "50000000")
	file(d+"energy_full", "45000000")
	file(d+"cycle_count", "200")
	h := batteryHealth(d)
	if h.Status != report.StatusOK || *h.LifeUsedPercent != 10 || *h.LifeRemainingPercent != 50 {
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

func TestThrottlingIsCountedOncePerPackage(t *testing.T) {
	file, _ := fakeRoot(t)
	for _, cpu := range []string{"cpu0", "cpu1", "cpu2", "cpu3"} {
		pkg := "0"
		if cpu >= "cpu2" {
			pkg = "1"
		}
		d := cpuDir + cpu + "/"
		file(d+"topology/physical_package_id", pkg)
		file(d+"thermal_throttle/core_throttle_count", "1")
		file(d+"thermal_throttle/package_throttle_count", "10")
	}
	// 4 cores × 1 + 2 packages × 10
	if h := cpuHealth(); h == nil || h.Metrics[report.MetricThrottleEvents] != 24 || h.Status != report.StatusWarning {
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
