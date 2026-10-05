package collect

import (
	"strings"

	"github.com/jaypipes/ghw"

	"github.com/jiegui2025/hwspec/internal/report"
)

func (c *collector) storage() {
	c.r.Storage = []report.Disk{}
	info, err := ghw.Block(ghw.WithDisableWarnings(), ghw.WithDisableTools())
	if err != nil {
		c.warn("storage: %v", err)
		return
	}
	needRoot := false
	for _, d := range info.Disks {
		if skipDisk(d.Name) || d.SizeBytes == 0 {
			continue
		}
		base := "/sys/block/" + d.Name
		disk := report.Disk{
			Name:               d.Name,
			WWN:                clean(d.WWN),
			SizeBytes:          d.SizeBytes,
			Transport:          transport(d.Name),
			Rotational:         readStr(base+"/queue/rotational") == "1",
			Type:               "unknown",
			Removable:          d.IsRemovable,
			LogicalBlockBytes:  readUint(base + "/queue/logical_block_size"),
			PhysicalBlockBytes: d.PhysicalBlockSizeBytes,
			Partitions:         []report.Partition{},
			Driver:             controllerDriver(base + "/device"),
		}
		// ghw takes model/serial from the udev database, which is missing in
		// containers and on some minimal systems; sysfs has them too.
		id := &report.Identity{Vendor: clean(d.Vendor), Model: clean(d.Model), Serial: clean(d.SerialNumber)}
		if id.Model == "" {
			id.Model = readStr(base + "/device/model")
		}
		if id.Vendor == "" {
			id.Vendor = readStr(base + "/device/vendor")
		}
		if id.Serial == "" {
			id.Serial = readStr(base + "/device/serial")
		}
		if !id.Empty() {
			disk.Identity = id
		}
		disk.Firmware = firmwareVersion(readStr(base+"/device/firmware_rev"), "nvme")
		if disk.Firmware == nil {
			disk.Firmware = firmwareVersion(readStr(base+"/device/rev"), "scsi")
		}
		// Only claim a type the kernel gives evidence for.
		switch rot := readStr(base + "/queue/rotational"); {
		case disk.Transport == "nvme":
			disk.Type = "nvme"
		case strings.HasPrefix(d.Name, "sr"):
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
		for _, part := range d.Partitions {
			disk.Partitions = append(disk.Partitions, report.Partition{
				Name:       part.Name,
				SizeBytes:  part.SizeBytes,
				Filesystem: clean(part.Type),
				Label:      clean(part.FilesystemLabel),
				UUID:       clean(part.UUID),
				MountPoint: part.MountPoint,
			})
		}
		switch {
		case disk.Type == "optical":
			// no SMART on optical drives
		case c.privileged:
			disk.Health = c.diskHealth(d.Name, disk.Transport)
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

func clean(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "_", " "))
	if s == "unknown" {
		return ""
	}
	return s
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
