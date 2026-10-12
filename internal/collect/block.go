package collect

import (
	"bufio"
	"bytes"
	"slices"
	"strconv"
	"strings"
)

// Block devices are read from sysfs and the udev database directly, with
// the values as recorded: udev's "safe" encodings (spaces as underscores
// in ID_FS_LABEL and ID_MODEL) are undone from their _ENC forms, never
// guessed, and a filesystem type such as crypto_LUKS stays as it is.

// udevProps returns the udev properties (E: lines) of the block device
// whose sysfs directory is dir, or nil without a udev database (containers,
// minimal systems).
func udevProps(dir string) map[string]string { return udevRecord("b", dir) }

// udevRecord is udevProps for a block ("b") or character ("c") device.
func udevRecord(kind, dir string) map[string]string {
	dev := readStr(dir + "/dev") // major:minor
	if dev == "" {
		return nil
	}
	b, err := readFile("/run/udev/data/" + kind + dev)
	if err != nil {
		return nil
	}
	props := map[string]string{}
	for line := range strings.Lines(string(b)) {
		if kv, ok := strings.CutPrefix(strings.TrimRight(line, "\n"), "E:"); ok {
			if k, v, ok := strings.Cut(kv, "="); ok {
				props[k] = v
			}
		}
	}
	return props
}

// udevValue returns a property from its _ENC form when there is one (udev
// escapes bytes as \xNN there, keeping the value exact), else as it is.
func udevValue(props map[string]string, key string) string {
	if enc, ok := props[key+"_ENC"]; ok {
		return strings.TrimSpace(unescapeHex(enc))
	}
	return strings.TrimSpace(props[key])
}

// unescapeHex decodes udev's \xNN escapes; anything else stays as it is.
func unescapeHex(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) && s[i+1] == 'x' {
			if v, err := strconv.ParseUint(s[i+2:i+4], 16, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// firstOf returns the first non-empty value.
func firstOf(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// mount is one line of the mount table.
type mount struct{ point, fstype string }

// mounts maps each device in the mount table to its first mount (a btrfs
// volume mounted at several places is listed under the first, normally /).
func mounts() map[string]mount {
	b, err := readFile("/proc/self/mounts")
	if err != nil {
		return nil
	}
	out := map[string]mount{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 {
			continue
		}
		if _, seen := out[f[0]]; !seen {
			out[f[0]] = mount{point: unescapeOctal(f[1]), fstype: f[2]}
		}
	}
	return out
}

// mountsByDevice maps each device number ("179:2") in mountinfo to its
// first mount, preferring one of the filesystem's root over a bind
// mount of part of it. It finds what mounts can't by name: a kernel
// that mounts the root filesystem itself, without an initramfs, lists it
// as /dev/root (init/do_mounts.c), which names no partition (#168).
func mountsByDevice() map[string]mount {
	b, err := readFile("/proc/self/mountinfo")
	if err != nil {
		return nil
	}
	out := map[string]mount{}
	whole := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		// id parent major:minor root mount-point options [optional...] -
		// fstype source super-options (proc(5))
		f := strings.Fields(sc.Text())
		sep := slices.Index(f, "-")
		if len(f) < 5 || sep < 6 || sep+1 >= len(f) {
			continue
		}
		dev, isRoot := f[2], f[3] == "/"
		if _, seen := out[dev]; !seen || isRoot && !whole[dev] {
			out[dev] = mount{point: unescapeOctal(f[4]), fstype: f[sep+1]}
			whole[dev] = isRoot
		}
	}
	return out
}

// unescapeOctal decodes the mount table's \ooo escapes (space, tab,
// newline, backslash).
func unescapeOctal(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// partitionNames lists the partitions of disk (in sysfs directory dir),
// in partition-number order. They are the entries named after the disk
// (nvme0n1p1, sda1) that have a partition number.
func partitionNames(dir, disk string) []string {
	type part struct {
		name string
		n    int
	}
	var parts []part
	for _, name := range list(dir) {
		if !strings.HasPrefix(name, disk) {
			continue
		}
		if n, err := strconv.Atoi(readStr(dir + "/" + name + "/partition")); err == nil {
			parts = append(parts, part{name, n})
		}
	}
	slices.SortStableFunc(parts, func(a, b part) int { return a.n - b.n })
	names := make([]string, len(parts))
	for i, p := range parts {
		names[i] = p.name
	}
	return names
}

// ueventValue returns a KEY=value property of a sysfs uevent file.
func ueventValue(dir, key string) string {
	for line := range strings.Lines(readStr(dir + "/uevent")) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), key+"="); ok {
			return v
		}
	}
	return ""
}
