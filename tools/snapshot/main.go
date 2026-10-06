// Command snapshot records the /sys and /proc files hwspec reads from the
// running machine into a directory, scrubbed of identifiers, for use as a
// test fixture:
//
//	go run ./tools/snapshot internal/collect/testdata/machines/my-laptop/root
//
// The tree goes in DEST-DIR, which must be empty or absent; machine.json
// goes next to it, holding what isn't a file: the uname, ethtool and
// Bluetooth management answers, the empty directories git can't store,
// and the files that couldn't be read (with their errors).
//
// It copies ghw's file set (CPU topology, block devices, PCI, USB, network)
// plus every path hwspec's own collectors read, into a private temporary
// directory. There it replaces serial numbers, MAC addresses, UUIDs, WWNs,
// labels and asset tags with placeholders and drops identifier-carrying
// files whose format it doesn't recognise; only then is the tree renamed
// into place. Review the result before committing it anyway.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/jaypipes/ghw/pkg/snapshot"

	"github.com/jiegui2025/hwspec/internal/collect"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the command line (without the program name) and returns the
// exit status: 0 success, 1 failure, 2 usage error.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("snapshot", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listOnly := fs.Bool("list", false, "print the paths hwspec reads and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *listOnly {
		for _, path := range tracedPaths("snapshot") {
			fmt.Fprintln(stdout, path)
		}
		return 0
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: snapshot [-list] DEST-DIR")
		return 2
	}
	dest := fs.Arg(0)
	// Ctrl-C or SIGTERM cancels the recording; record then removes its
	// unscrubbed temporary tree before returning, and says where it is. A
	// second signal ends the program at once and leaves that tree behind
	// (private, and ignored by git).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop() // a second Ctrl-C stops the program at once
	}()
	if err := record(ctx, dest, stderr); err != nil {
		fmt.Fprintln(stderr, "snapshot:", err)
		return 1
	}
	fmt.Fprintln(stdout, "wrote", dest, "- review it for identifiers before committing")
	return 0
}

// The machine being recorded; tests replace these with a synthetic one.
var (
	hostRoot    = "/" // where ghwMisses are looked for
	cloneTree   = snapshot.CloneTreeInto
	tracedPaths = collect.Paths
	capture     = collect.Collect
)

// ghwMisses are files ghw reads that its own snapshot doesn't copy.
var ghwMisses = []string{
	"/sys/block/*/queue/physical_block_size",
	"/sys/block/*/device/vendor", // SATA/SCSI disks, through the device link
	"/sys/block/*/device/model",
	"/run/udev/data/b*", // block devices: model, type, partition table
}

// record copies ghw's file set and every path hwspec's collectors read
// (traced from a real capture, so the set can't drift from the code) into
// a private temporary directory, scrubs it, and renames it to dest, with
// machine.json next to it. On any error or cancellation nothing is left
// behind.
func record(ctx context.Context, dest string, stderr io.Writer) error {
	dest = filepath.Clean(dest)
	if entries, err := os.ReadDir(dest); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s isn't empty; record into a new directory", dest)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(dest)
	machineJSON := filepath.Join(parent, "machine.json")
	if _, err := os.Lstat(machineJSON); err == nil {
		return fmt.Errorf("%s exists; it belongs to another recording", machineJSON)
	}
	resetMACs()
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, ".snapshot-*") // 0700: unscrubbed until the end
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	// On a signal, say where the unscrubbed tree is while it's removed.
	done, watched := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watched)
		select {
		case <-ctx.Done():
		case <-done:
			if ctx.Err() == nil {
				return
			}
		}
		fmt.Fprintf(stderr, "snapshot: interrupted; removing %s; press Ctrl-C again to leave it\n", tmp)
	}()
	defer func() { close(done); <-watched }()
	build := filepath.Join(tmp, "root")

	if err := cloneTree(ctx, build); err != nil {
		return fmt.Errorf("ghw clone: %w", err)
	}
	paths := tracedPaths("snapshot")
	for _, pattern := range ghwMisses {
		matches, err := filepath.Glob(filepath.Join(hostRoot, pattern))
		if err != nil {
			return err
		}
		paths = append(paths, matches...)
	}
	m := collect.Machine{Unreadable: map[string]int{}}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := replicate(build, path, m.Unreadable); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	if err := scrub(build); err != nil {
		return err
	}
	if err := redactNames(build); err != nil {
		return err
	}
	if m.EmptyDirs, err = emptyDirs(build); err != nil {
		return err
	}
	if err := answers(&m); err != nil {
		return err
	}
	js, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, "machine.json"), append(js, '\n'), 0o644); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Chmod(build, 0o755); err != nil {
		return err
	}
	if err := os.Remove(dest); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err // an empty dest is replaced
	}
	if err := os.Rename(build, dest); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(tmp, "machine.json"), machineJSON); err != nil {
		os.RemoveAll(dest) // a tree without its machine.json can't be replayed
		return err
	}
	return nil
}

// answers records what hwspec doesn't read from files (uname, ethtool,
// Bluetooth), from a real capture, so tests can answer those calls the way
// this machine did.
func answers(m *collect.Machine) error {
	r := capture("snapshot")
	m.Arch = r.OS.Arch
	m.Ethtool = map[string]string{}
	for _, n := range r.Network {
		if n.Firmware != nil {
			m.Ethtool[macs.text(n.Name)] = n.Firmware.Version
		}
	}
	m.Bluetooth = map[string]collect.MachineBT{}
	for _, b := range r.Bluetooth {
		if b.Version == "" {
			continue // the management socket didn't answer
		}
		addr, ok := macs.mac(b.Address)
		if !ok {
			return fmt.Errorf("bluetooth %s: address %q isn't a MAC address", b.Name, b.Address)
		}
		if strings.HasPrefix(b.Version, "unknown") {
			return fmt.Errorf("bluetooth %s: version %q is unknown to this hwspec; teach btVersion first", b.Name, b.Version)
		}
		// The local name usually is the hostname.
		m.Bluetooth[b.Name] = collect.MachineBT{Address: addr, Version: b.Version, ManufacturerID: b.ManufacturerID,
			Powered: b.Powered != nil && *b.Powered, Name: "fixture"}
	}
	return nil
}

// replicate recreates path under dest so that it resolves exactly as on the
// live system: symlinked components become the same symlinks, and their
// targets are replicated in turn. A path that doesn't exist is skipped; a
// file that can't be read is recorded in unreadable with its error number;
// any other error stops the recording. MAC addresses in path names (a
// Bluetooth mouse's hid-<MAC>-battery) are redacted.
func replicate(dest, path string, unreadable map[string]int) error {
	return replicateDepth(dest, filepath.Clean(path), path, unreadable, 0)
}

func replicateDepth(dest, path, asked string, unreadable map[string]int, depth int) error {
	if depth > 40 {
		return fmt.Errorf("too many symlinks")
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	cur := "/"
	for i, part := range parts {
		next := filepath.Join(cur, part)
		fi, err := os.Lstat(next)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		out := filepath.Join(dest, macs.text(next))
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(next)
			if err != nil {
				return err
			}
			if _, err := os.Lstat(out); errors.Is(err, fs.ErrNotExist) {
				if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
					return err
				}
				if err := os.Symlink(macs.text(target), out); err != nil {
					return err
				}
			}
			resolved := target
			if !filepath.IsAbs(target) {
				resolved = filepath.Join(cur, target)
			}
			return replicateDepth(dest, filepath.Join(append([]string{resolved}, parts[i+1:]...)...), asked, unreadable, depth+1)
		case fi.IsDir():
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
		case i == len(parts)-1:
			data, err := os.ReadFile(next)
			var errno syscall.Errno
			switch {
			case errors.Is(err, fs.ErrNotExist):
				return nil
			case errors.As(err, &errno):
				// Root-only DMI serials, attributes a driver refuses: the
				// capture sees the same error when replayed.
				unreadable[macs.text(asked)] = int(errno)
				return nil
			case err != nil:
				return err
			}
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return err
			}
			return os.WriteFile(out, data, 0o644)
		}
		cur = next
	}
	return nil
}

// emptyDirs lists the directories under root holding nothing, which git
// can't store (a network interface's wireless/).
func emptyDirs(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			out = append(out, "/"+rel)
		}
		return nil
	})
	return out, err
}

var (
	// Files whose whole content identifies one part or person.
	serialFile = regexp.MustCompile(`/(serial|product_serial|product_uuid|board_serial|chassis_serial|board_asset_tag|chassis_asset_tag|serial_number|wwid|eui|nguid|uuid|subsysnqn|uniq)$`)
	macFile    = regexp.MustCompile(`/net/[^/]+/address$`)
	mountsFile = regexp.MustCompile(`^/proc/(?:self/|\d+/)?(?:mounts|mountinfo)$`)
	// Lines naming one physical part: a board serial in /proc/cpuinfo (ARM),
	// the NVIDIA GPU UUID.
	serialLine = regexp.MustCompile(`(?m)^((?:Serial|GPU UUID)\s*:).*$`)
	// MAC addresses with any of the usual separators.
	macAddress = regexp.MustCompile(`(?i)\b[0-9a-f]{2}(?::[0-9a-f]{2}){5}\b|\b[0-9a-f]{2}(?:-[0-9a-f]{2}){5}\b|\b[0-9a-f]{2}(?:_[0-9a-f]{2}){5}\b`)
	// Interface names udev builds from the MAC (enx001122334455).
	macName = regexp.MustCompile(`(?i)\b(enx|wlx|wwx)([0-9a-f]{12})\b`)
	// Block devices a mount table may name; anything else (mapper names
	// holding host or volume names, network shares) is dropped.
	mountSource = regexp.MustCompile(`^/dev/(nvme\d+n\d+(p\d+)?|sd[a-z]+\d*|hd[a-z]+\d*|mmcblk\d+(p\d+)?|vd[a-z]+\d*|xvd[a-z]+\d*)$`)
)

// ueventKeys are the uevent properties ghw reads; true marks the ones
// naming one part, kept with a placeholder. Every other property is
// dropped.
var ueventKeys = map[string]bool{
	"MAJOR": false, "MINOR": false, "DEVNAME": false, "DEVTYPE": false, "DISKSEQ": false, "PARTN": false,
	"DRIVER": false, "MODALIAS": false, "PRODUCT": false, "TYPE": false, "INTERFACE": false, "BUSNUM": false, "DEVNUM": false,
	"PARTUUID": true, "PARTNAME": true,
}

// mountPoints are the system places a mount table keeps.
var mountPoints = map[string]bool{
	"/": true, "/boot": true, "/boot/efi": true, "/efi": true, "/home": true, "/root": true, "/srv": true, "/opt": true,
	"/usr": true, "/var": true, "/var/cache": true, "/var/lib": true, "/var/log": true, "/var/tmp": true, "/tmp": true,
	"/nix": true, "/nix/store": true,
}

// udevKeys are the udev properties ghw reads; true marks the ones naming
// one part, kept with a placeholder. Every other line of a udev record is
// dropped.
var udevKeys = map[string]bool{
	"ID_MODEL": false, "ID_FS_TYPE": false, "ID_PATH": false,
	"ID_SERIAL": true, "ID_SERIAL_SHORT": true, "ID_SCSI_SERIAL": true, "ID_WWN": true, "ID_WWN_WITH_EXTENSION": true,
	"DM_WWN": true, "ID_FS_LABEL": true, "ID_PART_ENTRY_NAME": true, "ID_PART_ENTRY_UUID": true,
}

// scrub replaces or removes every identifier under dest. It fails closed:
// an identifier-carrying file in a format it doesn't recognise is removed,
// not copied.
func scrub(dest string) error {
	// The SMBIOS table holds every firmware serial in a binary format;
	// fixtures build SMBIOS data in tests instead.
	if err := os.RemoveAll(filepath.Join(dest, "sys/firmware/dmi/tables")); err != nil {
		return err
	}
	return filepath.Walk(dest, func(path string, fi os.FileInfo, err error) error {
		if err != nil || !fi.Mode().IsRegular() {
			return err
		}
		rel, err := filepath.Rel(dest, path)
		if err != nil {
			return err
		}
		rel = "/" + rel
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out, keep := scrubFile(rel, data)
		if !keep {
			return os.Remove(path)
		}
		if !binaryFile(rel) {
			// MACs turn up anywhere: phys, HID_PHYS=, ieee80211 macaddress.
			out = []byte(macs.text(string(out)))
		}
		if bytes.Equal(out, data) {
			return nil
		}
		return os.WriteFile(path, out, fi.Mode().Perm()|0o200)
	})
}

// scrubFile returns a file's scrubbed content, or keep=false when the file
// carries an identifier in a format scrub doesn't recognise.
func scrubFile(rel string, data []byte) (out []byte, keep bool) {
	switch {
	case serialFile.MatchString(rel):
		// An empty value stays empty: "REDACTED" would invent one.
		if len(bytes.TrimSpace(data)) == 0 {
			return data, true
		}
		return []byte("REDACTED\n"), true
	case macFile.MatchString(rel):
		mac, ok := macs.mac(strings.TrimSpace(string(data)))
		return []byte(mac + "\n"), ok
	case mountsFile.MatchString(rel):
		return scrubMounts(data), true
	case strings.HasPrefix(rel, "/run/udev/data/"):
		return scrubUdev(data), true
	case strings.HasSuffix(rel, "/edid"):
		return scrubEDID(data)
	case strings.HasSuffix(rel, "/uevent"):
		return scrubUevent(data), true
	case strings.HasSuffix(rel, "/eeprom"):
		return scrubSPD(data)
	case rel == "/proc/cpuinfo" || strings.HasPrefix(rel, "/proc/driver/nvidia/"):
		return serialLine.ReplaceAll(data, []byte("${1} REDACTED")), true
	}
	return data, true
}

// binaryFile tells the files whose bytes aren't text.
func binaryFile(rel string) bool {
	return strings.HasSuffix(rel, "/edid") || strings.HasSuffix(rel, "/eeprom")
}

// macMap redacts MAC addresses consistently through one recording: an
// address keeps its maker prefix (it names the maker) and gets a number in
// place of the rest, so two devices from one maker stay apart. A locally
// administered (randomised) address has no maker and is replaced whole.
type macMap struct {
	seen   map[string]string // lower-case address → redacted, lower-case
	placed map[string]bool   // redacted addresses, which map to themselves
	count  map[string]int    // per prefix ("02:00:00" for local ones)
}

var macs = newMACMap()

func newMACMap() *macMap {
	return &macMap{seen: map[string]string{}, placed: map[string]bool{}, count: map[string]int{}}
}

func resetMACs() { macs = newMACMap() }

// mac redacts one address, keeping its separator and letter case. ok is
// false when s isn't a MAC address.
func (m *macMap) mac(s string) (string, bool) {
	if len(s) != 17 || !macAddress.MatchString(s) {
		return "", false
	}
	sep := s[2:3]
	key := strings.ToLower(strings.NewReplacer(sep, ":").Replace(s))
	if m.placed[key] {
		return s, true // already redacted (a later pass over the same text)
	}
	r, ok := m.seen[key]
	if !ok {
		first, err := strconv.ParseUint(key[:2], 16, 8)
		if err != nil {
			return "", false
		}
		prefix := key[:8]
		if first&0x02 != 0 {
			prefix = "02:00:00"
		}
		m.count[prefix]++
		n := m.count[prefix]
		r = fmt.Sprintf("%s:%02x:%02x:%02x", prefix, n>>16&0xFF, n>>8&0xFF, n&0xFF)
		m.seen[key] = r
		m.placed[r] = true
	}
	r = strings.ReplaceAll(r, ":", sep)
	if strings.ToUpper(s) == s {
		r = strings.ToUpper(r)
	}
	return r, true
}

// text redacts every MAC address in s, including interface names built
// from one (enx001122334455).
func (m *macMap) text(s string) string {
	s = macAddress.ReplaceAllStringFunc(s, func(a string) string {
		r, _ := m.mac(a)
		return r
	})
	return macName.ReplaceAllStringFunc(s, func(name string) string {
		hex := name[3:]
		var parts []string
		for i := 0; i < 12; i += 2 {
			parts = append(parts, hex[i:i+2])
		}
		r, _ := m.mac(strings.Join(parts, ":"))
		return name[:3] + strings.ReplaceAll(r, ":", "")
	})
}

// redactNames renames every file, directory and symlink target under root
// whose name holds a MAC address (ghw's clone copies names as they are).
func redactNames(root string) error {
	var paths []string
	if err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		paths = append(paths, path)
		return err
	}); err != nil {
		return err
	}
	for _, path := range slices.Backward(paths) { // children before their parents
		if target, err := os.Readlink(path); err == nil && macs.text(target) != target {
			if err := os.Remove(path); err != nil {
				return err
			}
			if err := os.Symlink(macs.text(target), path); err != nil {
				return err
			}
		}
		if base := filepath.Base(path); macs.text(base) != base {
			if err := os.Rename(path, filepath.Join(filepath.Dir(path), macs.text(base))); err != nil {
				return err
			}
		}
	}
	return nil
}

// scrubMounts keeps only block devices mounted in system places, without
// btrfs subvolume names. LUKS and LVM mappings name volumes and hosts,
// /home/<user>, /run/user, /media and /mnt hold names, labels and browser
// profiles, and network and virtual filesystems name servers and users.
func scrubMounts(data []byte) []byte {
	var b bytes.Buffer
	for _, line := range strings.SplitAfter(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || !mountSource.MatchString(f[0]) || !mountPoints[f[1]] {
			continue
		}
		var opts []string
		for o := range strings.SplitSeq(f[3], ",") {
			if !strings.HasPrefix(o, "subvol=") && !strings.HasPrefix(o, "subvolid=") {
				opts = append(opts, o)
			}
		}
		f[3] = strings.Join(opts, ",")
		b.WriteString(strings.Join(f, " ") + "\n")
	}
	return b.Bytes()
}

// scrubUevent keeps the properties ghw reads, with placeholders for those
// naming one part, and drops every other line.
func scrubUevent(data []byte) []byte {
	var b bytes.Buffer
	for _, line := range strings.SplitAfter(string(data), "\n") {
		key, _, ok := strings.Cut(line, "=")
		secret, known := ueventKeys[key]
		switch {
		case !ok || !known:
		case secret:
			b.WriteString(key + "=REDACTED\n")
		default:
			b.WriteString(line)
		}
	}
	return b.Bytes()
}

// scrubUdev keeps the properties ghw reads, with placeholders for those
// naming one part, and drops every other line.
func scrubUdev(data []byte) []byte {
	var b bytes.Buffer
	for _, line := range strings.SplitAfter(string(data), "\n") {
		key, _, ok := strings.Cut(strings.TrimPrefix(line, "E:"), "=")
		secret, known := udevKeys[key]
		switch {
		case !ok || !strings.HasPrefix(line, "E:") || !known:
		case secret:
			b.WriteString("E:" + key + "=REDACTED\n")
		default:
			b.WriteString(line)
		}
	}
	return b.Bytes()
}

// scrubEDID keeps the 128-byte base block, zeroes the numeric serial and
// blanks the serial-number text descriptor, then fixes the checksum.
// Extension blocks (CEA, DisplayID) can carry serials of their own, so they
// are dropped; anything shorter than a base block is too.
func scrubEDID(b []byte) ([]byte, bool) {
	if len(b) < 128 {
		return nil, false
	}
	out := append([]byte(nil), b[:128]...)
	out[126] = 0 // no extension blocks
	copy(out[12:16], []byte{0, 0, 0, 0})
	for _, off := range []int{54, 72, 90, 108} {
		d := out[off : off+18]
		if d[0] == 0 && d[1] == 0 && d[3] == 0xFF {
			copy(d[5:], []byte("REDACTED\n    "))
		}
	}
	var sum byte
	for _, c := range out[:127] {
		sum += c
	}
	out[127] = -sum
	return out, true
}

// scrubSPD replaces a memory module's serial (DDR4 bytes 325-328, DDR5
// 517-520) with a fixed value, so the module still matches by serial. An
// EEPROM of another kind is dropped.
func scrubSPD(b []byte) ([]byte, bool) {
	out := append([]byte(nil), b...)
	switch {
	case len(out) >= 512 && out[2] == 0x0C:
		copy(out[325:329], []byte{0x12, 0x34, 0x56, 0x78})
	case len(out) >= 1024 && out[2] == 0x12:
		copy(out[517:521], []byte{0x12, 0x34, 0x56, 0x78})
	default:
		return nil, false
	}
	return out, true
}
