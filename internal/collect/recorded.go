package collect

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/jiegui2025/hwspec/internal/report"
)

// A recorded machine (tools/snapshot) is a directory holding root/, the
// files hwspec reads, and machine.json, everything else: the answers to
// calls that aren't file reads, and what files can't carry.

// Machine is what a recording holds besides files.
type Machine struct {
	Arch      string               `json:"arch"`
	Ethtool   map[string]string    `json:"ethtool,omitempty"`   // interface → firmware version
	Bluetooth map[string]MachineBT `json:"bluetooth,omitempty"` // hci0 → management answer
	// WiFi is each wireless interface's radio, as nl80211 described it
	// (#111): interface → radio.
	WiFi map[string]report.WiFiRadio `json:"wifi,omitempty"`
	// EmptyDirs are directories that exist only to be tested for (a
	// network interface's wireless/); git can't store them.
	EmptyDirs []string `json:"empty_dirs,omitempty"`
	// Unreadable are files the recording couldn't read, with the error
	// number they failed with (13: permission denied, for root-only DMI
	// serials); replayed reads fail the same way.
	Unreadable map[string]int `json:"unreadable,omitempty"`
}

// MachineBT is a controller's answer to the management Read Info command.
type MachineBT struct {
	Address        string `json:"address"`
	Version        string `json:"version"` // core specification, e.g. "5.1"
	ManufacturerID int    `json:"manufacturer_id"`
	Powered        bool   `json:"powered"`
	Name           string `json:"name"`
	// Firmware is the controller's answer to HCI Read Local Version
	// Information (#202); absent when it gave none.
	Firmware *MachineBTFirmware `json:"firmware,omitempty"`
}

// MachineBTFirmware is the firmware part of an HCI Read Local Version
// reply.
type MachineBTFirmware struct {
	HCIRevision   uint16 `json:"hci_revision"`
	LMPSubversion uint16 `json:"lmp_subversion"`
}

// Paths runs a capture of the running machine and returns every path the
// collectors touched, sorted: files read, directories listed, symlinks
// followed and paths only probed for existence (/.dockerenv, /run/...).
// ghw's own reads (CPU topology, block devices) aren't included; ghw's
// snapshot package covers those.
func Paths(version string) []string {
	captureMu.Lock()
	defer captureMu.Unlock()
	defer saveHooks()()
	seen := map[string]bool{}
	traceRead = func(path string) { seen[path] = true }
	collectNow(version)
	out := make([]string, 0, len(seen))
	for path := range seen {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

// CollectRecorded captures a recorded machine instead of the running one,
// as an unprivileged user on a host named "recorded". Drive health isn't
// recorded. It recreates the recording's empty directories under dir.
func CollectRecorded(dir, version string) (*report.Report, error) {
	m, err := readMachine(filepath.Join(dir, "machine.json"))
	if err != nil {
		return nil, err
	}
	rootDir := filepath.Join(dir, "root")
	if _, err := os.Stat(rootDir); err != nil {
		return nil, err
	}
	bt := map[uint16]*mgmtInfo{}
	btFirmware := map[uint16]*hciVersion{}
	for name, b := range m.Bluetooth {
		rest, isHCI := strings.CutPrefix(name, "hci")
		n, err := strconv.ParseUint(rest, 10, 16)
		if !isHCI || err != nil {
			return nil, fmt.Errorf("machine.json: bluetooth controller %q", name)
		}
		index := uint16(n)
		info := &mgmtInfo{address: b.Address, manufacturer: uint16(b.ManufacturerID), name: b.Name}
		if b.ManufacturerID < 0 || b.ManufacturerID > 0xFFFF {
			return nil, fmt.Errorf("machine.json: %s manufacturer ID %d out of range", name, b.ManufacturerID)
		}
		version, ok := btVersionCode(b.Version)
		if !ok {
			return nil, fmt.Errorf("machine.json: %s Bluetooth version %q unknown", name, b.Version)
		}
		info.version = version
		if b.Powered {
			info.settings = 1
		}
		bt[index] = info
		if f := b.Firmware; f != nil {
			btFirmware[index] = &hciVersion{hciRevision: f.HCIRevision, lmpSubver: f.LMPSubversion}
		}
	}
	failing := map[string]error{}
	for path, errno := range m.Unreadable {
		if errno <= 0 || errno > 4095 {
			return nil, fmt.Errorf("machine.json: %s: error number %d", path, errno)
		}
		failing[path] = syscall.Errno(errno)
	}
	for _, path := range append(slices.Collect(maps.Keys(m.Unreadable)), m.EmptyDirs...) {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, fmt.Errorf("machine.json: path %q isn't absolute and clean", path)
		}
	}
	for _, d := range m.EmptyDirs {
		if err := os.MkdirAll(filepath.Join(rootDir, d), 0o750); err != nil {
			return nil, err
		}
	}

	captureMu.Lock()
	defer captureMu.Unlock()
	defer saveHooks()()
	root, unreadable = rootDir, failing
	geteuid = func() int { return 1000 }
	hostname = func() (string, error) { return "recorded", nil }
	uname = func() (string, string) { return "", m.Arch }
	ethtoolDrvinfo = func(ifname string) (string, error) {
		if fw, ok := m.Ethtool[ifname]; ok {
			return fw, nil
		}
		return "", syscall.EOPNOTSUPP
	}
	readBTInfo = func(index uint16) (*mgmtInfo, error) {
		if info, ok := bt[index]; ok {
			return info, nil
		}
		return nil, errors.New("not recorded")
	}
	readBTVersion = func(index uint16) (*hciVersion, error) {
		if v, ok := btFirmware[index]; ok {
			return v, nil
		}
		return nil, errors.New("not recorded")
	}
	readRadios = func(ifaces map[string]int) (map[string]*report.WiFiRadio, error) {
		out := map[string]*report.WiFiRadio{}
		for name := range ifaces {
			if r, ok := m.WiFi[name]; ok {
				out[name] = &r
			}
		}
		return out, nil
	}
	nvmeHealthFn = func(string) (*report.Health, error) { return nil, errors.New("not recorded") }
	tpmTransmit = func(string, []byte) ([]byte, error) { return nil, errors.New("not recorded") }
	readKmsg = func() ([]string, int, error) { return nil, 0, errors.New("not recorded") }
	findSmartctl = func() string { return "" }
	return collectNow(version), nil
}

// readMachine decodes machine.json strictly: a misspelt field is an error,
// not a silently missing answer.
func readMachine(path string) (*Machine, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m Machine
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if strings.TrimSpace(m.Arch) == "" {
		return nil, fmt.Errorf("%s: no arch", path)
	}
	return &m, nil
}

// btVersionCode is btVersion's inverse.
func btVersionCode(v string) (byte, bool) {
	for code := range byte(len(btVersions)) {
		if btVersions[code] == v {
			return code, true
		}
	}
	return 0, false
}
