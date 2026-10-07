package collect

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jiegui2025/hwspec/internal/report"
	"github.com/jiegui2025/hwspec/internal/smbios/smbiostest"
	"github.com/jiegui2025/hwspec/internal/spd"
)

// Machines unlike the recorded ones, built file by file: what a capture
// must make of an ARM board, a VM, a laptop, a server captured as root.

// asMachine makes the calls that aren't file reads answer like a machine
// with the given architecture and effective user: no Bluetooth, no
// ethtool, no NVMe, no smartctl. Every hook is restored afterwards.
func asMachine(t *testing.T, arch string, euid int) {
	t.Helper()
	t.Cleanup(saveHooks())
	uname = func() (string, string) { return "6.12.0-test", arch }
	geteuid = func() int { return euid }
	hostname = func() (string, error) { return "scenario", nil }
	readBTInfo = func(uint16) (*mgmtInfo, error) { return nil, errors.New("no controller") }
	readBTVersion = func(uint16) (*hciVersion, error) { return nil, errors.New("no controller") }
	ethtoolDrvinfo = func(string) (string, error) { return "", syscall.EOPNOTSUPP }
	nvmeHealthFn = func(string) (*report.Health, error) { return nil, errors.New("no NVMe") }
	findSmartctl = func() string { return "" }
	runCommand = func(time.Duration, string, ...string) ([]byte, error) { return nil, errors.New("no commands") }
}

func hasWarning(r *report.Report, part string) bool {
	for _, w := range r.Warnings {
		if strings.Contains(w, part) {
			return true
		}
	}
	return false
}

// A Raspberry Pi running hwspec in a container: no SMBIOS, the board names
// itself in the device tree, os-release only under /usr/lib.
func TestARMBoardInAContainer(t *testing.T) {
	file, _ := fakeRoot(t)
	asMachine(t, "aarch64", 1000)
	file("/usr/lib/os-release", "NAME=\"Debian GNU/Linux\"\nID=debian\nVERSION_ID=\"12\"\nPRETTY_NAME='Debian 12'\n# comment\n")
	file("/proc/cpuinfo", "processor\t: 0\nBogoMIPS\t: 108.00\nFeatures\t: fp asimd\n\nHardware\t: BCM2835\nRevision\t: d03114\nSerial\t\t: 10000000abcdef01\nModel\t\t: Raspberry Pi 4 Model B Rev 1.4\n")
	file("/proc/device-tree/model", "Raspberry Pi 4 Model B Rev 1.4\x00")
	file("/run/.containerenv", "")
	file("/proc/sys/kernel/osrelease", "6.6.51+rpt-rpi-v8")

	r := Collect("test")
	checkFirmwareComplete(t, r)
	if r.OS.PrettyName != "Debian 12" || r.OS.Arch != "aarch64" || r.OS.Kernel != "6.6.51+rpt-rpi-v8" {
		t.Errorf("os = %+v", r.OS)
	}
	if r.OS.Virtualization != "container" || r.OS.BootMode != "" {
		t.Errorf("virtualization %q, boot mode %q", r.OS.Virtualization, r.OS.BootMode)
	}
	if r.System.Identity == nil || r.System.Identity.Model != "Raspberry Pi 4 Model B Rev 1.4" {
		t.Errorf("system = %+v", r.System.Identity)
	}
	// The board model isn't the CPU: the SoC name is.
	if r.CPU.Identity == nil || r.CPU.Identity.Model != "BCM2835" {
		t.Errorf("cpu = %+v", r.CPU.Identity)
	}
	if r.Privileged || r.Hostname != "scenario" {
		t.Errorf("privileged %v, host %q", r.Privileged, r.Hostname)
	}
}

// An x86 VM booted with legacy BIOS, seen as root: the hypervisor flag and
// the SMBIOS vendor both say "vm".
func TestLegacyBIOSVirtualMachine(t *testing.T) {
	file, _ := fakeRoot(t)
	asMachine(t, "x86_64", 0)
	file("/etc/os-release", "PRETTY_NAME=\"Fedora Linux 41\"\n")
	file("/proc/cpuinfo", "processor\t: 0\nvendor_id\t: GenuineIntel\nmodel name\t: QEMU Virtual CPU version 2.5+\nflags\t\t: fpu sse2 hypervisor\nmicrocode\t: 0x1\n")
	file("/sys/firmware/dmi/tables/smbios_entry_point", "")
	for name, v := range map[string]string{
		"sys_vendor": "QEMU", "product_name": "Standard PC (i440FX + PIIX, 1996)", "product_version": "pc-i440fx-9.0",
		"bios_vendor": "SeaBIOS", "bios_version": "1.16.3", "bios_date": "04/01/2014", "chassis_type": "1",
		"board_vendor": "Not Specified", "product_serial": "Not Specified",
	} {
		file(dmiDir+name, v+"\n")
	}
	r := Collect("test")
	checkFirmwareComplete(t, r)
	if r.OS.Virtualization != "vm" || r.OS.BootMode != "bios" || !r.Privileged {
		t.Errorf("os = %+v, privileged %v", r.OS, r.Privileged)
	}
	if fw := r.System.Firmware; fw == nil || fw.Vendor != "SeaBIOS" || fw.Version != "1.16.3" || fw.Source != "dmi" {
		t.Errorf("firmware = %+v", fw)
	}
	if r.System.ChassisType != "Other" || r.Board.Identity != nil || r.System.Identity.Serial != "" {
		t.Errorf("placeholders kept: chassis %q, board %+v, serial %q", r.System.ChassisType, r.Board.Identity, r.System.Identity.Serial)
	}
	// The hypervisor's microcode revision is reported as it is.
	if fw := r.CPU.Firmware; fw == nil || fw.Version != "0x1" || fw.Source != "microcode" {
		t.Errorf("microcode = %+v", fw)
	}
}

// An ASUS desktop whose firmware left the DMI system strings at their
// defaults: they're unknown, not names, while the board keeps its own.
func TestASUSFirmwareDefaultsAreUnknown(t *testing.T) {
	file, _ := fakeRoot(t)
	asMachine(t, "x86_64", 0)
	for name, v := range map[string]string{
		"sys_vendor": "System manufacturer", "product_name": "System Product Name",
		"product_version": "System Version", "product_serial": "System Serial Number", "product_sku": "SKU",
		"board_vendor": "ASUSTeK COMPUTER INC.", "board_name": "ROG STRIX X570-E GAMING WIFI II",
	} {
		file(dmiDir+name, v+"\n")
	}
	r := Collect("test")
	checkFirmwareComplete(t, r)
	if r.System.Identity != nil {
		t.Errorf("system identity = %+v, want none", r.System.Identity)
	}
	if b := r.Board.Identity; b == nil || b.Vendor != "ASUSTeK COMPUTER INC." || b.Model != "ROG STRIX X570-E GAMING WIFI II" {
		t.Errorf("board identity = %+v", b)
	}
	js, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"System manufacturer", "System Product Name", "System Version", "System Serial Number", `"SKU"`} {
		if strings.Contains(string(js), s) {
			t.Errorf("capture contains %s", s)
		}
	}

	// Newer ASUS firmware names the vendor but keeps the default model.
	file(dmiDir+"sys_vendor", "ASUS\n")
	if id := Collect("test").System.Identity; id == nil || id.Vendor != "ASUS" || id.Model != "" {
		t.Errorf("system identity = %+v, want vendor ASUS only", id)
	}
}

// A UEFI machine whose DMI serials are root-only, with Secure Boot on.
func TestUEFIWithSecureBootAndRootOnlySerials(t *testing.T) {
	file, _ := fakeRoot(t)
	asMachine(t, "x86_64", 1000)
	file("/proc/cpuinfo", "flags\t\t: fpu sse2\n")
	file("/sys/firmware/efi/efivars/SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b8c", "\x06\x00\x00\x00\x01")
	file(dmiDir+"sys_vendor", "LENOVO\n")
	file(dmiDir+"product_serial", "PF123456\n")
	unreadable = map[string]error{dmiDir + "product_serial": syscall.EACCES}
	r := Collect("test")
	checkFirmwareComplete(t, r)
	if r.OS.BootMode != "uefi" || r.OS.SecureBoot == nil || !*r.OS.SecureBoot || r.OS.Virtualization != "none" {
		t.Errorf("os = %+v", r.OS)
	}
	if !hasWarning(r, "dmi product_serial: needs root") || r.System.Identity.Serial != "" {
		t.Errorf("warnings = %v, serial %q", r.Warnings, r.System.Identity.Serial)
	}
}

// A system with almost nothing readable still produces a capture, and says
// what's missing.
func TestBareSystemReportsWhatIsMissing(t *testing.T) {
	fakeRoot(t)
	asMachine(t, "riscv64", 1000)
	r := Collect("test")
	checkFirmwareComplete(t, r)
	for _, want := range []string{"os-release: not found", "dmi: /sys/class/dmi/id not present", "cpu:", "pci: no devices"} {
		if !hasWarning(r, want) {
			t.Errorf("no warning %q in %v", want, r.Warnings)
		}
	}
	if r.OS.Virtualization != "" || r.OS.BootMode != "" {
		t.Errorf("claims without evidence: %+v", r.OS)
	}
}

// A workstation captured as root: SMBIOS lists the modules, their SPD
// fills in manufacture dates (matched by serial, or by a part number only
// one module has), and an SPD the list doesn't match is reported.
func TestRootCaptureListsModulesFromTheFirmware(t *testing.T) {
	file, _ := fakeRoot(t)
	var table []byte
	table = append(table, smbiostest.MemoryArray(0x1000, 128<<20, 4, 0x06)...)
	for _, m := range []smbiostest.Module{
		{Array: 0x1000, SizeMiB: 16384, Locator: "DIMM_A1", Bank: "BANK 0", Manufacturer: "Samsung", Serial: "12345678", Part: "M393A2K40DB3", SpeedMTs: 3200, FormFactor: 0x09, Type: 0x1A},
		{Array: 0x1000, SizeMiB: 16384, Locator: "DIMM_B1", Bank: "BANK 1", Manufacturer: "Samsung", Serial: "", Part: "M393A2K43EB3", SpeedMTs: 3200, FormFactor: 0x09, Type: 0x1A},
		{Array: 0x1000, Locator: "DIMM_C1"}, // empty slot
	} {
		table = append(table, smbiostest.MemoryDevice(m)...)
	}
	file("/sys/firmware/dmi/tables/DMI", string(append(table, smbiostest.End()...)))
	file("/proc/meminfo", "MemTotal:       32000000 kB\nSwapTotal:      2097148 kB\nBogus line\nHugePages_Total: x\n")
	spd := func(serial []byte, part string, week byte) string {
		b := make([]byte, 512)
		b[2], b[3], b[4], b[12], b[13] = 0x0C, 0x01, 0x85, 0x01, 0x03
		b[320], b[321], b[323], b[324] = 0x80, 0xCE, 0x22, week
		copy(b[325:329], serial)
		copy(b[329:349], part)
		return string(b)
	}
	file("/sys/bus/i2c/drivers/ee1004/0-0050/eeprom", spd([]byte{0x12, 0x34, 0x56, 0x78}, "M393A2K40DB3", 0x05))
	file("/sys/bus/i2c/drivers/ee1004/0-0051/eeprom", spd([]byte{0xAA, 0, 0, 1}, "M393A2K43EB3", 0x06))
	file("/sys/bus/i2c/drivers/ee1004/0-0052/eeprom", spd([]byte{0xBB, 0, 0, 2}, "SOMEOTHERPART", 0x07))
	file("/sys/bus/i2c/drivers/ee1004/0-0053/eeprom", "\x00\x00\x0B") // DDR3: unsupported
	file("/sys/bus/i2c/drivers/ee1004/bind", "")

	c := &collector{r: &report.Report{}, privileged: true}
	c.memory()
	m := c.r.Memory
	if m.TotalBytes != 32000000<<10 || m.SwapBytes != 2097148<<10 || m.MaxCapacityBytes != 128<<30 || m.Slots != 4 || m.ECC != "Multi-bit ECC" {
		t.Errorf("memory = %+v", m)
	}
	if len(m.Modules) != 2 || m.InstalledBytes != 32<<30 {
		t.Fatalf("modules = %+v", m.Modules)
	}
	a1, b1 := m.Modules[0], m.Modules[1]
	if a1.Identity.ManufactureDate != "2022-W05" || a1.Identity.ManufactureDateSource != "spd" || a1.Identity.Vendor != "Samsung" {
		t.Errorf("A1 (by serial) = %+v", a1.Identity)
	}
	if b1.Identity.ManufactureDate != "2022-W06" || b1.Identity.Serial != "AA000001" {
		t.Errorf("B1 (by part number) = %+v", b1.Identity)
	}
	if !hasWarning(c.r, "SPD 0-0052") || !hasWarning(c.r, "memory module SPD 0-0053") {
		t.Errorf("warnings = %v", c.r.Warnings)
	}
}

// Two identical modules: a part number shared by both can't tell them
// apart, so the SPD isn't applied to either.
func TestIdenticalModulesAreNotGuessed(t *testing.T) {
	mods := []report.MemoryModule{
		{Locator: "A", Identity: &report.Identity{PartNumber: "X1"}},
		{Locator: "B", Identity: &report.Identity{PartNumber: "x1"}},
		{Locator: "C"},
	}
	info := &spd.Info{PartNumber: "X1"}
	if i := matchModule(mods, info, map[int]bool{}); i != -1 {
		t.Errorf("matched %d", i)
	}
	if i := matchModule(mods, info, map[int]bool{0: true}); i != 1 {
		t.Errorf("with A taken, matched %d", i)
	}
}

// A SMBIOS table that can't be read (not missing: unreadable) is reported.
func TestUnreadableSMBIOSIsReported(t *testing.T) {
	_, _ = fakeRoot(t)
	if err := os.MkdirAll(filepath.Join(root, "/sys/firmware/dmi/tables/DMI"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &collector{r: &report.Report{}, privileged: true}
	c.smbiosModules()
	if !hasWarning(c.r, "SMBIOS table (") {
		t.Errorf("warnings = %v", c.r.Warnings)
	}
}

// EDAC entries that aren't DIMMs, or have no counts or label, are skipped
// or reported.
func TestEDACEntriesWithoutCountsOrLabels(t *testing.T) {
	file, _ := fakeRoot(t)
	file("/sys/devices/system/edac/mc/power/control", "auto")
	file("/sys/devices/system/edac/mc/mc0/ce_count", "0")
	file("/sys/devices/system/edac/mc/mc0/dimm0/dimm_label", "A1")
	file("/sys/devices/system/edac/mc/mc0/dimm1/dimm_ce_count", "0")
	file("/sys/devices/system/edac/mc/mc0/dimm1/dimm_ue_count", "0")
	c := &collector{r: &report.Report{}}
	c.r.Memory.Modules = []report.MemoryModule{{Locator: "A1"}}
	c.edac()
	if c.r.Memory.Modules[0].Health != nil || !hasWarning(c.r, "has no label") {
		t.Errorf("health %+v, warnings %v", c.r.Memory.Modules[0].Health, c.r.Warnings)
	}
}

// A laptop's own battery is listed; a wireless mouse's and the charger
// aren't.
func TestLaptopBatteryButNotPeripheralsOrChargers(t *testing.T) {
	file, _ := fakeRoot(t)
	b := "/sys/class/power_supply/BAT0/"
	for name, v := range map[string]string{
		"type": "Battery", "manufacturer": "SMP", "model_name": "5B10W13930", "serial_number": " 1234 ",
		"technology": "Li-poly", "status": "Discharging", "capacity": "81",
		"manufacture_year": "2021", "manufacture_month": "3", "manufacture_day": "17",
		"energy_full_design": "57000000", "energy_full": "51300000", "cycle_count": "0",
	} {
		file(b+name, v)
	}
	file("/sys/class/power_supply/hidpp_battery_0/type", "Battery")
	file("/sys/class/power_supply/hidpp_battery_0/scope", "Device")
	file("/sys/class/power_supply/AC/type", "Mains")
	file("/sys/class/power_supply/BAT1/type", "Battery") // no data at all

	c := &collector{r: &report.Report{}}
	c.batteries()
	if len(c.r.Batteries) != 2 {
		t.Fatalf("batteries = %+v", c.r.Batteries)
	}
	bat := c.r.Batteries[0]
	if bat.Name != "BAT0" || bat.CapacityPercent != 81 || bat.Technology != "Li-poly" ||
		bat.Identity.Serial != "1234" || bat.Identity.ManufactureDate != "2021-03-17" || bat.Identity.ManufactureDateSource != "battery" {
		t.Errorf("BAT0 = %+v, %+v", bat, bat.Identity)
	}
	// 90% of design: no cycle count, so no estimate.
	if h := bat.Health; h == nil || h.Status != report.StatusOK || h.Estimate != nil || h.Metrics[report.MetricCapacityPercent] != 90 {
		t.Errorf("BAT0 health = %+v", h)
	}
	if empty := c.r.Batteries[1]; empty.Identity != nil || empty.Health != nil {
		t.Errorf("BAT1 = %+v", empty)
	}
	// An unknown driver verdict is a warning, worn or not.
	file(b+"health", "Unknown-Future-State")
	if h := batteryHealth(b); h.Status != report.StatusWarning || !strings.Contains(h.Reasons[0], "unknown-future-state") {
		t.Errorf("unknown verdict = %+v", h)
	}
}

// How a disk is attached comes from where it sits in sysfs.
func TestDiskTransportFromItsSysfsPath(t *testing.T) {
	_, link := fakeRoot(t)
	for name, target := range map[string]string{
		"sda": "../devices/pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0/block/sda",
		"sdb": "../devices/pci0000:00/0000:00:14.0/usb2/2-1/2-1:1.0/host1/target1:0:0/1:0:0:0/block/sdb",
		"sdc": "../devices/platform/soc/scsi/host2/target2:0:0/2:0:0:0/block/sdc",
	} {
		link("/sys/block/"+name, target)
		if err := os.MkdirAll(filepath.Join(root, "sys/block", target), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link("/sys/devices/platform/soc/scsi/host2/target2:0:0/2:0:0:0/subsystem", "../../../../../../../bus/scsi")
	if err := os.MkdirAll(filepath.Join(root, "sys/bus/scsi"), 0o755); err != nil {
		t.Fatal(err)
	}
	link("/sys/devices/platform/soc/scsi/host2/target2:0:0/2:0:0:0/block/sdc/device", "../../../2:0:0:0")
	for name, want := range map[string]string{
		"nvme0n1": "nvme", "mmcblk0": "mmc", "vda": "virtio", "xvda": "xen",
		"sda": "sata", "sdb": "usb", "sdc": "scsi", "sdz": "unknown",
	} {
		if got := transport(name); got != want {
			t.Errorf("transport(%s) = %q, want %q", name, got, want)
		}
	}
}

// As root, drive health comes from the NVMe log page or smartctl; drives
// without SMART are skipped, and failures are reported.
func TestRootCaptureReadsDriveHealth(t *testing.T) {
	oldNVMe, oldFind, oldRun := nvmeHealthFn, findSmartctl, runCommand
	t.Cleanup(func() { nvmeHealthFn, findSmartctl, runCommand = oldNVMe, oldFind, oldRun })
	var asked string
	nvmeHealthFn = func(dev string) (*report.Health, error) {
		asked = dev
		if dev == "/dev/nvme1" {
			return nil, errors.New("permission denied")
		}
		return &report.Health{Status: report.StatusOK}, nil
	}
	findSmartctl = func() string { return "/usr/sbin/smartctl" }
	runCommand = func(_ time.Duration, _ string, args ...string) ([]byte, error) {
		if args[len(args)-1] == "/dev/sdb" {
			return nil, errors.New("exit status 2")
		}
		return []byte(`{"smart_status":{"passed":true},"temperature":{"current":31},"power_cycle_count":12,
			"nvme_smart_health_information_log":null,"endurance_used":{"current_percent":3}}`), nil
	}
	c := &collector{r: &report.Report{}, privileged: true}
	if h := c.diskHealth("nvme0n1", "nvme"); h == nil || asked != "/dev/nvme0" {
		t.Errorf("nvme: %+v, asked %s", h, asked)
	}
	if h := c.diskHealth("nvme1n1", "nvme"); h != nil || !hasWarning(c.r, "drive health nvme1n1: permission denied") {
		t.Errorf("nvme error: %+v, %v", h, c.r.Warnings)
	}
	for _, tr := range []string{"mmc", "virtio"} {
		if h := c.diskHealth("x", tr); h != nil {
			t.Errorf("%s has SMART? %+v", tr, h)
		}
	}
	h := c.diskHealth("sda", "sata")
	if h == nil || h.Metrics[report.MetricTemperatureC] != 31 || h.Metrics[report.MetricPowerCycles] != 12 || *h.LifeUsedPercent != 3 {
		t.Errorf("sata = %+v", h)
	}
	if h := c.diskHealth("sdb", "usb"); h != nil || !hasWarning(c.r, "smartctl 7.0 or newer") {
		t.Errorf("smartctl failure: %+v, %v", h, c.r.Warnings)
	}
}

// smartctl is only run from a root-owned system directory.
func TestSmartctlIsOnlyTakenFromRootOwnedPlaces(t *testing.T) {
	old := smartctlDirs
	t.Cleanup(func() { smartctlDirs = old })
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "smartctl"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	smartctlDirs = []string{filepath.Join(dir, "missing"), dir}
	if got := findSmartctl(); got != "" && os.Geteuid() != 0 {
		t.Errorf("user-owned smartctl accepted: %s", got)
	}
	if isExecutable(filepath.Join(dir, "missing")) || !isExecutable(filepath.Join(dir, "smartctl")) {
		t.Error("isExecutable")
	}
}

// The NVMe health log's temperature is in Kelvin; 0 means not reported.
func TestNVMeTemperature(t *testing.T) {
	log := make([]byte, 512)
	log[1], log[2] = 0x2C, 0x01 // 300 K
	if h := parseNVMeSMART(log); h.Metrics[report.MetricTemperatureC] != 27 {
		t.Errorf("temperature = %v", h.Metrics)
	}
	if h := parseNVMeSMART(make([]byte, 512)); h.Metrics[report.MetricTemperatureC] != 0 {
		t.Errorf("unreported temperature = %v", h.Metrics)
	}
}

// Bluetooth's management socket answers with events; only the reply to
// our Read Info command counts.
func TestBluetoothManagementReplies(t *testing.T) {
	reply := func(event, index, opcode uint16, status byte, payload []byte) []byte {
		b := []byte{byte(event), byte(event >> 8), byte(index), byte(index >> 8), 0, 0, byte(opcode), byte(opcode >> 8), status}
		return append(b, payload...)
	}
	p := make([]byte, readInfoReplyBytes)
	copy(p[0:6], []byte{0x56, 0x34, 0x12, 0x62, 0xF2, 0x60})
	p[6], p[7], p[13] = 11, 2, 0x01 // version 5.2, Intel, powered
	copy(p[20:], "laptop\x00")
	for _, c := range []struct {
		name string
		ev   []byte
		want string // "" = keep reading; "error" = an error
	}{
		{"short", []byte{1, 0}, ""},
		{"other controller", reply(evCmdComplete, 1, opReadInfo, 0, p), ""},
		{"other command", reply(evCmdComplete, 0, 0x0005, 0, p), ""},
		{"failed", reply(evCmdStatus, 0, opReadInfo, 0x11, nil), "error"},
		{"truncated", reply(evCmdComplete, 0, opReadInfo, 0, p[:10]), "error"},
		{"answer", reply(evCmdComplete, 0, opReadInfo, 0, p), "60:F2:62:12:34:56"},
	} {
		info, err := parseReadInfoReply(0, c.ev)
		switch {
		case c.want == "" && (info != nil || err != nil),
			c.want == "error" && err == nil,
			c.want != "" && c.want != "error" && (err != nil || info.address != c.want || info.version != 11 || info.manufacturer != 2 || info.settings&1 == 0 || info.name != "laptop"):
			t.Errorf("%s: %+v, %v", c.name, info, err)
		}
	}
}

// Hybrid Intel CPUs list their performance and efficiency cores; a
// modular frequency driver names its module.
func TestHybridCPUAndModularScalingDriver(t *testing.T) {
	file, _ := fakeRoot(t)
	asMachine(t, "x86_64", 1000)
	file("/proc/cpuinfo", "vendor_id\t: GenuineIntel\nmodel name\t: 13th Gen Intel(R) Core(TM) i7-1360P\nflags\t\t: vmx\n")
	file("/sys/devices/cpu_core/cpus", "0-7\n")
	file("/sys/devices/cpu_atom/cpus", "8-15\n")
	file(cpuDir+"cpu0/cpufreq/scaling_driver", "acpi-cpufreq")
	file("/sys/module/acpi_cpufreq/initstate", "live")
	file(cpuDir+"cpu0/cache/power/control", "auto")
	c := &collector{r: &report.Report{}}
	c.cpu()
	if len(c.r.CPU.CoreTypes) != 2 || c.r.CPU.CoreTypes[0].Threads != 8 || c.r.CPU.CoreTypes[1].Name != "efficiency" {
		t.Errorf("core types = %+v", c.r.CPU.CoreTypes)
	}
	if d := c.r.CPU.Driver; d == nil || d.Module != "acpi_cpufreq" || d.Builtin {
		t.Errorf("driver = %+v", d)
	}
	if c.r.CPU.Virtualization != "vmx" {
		t.Errorf("virtualization = %q", c.r.CPU.Virtualization)
	}
}

// Every kind of disk, on a system without udev (a container, a minimal
// install): they are found in sysfs, the identity comes from sysfs files,
// and the type is only what the kernel gives evidence for.
func TestDisksOfEveryKindWithoutUdev(t *testing.T) {
	file, link := fakeRoot(t)
	asMachine(t, "x86_64", 0)
	disk := func(name, devicePath, rotational string, files map[string]string) {
		dev := "/sys/devices/" + devicePath + "/block/" + name
		link("/sys/block/"+name, "../devices/"+devicePath+"/block/"+name)
		file(dev+"/size", "2000000")
		file(dev+"/removable", "0")
		file(dev+"/dev", "8:0")
		file(dev+"/queue/rotational", rotational)
		file(dev+"/queue/logical_block_size", "512")
		file(dev+"/queue/physical_block_size", "4096")
		link(dev+"/device", "../..")
		for k, v := range files {
			file("/sys/devices/"+devicePath+"/"+k, v)
		}
	}
	disk("sda", "pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0", "1", map[string]string{"model": "ST2000DM008", "vendor": "ATA", "rev": "0001"})
	disk("sdb", "pci0000:00/0000:00:14.0/usb2/2-1/2-1:1.0/host1/target1:0:0/1:0:0:0", "0", map[string]string{"model": "Extreme SSD", "vendor": "SanDisk", "serial": "3233"})
	disk("sr0", "pci0000:00/0000:00:17.0/ata2/host1/target1:0:0/1:0:0:0", "1", map[string]string{"model": "DVD-RW"})
	disk("mmcblk0", "platform/fe340000.mmc/mmc_host/mmc0/mmc0:0001", "0", map[string]string{"name": "DG4064", "fwrev": "0x7", "rev": "0x8"})
	disk("vda", "pci0000:00/0000:00:04.0/virtio1", "1", nil)
	oldRun := runCommand
	runCommand = func(time.Duration, string, ...string) ([]byte, error) {
		return []byte(`{"smart_status":{"passed":true}}`), nil
	}
	findSmartctl = func() string { return "/usr/sbin/smartctl" }
	t.Cleanup(func() { runCommand = oldRun })

	c := &collector{r: &report.Report{}, privileged: true}
	c.storage()
	got := map[string]report.Disk{}
	for _, d := range c.r.Storage {
		got[d.Name] = d
	}
	for name, want := range map[string]struct{ typ, transport, model, fw string }{
		"sda":     {"hdd", "sata", "ST2000DM008", "0001"},
		"sdb":     {"ssd", "usb", "Extreme SSD", ""},
		"sr0":     {"optical", "sata", "DVD-RW", ""},
		"mmcblk0": {"flash", "mmc", "", "0x7"},
		"vda":     {"virtual", "virtio", "", ""},
	} {
		d, ok := got[name]
		if !ok {
			t.Errorf("%s not found (got %v)", name, c.r.Storage)
			continue
		}
		var model, fw string
		if d.Identity != nil {
			model = d.Identity.Model
		}
		if d.Firmware != nil {
			fw = d.Firmware.Version
		}
		if d.Type != want.typ || d.Transport != want.transport || model != want.model || fw != want.fw {
			t.Errorf("%s = type %q via %q, model %q, fw %q; want %+v", name, d.Type, d.Transport, model, fw, want)
		}
	}
	if d := got["sdb"]; d.Identity == nil || d.Identity.Vendor != "SanDisk" || d.Identity.Serial != "3233" {
		t.Errorf("sdb identity = %+v", d.Identity)
	}
	if got["sr0"].Health != nil || got["mmcblk0"].Health != nil || got["sda"].Health == nil {
		t.Error("drive health: optical and eMMC have no SMART, SATA does")
	}
	// A drive's firmware is never silently absent; a virtual disk's is the
	// host's, so it has no block.
	if fw := got["sdb"].Firmware; fw == nil || fw.Status != report.FirmwareUnknown || fw.Reason != "the kernel doesn't expose this drive's firmware revision" {
		t.Errorf("sdb firmware = %+v", fw)
	}
	if fw := got["vda"].Firmware; fw != nil {
		t.Errorf("vda firmware = %+v", fw)
	}
}

// Partitions as udev and the kernel record them, unaltered (#142): a
// label with an underscore keeps it, an unmounted LUKS or LVM partition
// keeps its type, uuid is the filesystem's UUID and partuuid the partition
// table entry's. A label or model with spaces comes from udev's exact
// _ENC form, and without udev the mount table and the kernel fill in.
func TestPartitionsAsRecorded(t *testing.T) {
	file, link := fakeRoot(t)
	asMachine(t, "x86_64", 1000)
	dev := "/sys/devices/pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0/block/sda"
	link("/sys/block/sda", "../devices/pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0/block/sda")
	link(dev+"/device", "../..")
	file(dev+"/size", "1000215216")
	file(dev+"/dev", "8:0")
	file(dev+"/removable", "1")
	file(dev+"/queue/physical_block_size", "4096")
	file(dev+"/queue/rotational", "0")
	file(dev+"/alignment_offset", "0")            // not a partition
	file(dev+"/device/model", "Samsung SSD 860 ") // the SCSI inquiry's 16 characters
	file(dev+"/device/vendor", "ATA")
	file("/run/udev/data/b8:0", "S:disk/by-id/ata-x\n"+
		"E:ID_MODEL=Samsung_SSD_860_EVO_500GB\nE:ID_MODEL_ENC=Samsung\\x20SSD\\x20860\\x20EVO\\x20500GB\\x20\\x20\n"+
		"E:ID_SERIAL=Samsung_SSD_860_EVO_500GB_S3Z1NB0K_123\nE:ID_SERIAL_SHORT=S3Z1NB0K_123\nE:ID_WWN=0x5002538e40a1b2c3\nE:ID_WWN_WITH_EXTENSION=0x5002538e40a1b2c3d4e5f6\n")
	part := func(name, n, devNo, udev, uevent string) {
		file(dev+"/"+name+"/partition", n)
		file(dev+"/"+name+"/size", "2048")
		file(dev+"/"+name+"/dev", devNo)
		file(dev+"/"+name+"/uevent", uevent)
		if udev != "" {
			file("/run/udev/data/b"+devNo, udev)
		}
	}
	part("sda1", "1", "8:1", "E:ID_FS_TYPE=vfat\nE:ID_FS_LABEL=SYSTEM_DRV\nE:ID_FS_LABEL_ENC=SYSTEM_DRV\nE:ID_FS_UUID=6A26-3981\nE:ID_FS_UUID_ENC=6A26-3981\n"+
		"E:ID_PART_ENTRY_UUID=862a5947-c13a-4f9b-9f61-22f09238fa7f\n", "PARTUUID=862a5947-c13a-4f9b-9f61-22f09238fa7f\n")
	part("sda2", "2", "8:2", "E:ID_FS_TYPE=crypto_LUKS\nE:ID_FS_UUID=4ac9c9e9-6d1b-4984-a0f2-13842b536ee3\nE:ID_PART_ENTRY_UUID=6f302dbe-05e9-4aec-a960-ed9595fae5d1\n", "")
	part("sda10", "10", "8:10", "E:ID_FS_TYPE=LVM2_member\nE:ID_FS_LABEL=My_Data\nE:ID_FS_LABEL_ENC=My\\x20Data\n", "")
	part("sda3", "3", "8:3", "", "PARTUUID=0b4f7c1e-03\nMAJOR=8\n") // no udev record
	part("sda4", "4", "8:4", "E:ID_FS_TYPE=ntfs\n", "")
	file("/proc/self/mounts", "/dev/sda1 /boot/efi vfat rw 0 0\n/dev/sda3 /run/media/x/My\\040Disk ext4 rw 0 0\n/dev/sda3 /second ext4 rw 0 0\n"+
		"/dev/sda4 /win ntfs3 rw 0 0\nproc /proc proc rw 0 0\ngarbage\ntwo fields\n")
	// A SCSI disk: its unit serial (VPD page 80h) is the serial.
	scsi := "/sys/devices/pci0000:00/0000:00:1f.2/host2/target2:0:0/2:0:0:0/block/sdb"
	link("/sys/block/sdb", "../devices/pci0000:00/0000:00:1f.2/host2/target2:0:0/2:0:0:0/block/sdb")
	file(scsi+"/size", "1000")
	file(scsi+"/dev", "8:16")
	file("/run/udev/data/b8:16", "E:ID_SCSI_SERIAL=Z1X2C3V4\nE:ID_SERIAL_SHORT=35000c500a1b2c3d4\n")
	traced := map[string]bool{}
	traceRead = func(path string) { traced[path] = true }
	t.Cleanup(func() { traceRead = nil })

	c := &collector{r: &report.Report{}}
	c.storage()
	if len(c.r.Storage) != 2 {
		t.Fatalf("disks = %+v", c.r.Storage)
	}
	if id := c.r.Storage[1].Identity; id == nil || id.Serial != "Z1X2C3V4" {
		t.Errorf("SCSI disk identity = %+v", id)
	}
	d := c.r.Storage[0]
	if id := d.Identity; id == nil || id.Model != "Samsung SSD 860 EVO 500GB" || id.Vendor != "ATA" || id.Serial != "S3Z1NB0K_123" {
		t.Errorf("identity = %+v", d.Identity)
	}
	if d.WWN != "0x5002538e40a1b2c3d4e5f6" || !d.Removable || d.PhysicalBlockBytes != 4096 || d.SizeBytes != 1000215216*512 {
		t.Errorf("disk = %+v", d)
	}
	want := []report.Partition{
		{Name: "sda1", SizeBytes: 2048 * 512, Filesystem: "vfat", Label: "SYSTEM_DRV", UUID: "6A26-3981", PartUUID: "862a5947-c13a-4f9b-9f61-22f09238fa7f", MountPoint: "/boot/efi"},
		{Name: "sda2", SizeBytes: 2048 * 512, Filesystem: "crypto_LUKS", UUID: "4ac9c9e9-6d1b-4984-a0f2-13842b536ee3", PartUUID: "6f302dbe-05e9-4aec-a960-ed9595fae5d1"},
		{Name: "sda3", SizeBytes: 2048 * 512, Filesystem: "ext4", PartUUID: "0b4f7c1e-03", MountPoint: "/run/media/x/My Disk"},
		{Name: "sda4", SizeBytes: 2048 * 512, Filesystem: "ntfs", MountPoint: "/win"}, // the filesystem, not the driver mounting it
		{Name: "sda10", SizeBytes: 2048 * 512, Filesystem: "LVM2_member", Label: "My Data"},
	}
	if !reflect.DeepEqual(d.Partitions, want) {
		t.Errorf("partitions:\n got %+v\nwant %+v", d.Partitions, want)
	}
	// Every read goes through the collectors' file layer, so a recording
	// copies it; only entries named after the disk are probed.
	for _, path := range []string{"/run/udev/data/b8:0", "/run/udev/data/b8:1", "/sys/block/sda/device/vendor", "/sys/block/sda/device/model", "/sys/block/sda/queue/physical_block_size", "/proc/self/mounts"} {
		if !traced[path] {
			t.Errorf("%s not traced", path)
		}
	}
	if traced["/sys/block/sda/alignment_offset/partition"] {
		t.Error("probed a disk attribute for a partition number")
	}
}

// The unescapers decode only well-formed escapes.
func TestUnescapers(t *testing.T) {
	for in, want := range map[string]string{`My\x20Data`: "My Data", `a\x2`: `a\x2`, `a\xZZb`: `a\xZZb`, `\x41\x42`: "AB", `\\x41`: `\A`, `a\b12c`: `a\b12c`} {
		if got := unescapeHex(in); got != want {
			t.Errorf("unescapeHex(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{`My\040Disk`: "My Disk", `a\134b`: `a\b`, `a\04`: `a\04`, `a\09x`: `a\09x`, `\011\012`: "\t\n"} {
		if got := unescapeOctal(in); got != want {
			t.Errorf("unescapeOctal(%q) = %q, want %q", in, got, want)
		}
	}
}

// Monitors: a numeric EDID serial when there's no serial string, a model
// year instead of a manufacture date (week 255), and a broken EDID that is
// reported rather than guessed at.
func TestMonitorsFromTheirEDID(t *testing.T) {
	file, _ := fakeRoot(t)
	edid := func(serial uint32, week, year byte) string {
		b := make([]byte, 128)
		copy(b, []byte{0, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0})
		b[8], b[9] = 0x10, 0xAC // DEL
		b[12], b[13], b[14], b[15] = byte(serial), byte(serial>>8), byte(serial>>16), byte(serial>>24)
		b[16], b[17], b[18], b[19], b[21], b[22] = week, year, 1, 4, 60, 34
		return string(b)
	}
	for conn, data := range map[string]string{
		"card0-DP-1":     edid(12345678, 38, 30),
		"card0-DP-2":     edid(0x01010101, 255, 33), // placeholder serial, model year 2023
		"card0-HDMI-A-1": "not an EDID",
		"card0-eDP-1":    "",
	} {
		file("/sys/class/drm/"+conn+"/status", "connected")
		file("/sys/class/drm/"+conn+"/edid", data)
	}
	file("/sys/class/drm/card0-DP-3/status", "disconnected")
	c := &collector{r: &report.Report{}}
	c.displays()
	got := map[string]report.Display{}
	for _, d := range c.r.Displays {
		got[d.Connector] = d
	}
	if d := got["card0-DP-1"]; d.Identity == nil || d.Identity.Serial != "12345678" || d.Identity.ManufactureDate != "2020-W38" || d.DiagonalIn == 0 {
		t.Errorf("DP-1 = %+v %+v", d, d.Identity)
	}
	if d := got["card0-DP-2"]; d.ModelYear != 2023 || d.Identity.ManufactureDate != "" || d.Identity.Serial != "" {
		t.Errorf("DP-2 = %+v %+v", d, d.Identity)
	}
	if _, listed := got["card0-DP-3"]; listed || !hasWarning(c.r, "display card0-HDMI-A-1: edid") {
		t.Errorf("displays %v, warnings %v", got, c.r.Warnings)
	}
}

// Every kind of hwmon reading gets its unit; unreadable ones are skipped.
func TestSensorReadingsOfEveryKind(t *testing.T) {
	file, _ := fakeRoot(t)
	h := "/sys/class/hwmon/hwmon0/"
	for name, v := range map[string]string{"name": "nct6798", "fan1_input": "1200", "power1_average": "15500000", "in0_input": "1050",
		"curr1_input": "2500", "temp1_input": "42000", "temp1_label": "CPU", "temp2_input": "x"} {
		file(h+name, v)
	}
	c := &collector{r: &report.Report{}}
	c.sensors()
	units := map[string]string{}
	for _, r := range c.r.Sensors[0].Readings {
		units[r.Kind] = r.Unit
	}
	for kind, unit := range map[string]string{"fan": "RPM", "power": "W", "voltage": "V", "current": "A", "temperature": "C"} {
		if units[kind] != unit {
			t.Errorf("%s unit = %q, want %q (all: %v)", kind, units[kind], unit, units)
		}
	}
	if len(c.r.Sensors[0].Readings) != 5 {
		t.Errorf("readings = %+v", c.r.Sensors[0].Readings)
	}
}

// An SPD EEPROM that only root can read is reported as needing root.
func TestRootOnlySPDNeedsRoot(t *testing.T) {
	file, _ := fakeRoot(t)
	file("/sys/bus/i2c/drivers/spd5118/1-0051/eeprom", "x")
	unreadable = map[string]error{"/sys/bus/i2c/drivers/spd5118/1-0051/eeprom": syscall.EACCES}
	c := &collector{r: &report.Report{}}
	c.spdModules()
	if !hasWarning(c.r, "memory module SPD 1-0051: needs root") {
		t.Errorf("warnings = %v", c.r.Warnings)
	}
}

// With root, the capture records the firmware's processor socket, slots
// and onboard devices as the reference machine's table states them, and
// reads the table once for memory and these.
func TestFirmwareSlotsSocketAndOnboardDevices(t *testing.T) {
	file, _ := fakeRoot(t)
	file("/sys/firmware/dmi/tables/DMI", string(smbiostest.EliteDesk800G5Mini()))
	reads := 0
	traceRead = func(path string) {
		if path == "/sys/firmware/dmi/tables/DMI" {
			reads++
		}
	}
	c := &collector{r: &report.Report{}, privileged: true}
	c.memory()
	c.firmwareTables()
	if reads != 1 {
		t.Errorf("DMI read %d times, want 1", reads)
	}
	if want := []report.CPUPackage{{Designation: "U3E1", Package: "Socket LGA1151", Mounting: "socket", Populated: true}}; !reflect.DeepEqual(c.r.CPU.Packages, want) {
		t.Errorf("packages %+v", c.r.CPU.Packages)
	}
	b := c.r.Board
	wantSlots := []report.Slot{
		{Designation: "Slot1 / DGPU PCIEXP", Type: "PCI Express Gen 3 x8", Width: "x8", Usage: "Available", Length: "Long Length", ID: 1, Address: "0000:00:01.0"},
		{Designation: "Slot2 / M2 WLAN/BT", Type: "PCI Express Gen 3 x1", Width: "x1", Usage: "In use", Length: "Other", ID: 2, Address: "0000:00:1c.7"},
		{Designation: "Slot3 / M2 SSD", Type: "PCI Express Gen 3 x4", Width: "x4", Usage: "In use", Length: "Other", ID: 3, Address: "0000:00:1b.4"},
		{Designation: "Slot4 / M2 SSD", Type: "PCI Express Gen 3 x4", Width: "x4", Usage: "Available", Length: "Other", ID: 4, Address: "0000:00:1b.0"},
		{Designation: "Slot5 / TBT Fiber Combo", Type: "PCI Express Gen 3 x4", Width: "x4", Usage: "Available", Length: "Long Length", ID: 5, Address: "0000:00:1d.0"},
	}
	if !reflect.DeepEqual(b.Slots, wantSlots) {
		t.Errorf("slots\n got %+v\nwant %+v", b.Slots, wantSlots)
	}
	// False flags and slot 0 are written, not left out.
	js, _ := json.Marshal([]any{report.CPUPackage{}, report.OnboardDevice{}, report.Slot{}})
	for _, want := range []string{`"populated":false`, `"enabled":false`, `"id":0`} {
		if !strings.Contains(string(js), want) {
			t.Errorf("%s lacks %s", js, want)
		}
	}
	if want := []report.OnboardDevice{{Designation: "Onboard IGD", Type: "Video", Enabled: true, Instance: 1, Address: "0000:00:02.0"},
		{Designation: "Onboard Lan", Type: "Ethernet", Enabled: true, Instance: 1, Address: "0000:00:1f.6"}}; !reflect.DeepEqual(b.OnboardDevices, want) {
		t.Errorf("onboard %+v", b.OnboardDevices)
	}
}

// Without root the table is unreadable: no slots, socket or onboard
// devices, and only memory's one warning about it.
func TestFirmwareTablesNeedRootAndWarnOnce(t *testing.T) {
	file, _ := fakeRoot(t)
	file("/sys/firmware/dmi/tables/DMI", string(smbiostest.EliteDesk800G5Mini()))
	unreadable = map[string]error{"/sys/firmware/dmi/tables/DMI": syscall.EACCES}
	c := &collector{r: &report.Report{}}
	c.smbiosModules()
	c.firmwareTables()
	if c.r.CPU.Packages != nil || c.r.Board.Slots != nil || c.r.Board.OnboardDevices != nil {
		t.Errorf("got %+v %+v", c.r.CPU.Packages, c.r.Board)
	}
	if len(c.r.Warnings) != 1 || !hasWarning(c.r, "SMBIOS table (memory modules and slots, CPU sockets, expansion slots, onboard devices): needs root") {
		t.Errorf("warnings %q", c.r.Warnings)
	}
}

// A whole capture as root records the firmware's socket, slots and
// onboard devices: the collector is part of Collect, not only callable.
func TestRootCaptureRecordsTheFirmwareTables(t *testing.T) {
	file, _ := fakeRoot(t)
	asMachine(t, "x86_64", 0)
	file("/proc/cpuinfo", "flags\t\t: fpu sse2\n")
	file("/sys/firmware/dmi/tables/DMI", string(smbiostest.EliteDesk800G5Mini()))
	r := Collect("test")
	checkFirmwareComplete(t, r)
	if len(r.CPU.Packages) != 1 || r.CPU.Packages[0].Package != "Socket LGA1151" || len(r.Board.Slots) != 5 || len(r.Board.OnboardDevices) != 2 {
		t.Errorf("packages %+v, slots %d, onboard %d", r.CPU.Packages, len(r.Board.Slots), len(r.Board.OnboardDevices))
	}
}

// Every memory slot the firmware describes is listed, used or empty: the
// reference machine's two SODIMM slots (DIMM1/ChannelB, DIMM3/ChannelA,
// both used, as `dmidecode -t 17` printed them on 2026-10-06), and a
// four-slot board with two empty.
func TestMemorySlotUsage(t *testing.T) {
	yes, no := new(bool), new(bool)
	*yes = true
	for _, c := range []struct {
		name    string
		modules []smbiostest.Module
		slots   int
		want    []report.MemorySlot
	}{
		{"reference machine", []smbiostest.Module{
			{Array: 0x0007, SizeMiB: 16384, Locator: "DIMM1", Bank: "ChannelB", FormFactor: 0x0D, Type: 0x1A, SpeedMTs: 3200},
			{Array: 0x0007, SizeMiB: 16384, Locator: "DIMM3", Bank: "ChannelA", FormFactor: 0x0D, Type: 0x1A, SpeedMTs: 3200},
		}, 2, []report.MemorySlot{
			{Locator: "DIMM1", BankLocator: "ChannelB", Populated: yes, FormFactor: "SODIMM"},
			{Locator: "DIMM3", BankLocator: "ChannelA", Populated: yes, FormFactor: "SODIMM"},
		}},
		{"two of four used", []smbiostest.Module{
			{Array: 0x0007, SizeMiB: 8192, Locator: "DIMM_A1", FormFactor: 0x09, Type: 0x22},
			{Array: 0x0007, Locator: "DIMM_A2", FormFactor: 0x09},
			{Array: 0x0007, SizeMiB: 8192, Locator: "DIMM_B1", FormFactor: 0x09, Type: 0x22},
			{Array: 0x0007, Locator: "DIMM_B2", FormFactor: 0x09},
		}, 4, []report.MemorySlot{
			{Locator: "DIMM_A1", Populated: yes, FormFactor: "DIMM"}, {Locator: "DIMM_A2", Populated: no, FormFactor: "DIMM"},
			{Locator: "DIMM_B1", Populated: yes, FormFactor: "DIMM"}, {Locator: "DIMM_B2", Populated: no, FormFactor: "DIMM"},
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			table := smbiostest.MemoryArray(0x0007, 64<<20, uint16(c.slots), 0x03)
			for _, m := range c.modules {
				table = append(table, smbiostest.MemoryDevice(m)...)
			}
			file, _ := fakeRoot(t)
			file("/sys/firmware/dmi/tables/DMI", string(append(table, smbiostest.End()...)))
			col := &collector{r: &report.Report{}, privileged: true}
			col.smbiosModules()
			mem := col.r.Memory
			if !reflect.DeepEqual(mem.SlotUsage, c.want) || mem.Slots != c.slots {
				t.Errorf("slots %d %+v\nwant %d %+v", mem.Slots, mem.SlotUsage, c.slots, c.want)
			}
			used := 0
			for _, s := range c.want {
				if s.Populated != nil && *s.Populated {
					used++
				}
			}
			if len(mem.Modules) != used {
				t.Errorf("%d modules, want %d", len(mem.Modules), used)
			}
		})
	}
}
