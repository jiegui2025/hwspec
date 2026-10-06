// Package ids resolves hardware IDs (PCI, USB, monitor vendors, MAC
// prefixes, memory manufacturers, AMD GPU names) to names, entirely offline.
//
// Each database has up to three sources, and the newest one is used:
//
//   - the copy embedded in the binary (always present)
//   - the distro's copy, e.g. /usr/share/hwdata/pci.ids
//   - a synced copy in $XDG_DATA_HOME/hwspec/ids/, written by
//     `hwspec ids update`
//
// "Newest" compares the date in the embedded and synced manifests, the
// file's own "Version"/"Date" header, or, for distro files without one,
// the file's modification time. If the newest source can't be read, the
// next newest is used. The user's corrections in
// $XDG_CONFIG_HOME/hwspec/overrides.ids are then applied on top.
package ids

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"embed"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed data/*.ids.gz data/manifest.json
var embedded embed.FS

// Kind identifies one ID database.
type Kind string

const (
	PCI    Kind = "pci"
	USB    Kind = "usb"
	PNP    Kind = "pnp"
	OUI    Kind = "oui"
	JEDEC  Kind = "jedec"
	AMDGPU Kind = "amdgpu"
	BT     Kind = "bluetooth"
	CPU    Kind = "cpu"
)

var Kinds = []Kind{PCI, USB, PNP, OUI, JEDEC, AMDGPU, BT, CPU}

type spec struct {
	file   string   // embedded and synced file name
	system []string // distro locations, first found is used
	parse  func(r io.Reader, m map[string]string) error
}

var specs = map[Kind]spec{
	PCI: {"pci.ids", hwdata("pci.ids"), parsePCIStyle},
	USB: {"usb.ids", hwdata("usb.ids"), parsePCIStyle},
	PNP: {"pnp.ids", hwdata("pnp.ids"), parseTabbed},
	OUI: {"oui.ids", append(hwdata("oui.txt"), "/usr/share/ieee-data/oui.txt"), parseOUI},
	// No distro ships this as a data file; decode-dimms has it in Perl code.
	JEDEC: {"jedec.ids", nil, parseJEDEC},
	AMDGPU: {"amdgpu.ids", []string{
		"/usr/share/libdrm/amdgpu.ids",
		"/run/current-system/sw/share/libdrm/amdgpu.ids",
	}, parseAMDGPU},
	BT:  {"bluetooth.ids", nil, parseTabbed},
	CPU: {"cpu.ids", nil, parseCPU},
}

func hwdata(name string) []string {
	return []string{
		"/usr/share/hwdata/" + name,
		"/usr/share/misc/" + name,
		"/usr/share/" + name,
		"/run/current-system/sw/share/hwdata/" + name, // NixOS
	}
}

// These are variables so tests can redirect them.
var (
	systemEnabled = true
	syncedDir     = xdgPath("XDG_DATA_HOME", ".local/share", "hwspec/ids")
	overridesPath = xdgPath("XDG_CONFIG_HOME", ".config", "hwspec/overrides.ids")
)

func xdgPath(env, fallback, rel string) string {
	base := os.Getenv(env)
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, fallback)
	}
	return filepath.Join(base, rel)
}

// UseEnvironment re-reads $XDG_DATA_HOME, $XDG_CONFIG_HOME and $HOME for
// the synced and overrides locations (they are read once at start-up) and
// drops loaded databases, so the next lookup uses the new locations.
//
// UseEnvironment, UseSystemDatabases and UseEmbeddedOnly change where
// names come from; they must not run concurrently with lookups.
func UseEnvironment() {
	syncedDir = xdgPath("XDG_DATA_HOME", ".local/share", "hwspec/ids")
	overridesPath = xdgPath("XDG_CONFIG_HOME", ".config", "hwspec/overrides.ids")
	Reset()
}

// UseSystemDatabases turns the distribution's copies (/usr/share/hwdata,
// ...) on or off as a source of names.
func UseSystemDatabases(on bool) {
	systemEnabled = on
	Reset()
}

// UseEmbeddedOnly makes lookups use only the databases built into hwspec:
// no distribution copies, synced databases or overrides. Names then don't
// depend on the machine, as tests comparing captures need.
func UseEmbeddedOnly() {
	systemEnabled, syncedDir, overridesPath = false, "", ""
	Reset()
}

// OverridesPath is where user corrections are read from.
func OverridesPath() string { return overridesPath }

// Layer describes a source that was used for a database (or tried and
// failed, in which case Err is set).
type Layer struct {
	Source  string // "embedded" or a file path
	Date    string // how current the source is, "" if unknown
	Entries int    // names this layer contributed
	Err     string
}

type db struct {
	names  map[string]string
	layers []Layer
}

// entry holds one database: loaded at most once, and marked used by the
// first lookup. Each kind loads on its own, so several can load at once.
type entry struct {
	once sync.Once
	d    *db
	used bool // guarded by mu
}

var (
	mu  sync.Mutex
	dbs = map[Kind]*entry{}
)

func entryFor(k Kind) *entry {
	mu.Lock()
	defer mu.Unlock()
	e := dbs[k]
	if e == nil {
		e = &entry{}
		dbs[k] = e
	}
	return e
}

func get(k Kind) *db {
	e := entryFor(k)
	e.once.Do(func() { e.d = load(k) })
	mu.Lock()
	e.used = true
	mu.Unlock()
	return e.d
}

// Preload loads the given databases in parallel and returns when all are
// loaded. It doesn't mark them used: only a lookup does, so preloading a
// database a report never needs doesn't add it to Loaded.
func Preload(kinds ...Kind) {
	var wg sync.WaitGroup
	for _, k := range kinds {
		e := entryFor(k)
		wg.Go(func() { e.once.Do(func() { e.d = load(k) }) })
	}
	wg.Wait()
}

// Reset drops loaded databases so the next lookup reloads them (used by
// tests, and after syncing new files).
func Reset() {
	mu.Lock()
	dbs = map[Kind]*entry{}
	mu.Unlock()
	ovMu.Lock()
	ovCache = map[string]ovResult{}
	ovMu.Unlock()
}

type source struct {
	name string
	open func() (io.ReadCloser, error)
	date string
	// parsed, when set, returns the source's names already parsed and
	// shared: the embedded copy, which never changes, is parsed once per
	// process however often databases are reset (tests reset them a lot).
	parsed func() (map[string]string, error)
}

func load(k Kind) *db {
	s := specs[k]
	d := &db{names: make(map[string]string, sizeHint(s.file))}

	// Candidates in tie-break order: on equal dates the earlier one wins.
	var cands []source
	if syncedDir != "" {
		path := filepath.Join(syncedDir, s.file+".gz")
		if _, err := os.Stat(path); err == nil {
			// Synced files are dated by the signed manifest saved with them;
			// without a readable manifest there's no trustworthy date, so
			// they aren't used (and the reason is shown by `hwspec ids`).
			if m, err := syncedManifest(); err != nil {
				d.layers = append(d.layers, Layer{Source: path, Err: "synced manifest unreadable: " + err.Error()})
			} else {
				cands = append(cands, source{name: path, open: fileOpener(path), date: manifestDate(m, s.file)})
			}
		}
	}
	if systemEnabled {
		for _, path := range s.system {
			st, err := os.Stat(path)
			if err != nil {
				continue
			}
			src := source{name: path, open: fileOpener(path)}
			// Undated files are dated by modification time, unless that is
			// in the future too (a broken clock): then they rank as oldest.
			if src.date = sourceDate(src.open); src.date == "" && st.ModTime().Before(time.Now().Add(24*time.Hour)) {
				src.date = st.ModTime().UTC().Format("2006-01-02")
			}
			cands = append(cands, src)
			break
		}
	}
	cands = append(cands, source{name: "embedded", date: manifestDate(embeddedManifest(), s.file),
		parsed: func() (map[string]string, error) { return embeddedNames(k) }})
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].date > cands[j].date })

	shared := false // d.names is the shared embedded map: copy before changing it
	for _, src := range cands {
		layer := Layer{Source: src.name, Date: src.date}
		var err error
		if src.parsed != nil {
			var m map[string]string
			if m, err = src.parsed(); err == nil {
				d.names, shared = m, true
			}
		} else {
			var r io.ReadCloser
			if r, err = src.open(); err == nil {
				err = s.parse(r, d.names)
				r.Close()
			}
		}
		if err == nil && len(d.names) == 0 {
			err = errors.New("no entries")
		}
		if err != nil {
			layer.Err = err.Error()
			d.names, shared = make(map[string]string, sizeHint(s.file)), false
			d.layers = append(d.layers, layer)
			continue // fall back to the next newest
		}
		layer.Entries = len(d.names)
		d.layers = append(d.layers, layer)
		break
	}

	// Mistakes in the overrides file are reported once, by OverridesError,
	// not as a layer of every database.
	ov, _ := loadOverrides(overridesPath)
	if len(ov[k]) > 0 {
		if shared {
			d.names = maps.Clone(d.names)
		}
		maps.Copy(d.names, ov[k])
		d.layers = append(d.layers, Layer{Source: overridesPath, Entries: len(ov[k])})
	}
	return d
}

var (
	embManifestOnce sync.Once
	embManifest     *Manifest
)

func embeddedManifest() *Manifest {
	embManifestOnce.Do(func() {
		if b, err := embedded.ReadFile("data/manifest.json"); err == nil {
			embManifest, _ = ParseManifest(b)
		}
	})
	return embManifest
}

// syncedManifest reads the manifest saved by the last `hwspec ids update`.
// It returns (nil, nil) when nothing has been synced.
func syncedManifest() (*Manifest, error) {
	if syncedDir == "" {
		return nil, nil // no synced databases (no home directory, or embedded only)
	}
	b, err := os.ReadFile(filepath.Join(syncedDir, "manifest.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return ParseManifest(b)
}

var (
	embMu     sync.Mutex
	embParsed = map[Kind]*embeddedParse{}
)

type embeddedParse struct {
	once  sync.Once
	names map[string]string
	err   error
}

// embeddedNames parses the embedded copy of a database once per process
// and shares the result, which callers must not change.
func embeddedNames(k Kind) (map[string]string, error) {
	embMu.Lock()
	e := embParsed[k]
	if e == nil {
		e = &embeddedParse{}
		embParsed[k] = e
	}
	embMu.Unlock()
	e.once.Do(func() {
		s := specs[k]
		raw, err := embedded.ReadFile("data/" + s.file + ".gz")
		if err != nil {
			e.err = err
			return
		}
		r, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			e.err = err
			return
		}
		m := make(map[string]string, sizeHint(s.file))
		if e.err = s.parse(r, m); e.err == nil {
			e.names = m
		}
	})
	return e.names, e.err
}

// sizeHint is how many names the embedded copy of a database holds, so
// its map is allocated once instead of growing through every rehash.
func sizeHint(file string) int {
	if m := embeddedManifest(); m != nil {
		return m.Files[file+".gz"].Entries
	}
	return 0
}

func manifestDate(m *Manifest, file string) string {
	if m == nil {
		return ""
	}
	return m.Files[file+".gz"].Date
}

func fileOpener(path string) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) {
		f, err := os.Open(path)
		if err != nil || !strings.HasSuffix(path, ".gz") {
			return f, err
		}
		zr, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		return struct {
			io.Reader
			io.Closer
		}{zr, f}, nil
	}
}

var dateLine = regexp.MustCompile(`^#\s*(?:Version|Date):\s*(\d{4})[.-](\d{2})[.-](\d{2})`)

// sourceDate reads the "# Version: 2026.09.03" / "# Date: 2026-09-03" header
// that pci.ids, usb.ids and the generated files carry. "" if there is none.
func sourceDate(open func() (io.ReadCloser, error)) string {
	r, err := open()
	if err != nil {
		return ""
	}
	defer r.Close()
	sc := bufio.NewScanner(r)
	for i := 0; i < 30 && sc.Scan(); i++ {
		if m := dateLine.FindStringSubmatch(sc.Text()); m != nil {
			date := m[1] + "-" + m[2] + "-" + m[3]
			// A date in the future can't be right, and would make the file
			// look newest forever; treat it as undated.
			if date > time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02") {
				return ""
			}
			return date
		}
	}
	_ = sc.Err() // deliberately unchecked: a read error mid-header also means "undated"
	return ""
}

func scanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	return sc
}

// parsePCIStyle reads the pci.ids / usb.ids format into keys:
//
//	"8086"                vendor
//	"8086:3e92"           device
//	"8086:3e92:103c:8595" subsystem (pci.ids only)
//	"class:03", "class:0300", "class:030000"
func parsePCIStyle(r io.Reader, m map[string]string) error {
	var vendor, device, class, subclass string
	inClass := false
	sc := scanner(r)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		switch {
		case strings.HasPrefix(line, "C "):
			inClass = true
			id, label := split(line[2:])
			class = id
			m["class:"+class] = label
		case line[0] != '\t':
			// A new top-level section. usb.ids has non-vendor sections
			// (AT, HID, L, ...) after the vendors; their keys never collide
			// with 4-digit hex vendor IDs, so storing them is harmless.
			inClass = false
			id, label := split(line)
			vendor = id
			m[vendor] = label
		case strings.HasPrefix(line, "\t\t"):
			fields := line[2:]
			if inClass {
				id, label := split(fields)
				m["class:"+class+subclass+id] = label
				continue
			}
			// "\t\tSUBVENDOR SUBDEVICE  Name"
			parts := strings.SplitN(fields, " ", 3)
			if len(parts) == 3 {
				m[vendor+":"+device+":"+strings.ToLower(parts[0]+":"+parts[1])] = cleanName(parts[2])
			}
		default: // one tab
			id, label := split(line[1:])
			if inClass {
				subclass = id
				m["class:"+class+subclass] = label
				continue
			}
			device = id
			m[vendor+":"+device] = label
		}
	}
	return sc.Err()
}

func split(s string) (id, label string) {
	id, label, _ = strings.Cut(s, " ")
	return strings.ToLower(id), cleanName(label)
}

// parseTabbed reads "KEY<TAB>Name" lines (pnp.ids).
func parseTabbed(r io.Reader, m map[string]string) error {
	sc := scanner(r)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "\t"); ok {
			m[strings.ToUpper(strings.TrimSpace(k))] = cleanName(v)
		}
	}
	return sc.Err()
}

// parseOUI reads both the raw IEEE oui.txt ("040E3C     (base 16)  Name")
// and the compact "040E3C<TAB>Name" form the embedded copy uses.
func parseOUI(r io.Reader, m map[string]string) error {
	sc := scanner(r)
	for sc.Scan() {
		line := sc.Text()
		if prefix, name, ok := strings.Cut(line, "(base 16)"); ok {
			if k := strings.TrimSpace(prefix); len(k) == 6 {
				m[strings.ToUpper(k)] = cleanName(name)
			}
			continue
		}
		if k, v, ok := strings.Cut(line, "\t"); ok && len(k) == 6 && !strings.HasPrefix(k, "#") {
			m[strings.ToUpper(k)] = cleanName(v)
		}
	}
	return sc.Err()
}

// parseJEDEC reads "BANK ID<TAB>Name" lines into "BANK:ID" keys, e.g. "6:77".
func parseJEDEC(r io.Reader, m map[string]string) error {
	sc := scanner(r)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		var bank, id int
		if _, err := fmt.Sscanf(k, "%d %x", &bank, &id); err == nil {
			m[jedecKey(bank, id)] = cleanName(v)
		}
	}
	return sc.Err()
}

// parseCPU reads "intel:6:9e[:10]<TAB>Codename<TAB>Microarchitecture" lines,
// keeping "Codename<TAB>Microarchitecture" as the value.
func parseCPU(r io.Reader, m map[string]string) error {
	sc := scanner(r)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "\t"); ok {
			codename, uarch, _ := strings.Cut(v, "\t")
			m[strings.ToLower(k)] = cleanName(codename) + "\t" + cleanName(uarch)
		}
	}
	return sc.Err()
}

func jedecKey(bank, id int) string { return fmt.Sprintf("%d:%02X", bank, id) }

// parseAMDGPU reads libdrm's "DEVICE,\tREVISION,\tName" lines into
// "device:revision" keys, e.g. "1114:c2".
func parseAMDGPU(r io.Reader, m map[string]string) error {
	sc := scanner(r)
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), ",", 3)
		if len(parts) != 3 || strings.HasPrefix(parts[0], "#") {
			continue
		}
		m[norm(parts[0])+":"+norm(parts[1])] = cleanName(parts[2])
	}
	return sc.Err()
}

func norm(id string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(id), "0x"))
}

// PCIVendor returns the vendor name for a 4-digit hex ID, or "".
func PCIVendor(vendor string) string { return get(PCI).names[norm(vendor)] }

// PCIDevice returns the device name, or "".
func PCIDevice(vendor, device string) string {
	return get(PCI).names[norm(vendor)+":"+norm(device)]
}

// PCISubsystem returns the subsystem (board) name, or "".
func PCISubsystem(vendor, device, subVendor, subDevice string) string {
	return get(PCI).names[norm(vendor)+":"+norm(device)+":"+norm(subVendor)+":"+norm(subDevice)]
}

// PCIClass takes the 6-hex-digit class code (e.g. "030000") and returns the
// subclass name ("VGA compatible controller"), or the class name if there is
// no subclass entry. Like lspci, it ignores the programming interface.
func PCIClass(code string) string {
	c := norm(code)
	names := get(PCI).names
	for _, n := range []int{4, 2} {
		if len(c) >= n {
			if name, ok := names["class:"+c[:n]]; ok {
				return name
			}
		}
	}
	return ""
}

// USBVendor returns the vendor name for a 4-digit hex ID, or "".
func USBVendor(vendor string) string { return get(USB).names[norm(vendor)] }

// USBProduct returns the product name, or "".
func USBProduct(vendor, product string) string {
	return get(USB).names[norm(vendor)+":"+norm(product)]
}

// USBClass returns the name of a 2-hex-digit USB class code, or "".
func USBClass(code string) string { return get(USB).names["class:"+norm(code)] }

// PNPVendor maps a 3-letter EDID manufacturer ID (e.g. "DEL") to a name.
func PNPVendor(id string) string { return get(PNP).names[strings.ToUpper(strings.TrimSpace(id))] }

// AMDGPUName returns the retail name for an AMD GPU (device and revision
// in hex, e.g. "1114", "c2"), or "".
func AMDGPUName(device, revision string) string {
	return get(AMDGPU).names[norm(device)+":"+norm(revision)]
}

// BluetoothCompany returns the company for a Bluetooth SIG company ID, or "".
func BluetoothCompany(id uint16) string {
	return get(BT).names[fmt.Sprintf("%04X", id)]
}

// CPUCodename returns the codename and core microarchitecture for an x86
// CPU signature, preferring a stepping-specific entry. vendor is the CPUID
// vendor string ("GenuineIntel", "AuthenticAMD").
func CPUCodename(vendor string, family, model, stepping int) (codename, uarch string) {
	var v string
	switch vendor {
	case "GenuineIntel":
		v = "intel"
	case "AuthenticAMD":
		v = "amd"
	default:
		return "", ""
	}
	names := get(CPU).names
	key := fmt.Sprintf("%s:%x:%02x", v, family, model)
	val, ok := names[fmt.Sprintf("%s:%d", key, stepping)]
	if !ok {
		val = names[key]
	}
	codename, uarch, _ = strings.Cut(val, "\t")
	return codename, uarch
}

// Layers reports where a database's entries came from.
func Layers(k Kind) []Layer { return get(k).layers }

// Entries returns how many names a database holds.
func Entries(k Kind) int { return len(get(k).names) }

// Loaded describes the databases looked up so far, for recording in a report:
// the source each one's names came from, e.g. "synced (2026-10-05)" or
// "/usr/share/hwdata/pci.ids (2026-09-03) + overrides". Sources that failed
// are left out. The synced copy and the overrides file live in the user's
// home directory, so they are named by role rather than path.
func Loaded() map[Kind]string {
	mu.Lock()
	defer mu.Unlock()
	out := map[Kind]string{}
	for k, e := range dbs {
		if !e.used {
			continue // preloaded, never looked up
		}
		d := e.d
		var parts []string
		for _, l := range d.layers {
			if l.Err != "" {
				continue
			}
			s := l.Source
			switch {
			case s == overridesPath:
				s = "overrides"
			case syncedDir != "" && strings.HasPrefix(s, syncedDir+string(filepath.Separator)):
				s = "synced"
			}
			if l.Date != "" {
				s += " (" + l.Date + ")"
			}
			parts = append(parts, s)
		}
		out[k] = strings.Join(parts, " + ")
	}
	return out
}

// OverridesError reports problems in the overrides file (nil if it is
// missing or fine). Valid lines still apply when some lines are bad.
func OverridesError() error {
	_, err := loadOverrides(overridesPath)
	return err
}
