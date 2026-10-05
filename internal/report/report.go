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
	SchemaVersion int       `json:"schema_version"`
	Tool          Tool      `json:"tool"`
	CapturedAt    time.Time `json:"captured_at"`
	Hostname      string    `json:"hostname"`
	// Privileged is true when the capture ran as root, so serial numbers,
	// memory modules (SMBIOS, SPD) and drive health could be read.
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
	Sensors   []Sensor              `json:"sensors"`
	PCI       []PCIDevice           `json:"pci"`
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

// Firmware is the firmware a part runs, as the part or its driver reports it.
type Firmware struct {
	Vendor  string `json:"vendor,omitempty"`
	Version string `json:"version"`
	Date    string `json:"date,omitempty"`
	Release string `json:"release,omitempty"`
	// Source says where the version was read: dmi, microcode, nvme, sata,
	// ethtool, usb, vbios, nvidia.
	Source string `json:"source"`
}

// Driver is the kernel driver bound to a part.
type Driver struct {
	Name   string `json:"name"`
	Module string `json:"module,omitempty"` // the kernel module providing it, if not built in
	// Version is the module's own version; in-tree modules usually have
	// none, and match the kernel.
	Version    string `json:"version,omitempty"`
	SrcVersion string `json:"srcversion,omitempty"`
	Builtin    bool   `json:"builtin,omitempty"`
	// From the module's taint flags (O, P, E).
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
}

type Board struct {
	Identity *Identity `json:"identity,omitempty"`
	AssetTag string    `json:"asset_tag,omitempty"`
}

type CPU struct {
	Identity *Identity `json:"identity,omitempty"` // vendor = CPUID vendor string, model = brand string
	// x86 signature (family and model as the kernel reports them, decimal).
	Family   int `json:"family,omitempty"`
	ModelID  int `json:"model_id,omitempty"`
	Stepping int `json:"stepping,omitempty"`
	// Codename and Microarchitecture come from the cpu ID database.
	Codename          string     `json:"codename,omitempty"`
	Microarchitecture string     `json:"microarchitecture,omitempty"`
	Sockets           int        `json:"sockets"`
	Cores             int        `json:"cores"`
	Threads           int        `json:"threads"`
	CoreTypes         []CoreType `json:"core_types,omitempty"` // hybrid CPUs (Intel P/E cores)
	MinFreqMHz        int        `json:"min_freq_mhz,omitempty"`
	MaxFreqMHz        int        `json:"max_freq_mhz,omitempty"`
	Governor          string     `json:"governor,omitempty"`
	Caches            []Cache    `json:"caches"`
	Virtualization    string     `json:"virtualization,omitempty"` // vmx, svm
	Flags             []string   `json:"flags"`
	Firmware          *Firmware  `json:"firmware,omitempty"` // microcode
	Driver            *Driver    `json:"driver,omitempty"`   // frequency scaling
	Health            *Health    `json:"health,omitempty"`
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
	InstalledBytes   uint64         `json:"installed_bytes,omitempty"`
	MaxCapacityBytes uint64         `json:"max_capacity_bytes,omitempty"`
	Slots            int            `json:"slots,omitempty"`
	ECC              string         `json:"ecc,omitempty"`
	Modules          []MemoryModule `json:"modules"`
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
	DRAMVendorID string  `json:"dram_vendor_id,omitempty"`
	DRAMVendor   string  `json:"dram_vendor,omitempty"`
	Health       *Health `json:"health,omitempty"`
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
}

type Partition struct {
	Name       string `json:"name"`
	SizeBytes  uint64 `json:"size_bytes"`
	Filesystem string `json:"filesystem,omitempty"`
	Label      string `json:"label,omitempty"`
	UUID       string `json:"uuid,omitempty"`
	MountPoint string `json:"mount_point,omitempty"`
}

type GPU struct {
	PCIAddress string `json:"pci_address"`
	VendorID   string `json:"vendor_id"`
	DeviceID   string `json:"device_id"`
	// Identity.Model is the retail name where known (AMD: libdrm's
	// amdgpu.ids), otherwise the chip name; Chip is always the chip name.
	Chip      string    `json:"chip,omitempty"`
	Subsystem string    `json:"subsystem,omitempty"`
	VRAMBytes uint64    `json:"vram_bytes,omitempty"`
	BootVGA   bool      `json:"boot_vga"`
	DRMCard   string    `json:"drm_card,omitempty"`
	Link      *PCIeLink `json:"pcie_link,omitempty"`
	Outputs   []string  `json:"outputs"` // connectors, e.g. DP-1, HDMI-A-1
	Identity  *Identity `json:"identity,omitempty"`
	Firmware  *Firmware `json:"firmware,omitempty"`
	Driver    *Driver   `json:"driver,omitempty"`
}

type Display struct {
	Connector       string    `json:"connector"`                 // card1-DP-1
	ManufacturerID  string    `json:"manufacturer_id,omitempty"` // PNP ID, e.g. DEL
	WidthMM         int       `json:"width_mm,omitempty"`
	HeightMM        int       `json:"height_mm,omitempty"`
	DiagonalIn      float64   `json:"diagonal_in,omitempty"`
	NativeWidth     int       `json:"native_width,omitempty"`
	NativeHeight    int       `json:"native_height,omitempty"`
	NativeRefreshHz float64   `json:"native_refresh_hz,omitempty"`
	EDIDVersion     string    `json:"edid_version,omitempty"`
	Identity        *Identity `json:"identity,omitempty"` // part_number = EDID product code
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
	Address     string    `json:"address"`
	VendorID    string    `json:"vendor_id"`
	DeviceID    string    `json:"device_id"`
	SubVendorID string    `json:"subsystem_vendor_id,omitempty"`
	SubDeviceID string    `json:"subsystem_device_id,omitempty"`
	ClassCode   string    `json:"class_code"`
	Class       string    `json:"class"`
	Subsystem   string    `json:"subsystem,omitempty"`
	IOMMUGroup  string    `json:"iommu_group,omitempty"`
	Link        *PCIeLink `json:"pcie_link,omitempty"`
	Identity    *Identity `json:"identity,omitempty"` // vendor, model = device name, revision
	Driver      *Driver   `json:"driver,omitempty"`
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
