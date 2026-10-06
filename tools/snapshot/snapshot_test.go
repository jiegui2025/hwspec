package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jiegui2025/hwspec/internal/collect"
	"github.com/jiegui2025/hwspec/internal/report"
)

// tools/snapshot records a machine as a test fixture. What matters: the
// recording reproduces the machine, and it carries no identifier.

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Every kind of identifier the collectors can read is scrubbed; the facts
// around it are kept.
func TestScrubRemovesEveryIdentifier(t *testing.T) {
	resetMACs()
	dir := t.TempDir()
	files := map[string]string{
		"/sys/class/dmi/id/product_serial":                                         "PF123456\n",
		"/sys/class/dmi/id/product_uuid":                                           "4c4c4544-0042-3510-8051-b4c04f4e3732\n",
		"/sys/class/dmi/id/board_asset_tag":                                        "IT-0042\n",
		"/sys/class/dmi/id/chassis_serial":                                         "\n", // empty stays empty
		"/sys/class/dmi/id/product_name":                                           "ThinkPad T14\n",
		"/sys/class/net/eth0/address":                                              "00:e0:4c:68:01:23\n",
		"/sys/class/power_supply/BAT0/serial_number":                               "1234\n",
		"/sys/devices/pci0000:00/0000:00:1d.0/nvme/nvme0/nvme0n1/nvme0n1p2/uevent": "MAJOR=259\nPARTN=2\nPARTUUID=deadbeef-01\nPARTNAME=Alice's data\n",
		"/run/udev/data/b259:0": "S:disk/by-id/nvme-SAMSUNG_S4DXNF0M123456\nS:disk/by-path/pci-0000:01:00.0-nvme-1-part/by-partuuid/deadbeef-01\nS:disk/by-path/pci-0000:01:00.0-nvme-1\n" +
			"E:ID_SERIAL=SAMSUNG_S4DXNF0M123456\nE:ID_WWN=eui.0025\nE:ID_FS_LABEL=Alice\nE:DEVLINKS=/dev/disk/by-id/x\nE:ID_MODEL=SAMSUNG MZVLB256HAHQ\n" +
			"E:ID_MODEL_ENC=SAMSUNG\\x20MZVLB256HAHQ\nE:ID_FS_LABEL_ENC=Alice\\x27s\nE:ID_FS_UUID=4ac9c9e9-6d1b\nE:ID_FS_UUID_ENC=4ac9c9e9-6d1b\nE:ID_PART_ENTRY_UUID=cafe0123-02\n",
		"/proc/cpuinfo": "processor\t: 0\nHardware\t: BCM2835\nSerial\t\t: 10000000abcdef01\n",
		"/proc/driver/nvidia/gpus/0000:01:00.0/information": "Model: \t\t NVIDIA RTX\nGPU UUID: \t GPU-1234-5678\nVideo BIOS: \t 94.06\n",
		"/sys/firmware/dmi/tables/DMI":                      "\x11\x28serial-bearing table",
	}
	for path, content := range files {
		write(t, filepath.Join(dir, path), []byte(content))
	}
	edid := make([]byte, 128)
	copy(edid, []byte{0, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0})
	copy(edid[12:16], []byte{1, 2, 3, 4})
	copy(edid[72:], []byte{0, 0, 0, 0xFF, 0})
	copy(edid[77:], "SN123456789\n ")
	write(t, filepath.Join(dir, "/sys/class/drm/card1-DP-1/edid"), edid)
	spd4 := make([]byte, 512)
	spd4[2] = 0x0C
	copy(spd4[325:329], []byte{0xDE, 0xAD, 0xBE, 0xEF})
	write(t, filepath.Join(dir, "/sys/devices/i2c-7/7-0050/eeprom"), spd4)
	spd5 := make([]byte, 1024)
	spd5[2] = 0x12
	copy(spd5[517:521], []byte{0xDE, 0xAD, 0xBE, 0xEF})
	write(t, filepath.Join(dir, "/sys/devices/i2c-7/7-0051/eeprom"), spd5)

	if err := scrub(dir); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"PF123456", "4c4c4544", "IT-0042", "68:01:23", "1234\n", "deadbeef", "Alice", "S4DXNF0M123456", "eui.0025",
		"abcdef01", "GPU-1234", "SN123456789", "\xDE\xAD\xBE\xEF", "4ac9c9e9", "cafe0123"} {
		filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error {
			if err == nil && fi.Mode().IsRegular() && strings.Contains(read(t, path), secret) {
				t.Errorf("%q survives in %s", secret, strings.TrimPrefix(path, dir))
			}
			return nil
		})
	}
	for path, want := range map[string]string{
		"/sys/class/dmi/id/product_name":   "ThinkPad T14\n",
		"/sys/class/dmi/id/chassis_serial": "\n",
		"/sys/class/net/eth0/address":      "00:e0:4c:00:00:01\n", // maker prefix kept
	} {
		if got := read(t, filepath.Join(dir, path)); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	for path, keep := range map[string]string{
		"/run/udev/data/b259:0": "E:ID_MODEL=SAMSUNG MZVLB256HAHQ\nE:ID_MODEL_ENC=SAMSUNG\\x20MZVLB256HAHQ\n",
		"/proc/cpuinfo":         "Hardware\t: BCM2835",
		"/proc/driver/nvidia/gpus/0000:01:00.0/information": "Video BIOS: \t 94.06",
	} {
		if !strings.Contains(read(t, filepath.Join(dir, path)), keep) {
			t.Errorf("%s lost %q", path, keep)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "/sys/firmware/dmi/tables")); !os.IsNotExist(err) {
		t.Error("the SMBIOS table was kept")
	}
	// The EDID still checksums, so it still parses.
	scrubbed := []byte(read(t, filepath.Join(dir, "/sys/class/drm/card1-DP-1/edid")))
	var sum byte
	for _, b := range scrubbed {
		sum += b
	}
	if sum != 0 || !bytes.Equal(scrubbed[12:16], []byte{0, 0, 0, 0}) {
		t.Errorf("EDID checksum %d, serial % x", sum, scrubbed[12:16])
	}
	// Module serials become a fixed value, so SPD still matches by serial.
	if got := read(t, filepath.Join(dir, "/sys/devices/i2c-7/7-0050/eeprom"))[325:329]; got != "\x12\x34\x56\x78" {
		t.Errorf("DDR4 serial = % x", got)
	}
	if got := read(t, filepath.Join(dir, "/sys/devices/i2c-7/7-0051/eeprom"))[517:521]; got != "\x12\x34\x56\x78" {
		t.Errorf("DDR5 serial = % x", got)
	}
	if _, keep := scrubSPD([]byte{1, 2, 3}); keep {
		t.Error("an EEPROM of unknown kind was kept")
	}
}

// Symlinked paths are recreated as the same symlinks, so the collectors
// resolve devices, drivers and buses exactly as on the machine.
func TestReplicateKeepsSymlinks(t *testing.T) {
	src, dest := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "devices/pci0000:00/0000:00:1f.6/vendor"), []byte("0x8086\n"))
	if err := os.MkdirAll(filepath.Join(src, "class/net/eth0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../../devices/pci0000:00/0000:00:1f.6", filepath.Join(src, "class/net/eth0/device")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("loop", filepath.Join(src, "loop")); err != nil {
		t.Fatal(err)
	}
	if err := replicate(dest, filepath.Join(src, "class/net/eth0/device/vendor"), map[string]int{}); err != nil {
		t.Fatal(err)
	}
	link, err := os.Readlink(filepath.Join(dest, src, "class/net/eth0/device"))
	if err != nil || link != "../../../devices/pci0000:00/0000:00:1f.6" {
		t.Errorf("link = %q, %v", link, err)
	}
	if got := read(t, filepath.Join(dest, src, "class/net/eth0/device/vendor")); got != "0x8086\n" {
		t.Errorf("through the link: %q", got)
	}
	// Vanished paths are skipped; a symlink loop is an error.
	if err := replicate(dest, filepath.Join(src, "nothing/here"), map[string]int{}); err != nil {
		t.Errorf("missing path: %v", err)
	}
	if err := replicate(dest, filepath.Join(src, "loop/x"), map[string]int{}); err == nil {
		t.Error("a symlink loop was followed forever")
	}
}

// The real test of a recording: capturing it gives the same result as
// capturing the machine, apart from identifiers and readings that change
// by the second.
func TestARecordingReproducesThisMachine(t *testing.T) {
	if testing.Short() || os.Getenv("HWSPEC_SKIP_HOST_TESTS") != "" {
		t.Skip("records the whole machine")
	}
	if os.Geteuid() == 0 {
		t.Skip("as root the recording reads files a user capture can't")
	}
	if _, err := os.Stat("/sys/devices/system/cpu"); err != nil {
		t.Skip("no sysfs here (a build sandbox)")
	}
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	if code := run([]string{filepath.Join(dir, "root")}, &out, &errOut); code != 0 {
		t.Fatalf("snapshot: exit %d: %s", code, errOut.String())
	}
	recorded, err := collect.CollectRecorded(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	live := collect.Collect("test")
	// Nothing that identifies this machine or its user is in the recording.
	secrets := identifiers(t, live)
	if u := os.Getenv("USER"); u != "" {
		secrets = append(secrets, "/"+u+"/", "/"+u+"-")
	}
	filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error {
		if err != nil || !fi.Mode().IsRegular() {
			return err
		}
		data, _ := os.ReadFile(path)
		text, lowerPath := strings.ToLower(string(data)), strings.ToLower(path)
		for _, s := range secrets {
			if s = strings.ToLower(s); strings.Contains(text, s) || strings.Contains(lowerPath, s) {
				t.Errorf("%s holds an identifier of this machine (%.3s…)", strings.TrimPrefix(path, dir), s)
			}
		}
		return nil
	})
	if got, want := comparable(t, recorded), comparable(t, live); !reflect.DeepEqual(got, want) {
		g, _ := json.MarshalIndent(got, "", " ")
		w, _ := json.MarshalIndent(want, "", " ")
		gl, wl := strings.Split(string(g), "\n"), strings.Split(string(w), "\n")
		for i := 0; i < len(gl) && i < len(wl); i++ {
			if gl[i] != wl[i] {
				t.Fatalf("recording differs from the machine at line %d:\nrecorded: %s\n    live: %s", i+1, gl[i], wl[i])
			}
		}
		t.Fatalf("recording differs from the machine in length")
	}
}

// identifiers lists the live capture's serials, MACs, UUIDs, WWNs and
// names that only this machine has.
func identifiers(t *testing.T, r any) []string {
	t.Helper()
	b, _ := json.Marshal(r)
	var v any
	json.Unmarshal(b, &v)
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				switch k {
				case "address": // a Bluetooth MAC; PCI and USB addresses name a slot, not a part
					if a, ok := val.(string); ok && macAddress.MatchString(a) && len(a) == 17 {
						out = append(out, a)
					}
				case "serial", "mac", "uuid", "partuuid", "wwn", "local_name", "hostname":
					// Some USB devices report their product name ("Lenovo FHD
					// Webcam") as the serial; real serials have no spaces.
					if s, ok := val.(string); ok && len(s) > 4 && s != "REDACTED" && (k != "serial" || !strings.Contains(s, " ")) {
						out = append(out, s)
					}
				}
				walk(val)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(v)
	return out
}

// comparable drops identifiers (scrubbed in the recording) and readings
// that change between two captures.
func comparable(t *testing.T, r any) any {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	drop := map[string]bool{
		"captured_at": true, "hostname": true, "serial": true, "mac": true, "address": true, "uuid": true, "partuuid": true, "label": true,
		"local_name": true, "wwn": true, "asset_tag": true, "sensors": true, "metrics": true, "batteries": true, "warnings": true,
		"actual_freq_mhz": true, // a GPU's clock moves between two captures
	}
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k := range x {
				if drop[k] {
					delete(x, k)
				} else {
					x[k] = walk(x[k])
				}
			}
		case []any:
			for i := range x {
				x[i] = walk(x[i])
			}
		}
		return v
	}
	return walk(v)
}

func TestCommandLine(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(nil, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "usage") {
		t.Errorf("no arguments: exit %d, %q", code, errOut.String())
	}
	if code := run([]string{"-bogus"}, &out, &errOut); code != 2 {
		t.Errorf("unknown flag: exit %d", code)
	}
}

// Mount tables name users, browser profiles, encrypted volumes and network
// servers; only block devices mounted in system places stay.
func TestMountsKeepOnlySystemBlockDevices(t *testing.T) {
	in := "/dev/nvme0n1p2 / btrfs rw 0 0\n" +
		"/dev/nvme0n1p2 /home btrfs rw 0 0\n" +
		"/dev/mapper/luks-deadbeef01 /data ext4 rw 0 0\n" +
		"/dev/sdb1 /media/alice/Backup vfat rw 0 0\n" +
		"/dev/sdc1 /run/media/alice/USB vfat rw 0 0\n" +
		"/dev/nvme0n1p3 /home/alice/vm ext4 rw 0 0\n" +
		"fuse-overlayfs /run/user/1000/psd/alice-firefox-x fuse 0 0\n" +
		"nas.local:/export /mnt/nas nfs4 rw 0 0\n" +
		"/dev/mapper/fedora_alicepc-root /var ext4 rw 0 0\n" +
		"/dev/sdd1 /mnt/alice-photos ext4 rw 0 0\n" +
		"/dev/nvme0n1p2 /var/log btrfs rw,ssd,subvolid=258,subvol=/@home/alice 0 0\n" +
		"tmpfs /tmp tmpfs rw 0 0\n"
	want := "/dev/nvme0n1p2 / btrfs rw 0 0\n/dev/nvme0n1p2 /home btrfs rw 0 0\n/dev/nvme0n1p2 /var/log btrfs rw,ssd 0 0\n"
	if got := string(scrubMounts([]byte(in))); got != want {
		t.Errorf("mounts:\n%s", got)
	}
}

// udev records keep only what ghw reads; serials, labels and partition
// UUIDs keep their key with a placeholder.
func TestUdevRecordsKeepOnlyWhatGhwReads(t *testing.T) {
	in := "S:disk/by-id/nvme-SAMSUNG_123\nS:mapper/luks-deadbeef\nI:4944657\nE:ID_MODEL=SAMSUNG MZVLB256\nE:ID_SERIAL=SAMSUNG_123\n" +
		"E:DM_NAME=luks-deadbeef\nE:MD_NAME=alice-pc:0\nE:SCSI_IDENT_SERIAL=X\nE:ID_LOOP_BACKING_FILENAME=/home/alice/disk.img\nE:ID_FS_TYPE=btrfs\nG:systemd\n"
	want := "E:ID_MODEL=SAMSUNG MZVLB256\nE:ID_SERIAL=REDACTED\nE:ID_FS_TYPE=btrfs\n"
	if got := string(scrubUdev([]byte(in))); got != want {
		t.Errorf("udev:\n%s\nwant:\n%s", got, want)
	}
}

// Formats scrub doesn't recognise are dropped, not copied: a MAC that
// isn't six bytes, an EDID shorter than a block, an EEPROM that isn't
// DDR4 or DDR5. EDID extension blocks (which can carry serials) go too.
func TestUnrecognisedFormatsAreDropped(t *testing.T) {
	for rel, data := range map[string]string{
		"/sys/class/net/ib0/address":                "80:00:02:08:fe:80:00:00:00:00:00:00:00:02:c9:03:00:0b:4c:cf\n",
		"/sys/class/drm/card1-DP-1/edid":            "short",
		"/sys/bus/i2c/drivers/ee1004/0-0050/eeprom": "\x92\x11\x0B",
	} {
		if _, keep := scrubFile(rel, []byte(data)); keep {
			t.Errorf("%s kept", rel)
		}
	}
	edid := make([]byte, 256)
	edid[126] = 1
	out, keep := scrubFile("/sys/class/drm/card0-HDMI-A-1/edid", edid)
	if !keep || len(out) != 128 || out[126] != 0 {
		t.Errorf("EDID with an extension: kept %v, %d bytes, %d extensions", keep, len(out), out[126])
	}
	// A randomised (locally administered) MAC has no maker: all of it goes.
	resetMACs()
	if out, _ := scrubFile("/sys/class/net/wlan0/address", []byte("56:34:23:11:22:33\n")); string(out) != "02:00:00:00:00:01\n" {
		t.Errorf("random MAC = %q", out)
	}
	// Every uevent property naming one part.
	// uevent keeps only what ghw reads, with placeholders for identifiers.
	ev := "DEVTYPE=partition\nPOWER_SUPPLY_SERIAL_NUMBER=1234\nHID_UNIQ=aa:bb\nPARTUUID=deadbeef\nPARTNAME=Alice\nNAME=\"alice's mouse\"\nDM_NAME=luks-deadbeef\nHID_PHYS=usb-0000:00:14.0-1/input0\n"
	if out, _ := scrubFile("/sys/class/block/sda1/uevent", []byte(ev)); string(out) != "DEVTYPE=partition\nPARTUUID=REDACTED\nPARTNAME=REDACTED\n" {
		t.Errorf("uevent:\n%s", out)
	}
	for _, name := range []string{"subsysnqn", "uniq"} {
		if out, _ := scrubFile("/sys/x/"+name, []byte("nqn.2014.08.org.nvmexpress:uuid:1\n")); string(out) != "REDACTED\n" {
			t.Errorf("%s = %q", name, out)
		}
	}
}

// MAC addresses in file names (a Bluetooth mouse's battery, a USB network
// adapter named after its MAC) are redacted, and links to them follow.
func TestMACsInNamesAreRedacted(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "class/power_supply/hid-00:1b:dc:dd:ee:ff-battery/capacity"), []byte("80\n"))
	write(t, filepath.Join(dir, "class/net/enx00e04c680123/mtu"), []byte("1500\n"))
	if err := os.Symlink("hid-00:1b:dc:dd:ee:ff-battery", filepath.Join(dir, "class/power_supply/mouse")); err != nil {
		t.Fatal(err)
	}
	resetMACs()
	if err := redactNames(dir); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"class/power_supply/hid-00:1b:dc:00:00:01-battery/capacity", "class/net/enx00e04c000001/mtu", "class/power_supply/mouse/capacity"} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("%s: %v", want, err)
		}
	}
}

// A file the recording user can't read is recorded with its error, so the
// capture replays "needs root" instead of silently missing it.
func TestUnreadableFilesAreRecorded(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	src, dest := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "product_serial"), []byte("PF1\n"))
	if err := os.Chmod(filepath.Join(src, "product_serial"), 0); err != nil {
		t.Fatal(err)
	}
	unreadable := map[string]int{}
	if err := replicate(dest, filepath.Join(src, "product_serial"), unreadable); err != nil {
		t.Fatal(err)
	}
	if unreadable[filepath.Join(src, "product_serial")] != 13 {
		t.Errorf("unreadable = %v", unreadable)
	}
}

// A recording goes into a new directory, whatever way it is named, and
// leaves nothing behind when it can't be made.
func TestRecordingNeedsAnEmptyDestination(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "root", "old"), []byte("x"))
	var out, errOut bytes.Buffer
	for _, dest := range []string{filepath.Join(dir, "root"), filepath.Join(dir, "root") + "/", filepath.Join(dir, "root") + "/."} {
		if code := run([]string{dest}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "isn't empty") {
			t.Errorf("%s: exit %d, %q", dest, code, errOut.String())
		}
	}
	file := filepath.Join(dir, "file")
	write(t, file, []byte("x"))
	if code := run([]string{filepath.Join(file, "root")}, &out, &errOut); code != 1 {
		t.Errorf("under a file: exit %d", code)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".snapshot-*")); len(leftovers) != 0 {
		t.Errorf("left behind: %v", leftovers)
	}
}

// fakeMachine makes record copy a synthetic machine under src instead of
// this one: ghw's clone, the traced paths and the capture are replaced.
func fakeMachine(t *testing.T, src string, cloneErr error) {
	t.Helper()
	oldClone, oldPaths, oldCapture := cloneTree, tracedPaths, capture
	t.Cleanup(func() { cloneTree, tracedPaths, capture = oldClone, oldPaths, oldCapture })
	cloneTree = func(_ context.Context, dest string) error {
		if cloneErr != nil {
			return cloneErr
		}
		return os.MkdirAll(filepath.Join(dest, "sys/class/net/wlan0/wireless"), 0o755) // empty
	}
	tracedPaths = func(string) []string {
		return []string{filepath.Join(src, "dmi/product_name"), filepath.Join(src, "dmi/product_serial"), filepath.Join(src, "absent")}
	}
	on := true
	capture = func(string) *report.Report {
		return &report.Report{OS: report.OS{Arch: "riscv64"},
			Network:   []report.NIC{{Name: "enx00e04c680123", Firmware: &report.Firmware{Version: "1.0"}}, {Name: "lo"}},
			Bluetooth: []report.BluetoothController{{Name: "hci0", Address: "60:F2:62:12:34:56", Version: "5.2", ManufacturerID: 2, Powered: &on}, {Name: "hci1"}}}
	}
}

// Recording a machine: the scrubbed tree lands in DEST, machine.json next
// to it with every answer, empty directory and unreadable file.
func TestRecordWritesTheTreeAndMachineJSON(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "dmi/product_name"), []byte("Box\n"))
	write(t, filepath.Join(src, "dmi/product_serial"), []byte("PF1\n"))
	if err := os.Chmod(filepath.Join(src, "dmi/product_serial"), 0); err != nil {
		t.Fatal(err)
	}
	fakeMachine(t, src, nil)
	dir := t.TempDir()
	dest := filepath.Join(dir, "m", "root") + "/"
	var out, errOut bytes.Buffer
	if code := run([]string{dest}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if got := read(t, filepath.Join(dir, "m/root", src, "dmi/product_name")); got != "Box\n" {
		t.Errorf("copied file = %q", got)
	}
	m := read(t, filepath.Join(dir, "m/machine.json"))
	wants := []string{`"arch": "riscv64"`, `"enx00e04c000001": "1.0"`, `"address": "60:F2:62:00:00:01"`, `"name": "fixture"`,
		`"/sys/class/net/wlan0/wireless"`}
	if os.Geteuid() != 0 { // root reads the mode-000 file
		wants = append(wants, filepath.Join(src, "dmi/product_serial")+`": 13`)
	}
	for _, want := range wants {
		if !strings.Contains(m, want) {
			t.Errorf("machine.json lacks %s:\n%s", want, m)
		}
	}
	if strings.Contains(m, "hci1") || strings.Contains(m, `"lo"`) {
		t.Errorf("devices without answers recorded:\n%s", m)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, "m", ".snapshot-*")); len(leftovers) != 0 {
		t.Errorf("left behind: %v", leftovers)
	}
	// An empty destination directory is fine too.
	empty := filepath.Join(dir, "n", "root")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{empty}, &out, &errOut); code != 0 {
		t.Errorf("empty dest: exit %d: %s", code, errOut.String())
	}
}

// When recording fails partway, no unscrubbed copy is left anywhere.
func TestAFailedRecordingLeavesNothing(t *testing.T) {
	fakeMachine(t, t.TempDir(), errors.New("ghw: boom"))
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	if code := run([]string{filepath.Join(dir, "root")}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "ghw clone: ghw: boom") {
		t.Errorf("exit %d, %q", code, errOut.String())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("left behind: %v", entries)
	}
	// A Bluetooth address that isn't a MAC can't be redacted: refused.
	fakeMachine(t, t.TempDir(), nil)
	capture = func(string) *report.Report {
		return &report.Report{Bluetooth: []report.BluetoothController{{Name: "hci0", Address: "weird", Version: "5.2"}}}
	}
	if code := run([]string{filepath.Join(dir, "root")}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "isn't a MAC address") {
		t.Errorf("bad address: exit %d, %q", code, errOut.String())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("left behind: %v", entries)
	}
}

// -list prints the paths a capture reads, one per line.
func TestListPrintsTheTracedPaths(t *testing.T) {
	fakeMachine(t, "/src", nil)
	var out bytes.Buffer
	if code := run([]string{"-list"}, &out, io.Discard); code != 0 || out.String() != "/src/dmi/product_name\n/src/dmi/product_serial\n/src/absent\n" {
		t.Errorf("exit %d:\n%s", code, out.String())
	}
}

// A path that can't be followed (a file where a directory should be) stops
// the recording instead of being silently dropped.
func TestUntraversablePathsStopTheRecording(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "file"), []byte("x"))
	err := replicate(t.TempDir(), filepath.Join(src, "file", "below"), map[string]int{})
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("err = %v", err)
	}
}

// One recording redacts each MAC the same way everywhere, numbers the
// devices of one maker apart, keeps the address's separator and case, and
// catches MACs in any text file (phys, HID_PHYS=, ieee80211 macaddress).
func TestMACsAreRedactedConsistently(t *testing.T) {
	resetMACs()
	in := "phys=usb-00:1b:dc:aa:bb:cc/input0 other=00-1B-DC-11-22-33 again=00:1b:dc:aa:bb:cc wifi=WLX001BDCAABBCC"
	want := "phys=usb-00:1b:dc:00:00:01/input0 other=00-1B-DC-00-00-02 again=00:1b:dc:00:00:01 wifi=WLX001BDC000001"
	if got := macs.text(in); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if _, ok := macs.mac("not-a-mac"); ok {
		t.Error("a non-MAC was redacted as one")
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "sys/class/ieee80211/phy0/macaddress"), []byte("00:1b:dc:aa:bb:cc\n"))
	if err := scrub(dir); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dir, "sys/class/ieee80211/phy0/macaddress")); got != "00:1b:dc:00:00:01\n" {
		t.Errorf("macaddress = %q", got)
	}
}

// Ctrl-C (a cancelled context) stops the recording and leaves no
// unscrubbed tree; machine.json of another recording isn't overwritten;
// a Bluetooth version hwspec can't name isn't recorded.
func TestRecordingStopsCleanly(t *testing.T) {
	fakeMachine(t, t.TempDir(), nil)
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var msg bytes.Buffer
	if err := record(ctx, filepath.Join(dir, "a", "root"), &msg); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
	if !strings.Contains(msg.String(), "interrupted; removing "+filepath.Join(dir, "a", ".snapshot-")) || !strings.Contains(msg.String(), "press Ctrl-C again to leave it") {
		t.Errorf("message = %q", msg.String())
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "a")); len(entries) != 0 {
		t.Errorf("left behind: %v", entries)
	}
	write(t, filepath.Join(dir, "b", "machine.json"), []byte("{}"))
	if err := record(context.Background(), filepath.Join(dir, "b", "root"), io.Discard); err == nil || !strings.Contains(err.Error(), "belongs to another recording") {
		t.Errorf("existing machine.json: %v", err)
	}
	capture = func(string) *report.Report {
		return &report.Report{Bluetooth: []report.BluetoothController{{Name: "hci0", Address: "60:F2:62:12:34:56", Version: "unknown (99)"}}}
	}
	if err := record(context.Background(), filepath.Join(dir, "c", "root"), io.Discard); err == nil || !strings.Contains(err.Error(), "unknown to this hwspec") {
		t.Errorf("unknown version: %v", err)
	}
}
