package collect

import (
	"strconv"
	"strings"

	"github.com/jiegui2025/hwspec/internal/report"
)

func (c *collector) storage() {
	c.r.Storage = []report.Disk{}
	mounted := mounts()
	needRoot := false
	for _, name := range list("/sys/block") {
		base := "/sys/block/" + name
		size := readUint(base+"/size") * 512 // always in 512-byte sectors
		if skipDisk(name) || size == 0 {
			continue
		}
		u := udevProps(base)
		disk := report.Disk{
			Name:               name,
			WWN:                firstOf(u["ID_WWN_WITH_EXTENSION"], u["ID_WWN"]),
			SizeBytes:          size,
			Transport:          transport(name),
			Rotational:         readStr(base+"/queue/rotational") == "1",
			Type:               "unknown",
			Removable:          readStr(base+"/removable") == "1",
			LogicalBlockBytes:  readUint(base + "/queue/logical_block_size"),
			PhysicalBlockBytes: readUint(base + "/queue/physical_block_size"),
			Partitions:         []report.Partition{},
			Driver:             c.controllerDriver(base + "/device"),
		}
		// The udev database has the full model (ATA's is 40 characters, the
		// SCSI inquiry's in sysfs 16); sysfs is the fallback without udev.
		id := &report.Identity{
			Vendor: readStr(base + "/device/vendor"),
			Model:  firstOf(udevValue(u, "ID_MODEL"), readStr(base+"/device/model")),
			Serial: firstOf(u["ID_SCSI_SERIAL"], u["ID_SERIAL_SHORT"], u["ID_SERIAL"], readStr(base+"/device/serial")),
		}
		if !id.Empty() {
			disk.Identity = id
		}
		// Only claim a type the kernel gives evidence for.
		switch rot := readStr(base + "/queue/rotational"); {
		case disk.Transport == "nvme":
			disk.Type = "nvme"
		case strings.HasPrefix(name, "sr"):
			disk.Type = "optical"
		case disk.Transport == "mmc":
			disk.Type = "flash"
		case disk.Transport == "virtio" || disk.Transport == "xen":
			disk.Type = "virtual" // the backing storage is invisible to the guest
		case rot == "1":
			disk.Type = "hdd"
		case rot == "0":
			disk.Type = "ssd"
		}
		// A virtual disk's firmware is the host's; md RAID, zvols and other
		// block devices without a device link aren't drives.
		if disk.Type != "virtual" && exists(base+"/device") {
			disk.Firmware = diskFirmware(base, disk.Transport)
			if disk.Transport == "nvme" {
				disk.Firmware.InstanceIDs = nvmeInstanceIDs(base + "/device")
			}
		}
		for _, part := range partitionNames(base, name) {
			dir := base + "/" + part
			pu := udevProps(dir)
			m := mounted["/dev/"+part]
			disk.Partitions = append(disk.Partitions, report.Partition{
				Name:       part,
				SizeBytes:  readUint(dir+"/size") * 512,
				Filesystem: firstOf(pu["ID_FS_TYPE"], m.fstype),
				Label:      udevValue(pu, "ID_FS_LABEL"),
				UUID:       udevValue(pu, "ID_FS_UUID"),
				PartUUID:   firstOf(pu["ID_PART_ENTRY_UUID"], ueventValue(dir, "PARTUUID")),
				MountPoint: m.point,
			})
		}
		switch {
		case disk.Type == "optical" || disk.Type == "virtual" || disk.Transport == "mmc":
			// no SMART on optical drives, virtual (virtio, Xen) disks or
			// eMMC/SD cards, as root or not (diskHealth)
		case c.privileged:
			disk.Health = c.diskHealth(name, disk.Transport)
		default:
			needRoot = true
		}
		c.r.Storage = append(c.r.Storage, disk)
	}
	if needRoot {
		c.warn("drive health (SMART): needs root (run with --full)")
	}
}

func skipDisk(name string) bool {
	for _, prefix := range []string{"loop", "zram", "ram", "dm-", "nbd"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// transport works out how a disk is attached from its sysfs device path.
func transport(name string) string {
	switch {
	case strings.HasPrefix(name, "nvme"):
		return "nvme"
	case strings.HasPrefix(name, "mmcblk"):
		return "mmc"
	case strings.HasPrefix(name, "vd"):
		return "virtio"
	case strings.HasPrefix(name, "xvd"):
		return "xen"
	}
	bus, _ := busOf("/sys/block/" + name + "/device")
	path := realPath("/sys/block/" + name)
	switch {
	case strings.Contains(path, "/usb"):
		return "usb"
	case strings.Contains(path, "/ata"):
		return "sata"
	case bus != "":
		return bus
	}
	return "unknown"
}

// diskFirmware reads a drive's firmware revision from its transport's
// attribute: an eMMC's fwrev (its rev is the EXT_CSD revision), an NVMe
// drive's firmware_rev, a SCSI, SATA or USB disk's rev. Without one, the
// block says why.
func diskFirmware(base, transport string) *report.Firmware {
	attrs := [][2]string{{"firmware_rev", "nvme"}, {"rev", "scsi"}}
	if transport == "mmc" {
		attrs = [][2]string{{"fwrev", "mmc"}}
	}
	reported := ""
	for _, a := range attrs {
		v := readStr(base + "/device/" + a[0])
		if fw := firmwareVersion(v, a[1]); fw != nil {
			return fw
		}
		if reported == "" {
			reported = v
		}
	}
	if reported != "" {
		return report.UnknownFirmware("the drive reports " + strconv.Quote(reported))
	}
	return report.UnknownFirmware("the kernel doesn't expose this drive's firmware revision")
}
