// Package report defines the hwspec file format.
//
// Bump SchemaVersion when a field is renamed, removed or changes meaning.
// Adding fields is backwards compatible and doesn't need a bump.
//
// Every device type carries up to four shared blocks (ADR 0008): Identity,
// Firmware, Driver and Health, each present only when something in it was
// read.
package report

import "time"

// SchemaVersion is the format's version, written in every capture's
// schema_version.
const SchemaVersion = 1

// Report is one capture of a machine. Every field's doc comment is its
// description in the published schema (tools/genschema), so each field
// has one.
type Report struct {
	// Schema is the URL of the format's JSON Schema (package schema), for
	// editors and validators. Captures from before it was added lack it.
	Schema        string `json:"$schema,omitempty"`
	SchemaVersion int    `json:"schema_version"` // the format's version (SchemaVersion)
	Tool          Tool   `json:"tool"`
	// CapturedAt is when the capture was taken (RFC 3339).
	CapturedAt time.Time `json:"captured_at"`
	Hostname   string    `json:"hostname"` // the machine's hostname; "" when redacted
	// Privileged is true when the capture ran as root, so the SMBIOS memory
	// list, most serial numbers and drive health could be read. Memory
	// modules' SPD (serials included) is often readable without root.
	Privileged bool `json:"privileged"`
	// Redacted is true when what ties the capture to one machine or person
	// was removed (--redact): serial numbers, UUIDs, WWNs, MAC and
	// Bluetooth addresses, the hostname, the asset tag, partition labels
	// and personal mount points. Manufacture dates are kept to the month.
	Redacted bool `json:"redacted"`

	OS        OS                    `json:"os"`
	System    System                `json:"system"`
	Board     Board                 `json:"board"`
	CPU       CPU                   `json:"cpu"`
	Memory    Memory                `json:"memory"`
	Storage   []Disk                `json:"storage"`
	GPUs      []GPU                 `json:"gpus"`
	Displays  []Display             `json:"displays"`
	Network   []NIC                 `json:"network"`
	Bluetooth []BluetoothController `json:"bluetooth"`
	Audio     []SoundCard           `json:"audio"`
	Batteries []Battery             `json:"batteries"`
	TPM       *TPM                  `json:"tpm,omitempty"`
	Sensors   []Sensor              `json:"sensors"`
	PCI       []PCIDevice           `json:"pci"`
	Kernel    *Kernel               `json:"kernel,omitempty"`
	USB       []USBDevice           `json:"usb"`

	// Warnings lists what couldn't be read and why (e.g. permission denied).
	Warnings []string `json:"warnings"`
}

// Tool is the program that wrote the capture.
type Tool struct {
	Name    string `json:"name"`    // "hwspec"
	Version string `json:"version"` // its version, e.g. "v0.1.0" or "dev"
	// IDDatabases records, per ID database, the source its names came from
	// (e.g. "pci": "/usr/share/hwdata/pci.ids (2026-09-03) + overrides").
	IDDatabases map[string]string `json:"id_databases"`
}

// --- Shared detail blocks (ADR 0008) ---

// Identity names one physical part.
type Identity struct {
	Vendor     string `json:"vendor,omitempty"`      // the maker's name
	Model      string `json:"model,omitempty"`       // the product's name
	PartNumber string `json:"part_number,omitempty"` // the maker's part number
	Serial     string `json:"serial,omitempty"`      // removed by --redact
	Revision   string `json:"revision,omitempty"`    // the hardware revision
	// ManufactureDate is ISO 8601 at the precision the part reports:
	// "2020", "2020-03", "2020-W10" or "2020-03-14".
	ManufactureDate       string `json:"manufacture_date,omitempty"`
	ManufactureDateSource string `json:"manufacture_date_source,omitempty"` // spd, edid, battery
}

// Firmware is the firmware a part runs, as the part or its driver reports
// it. A part that has firmware always has this block (ADR 0008): with a
// version and its source, or, when the version can't be read, Status
// "unknown" and the Reason, never silently absent.
type Firmware struct {
	// Name says which firmware of a part this is, where a part has several
	// (a GPU's firmware_components: "GuC", "HuC", "GuC (gt1)").
	Name    string `json:"name,omitempty"`
	Vendor  string `json:"vendor,omitempty"`  // the firmware's maker, where reported
	Version string `json:"version,omitempty"` // as the part reports it
	Date    string `json:"date,omitempty"`    // the build date, as the firmware states it
	// Release is a second version number the firmware reports: the BIOS
	// release ("5.17"), or a Bluetooth controller's HCI revision.
	Release string `json:"release,omitempty"`
	// Source says where the version was read: dmi, microcode, nvme, scsi
	// (SATA, SAS and USB disks), mmc, ethtool, usb, vbios (amdgpu, and the
	// NVIDIA driver's /proc file), mei, udev (a TPM 2.0, from systemd's
	// tpm2_id), tpm (a TPM 2.0 asked under --full), caps (a TPM 1.2), debugfs
	// (Intel GuC and HuC, under --full), amdgpu (an AMD GPU's blocks), hci
	// (a Bluetooth controller: version is the LMP subversion, release the HCI
	// revision).
	Source string `json:"source,omitempty"`
	Status string `json:"status,omitempty"` // "unknown" when Version couldn't be read
	Reason string `json:"reason,omitempty"` // why it's unknown, e.g. "needs --full"
	// InstanceIDs are the IDs fwupd knows this part by, each with the GUID
	// LVFS releases name it with (ADR 0012): derived from the part's own
	// IDs, so a capture can be compared with a firmware catalogue offline.
	InstanceIDs []InstanceID `json:"instance_ids,omitempty"`
}

// InstanceID is an fwupd instance ID ("NVME\VEN_144D&DEV_A808") and its
// GUID: UUID version 5 in the DNS namespace over the ID's text.
type InstanceID struct {
	ID   string `json:"id"`   // e.g. NVME\VEN_144D&DEV_A808
	GUID string `json:"guid"` // the ID's GUID, lowercase
}

// FirmwareUnknown is Firmware.Status for a version that couldn't be read.
const FirmwareUnknown = "unknown"

// Firmware.Source values: where a version was read.
const (
	FirmwareFromDMI       = "dmi"
	FirmwareFromMicrocode = "microcode"
	FirmwareFromNVMe      = "nvme"
	FirmwareFromSCSI      = "scsi"
	FirmwareFromMMC       = "mmc"
	FirmwareFromEthtool   = "ethtool"
	FirmwareFromUSB       = "usb"
	FirmwareFromVBIOS     = "vbios"
	FirmwareFromMEI       = "mei"
	FirmwareFromUdev      = "udev"
	FirmwareFromTPM       = "tpm"
	FirmwareFromTPMCaps   = "caps"
	FirmwareFromDebugfs   = "debugfs"
	FirmwareFromAMDGPU    = "amdgpu"
	FirmwareFromHCI       = "hci"
)

// UnknownFirmware is the block of a part whose firmware version can't be
// read, and why.
func UnknownFirmware(reason string) *Firmware {
	return &Firmware{Status: FirmwareUnknown, Reason: reason}
}

// Known says whether the block holds a version.
func (f *Firmware) Known() bool { return f != nil && f.Version != "" }

// Driver is the kernel driver bound to a part.
type Driver struct {
	Name   string `json:"name"`             // the driver's name, e.g. iwlwifi
	Module string `json:"module,omitempty"` // the kernel module providing it, if not built in
	// Version is the module's own version; in-tree modules usually have
	// none, and match the kernel.
	Version string `json:"version,omitempty"`
	// SrcVersion is the checksum of the module's source (modinfo
	// srcversion), which tells two builds of one version apart.
	SrcVersion string `json:"srcversion,omitempty"`
	Builtin    bool   `json:"builtin,omitempty"` // built into the kernel, not a loadable module
	// InTree is false for a module built outside the kernel tree (taint
	// flag O). Absent when the taint flags can't be read, as for
	// Proprietary and Unsigned.
	InTree *bool `json:"in_tree,omitempty"`
	// Proprietary is true for a module under a non-GPL-compatible licence
	// (taint flag P).
	Proprietary *bool `json:"proprietary,omitempty"`
	// Unsigned is true for a module loaded without a valid signature
	// (taint flag E), set only when the kernel exposes
	// module.sig_enforce; unpatched kernels before 5.13 can expose it
	// without module signing.
	Unsigned *bool `json:"unsigned,omitempty"`
}

// Health describes a part's condition and wear.
type Health struct {
	Status  string   `json:"status"`            // ok, warning, failing, unknown
	Reasons []string `json:"reasons,omitempty"` // why the status isn't ok, one line each
	Source  string   `json:"source,omitempty"`  // what it was read from (HealthFrom*)
	// LifeUsedPercent and LifeRemainingPercent come only from the part's
	// own wear indicator (NVMe percentage used, SSD endurance, battery
	// capacity versus design, measured on its 100% to 80% range). Used may
	// exceed 100; remaining stops at 0.
	LifeUsedPercent      *float64  `json:"life_used_percent,omitempty"`
	LifeRemainingPercent *float64  `json:"life_remaining_percent,omitempty"` // see life_used_percent
	Estimate             *Estimate `json:"estimate,omitempty"`
	// Metrics are measurements by name (the Metric* constants; the
	// schema lists them as examples), in the unit the name ends with, or
	// a count. Newer builds may add names.
	Metrics map[string]float64 `json:"metrics,omitempty"`
}

// Estimate is a derived figure; Method says how it was computed.
type Estimate struct {
	What   string  `json:"what"`   // e.g. "cycles until 80% capacity"
	Value  float64 `json:"value"`  // in unit
	Unit   string  `json:"unit"`   // e.g. "cycles"
	Method string  `json:"method"` // how Value was computed, in words
}

// Health statuses.
const (
	StatusOK      = "ok"
	StatusWarning = "warning"
	StatusFailing = "failing"
	StatusUnknown = "unknown"
)

// Metric names used in Health.Metrics.
const (
	MetricTemperatureC       = "temperature_c"
	MetricPowerOnHours       = "power_on_hours"
	MetricPowerCycles        = "power_cycles"
	MetricDataReadBytes      = "data_read_bytes"
	MetricDataWrittenBytes   = "data_written_bytes"
	MetricAvailableSpare     = "available_spare_percent"
	MetricUnsafeShutdowns    = "unsafe_shutdowns"
	MetricMediaErrors        = "media_errors"
	MetricReallocatedSectors = "reallocated_sectors"
	MetricPendingSectors     = "pending_sectors"
	MetricCycleCount         = "cycle_count"
	MetricFullWh             = "full_wh"
	MetricCapacityPercent    = "capacity_percent" // full charge ÷ design, may exceed 100
	MetricDesignWh           = "design_wh"
	MetricECCCorrected       = "ecc_corrected"
	MetricECCUncorrected     = "ecc_uncorrected"
	MetricThrottleEvents     = "thermal_throttle_events"
	MetricRxErrors           = "rx_errors"
	MetricTxErrors           = "tx_errors"
	MetricRxDropped          = "rx_dropped"
	MetricTxDropped          = "tx_dropped"
	MetricRxPackets          = "rx_packets"
	MetricTxPackets          = "tx_packets"
)

// Health.Source values: what the status and metrics were read from.
const (
	HealthFromNVMe            = "nvme"             // the NVMe SMART log
	HealthFromSmartctl        = "smartctl"         // smartmontools, for SATA and SCSI drives
	HealthFromEDAC            = "edac"             // the kernel's memory error counters
	HealthFromPowerSupply     = "power_supply"     // a battery's charge and capacity
	HealthFromStatistics      = "statistics"       // a network interface's counters
	HealthFromThermalThrottle = "thermal_throttle" // a CPU's throttling counters
)

// Vocabularies lists, by "Type.Field", the values of a field that readers
// branch on; for a map, its keys. genschema publishes each list as the
// field's examples. A published value stays (ADR 0003): renaming or
// removing one breaks readers, so the lists may only grow, which a test
// against testdata/vocabularies.json enforces.
var Vocabularies = map[string][]string{
	"Health.Status": {StatusOK, StatusWarning, StatusFailing, StatusUnknown},
	"Health.Source": {HealthFromNVMe, HealthFromSmartctl, HealthFromEDAC, HealthFromPowerSupply,
		HealthFromStatistics, HealthFromThermalThrottle},
	"Health.Metrics": {MetricTemperatureC, MetricPowerOnHours, MetricPowerCycles, MetricDataReadBytes,
		MetricDataWrittenBytes, MetricAvailableSpare, MetricUnsafeShutdowns, MetricMediaErrors,
		MetricReallocatedSectors, MetricPendingSectors, MetricCycleCount, MetricFullWh,
		MetricCapacityPercent, MetricDesignWh, MetricECCCorrected, MetricECCUncorrected,
		MetricThrottleEvents, MetricRxErrors, MetricTxErrors, MetricRxDropped, MetricTxDropped,
		MetricRxPackets, MetricTxPackets},
	"Firmware.Status": {FirmwareUnknown},
	"Firmware.Source": {FirmwareFromDMI, FirmwareFromMicrocode, FirmwareFromNVMe, FirmwareFromSCSI,
		FirmwareFromMMC, FirmwareFromEthtool, FirmwareFromUSB, FirmwareFromVBIOS, FirmwareFromMEI,
		FirmwareFromUdev, FirmwareFromTPM, FirmwareFromTPMCaps, FirmwareFromDebugfs, FirmwareFromAMDGPU,
		FirmwareFromHCI},
	"ModuleIndex.Status":     {ModulesFound, ModulesOtherRelease, ModulesNone},
	"BlacklistedModule.Kind": {BlacklistAlias, BlacklistKernel, BlacklistInstall},
}

// --- Devices ---

// OS is the running operating system, from os-release and the kernel.
type OS struct {
	Name           string `json:"name"`              // os-release NAME, e.g. "Fedora Linux"
	ID             string `json:"id"`                // os-release ID, e.g. "fedora"
	IDLike         string `json:"id_like,omitempty"` // os-release ID_LIKE, space-separated
	Version        string `json:"version,omitempty"` // os-release VERSION_ID; absent on rolling releases
	PrettyName     string `json:"pretty_name"`       // os-release PRETTY_NAME
	Kernel         string `json:"kernel"`            // the kernel release, as uname -r
	Arch           string `json:"arch"`              // the machine, as uname -m, e.g. x86_64
	Init           string `json:"init,omitempty"`    // process 1's name, e.g. systemd
	Virtualization string `json:"virtualization"`    // none, vm, container; "" if unknown
	BootMode       string `json:"boot_mode"`         // uefi, bios; "" if unknown
	// SecureBoot is the firmware's Secure Boot state; absent when not UEFI
	// or not readable.
	SecureBoot *bool `json:"secure_boot,omitempty"`
}

// System is the machine as a whole; its firmware is the BIOS/UEFI.
type System struct {
	// Identity is the machine's (SMBIOS type 1): model is the product
	// name, part_number the SKU.
	Identity      *Identity `json:"identity,omitempty"`
	Family        string    `json:"family,omitempty"`         // the product family, as the firmware names it
	UUID          string    `json:"uuid,omitempty"`           // the SMBIOS system UUID (root only)
	ChassisType   string    `json:"chassis_type"`             // e.g. Desktop, Notebook, Mini PC
	ChassisVendor string    `json:"chassis_vendor,omitempty"` // the enclosure's maker
	ChassisSerial string    `json:"chassis_serial,omitempty"` // root only
	Firmware      *Firmware `json:"firmware,omitempty"`       // the BIOS or UEFI firmware
	// MEFirmware is the Intel Management Engine (CSME) firmware, and
	// ECFirmware the embedded controller's: separate controllers with
	// their own firmware.
	MEFirmware *Firmware `json:"me_firmware,omitempty"`
	ECFirmware *Firmware `json:"ec_firmware,omitempty"`
	// ESRT is the UEFI firmware's EFI System Resource Table: the firmware
	// it can update by capsule, each by the GUID LVFS releases name.
	// Absent without UEFI or an ESRT.
	ESRT *ESRT `json:"esrt,omitempty"`
}

// ESRT lists the firmware a UEFI machine can update by capsule. Its
// entries are root-only: without root, Status is "unknown" and Reason says
// so.
type ESRT struct {
	Entries []ESRTEntry `json:"entries,omitempty"`
	Status  string      `json:"status,omitempty"` // "unknown" when the entries couldn't be read
	Reason  string      `json:"reason,omitempty"` // why they couldn't, e.g. "needs --full"
}

// ESRTEntry is one updatable firmware. Versions are the 32-bit numbers the
// firmware reports, before any vendor format is applied.
type ESRTEntry struct {
	FWClass   string `json:"fw_class"`   // the GUID an update names
	FWType    string `json:"fw_type"`    // system, device, uefi-driver, unknown
	FWVersion uint32 `json:"fw_version"` // the installed version
	// LowestSupportedFWVersion is the oldest version the firmware accepts
	// as an update.
	LowestSupportedFWVersion uint32 `json:"lowest_supported_fw_version"`
}

// TPM is the machine's Trusted Platform Module.
type TPM struct {
	// SpecVersionMajor is the TCG specification major version the TPM
	// implements (1 or 2).
	SpecVersionMajor int       `json:"spec_version_major"`
	Firmware         *Firmware `json:"firmware,omitempty"`
}

// Board is the mainboard (SMBIOS type 2).
type Board struct {
	Identity *Identity `json:"identity,omitempty"`
	AssetTag string    `json:"asset_tag,omitempty"` // the owner's inventory tag, if set
	// Slots and OnboardDevices are the firmware's SMBIOS tables (types 9
	// and 41, root only) as it states them. Their addresses can be wrong:
	// compare them with the PCI topology before trusting them.
	Slots          []Slot          `json:"slots,omitempty"`
	OnboardDevices []OnboardDevice `json:"onboard_devices,omitempty"`
}

// CPUPackage is one processor socket's entry (SMBIOS type 4).
type CPUPackage struct {
	Designation string `json:"designation"`       // the socket's label, e.g. U3E1
	Package     string `json:"package,omitempty"` // e.g. "Socket LGA1151", "Socket BGA1528"
	// Mounting is "socket", "slot" (a cartridge) or "onboard" (soldered)
	// when the package's name says so.
	Mounting  string `json:"mounting,omitempty"`
	Populated bool   `json:"populated"` // whether a processor is in it
}

// Slot is an expansion slot the firmware lists (SMBIOS type 9).
type Slot struct {
	Designation string `json:"designation"`      // e.g. "Slot3 / M2 SSD"
	Type        string `json:"type,omitempty"`   // e.g. "PCI Express Gen 3 x4"
	Width       string `json:"width,omitempty"`  // electrical width, e.g. "x4"; the type's xN is the physical one
	Usage       string `json:"usage,omitempty"`  // Available, In use, Unavailable
	Length      string `json:"length,omitempty"` // e.g. "Long Length"
	ID          int    `json:"id"`               // 0 is a valid slot number (ACPI _SUN)
	// Address is the PCI address the firmware gives for the slot, as
	// written; "" when it gives none.
	Address string `json:"address,omitempty"`
}

// OnboardDevice is a device the firmware says is soldered onto the board
// (SMBIOS type 41).
type OnboardDevice struct {
	Designation string `json:"designation"`        // e.g. "Onboard IGD"
	Type        string `json:"type,omitempty"`     // e.g. "Video"
	Enabled     bool   `json:"enabled"`            // whether the firmware has it enabled
	Instance    int    `json:"instance,omitempty"` // tells devices of one type apart
	Address     string `json:"address,omitempty"`  // as for Slot.Address
}

// CPU is the machine's processors, taken together.
type CPU struct {
	Identity *Identity `json:"identity,omitempty"` // vendor = CPUID vendor string, model = brand string
	// Family, ModelID and Stepping are the x86 signature, as the kernel
	// reports it (decimal).
	Family   int `json:"family,omitempty"`
	ModelID  int `json:"model_id,omitempty"` // see family
	Stepping int `json:"stepping,omitempty"` // see family
	// Codename and Microarchitecture come from the cpu ID database.
	Codename          string `json:"codename,omitempty"`
	Microarchitecture string `json:"microarchitecture,omitempty"` // e.g. "Golden Cove"; see codename
	Sockets           int    `json:"sockets"`                     // physical processor packages the kernel sees
	// Packages is what the firmware says each processor socket takes
	// (SMBIOS type 4, root only), and whether that package is socketed
	// or soldered.
	Packages   []CPUPackage `json:"packages,omitempty"`
	Cores      int          `json:"cores"`                  // physical cores, all sockets
	Threads    int          `json:"threads"`                // logical CPUs, all sockets
	CoreTypes  []CoreType   `json:"core_types,omitempty"`   // hybrid CPUs (Intel P/E cores)
	MinFreqMHz int          `json:"min_freq_mhz,omitempty"` // the lowest clock cpufreq allows
	MaxFreqMHz int          `json:"max_freq_mhz,omitempty"` // the highest, boost included
	// Governor is cpufreq's scaling governor (CPU 0's), e.g. powersave.
	Governor       string    `json:"governor,omitempty"`
	Caches         []Cache   `json:"caches"`
	Virtualization string    `json:"virtualization,omitempty"` // vmx, svm
	Flags          []string  `json:"flags"`                    // the kernel's CPU flags (/proc/cpuinfo)
	Firmware       *Firmware `json:"firmware,omitempty"`       // microcode
	Driver         *Driver   `json:"driver,omitempty"`         // frequency scaling
	// Health is from the thermal throttling counters.
	Health *Health `json:"health,omitempty"`
}

// CoreType is one kind of core on a hybrid CPU.
type CoreType struct {
	Name    string `json:"name"`    // performance, efficiency
	Threads int    `json:"threads"` // logical CPUs of this kind
}

// Cache is one level and type of CPU cache.
type Cache struct {
	Level     int    `json:"level"`      // 1, 2, 3
	Type      string `json:"type"`       // Data, Instruction, Unified
	SizeBytes uint64 `json:"size_bytes"` // one instance's size
	Instances int    `json:"instances"`  // how many copies exist (e.g. one L2 per core)
}

// Memory is the machine's RAM: what the kernel sees, and the modules.
type Memory struct {
	// TotalBytes is what the kernel can use (MemTotal), a little less than
	// InstalledBytes because firmware and the kernel reserve some.
	TotalBytes uint64 `json:"total_bytes"`
	SwapBytes  uint64 `json:"swap_bytes"` // swap space (SwapTotal)
	// InstalledBytes is the modules' total, from SMBIOS (root only), as
	// are MaxCapacityBytes, Slots and ECC.
	InstalledBytes   uint64 `json:"installed_bytes,omitempty"`
	MaxCapacityBytes uint64 `json:"max_capacity_bytes,omitempty"` // the most the board takes, as the firmware states
	Slots            int    `json:"slots,omitempty"`              // memory slots on the board
	ECC              string `json:"ecc,omitempty"`                // the error correction in use, e.g. None, Single-bit ECC
	// SlotUsage lists every memory slot the firmware describes (SMBIOS
	// type 17, root only), used or empty. Absent means not read, never
	// "no slots"; it is never inferred from the usable memory.
	SlotUsage []MemorySlot   `json:"slot_usage,omitempty"`
	Modules   []MemoryModule `json:"modules"`
}

// MemorySlot is one memory slot and whether a module is in it.
type MemorySlot struct {
	Locator     string `json:"locator"`                // the slot's label, e.g. DIMM A1
	BankLocator string `json:"bank_locator,omitempty"` // e.g. BANK 0, P0 CHANNEL A
	// Populated is whether a module is in the slot; absent when the
	// firmware doesn't say.
	Populated  *bool  `json:"populated,omitempty"`
	FormFactor string `json:"form_factor,omitempty"` // DIMM, SODIMM...
}

// MemoryModule is one installed memory module, from SMBIOS (root only)
// or, without it, the module's SPD.
type MemoryModule struct {
	// Locator is the slot's label, e.g. DIMM A1; for a module read from
	// SPD alone, "SPD" and its I2C device, e.g. "SPD 0-0050".
	Locator       string `json:"locator"`
	BankLocator   string `json:"bank_locator,omitempty"`          // e.g. BANK 0, P0 CHANNEL A
	SizeBytes     uint64 `json:"size_bytes"`                      // the module's capacity
	Type          string `json:"type,omitempty"`                  // DDR4, DDR5, LPDDR5...
	FormFactor    string `json:"form_factor,omitempty"`           // DIMM, SODIMM...
	SpeedMTs      int    `json:"speed_mts,omitempty"`             // the rated speed, in MT/s
	ConfiguredMTs int    `json:"configured_speed_mts,omitempty"`  // the speed it runs at, in MT/s
	DataWidth     int    `json:"data_width_bits,omitempty"`       // e.g. 64
	TotalWidth    int    `json:"total_width_bits,omitempty"`      // data plus ECC bits, e.g. 72
	Rank          int    `json:"rank,omitempty"`                  // ranks on the module
	VoltageMV     int    `json:"configured_voltage_mv,omitempty"` // the voltage it runs at, in millivolts
	// Identity.Vendor is decoded from a JEDEC code when the firmware gives
	// one; ManufacturerRaw then keeps the firmware's string, e.g.
	// "Unknown - [0xF785]".
	Identity        *Identity `json:"identity,omitempty"`
	ManufacturerRaw string    `json:"manufacturer_raw,omitempty"` // see identity
	// DRAMVendorID is the memory chips' maker, which can differ from the
	// module's (from SPD): its JEDEC code, and DRAMVendor the name
	// resolve fills in.
	DRAMVendorID string    `json:"dram_vendor_id,omitempty"`
	DRAMVendor   string    `json:"dram_vendor,omitempty"` // see dram_vendor_id
	Health       *Health   `json:"health,omitempty"`      // EDAC error counts
	Mounting     *Mounting `json:"mounting,omitempty"`
}

// Disk is one block device: a drive, a card, or a virtual disk.
type Disk struct {
	Name      string `json:"name"`          // nvme0n1, sda
	WWN       string `json:"wwn,omitempty"` // the World Wide Name, a unique ID
	SizeBytes uint64 `json:"size_bytes"`    // the disk's capacity
	// Type is nvme, ssd, hdd, flash, virtual, optical or unknown; it is
	// the authority on whether a disk spins.
	Type      string `json:"type"`
	Transport string `json:"transport"` // nvme, sata, usb, virtio, mmc...
	// Rotational is the kernel's flag. false also means it couldn't be
	// read, so it's not evidence of an SSD: read type.
	Rotational         bool        `json:"rotational"`
	Removable          bool        `json:"removable"`                      // the kernel's flag: removable media
	LogicalBlockBytes  uint64      `json:"logical_block_bytes,omitempty"`  // the sector size addressed
	PhysicalBlockBytes uint64      `json:"physical_block_bytes,omitempty"` // the sector size written
	Partitions         []Partition `json:"partitions"`
	Identity           *Identity   `json:"identity,omitempty"`
	Firmware           *Firmware   `json:"firmware,omitempty"`
	Driver             *Driver     `json:"driver,omitempty"`
	Health             *Health     `json:"health,omitempty"`
	Mounting           *Mounting   `json:"mounting,omitempty"` // eMMC and SD cards
}

// Partition is one partition of a disk.
type Partition struct {
	Name       string `json:"name"`                 // e.g. nvme0n1p1
	SizeBytes  uint64 `json:"size_bytes"`           // the partition's size
	Filesystem string `json:"filesystem,omitempty"` // e.g. ext4, vfat, crypto_LUKS
	Label      string `json:"label,omitempty"`      // the filesystem's label
	UUID       string `json:"uuid,omitempty"`       // the filesystem's UUID (lsblk UUID)
	PartUUID   string `json:"partuuid,omitempty"`   // the partition table entry's UUID (lsblk PARTUUID)
	// MountPoint is where it's mounted; --redact keeps only system ones
	// such as / and /boot.
	MountPoint string `json:"mount_point,omitempty"`
}

// GPU is a display controller (PCI class 03).
type GPU struct {
	PCIAddress string `json:"pci_address"` // e.g. 0000:00:02.0
	VendorID   string `json:"vendor_id"`   // PCI vendor ID, 4 hex digits
	DeviceID   string `json:"device_id"`   // PCI device ID, 4 hex digits
	// Identity.Model is the retail name where known (AMD: libdrm's
	// amdgpu.ids), otherwise the chip name; Chip is always the chip name.
	Chip      string     `json:"chip,omitempty"`
	Subsystem string     `json:"subsystem,omitempty"`  // the card maker's name for it (PCI subsystem)
	VRAMBytes uint64     `json:"vram_bytes,omitempty"` // dedicated memory (amdgpu)
	Clocks    *GPUClocks `json:"clocks,omitempty"`
	// BootVGA is the kernel's boot_vga flag: the GPU the firmware showed
	// the boot screen on. false also means it couldn't be read.
	BootVGA  bool      `json:"boot_vga"`
	DRMCard  string    `json:"drm_card,omitempty"` // e.g. card1
	Link     *PCIeLink `json:"pcie_link,omitempty"`
	Outputs  []string  `json:"outputs"` // connectors, e.g. DP-1, HDMI-A-1
	Identity *Identity `json:"identity,omitempty"`
	// Firmware is the video BIOS (amdgpu, NVIDIA).
	Firmware *Firmware `json:"firmware,omitempty"`
	// FirmwareComponents are the GPU's other firmware, each named: Intel's
	// GuC and HuC (debugfs, --full), amdgpu's blocks (smc, sos, vcn, …).
	FirmwareComponents []Firmware `json:"firmware_components,omitempty"`
	Driver             *Driver    `json:"driver,omitempty"`
}

// Display is a connected monitor or panel, from its EDID.
type Display struct {
	Connector       string  `json:"connector"`                   // card1-DP-1
	ManufacturerID  string  `json:"manufacturer_id,omitempty"`   // PNP ID, e.g. DEL
	WidthMM         int     `json:"width_mm,omitempty"`          // the image's width
	HeightMM        int     `json:"height_mm,omitempty"`         // the image's height
	DiagonalIn      float64 `json:"diagonal_in,omitempty"`       // in inches, from width_mm and height_mm
	NativeWidth     int     `json:"native_width,omitempty"`      // the preferred mode's pixels across
	NativeHeight    int     `json:"native_height,omitempty"`     // the preferred mode's pixels down
	NativeRefreshHz float64 `json:"native_refresh_hz,omitempty"` // the preferred mode's refresh rate
	EDIDVersion     string  `json:"edid_version,omitempty"`      // e.g. "1.4"
	// BestMode is the largest mode (by area) the driver offers this
	// display on its connector, e.g. "3840x2160" (#112). sysfs lists the
	// display's preferred mode first, which isn't always the largest.
	BestMode string `json:"best_mode,omitempty"`
	// ModeCount is how many modes the driver offers it: resolution and
	// timing pairs, so one resolution at several refresh rates counts
	// several times.
	ModeCount int `json:"mode_count,omitempty"`
	// ModelYear is set instead of a manufacture date when the EDID gives
	// the model year only.
	ModelYear int       `json:"model_year,omitempty"`
	Identity  *Identity `json:"identity,omitempty"` // part_number = EDID product code
}

// NIC is a network interface backed by hardware (virtual ones are left
// out).
type NIC struct {
	Name string `json:"name"` // the interface's name, e.g. enp3s0
	// Type is ethernet, wireless, or "other (N)" with the kernel's ARP
	// hardware type N.
	Type       string `json:"type"`
	MAC        string `json:"mac,omitempty"`         // the hardware address, lowercase
	MACVendor  string `json:"mac_vendor,omitempty"`  // registered owner of the MAC prefix
	Bus        string `json:"bus,omitempty"`         // pci, usb
	BusAddress string `json:"bus_address,omitempty"` // the device's address on that bus
	// State is the kernel's operstate when captured: up, down, dormant,
	// unknown...
	State     string    `json:"state"`
	SpeedMbps int       `json:"speed_mbps,omitempty"` // the link's speed, while it's up
	Duplex    string    `json:"duplex,omitempty"`     // full, half; while the link is up
	MTU       int       `json:"mtu,omitempty"`        // in bytes
	Identity  *Identity `json:"identity,omitempty"`
	Firmware  *Firmware `json:"firmware,omitempty"`
	Driver    *Driver   `json:"driver,omitempty"`
	Health    *Health   `json:"health,omitempty"` // error and drop counters since boot
	// Radio is a wireless NIC's radio, as the kernel's nl80211 describes
	// it (#111); absent when it doesn't.
	Radio *WiFiRadio `json:"radio,omitempty"`
}

// WiFiRadio is what a Wi-Fi radio can do, from nl80211 (the interface
// `iw phy` uses).
type WiFiRadio struct {
	// Generation is "Wi-Fi 4" (HT), "Wi-Fi 5" (VHT), "Wi-Fi 6" (HE),
	// "Wi-Fi 6E" (HE with a 6 GHz band) or "Wi-Fi 7" (EHT): the highest
	// capability the radio has; absent for a radio with none of them.
	Generation string `json:"generation,omitempty"`
	// Bands are "2.4 GHz", "5 GHz", "6 GHz" and "60 GHz", as present.
	Bands []string `json:"bands"`
	// TXChains is how many transmit antennas are available, counted from
	// nl80211's bitmask.
	TXChains int `json:"tx_chains,omitempty"`
	// RXChains is how many receive antennas are available, counted from
	// nl80211's bitmask.
	RXChains int `json:"rx_chains,omitempty"`
	// MaxSpatialStreams is the most spatial streams any band's HT, VHT,
	// HE or EHT MCS map allows.
	MaxSpatialStreams int    `json:"max_spatial_streams,omitempty"`
	Source            string `json:"source"` // "nl80211"
}

// BluetoothController is a Bluetooth adapter the kernel knows (hciN).
type BluetoothController struct {
	Name           string `json:"name"`                      // hci0
	Address        string `json:"address,omitempty"`         // the controller's Bluetooth address
	AddressVendor  string `json:"address_vendor,omitempty"`  // registered owner of the address prefix
	ManufacturerID int    `json:"manufacturer_id,omitempty"` // Bluetooth SIG company ID of the chip
	Manufacturer   string `json:"manufacturer,omitempty"`    // that company's name
	Version        string `json:"version,omitempty"`         // core spec version, e.g. "5.2"
	// LocalName is the name it advertises, often the hostname.
	LocalName  string    `json:"local_name,omitempty"`
	Powered    *bool     `json:"powered,omitempty"`     // whether the radio is on; absent if unknown
	Bus        string    `json:"bus,omitempty"`         // usb, pci...
	BusAddress string    `json:"bus_address,omitempty"` // the adapter's address on that bus
	Identity   *Identity `json:"identity,omitempty"`    // the USB/PCI adapter
	Firmware   *Firmware `json:"firmware,omitempty"`
	Driver     *Driver   `json:"driver,omitempty"`
}

// SoundCard is an ALSA sound card (/proc/asound/cards).
type SoundCard struct {
	Index      int          `json:"index"`                 // the card's number, as in cardN
	ID         string       `json:"id"`                    // ALSA's short ID, e.g. PCH
	Name       string       `json:"name"`                  // ALSA's name, e.g. "HDA Intel PCH"
	Bus        string       `json:"bus,omitempty"`         // pci, usb...
	BusAddress string       `json:"bus_address,omitempty"` // the card's address on that bus
	Codecs     []AudioCodec `json:"codecs,omitempty"`
	Driver     *Driver      `json:"driver,omitempty"`
}

// AudioCodec is an HD Audio codec chip, named by the kernel.
type AudioCodec struct {
	VendorID    string    `json:"vendor_id"`              // e.g. 14f15098: PCI vendor 14f1, device 5098
	SubsystemID string    `json:"subsystem_id,omitempty"` // the board maker's ID for its use of the codec
	Identity    *Identity `json:"identity,omitempty"`
}

// Battery is one of the machine's own batteries (power_supply);
// peripherals' batteries are left out.
type Battery struct {
	Name       string `json:"name"`                 // the kernel's name, e.g. BAT0
	Technology string `json:"technology,omitempty"` // e.g. Li-ion
	Status     string `json:"status,omitempty"`     // charging, discharging...
	// CapacityPercent is the charge level (% charged) when captured, not
	// wear: health.metrics.capacity_percent is the full charge against
	// the design capacity.
	CapacityPercent int       `json:"capacity_percent,omitempty"`
	Identity        *Identity `json:"identity,omitempty"`
	Health          *Health   `json:"health,omitempty"`
}

// Sensor is one hardware monitoring chip (hwmon) and its readings.
type Sensor struct {
	Chip     string          `json:"chip"`             // coretemp, nvme, amdgpu...
	Device   string          `json:"device,omitempty"` // the address of the device it measures
	Readings []SensorReading `json:"readings"`
}

// SensorReading is one sensor's value when captured.
type SensorReading struct {
	Label string  `json:"label"`          // the chip's label, or the sensor's name (temp1)
	Kind  string  `json:"kind"`           // temperature, fan, voltage, power, current
	Value float64 `json:"value"`          // in unit
	Unit  string  `json:"unit"`           // C, RPM, V, W, A
	Max   float64 `json:"max,omitempty"`  // the high threshold, in unit
	Crit  float64 `json:"crit,omitempty"` // the critical threshold, in unit
}

// PCIDevice is one PCI function.
type PCIDevice struct {
	Address string `json:"address"` // domain:bus:device.function, e.g. 0000:00:1f.3
	// Parent is the bridge or root port the device sits behind, from the
	// kernel's device path (for a root port behind Intel VMD, the VMD
	// controller). Absent on a platform root bus, and when the path can't
	// be resolved, which the capture warns about.
	Parent      string `json:"parent,omitempty"`
	VendorID    string `json:"vendor_id"`                     // 4 hex digits
	DeviceID    string `json:"device_id"`                     // 4 hex digits
	SubVendorID string `json:"subsystem_vendor_id,omitempty"` // the card or board maker's vendor ID
	SubDeviceID string `json:"subsystem_device_id,omitempty"` // that maker's ID for the product
	// ClassCode is the class, subclass and programming interface, 6 hex
	// digits, e.g. 030000 (VGA).
	ClassCode  string    `json:"class_code"`
	Class      string    `json:"class"`                 // the class code's name, e.g. "VGA compatible controller"
	Subsystem  string    `json:"subsystem,omitempty"`   // the subsystem IDs' name
	IOMMUGroup string    `json:"iommu_group,omitempty"` // the IOMMU group's number; absent without an IOMMU
	Link       *PCIeLink `json:"pcie_link,omitempty"`
	// Label is the firmware's name for the device (sysfs label), and
	// LabelSource where the kernel got it: "smbios" (type 41, an onboard
	// device) or "acpi" (a _DSM name, which says nothing about mounting).
	Label       string `json:"label,omitempty"`
	LabelSource string `json:"label_source,omitempty"` // see label
	// Mounting says whether the device is soldered on or sits in a slot,
	// for storage, network, display, multimedia and wireless devices.
	Mounting *Mounting `json:"mounting,omitempty"`
	Identity *Identity `json:"identity,omitempty"` // vendor, model = device name, revision
	Driver   *Driver   `json:"driver,omitempty"`
	// ModuleCandidates are the kernel modules whose aliases match a
	// driverless device's modalias, in the running kernel's module index:
	// [] when none does. Absent for a device with a driver, and when the
	// index couldn't be read (kernel.module_index says why).
	ModuleCandidates []ModuleCandidate `json:"module_candidates,omitzero"`
}

// ModuleCandidate is a kernel module that claims a device by its alias.
type ModuleCandidate struct {
	Module  string `json:"module"`  // with "_" for "-", as modprobe treats them
	Builtin bool   `json:"builtin"` // built into the kernel: always available
	Loaded  bool   `json:"loaded"`  // built in, or loaded (/sys/module/<m>/initstate "live")
}

// Kernel is what the running kernel provides beyond its version
// (os.kernel).
type Kernel struct {
	// ModuleIndex says where the running kernel's module index is, or
	// what was found instead.
	ModuleIndex *ModuleIndex `json:"module_index,omitempty"`
	// ModuleBlacklist lists the modules kept from loading, by the three
	// ways hwspec reads (#212): modprobe.d's blacklist lines and
	// modprobe.blacklist=, the kernel's module_blacklist=, and modprobe.d
	// install lines that run a no-op instead. Absent: not read (a capture
	// from before it). Empty: none of those name a module; another way to
	// keep one from loading (an install script, a udev rule) isn't read,
	// so it doesn't mean nothing can.
	ModuleBlacklist []BlacklistedModule `json:"module_blacklist,omitzero"`
	// FirmwareFailures are the "Direct firmware load … failed" warnings in
	// the kernel log (/dev/kmsg, --full only). Absent: not read (no root,
	// or a capture from before it). Empty: none logged, though a driver
	// that asked quietly, or a log that wrapped, logs nothing.
	FirmwareFailures []FirmwareFailure `json:"firmware_failures,omitzero"`
}

// FirmwareFailure is a firmware file the kernel log says failed to load,
// for which device and driver (#213).
type FirmwareFailure struct {
	Device string `json:"device"`           // its bus address, e.g. 0000:02:00.0
	Driver string `json:"driver,omitempty"` // the driver that asked for the file
	File   string `json:"file"`             // as requested, e.g. iwlwifi-cc-a0-77.ucode
	Error  int    `json:"error"`            // the loader's negative errno, e.g. -2 (ENOENT)
}

// BlacklistedModule is one module kept from loading, how, and where: the
// modprobe.d file's path, or "cmdline".
type BlacklistedModule struct {
	Module string `json:"module"` // as modprobe names it, "-" read as "_"
	// Kind is how: "blacklist" (modprobe ignores the module's aliases, so
	// a device doesn't load it, but "modprobe <module>" still does),
	// "kernel" (module_blacklist=: the kernel refuses to load it at all),
	// or "install" (modprobe runs a no-op, /bin/false or /bin/true,
	// instead of loading it).
	Kind   string `json:"kind"`
	Source string `json:"source"` // the modprobe.d file's path, or "cmdline"
}

// The kinds of BlacklistedModule.
const (
	BlacklistAlias   = "blacklist"
	BlacklistKernel  = "kernel"
	BlacklistInstall = "install"
)

// ModuleIndex is what the module trees hold for the running kernel, as
// found, not why: /lib/modules, /usr/lib/modules and NixOS's
// /run/booted-system/kernel-modules/lib/modules.
type ModuleIndex struct {
	Release string `json:"release"` // os.kernel
	// Status is "found" (Dir holds its modules.alias), "other_release"
	// (only other kernels' modules are installed: what an upgrade without
	// a reboot leaves, the only evidence of one), or "none" (no module tree
	// at all: a container without the host's, or a kernel built without
	// modules).
	Status        string   `json:"status"`
	Dir           string   `json:"dir,omitempty"`            // the running kernel's module directory, when found
	OtherReleases []string `json:"other_releases,omitempty"` // the kernel releases whose modules are installed instead
}

// The module index statuses.
const (
	ModulesFound        = "found"
	ModulesOtherRelease = "other_release"
	ModulesNone         = "none"
)

// Mounting is whether a part is soldered on or removable, with what
// showed it.
type Mounting struct {
	// Kind is onboard (soldered), socket, slot, or unknown.
	Kind string `json:"kind"`
	// Slot names the slot as the firmware does ("Slot2 / M2 WLAN/BT");
	// SlotType is its type ("PCI Express Gen 3 x1"). When several slots
	// could hold the part, SlotType is set and Slot isn't.
	Slot     string `json:"slot,omitempty"`
	SlotType string `json:"slot_type,omitempty"` // see slot
	// Confidence is high (the evidence names this part), medium (it
	// fits only one way, but the firmware's own link is broken or the
	// type is ambiguous), or absent for unknown.
	Confidence string   `json:"confidence,omitempty"`
	Evidence   []string `json:"evidence,omitempty"` // what showed it, one finding each
	// Reason says why it's unknown, or what lowered the confidence.
	Reason string `json:"reason,omitempty"`
}

// GPUClocks is the graphics core's hardware clock range, and its measured
// clock when the capture was taken: a moment's value, not a sustained one.
// Present only when the driver exposes them without root.
type GPUClocks struct {
	MinFreqMHz int `json:"min_freq_mhz"` // the lowest hardware clock
	MaxFreqMHz int `json:"max_freq_mhz"` // the highest hardware clock
	// ActualFreqMHz is the measured clock. i915 may read 0 while the GPU
	// is in RC6 (by generation: a Gen9 GPU reads its minimum instead);
	// a value below the range is amdgpu's deep-sleep clock. Absent when
	// the driver doesn't report it or reports one outside the range.
	ActualFreqMHz *int `json:"actual_freq_mhz,omitempty"`
	// Source names the interface: "i915" (gt_RPn/RP0/act_freq_mhz) or
	// "amdgpu" (pp_dpm_sclk).
	Source string `json:"source"`
}

// PCIeLink is a PCI Express link's state when captured, and the most the
// device supports. A link below that may be saving power, or be held back
// by the slot.
type PCIeLink struct {
	Speed    string `json:"speed"`     // as the kernel writes it, e.g. "16.0 GT/s PCIe"
	Width    int    `json:"width"`     // lanes in use
	MaxSpeed string `json:"max_speed"` // the fastest the device supports
	MaxWidth int    `json:"max_width"` // the most lanes the device supports
}

// USBDevice is one USB device (root hubs are left out).
type USBDevice struct {
	Bus        int       `json:"bus"`                   // the bus number, as lsusb shows it
	Device     int       `json:"device"`                // the device number on that bus, as lsusb shows it
	Path       string    `json:"path"`                  // sysfs name, e.g. 1-2.3
	VendorID   string    `json:"vendor_id"`             // 4 hex digits
	ProductID  string    `json:"product_id"`            // 4 hex digits
	ClassCode  string    `json:"class_code,omitempty"`  // the device class, 2 hex digits; 00 means per interface
	Class      string    `json:"class,omitempty"`       // the class code's name
	SpeedMbps  float64   `json:"speed_mbps,omitempty"`  // the negotiated speed, e.g. 480 or 5000
	USBVersion string    `json:"usb_version,omitempty"` // the USB version the device declares, e.g. "2.10"
	Identity   *Identity `json:"identity,omitempty"`
	Firmware   *Firmware `json:"firmware,omitempty"` // device release number (bcdDevice)
	Drivers    []Driver  `json:"drivers,omitempty"`  // one per bound interface driver
	// Interfaces are the device's interfaces in its active configuration,
	// each with its own class and driver (#216). Absent: not read (a
	// capture from before them).
	Interfaces []USBInterface `json:"interfaces,omitzero"`
}

// USBInterface is one interface of a USB device: what it is, the driver
// bound to it, and, without one, the modules whose aliases claim it.
type USBInterface struct {
	Name string `json:"name"` // sysfs name, e.g. 1-2:1.0
	// ClassCode is the interface's class, subclass and protocol, hex,
	// e.g. "030102" (HID, boot interface, mouse).
	ClassCode string  `json:"class_code"`
	Modalias  string  `json:"modalias,omitempty"` // the alias modules are matched by, e.g. usb:v046DpC52Bd...
	Driver    *Driver `json:"driver,omitempty"`
	// ModuleCandidates are, for an interface without a driver, the
	// running kernel's modules whose aliases match its modalias, as for
	// a PCI device. Absent: not read.
	ModuleCandidates []ModuleCandidate `json:"module_candidates,omitzero"`
}

// EnsureIdentity returns *p, allocating it if nil, for setters.
func EnsureIdentity(p **Identity) *Identity {
	if *p == nil {
		*p = &Identity{}
	}
	return *p
}

// Empty reports whether an identity has nothing in it.
func (i *Identity) Empty() bool { return i == nil || *i == Identity{} }
