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

const SchemaVersion = 1

type Report struct {
	// Schema is the URL of the format's JSON Schema (package schema), for
	// editors and validators. Captures from before it was added lack it.
	Schema        string    `json:"$schema,omitempty"`
	SchemaVersion int       `json:"schema_version"`
	Tool          Tool      `json:"tool"`
	CapturedAt    time.Time `json:"captured_at"`
	Hostname      string    `json:"hostname"`
	// Privileged is true when the capture ran as root, so the SMBIOS memory
	// list, most serial numbers and drive health could be read. Memory
	// modules' SPD (serials included) is often readable without root.
	Privileged bool `json:"privileged"`
	Redacted   bool `json:"redacted"`

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

type Tool struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// IDDatabases records, per ID database, the source its names came from
	// (e.g. "pci": "/usr/share/hwdata/pci.ids (2026-09-03) + overrides").
	IDDatabases map[string]string `json:"id_databases"`
}

// --- Shared detail blocks (ADR 0008) ---

// Identity names one physical part.
type Identity struct {
	Vendor     string `json:"vendor,omitempty"`
	Model      string `json:"model,omitempty"`
	PartNumber string `json:"part_number,omitempty"`
	Serial     string `json:"serial,omitempty"`
	Revision   string `json:"revision,omitempty"`
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
	Vendor  string `json:"vendor,omitempty"`
	Version string `json:"version,omitempty"`
	Date    string `json:"date,omitempty"`
	Release string `json:"release,omitempty"`
	// Source says where the version was read: dmi, microcode, nvme, scsi
	// (SATA, SAS and USB disks), mmc, ethtool, usb, vbios (amdgpu, and the
	// NVIDIA driver's /proc file), mei, udev (a TPM 2.0, from systemd's
	// tpm2_id), tpm (a TPM 2.0 asked under --full), caps (a TPM 1.2), hci
	// (a Bluetooth controller: version is the LMP subversion, release the HCI
	// revision).
	Source string `json:"source,omitempty"`
	Status string `json:"status,omitempty"` // "unknown" when Version couldn't be read
	Reason string `json:"reason,omitempty"` // why it's unknown, e.g. "needs --full"
}

// FirmwareUnknown is Firmware.Status for a version that couldn't be read.
const FirmwareUnknown = "unknown"

// UnknownFirmware is the block of a part whose firmware version can't be
// read, and why.
func UnknownFirmware(reason string) *Firmware {
	return &Firmware{Status: FirmwareUnknown, Reason: reason}
}

// Known says whether the block holds a version.
func (f *Firmware) Known() bool { return f != nil && f.Version != "" }

// Driver is the kernel driver bound to a part.
type Driver struct {
	Name   string `json:"name"`
	Module string `json:"module,omitempty"` // the kernel module providing it, if not built in
	// Version is the module's own version; in-tree modules usually have
	// none, and match the kernel.
	Version    string `json:"version,omitempty"`
	SrcVersion string `json:"srcversion,omitempty"`
	Builtin    bool   `json:"builtin,omitempty"`
	// From the module's taint flags (O, P, E). Unsigned is set only when the
	// kernel exposes module.sig_enforce; unpatched kernels before 5.13 can
	// expose it without module signing.
	InTree      *bool `json:"in_tree,omitempty"`
	Proprietary *bool `json:"proprietary,omitempty"`
	Unsigned    *bool `json:"unsigned,omitempty"`
}

// Health describes a part's condition and wear.
type Health struct {
	Status  string   `json:"status"` // ok, warning, failing, unknown
	Reasons []string `json:"reasons,omitempty"`
	Source  string   `json:"source,omitempty"`
	// Only from the part's own wear indicator (NVMe percentage used, SSD
	// endurance, battery capacity versus design).
	LifeUsedPercent      *float64  `json:"life_used_percent,omitempty"`
	LifeRemainingPercent *float64  `json:"life_remaining_percent,omitempty"`
	Estimate             *Estimate `json:"estimate,omitempty"`
	// Measurements, by the Metric* names below.
	Metrics map[string]float64 `json:"metrics,omitempty"`
}

// Estimate is a derived figure; Method says how it was computed.
type Estimate struct {
	What   string  `json:"what"` // e.g. "cycles until 80% capacity"
	Value  float64 `json:"value"`
	Unit   string  `json:"unit"`
	Method string  `json:"method"`
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

// --- Devices ---

type OS struct {
	Name           string `json:"name"`
	ID             string `json:"id"`
	IDLike         string `json:"id_like,omitempty"`
	Version        string `json:"version,omitempty"`
	PrettyName     string `json:"pretty_name"`
	Kernel         string `json:"kernel"`
	Arch           string `json:"arch"`
	Init           string `json:"init,omitempty"`
	Virtualization string `json:"virtualization"` // none, vm, container; "" if unknown
	BootMode       string `json:"boot_mode"`      // uefi, bios; "" if unknown
	SecureBoot     *bool  `json:"secure_boot,omitempty"`
}

// System is the machine as a whole; its firmware is the BIOS/UEFI.
type System struct {
	Identity      *Identity `json:"identity,omitempty"` // model = product, part_number = SKU
	Family        string    `json:"family,omitempty"`
	UUID          string    `json:"uuid,omitempty"`
	ChassisType   string    `json:"chassis_type"`
	ChassisVendor string    `json:"chassis_vendor,omitempty"`
	ChassisSerial string    `json:"chassis_serial,omitempty"`
	Firmware      *Firmware `json:"firmware,omitempty"`
	// MEFirmware is the Intel Management Engine (CSME) firmware, and
	// ECFirmware the embedded controller's: separate controllers with
	// their own firmware.
	MEFirmware *Firmware `json:"me_firmware,omitempty"`
	ECFirmware *Firmware `json:"ec_firmware,omitempty"`
}

// TPM is the machine's Trusted Platform Module.
type TPM struct {
	// SpecVersionMajor is the TCG specification major version the TPM
	// implements (1 or 2).
	SpecVersionMajor int       `json:"spec_version_major"`
	Firmware         *Firmware `json:"firmware,omitempty"`
}

type Board struct {
	Identity *Identity `json:"identity,omitempty"`
	AssetTag string    `json:"asset_tag,omitempty"`
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
	Populated bool   `json:"populated"`
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
	Designation string `json:"designation"`    // e.g. "Onboard IGD"
	Type        string `json:"type,omitempty"` // e.g. "Video"
	Enabled     bool   `json:"enabled"`
	Instance    int    `json:"instance,omitempty"`
	Address     string `json:"address,omitempty"` // as for Slot.Address
}

type CPU struct {
	Identity *Identity `json:"identity,omitempty"` // vendor = CPUID vendor string, model = brand string
	// x86 signature (family and model as the kernel reports them, decimal).
	Family   int `json:"family,omitempty"`
	ModelID  int `json:"model_id,omitempty"`
	Stepping int `json:"stepping,omitempty"`
	// Codename and Microarchitecture come from the cpu ID database.
	Codename          string `json:"codename,omitempty"`
	Microarchitecture string `json:"microarchitecture,omitempty"`
	Sockets           int    `json:"sockets"`
	// Packages is what the firmware says each processor socket takes
	// (SMBIOS type 4, root only), and whether that package is socketed
	// or soldered.
	Packages       []CPUPackage `json:"packages,omitempty"`
	Cores          int          `json:"cores"`
	Threads        int          `json:"threads"`
	CoreTypes      []CoreType   `json:"core_types,omitempty"` // hybrid CPUs (Intel P/E cores)
	MinFreqMHz     int          `json:"min_freq_mhz,omitempty"`
	MaxFreqMHz     int          `json:"max_freq_mhz,omitempty"`
	Governor       string       `json:"governor,omitempty"`
	Caches         []Cache      `json:"caches"`
	Virtualization string       `json:"virtualization,omitempty"` // vmx, svm
	Flags          []string     `json:"flags"`
	Firmware       *Firmware    `json:"firmware,omitempty"` // microcode
	Driver         *Driver      `json:"driver,omitempty"`   // frequency scaling
	Health         *Health      `json:"health,omitempty"`
}

type CoreType struct {
	Name    string `json:"name"` // performance, efficiency
	Threads int    `json:"threads"`
}

type Cache struct {
	Level     int    `json:"level"`
	Type      string `json:"type"` // Data, Instruction, Unified
	SizeBytes uint64 `json:"size_bytes"`
	Instances int    `json:"instances"` // how many copies exist (e.g. one L2 per core)
}

type Memory struct {
	// TotalBytes is what the kernel can use (MemTotal), a little less than
	// InstalledBytes because firmware and the kernel reserve some.
	TotalBytes uint64 `json:"total_bytes"`
	SwapBytes  uint64 `json:"swap_bytes"`
	// From SMBIOS (root only).
	InstalledBytes   uint64 `json:"installed_bytes,omitempty"`
	MaxCapacityBytes uint64 `json:"max_capacity_bytes,omitempty"`
	Slots            int    `json:"slots,omitempty"`
	ECC              string `json:"ecc,omitempty"`
	// SlotUsage lists every memory slot the firmware describes (SMBIOS
	// type 17, root only), used or empty. Absent means not read, never
	// "no slots"; it is never inferred from the usable memory.
	SlotUsage []MemorySlot   `json:"slot_usage,omitempty"`
	Modules   []MemoryModule `json:"modules"`
}

// MemorySlot is one memory slot and whether a module is in it.
type MemorySlot struct {
	Locator     string `json:"locator"`
	BankLocator string `json:"bank_locator,omitempty"`
	// Populated is whether a module is in the slot; absent when the
	// firmware doesn't say.
	Populated  *bool  `json:"populated,omitempty"`
	FormFactor string `json:"form_factor,omitempty"`
}

type MemoryModule struct {
	Locator       string `json:"locator"`
	BankLocator   string `json:"bank_locator,omitempty"`
	SizeBytes     uint64 `json:"size_bytes"`
	Type          string `json:"type,omitempty"`        // DDR4, DDR5, LPDDR5...
	FormFactor    string `json:"form_factor,omitempty"` // DIMM, SODIMM...
	SpeedMTs      int    `json:"speed_mts,omitempty"`
	ConfiguredMTs int    `json:"configured_speed_mts,omitempty"`
	DataWidth     int    `json:"data_width_bits,omitempty"`
	TotalWidth    int    `json:"total_width_bits,omitempty"`
	Rank          int    `json:"rank,omitempty"`
	VoltageMV     int    `json:"configured_voltage_mv,omitempty"`
	// Identity.Vendor is decoded from a JEDEC code when the firmware gives
	// one; ManufacturerRaw then keeps the firmware's string, e.g.
	// "Unknown - [0xF785]".
	Identity        *Identity `json:"identity,omitempty"`
	ManufacturerRaw string    `json:"manufacturer_raw,omitempty"`
	// The memory chips' maker, which can differ from the module's (from
	// SPD): the JEDEC code and, filled in by resolve, its name.
	DRAMVendorID string    `json:"dram_vendor_id,omitempty"`
	DRAMVendor   string    `json:"dram_vendor,omitempty"`
	Health       *Health   `json:"health,omitempty"`
	Mounting     *Mounting `json:"mounting,omitempty"`
}

type Disk struct {
	Name               string      `json:"name"` // nvme0n1, sda
	WWN                string      `json:"wwn,omitempty"`
	SizeBytes          uint64      `json:"size_bytes"`
	Type               string      `json:"type"`      // nvme, ssd, hdd, flash, virtual, optical, unknown
	Transport          string      `json:"transport"` // nvme, sata, usb, virtio, mmc...
	Rotational         bool        `json:"rotational"`
	Removable          bool        `json:"removable"`
	LogicalBlockBytes  uint64      `json:"logical_block_bytes,omitempty"`
	PhysicalBlockBytes uint64      `json:"physical_block_bytes,omitempty"`
	Partitions         []Partition `json:"partitions"`
	Identity           *Identity   `json:"identity,omitempty"`
	Firmware           *Firmware   `json:"firmware,omitempty"`
	Driver             *Driver     `json:"driver,omitempty"`
	Health             *Health     `json:"health,omitempty"`
	Mounting           *Mounting   `json:"mounting,omitempty"` // eMMC and SD cards
}

type Partition struct {
	Name       string `json:"name"`
	SizeBytes  uint64 `json:"size_bytes"`
	Filesystem string `json:"filesystem,omitempty"`
	Label      string `json:"label,omitempty"`    // the filesystem's label
	UUID       string `json:"uuid,omitempty"`     // the filesystem's UUID (lsblk UUID)
	PartUUID   string `json:"partuuid,omitempty"` // the partition table entry's UUID (lsblk PARTUUID)
	MountPoint string `json:"mount_point,omitempty"`
}

type GPU struct {
	PCIAddress string `json:"pci_address"`
	VendorID   string `json:"vendor_id"`
	DeviceID   string `json:"device_id"`
	// Identity.Model is the retail name where known (AMD: libdrm's
	// amdgpu.ids), otherwise the chip name; Chip is always the chip name.
	Chip      string     `json:"chip,omitempty"`
	Subsystem string     `json:"subsystem,omitempty"`
	VRAMBytes uint64     `json:"vram_bytes,omitempty"`
	Clocks    *GPUClocks `json:"clocks,omitempty"`
	BootVGA   bool       `json:"boot_vga"`
	DRMCard   string     `json:"drm_card,omitempty"`
	Link      *PCIeLink  `json:"pcie_link,omitempty"`
	Outputs   []string   `json:"outputs"` // connectors, e.g. DP-1, HDMI-A-1
	Identity  *Identity  `json:"identity,omitempty"`
	Firmware  *Firmware  `json:"firmware,omitempty"`
	Driver    *Driver    `json:"driver,omitempty"`
}

type Display struct {
	Connector       string  `json:"connector"`                 // card1-DP-1
	ManufacturerID  string  `json:"manufacturer_id,omitempty"` // PNP ID, e.g. DEL
	WidthMM         int     `json:"width_mm,omitempty"`
	HeightMM        int     `json:"height_mm,omitempty"`
	DiagonalIn      float64 `json:"diagonal_in,omitempty"`
	NativeWidth     int     `json:"native_width,omitempty"`
	NativeHeight    int     `json:"native_height,omitempty"`
	NativeRefreshHz float64 `json:"native_refresh_hz,omitempty"`
	EDIDVersion     string  `json:"edid_version,omitempty"`
	// ModelYear is set instead of a manufacture date when the EDID gives
	// the model year only.
	ModelYear int       `json:"model_year,omitempty"`
	Identity  *Identity `json:"identity,omitempty"` // part_number = EDID product code
}

type NIC struct {
	Name       string    `json:"name"`
	Type       string    `json:"type"` // ethernet, wireless, other
	MAC        string    `json:"mac,omitempty"`
	MACVendor  string    `json:"mac_vendor,omitempty"` // registered owner of the MAC prefix
	Bus        string    `json:"bus,omitempty"`        // pci, usb
	BusAddress string    `json:"bus_address,omitempty"`
	State      string    `json:"state"`
	SpeedMbps  int       `json:"speed_mbps,omitempty"`
	Duplex     string    `json:"duplex,omitempty"`
	MTU        int       `json:"mtu,omitempty"`
	Identity   *Identity `json:"identity,omitempty"`
	Firmware   *Firmware `json:"firmware,omitempty"`
	Driver     *Driver   `json:"driver,omitempty"`
	Health     *Health   `json:"health,omitempty"`
}

type BluetoothController struct {
	Name string `json:"name"` // hci0
	// Address and its registered owner.
	Address        string    `json:"address,omitempty"`
	AddressVendor  string    `json:"address_vendor,omitempty"`
	ManufacturerID int       `json:"manufacturer_id,omitempty"` // Bluetooth SIG company ID of the chip
	Manufacturer   string    `json:"manufacturer,omitempty"`
	Version        string    `json:"version,omitempty"` // core spec version, e.g. "5.2"
	LocalName      string    `json:"local_name,omitempty"`
	Powered        *bool     `json:"powered,omitempty"`
	Bus            string    `json:"bus,omitempty"`
	BusAddress     string    `json:"bus_address,omitempty"`
	Identity       *Identity `json:"identity,omitempty"` // the USB/PCI adapter
	Firmware       *Firmware `json:"firmware,omitempty"`
	Driver         *Driver   `json:"driver,omitempty"`
}

type SoundCard struct {
	Index      int          `json:"index"`
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	Bus        string       `json:"bus,omitempty"`
	BusAddress string       `json:"bus_address,omitempty"`
	Codecs     []AudioCodec `json:"codecs,omitempty"`
	Driver     *Driver      `json:"driver,omitempty"`
}

// AudioCodec is an HD Audio codec chip, named by the kernel.
type AudioCodec struct {
	VendorID    string    `json:"vendor_id"` // e.g. 14f15098: PCI vendor 14f1, device 5098
	SubsystemID string    `json:"subsystem_id,omitempty"`
	Identity    *Identity `json:"identity,omitempty"`
}

type Battery struct {
	Name            string    `json:"name"`
	Technology      string    `json:"technology,omitempty"`
	Status          string    `json:"status,omitempty"` // charging, discharging...
	CapacityPercent int       `json:"capacity_percent,omitempty"`
	Identity        *Identity `json:"identity,omitempty"`
	Health          *Health   `json:"health,omitempty"`
}

type Sensor struct {
	Chip     string          `json:"chip"` // coretemp, nvme, amdgpu...
	Device   string          `json:"device,omitempty"`
	Readings []SensorReading `json:"readings"`
}

type SensorReading struct {
	Label string  `json:"label"`
	Kind  string  `json:"kind"` // temperature, fan, voltage, power, current
	Value float64 `json:"value"`
	Unit  string  `json:"unit"` // C, RPM, V, W, A
	Max   float64 `json:"max,omitempty"`
	Crit  float64 `json:"crit,omitempty"`
}

type PCIDevice struct {
	Address string `json:"address"`
	// Parent is the bridge or root port the device sits behind, from the
	// kernel's device path (for a root port behind Intel VMD, the VMD
	// controller). Absent on a platform root bus, and when the path can't
	// be resolved, which the capture warns about.
	Parent      string    `json:"parent,omitempty"`
	VendorID    string    `json:"vendor_id"`
	DeviceID    string    `json:"device_id"`
	SubVendorID string    `json:"subsystem_vendor_id,omitempty"`
	SubDeviceID string    `json:"subsystem_device_id,omitempty"`
	ClassCode   string    `json:"class_code"`
	Class       string    `json:"class"`
	Subsystem   string    `json:"subsystem,omitempty"`
	IOMMUGroup  string    `json:"iommu_group,omitempty"`
	Link        *PCIeLink `json:"pcie_link,omitempty"`
	// Label is the firmware's name for the device (sysfs label), and
	// LabelSource where the kernel got it: "smbios" (type 41, an onboard
	// device) or "acpi" (a _DSM name, which says nothing about mounting).
	Label       string `json:"label,omitempty"`
	LabelSource string `json:"label_source,omitempty"`
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
}

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
	Dir           string   `json:"dir,omitempty"`
	OtherReleases []string `json:"other_releases,omitempty"`
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
	SlotType string `json:"slot_type,omitempty"`
	// Confidence is high (the evidence names this part), medium (it
	// fits only one way, but the firmware's own link is broken or the
	// type is ambiguous), or absent for unknown.
	Confidence string   `json:"confidence,omitempty"`
	Evidence   []string `json:"evidence,omitempty"`
	// Reason says why it's unknown, or what lowered the confidence.
	Reason string `json:"reason,omitempty"`
}

// GPUClocks is the graphics core's hardware clock range, and its measured
// clock when the capture was taken: a moment's value, not a sustained one.
// Present only when the driver exposes them without root.
type GPUClocks struct {
	MinFreqMHz int `json:"min_freq_mhz"`
	MaxFreqMHz int `json:"max_freq_mhz"`
	// ActualFreqMHz is the measured clock. i915 may read 0 while the GPU
	// is in RC6 (by generation: a Gen9 GPU reads its minimum instead);
	// a value below the range is amdgpu's deep-sleep clock. Absent when
	// the driver doesn't report it or reports one outside the range.
	ActualFreqMHz *int `json:"actual_freq_mhz,omitempty"`
	// Source names the interface: "i915" (gt_RPn/RP0/act_freq_mhz) or
	// "amdgpu" (pp_dpm_sclk).
	Source string `json:"source"`
}

type PCIeLink struct {
	Speed    string `json:"speed"`
	Width    int    `json:"width"`
	MaxSpeed string `json:"max_speed"`
	MaxWidth int    `json:"max_width"`
}

type USBDevice struct {
	Bus        int       `json:"bus"`
	Device     int       `json:"device"`
	Path       string    `json:"path"` // sysfs name, e.g. 1-2.3
	VendorID   string    `json:"vendor_id"`
	ProductID  string    `json:"product_id"`
	ClassCode  string    `json:"class_code,omitempty"`
	Class      string    `json:"class,omitempty"`
	SpeedMbps  float64   `json:"speed_mbps,omitempty"`
	USBVersion string    `json:"usb_version,omitempty"`
	Identity   *Identity `json:"identity,omitempty"`
	Firmware   *Firmware `json:"firmware,omitempty"` // device release number (bcdDevice)
	Drivers    []Driver  `json:"drivers,omitempty"`  // one per bound interface driver
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
