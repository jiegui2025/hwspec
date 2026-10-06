package collect

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/jiegui2025/hwspec/internal/report"
	"github.com/jiegui2025/hwspec/internal/smbios"
)

func (c *collector) osInfo() {
	o := &c.r.OS
	rel := parseOSRelease(readStr("/etc/os-release"))
	if len(rel) == 0 {
		rel = parseOSRelease(readStr("/usr/lib/os-release"))
	}
	if len(rel) == 0 {
		c.warn("os-release: not found")
	}
	o.Name = rel["NAME"]
	o.ID = rel["ID"]
	o.IDLike = rel["ID_LIKE"]
	o.Version = rel["VERSION_ID"]
	o.PrettyName = rel["PRETTY_NAME"]

	release, machine := uname()
	o.Kernel = readStr("/proc/sys/kernel/osrelease")
	if o.Kernel == "" {
		o.Kernel = release
	}
	if o.Kernel == "" {
		c.warn("kernel version: /proc/sys/kernel/osrelease unreadable and uname failed")
	}
	o.Arch = machine
	o.Init = readStr("/proc/1/comm")

	// Only report what there is evidence for; "" means unknown.
	flags := c.cpuinfoField("flags")
	switch {
	case exists("/run/.containerenv"), exists("/.dockerenv"), readStr("/run/systemd/container") != "":
		o.Virtualization = "container"
	case strings.Contains(" "+flags+" ", " hypervisor "), c.r.System.Identity != nil && isVMVendor(c.r.System.Identity.Vendor, c.r.System.Identity.Model):
		o.Virtualization = "vm"
	case flags != "": // x86 exposes the hypervisor flag, so its absence is evidence
		o.Virtualization = "none"
	}

	switch {
	case exists("/sys/firmware/efi"):
		o.BootMode = "uefi"
		// efivars data = 4 attribute bytes + 1 value byte.
		b, err := readFile("/sys/firmware/efi/efivars/SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b8c")
		if err == nil && len(b) >= 5 {
			on := b[4] == 1
			o.SecureBoot = &on
		}
	case exists("/sys/firmware/dmi") && isX86(o.Arch):
		// x86 firmware with SMBIOS but no EFI runtime is legacy BIOS boot.
		// (Containers often hide /sys/firmware: then it stays unknown.)
		o.BootMode = "bios"
	}
}

func isX86(arch string) bool {
	switch arch {
	case "x86_64", "i386", "i486", "i586", "i686":
		return true
	}
	return false
}

// isVMVendor recognises the DMI identity of common hypervisors, which
// matters on ARM where CPUs have no "hypervisor" flag.
func isVMVendor(vendor, product string) bool {
	id := strings.ToLower(vendor + " " + product)
	for _, v := range []string{"qemu", "kvm", "vmware", "virtualbox", "innotek", "xen", "parallels",
		"bochs", "amazon ec2", "google compute engine", "openstack", "virtual machine", "cloud hypervisor"} {
		if strings.Contains(id, v) {
			return true
		}
	}
	return false
}

func parseOSRelease(s string) map[string]string {
	m := map[string]string{}
	for line := range strings.SplitSeq(s, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		if uq, err := strconv.Unquote(v); err == nil {
			v = uq
		} else {
			v = strings.Trim(v, `"'`)
		}
		m[k] = v
	}
	return m
}

// uname returns the kernel release and the machine (x86_64, aarch64, ...),
// or empty strings if the call fails.
var uname = func() (release, machine string) {
	var u syscall.Utsname
	if err := syscall.Uname(&u); err != nil {
		return "", ""
	}
	return utsString(u.Release[:]), utsString(u.Machine[:])
}

// utsString converts a NUL-terminated Utsname field (int8 or uint8
// depending on the architecture) to a string.
func utsString[T int8 | uint8](f []T) string {
	b := make([]byte, 0, len(f))
	for _, ch := range f {
		if ch == 0 {
			break
		}
		b = append(b, byte(ch))
	}
	return string(b)
}

const dmiDir = "/sys/class/dmi/id/"

func (c *collector) dmi() {
	if !exists(dmiDir) {
		// Device-tree boards (most ARM) name themselves there instead.
		if model := strings.TrimRight(readStr("/proc/device-tree/model"), "\x00"); model != "" {
			c.r.System.Identity = &report.Identity{Model: model}
			return
		}
		c.warn("dmi: /sys/class/dmi/id not present (no SMBIOS firmware tables, common on ARM boards)")
		return
	}
	var denied []string
	get := func(name string) string {
		v, err := readStrErr(dmiDir + name)
		if err != nil {
			if os.IsPermission(err) {
				denied = append(denied, name)
			}
			return ""
		}
		return smbios.Clean(v)
	}

	s := &c.r.System
	if id := (&report.Identity{
		Vendor:     get("sys_vendor"),
		Model:      get("product_name"),
		PartNumber: get("product_sku"),
		Serial:     get("product_serial"),
		Revision:   get("product_version"),
	}); !id.Empty() {
		s.Identity = id
	}
	s.Family = get("product_family")
	s.UUID = get("product_uuid")
	s.ChassisVendor = get("chassis_vendor")
	s.ChassisSerial = get("chassis_serial")
	if n, err := strconv.Atoi(get("chassis_type")); err == nil {
		s.ChassisType = smbios.ChassisType(n)
	}
	// The system's firmware is its BIOS/UEFI.
	if fw := firmwareVersion(get("bios_version"), "dmi"); fw != nil {
		fw.Vendor, fw.Date, fw.Release = get("bios_vendor"), get("bios_date"), get("bios_release")
		s.Firmware = fw
	}

	b := &c.r.Board
	if id := (&report.Identity{
		Vendor:   get("board_vendor"),
		Model:    get("board_name"),
		Revision: get("board_version"),
		Serial:   get("board_serial"),
	}); !id.Empty() {
		b.Identity = id
	}
	b.AssetTag = get("board_asset_tag")

	if len(denied) > 0 {
		c.warn("dmi %s: needs root (run with --full)", strings.Join(denied, ", "))
	}
}

// cpuinfoField returns a field from /proc/cpuinfo: the first occurrence,
// so per-CPU fields come from the first processor block, while fields that
// only appear in the trailing block (ARM's "Hardware", "Revision") are
// found too.
func (c *collector) cpuinfoField(key string) string {
	if c.cpuinfo == nil {
		c.cpuinfo = map[string]string{}
		f, err := openFile("/proc/cpuinfo")
		if err != nil {
			c.warn("cpu: %v", err)
			return ""
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			k, v, ok := strings.Cut(sc.Text(), ":")
			k = strings.TrimSpace(k)
			if _, seen := c.cpuinfo[k]; ok && !seen {
				c.cpuinfo[k] = strings.TrimSpace(v)
			}
		}
		if err := sc.Err(); err != nil {
			c.warn("cpu: reading /proc/cpuinfo: %v", err)
		}
	}
	return c.cpuinfo[key]
}
