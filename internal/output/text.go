package output

import (
	"fmt"
	"io"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jiegui2025/hwspec/internal/report"
)

func writeText(w io.Writer, r *report.Report) error {
	var b strings.Builder
	line := func(label, format string, args ...any) {
		fmt.Fprintf(&b, "  %-10s %s\n", label, fmt.Sprintf(format, args...))
	}
	section := func(name string) { fmt.Fprintf(&b, "\n%s\n", name) }
	var attention []string
	watch := func(what string, h *report.Health) {
		if h == nil || (h.Status != report.StatusWarning && h.Status != report.StatusFailing) {
			return
		}
		for _, reason := range h.Reasons {
			attention = append(attention, fmt.Sprintf("%s %s: %s", strings.ToUpper(h.Status), what, reason))
		}
		if len(h.Reasons) == 0 {
			attention = append(attention, fmt.Sprintf("%s %s", strings.ToUpper(h.Status), what))
		}
	}

	fmt.Fprintf(&b, "%s %s  ·  captured %s", r.Tool.Name, r.Tool.Version, r.CapturedAt.Format("2006-01-02 15:04 MST"))
	if r.Hostname != "" {
		fmt.Fprintf(&b, " on %s", r.Hostname)
	}
	if !r.Privileged {
		b.WriteString("  ·  limited (not root)")
	}
	b.WriteString("\n")

	section("System")
	line("Machine", "%s", product(r.System.Identity))
	if r.System.ChassisType != "" {
		line("Chassis", "%s", r.System.ChassisType)
	}
	line("Board", "%s", product(r.Board.Identity))
	if fw := r.System.Firmware; fw != nil {
		line("Firmware", "%s", join(fw.Vendor, fw.Version, fw.Date))
	}
	sb := ""
	if r.OS.SecureBoot != nil {
		sb = map[bool]string{true: ", Secure Boot on", false: ", Secure Boot off"}[*r.OS.SecureBoot]
	}
	boot := r.OS.BootMode
	if boot == "" {
		boot = "boot mode unknown"
	}
	line("OS", "%s, kernel %s (%s, %s%s)", r.OS.PrettyName, r.OS.Kernel, r.OS.Arch, boot, sb)
	if r.OS.Virtualization != "none" && r.OS.Virtualization != "" {
		line("Runs in", "%s", r.OS.Virtualization)
	}

	section("CPU")
	if id := r.CPU.Identity; id != nil {
		line("Model", "%s", id.Model)
	}
	switch c, u := r.CPU.Codename, r.CPU.Microarchitecture; {
	case c != "" && u != "" && u != c:
		line("Codename", "%s (%s cores)", c, u)
	case c != "":
		line("Codename", "%s", c)
	case u != "":
		line("Cores are", "%s", u)
	}
	cores := fmt.Sprintf("%d cores / %d threads", r.CPU.Cores, r.CPU.Threads)
	if r.CPU.Sockets > 1 {
		cores = fmt.Sprintf("%d sockets, ", r.CPU.Sockets) + cores
	}
	for _, t := range r.CPU.CoreTypes {
		cores += fmt.Sprintf(", %d %s threads", t.Threads, t.Name)
	}
	line("Cores", "%s", cores)
	if r.CPU.MaxFreqMHz > 0 {
		clock := fmt.Sprintf("%d–%d MHz", r.CPU.MinFreqMHz, r.CPU.MaxFreqMHz)
		if d := driverText(r.CPU.Driver); d != "" {
			clock += " (" + d + ")"
		}
		line("Clock", "%s", clock)
	}
	var caches []string
	for _, c := range r.CPU.Caches {
		caches = append(caches, fmt.Sprintf("L%d%s %s×%d", c.Level, cacheSuffix(c.Type), bytesStr(c.SizeBytes), c.Instances))
	}
	if len(caches) > 0 {
		line("Cache", "%s", strings.Join(caches, ", "))
	}
	if fw := r.CPU.Firmware; fw != nil {
		line("Microcode", "%s", fw.Version)
	}
	if h := r.CPU.Health; h != nil {
		line("Health", "%s", healthText(h))
		watch("CPU", h)
	}

	section("Memory")
	mem := bytesStr(r.Memory.TotalBytes) + " usable"
	if r.Memory.InstalledBytes > 0 {
		mem = bytesStr(r.Memory.InstalledBytes) + " installed, " + mem
	}
	line("Total", "%s", mem)
	if r.Memory.Slots > 0 {
		line("Slots", "%d used of %d, max %s", len(r.Memory.Modules), r.Memory.Slots, bytesStr(r.Memory.MaxCapacityBytes))
	}
	for _, m := range r.Memory.Modules {
		var maker, part, made string
		if id := m.Identity; id != nil {
			maker, part = id.Vendor, id.PartNumber
			if id.ManufactureDate != "" {
				made = "made " + id.ManufactureDate
			}
		}
		s := join(sizeOrEmpty(m.SizeBytes), m.Type, m.FormFactor, mts(m.ConfiguredMTs, m.SpeedMTs), maker, part)
		s = withParts(s, made, chips(m.DRAMVendor, maker), healthShort(m.Health))
		line(m.Locator, "%s", s)
		watch("memory "+m.Locator, m.Health)
	}

	if len(r.Storage) > 0 {
		section("Storage")
		for _, d := range r.Storage {
			typ := d.Type
			if typ == "unknown" {
				typ = ""
			}
			s := join(model(d.Identity), bytesStr(d.SizeBytes), typ)
			if d.Transport != d.Type {
				s += " via " + d.Transport
			}
			line(d.Name, "%s", withParts(s, fwText(d.Firmware), driverText(d.Driver), healthText(d.Health)))
			watch("disk "+d.Name, d.Health)
		}
	}

	if len(r.GPUs) > 0 || len(r.Displays) > 0 {
		section("Graphics")
		for _, g := range r.GPUs {
			s := product(g.Identity)
			if g.VRAMBytes > 0 {
				s += ", " + bytesStr(g.VRAMBytes) + " VRAM"
			}
			if c := g.Clocks; c != nil {
				if c.MinFreqMHz == c.MaxFreqMHz {
					s += fmt.Sprintf(", %d MHz", c.MaxFreqMHz)
				} else {
					s += fmt.Sprintf(", %d–%d MHz", c.MinFreqMHz, c.MaxFreqMHz)
				}
				switch a := c.ActualFreqMHz; {
				case a == nil:
				case *a == 0:
					s += " (idle at capture)"
				default:
					s += fmt.Sprintf(" (%d MHz at capture)", *a)
				}
			}
			line("GPU", "%s", withParts(s, fwText(g.Firmware), driverText(g.Driver)))
		}
		for _, d := range r.Displays {
			s := product(d.Identity)
			if d.NativeWidth > 0 {
				s += fmt.Sprintf(", %d×%d @ %.0f Hz", d.NativeWidth, d.NativeHeight, d.NativeRefreshHz)
			}
			if d.DiagonalIn > 0 {
				s += fmt.Sprintf(", %.1f\"", d.DiagonalIn)
			}
			made := ""
			if d.Identity != nil && d.Identity.ManufactureDate != "" {
				made = "made " + d.Identity.ManufactureDate
			}
			line("Display", "%s", withParts(s, made, d.Connector))
		}
	}

	if len(r.Network) > 0 {
		section("Network")
		for _, n := range r.Network {
			s := product(n.Identity) + ", " + n.Type + ", " + n.State
			if n.SpeedMbps > 0 {
				s += fmt.Sprintf(", %d Mb/s", n.SpeedMbps)
			}
			line(n.Name, "%s", withParts(s, fwText(n.Firmware), driverText(n.Driver), healthShort(n.Health)))
			watch("network "+n.Name, n.Health)
		}
	}

	if len(r.Bluetooth) > 0 {
		section("Bluetooth")
		for _, bt := range r.Bluetooth {
			s := product(bt.Identity)
			if bt.Version != "" {
				s += ", Bluetooth " + bt.Version
			}
			if f := strings.Fields(bt.Manufacturer); len(f) > 0 && !strings.Contains(strings.ToLower(s), strings.ToLower(f[0])) {
				s += ", chip by " + bt.Manufacturer
			}
			if bt.Powered != nil && !*bt.Powered {
				s += ", off"
			}
			line(bt.Name, "%s", withParts(s, driverText(bt.Driver)))
		}
	}

	if len(r.Audio) > 0 {
		section("Audio")
		for _, a := range r.Audio {
			var names []string
			for _, c := range a.Codecs {
				names = append(names, model(c.Identity))
			}
			s := a.Name
			if len(names) > 0 {
				s += ": " + strings.Join(names, ", ")
			}
			line(fmt.Sprintf("card %d", a.Index), "%s", withParts(s, driverText(a.Driver)))
		}
	}

	if len(r.Batteries) > 0 {
		section("Battery")
		for _, bt := range r.Batteries {
			s := product(bt.Identity)
			if bt.CapacityPercent > 0 {
				s += fmt.Sprintf(", %d%% charged", bt.CapacityPercent)
			}
			if h := bt.Health; h != nil {
				full, ok1 := h.Metrics[report.MetricFullWh]
				design, ok2 := h.Metrics[report.MetricDesignWh]
				if ok1 && ok2 {
					s += fmt.Sprintf(", holds %s of %s Wh", trimFloat(full), trimFloat(design))
				}
			}
			line(bt.Name, "%s", withParts(s, healthText(bt.Health)))
			watch("battery "+bt.Name, bt.Health)
		}
	}

	if len(r.USB) > 0 {
		section("USB")
		for _, u := range r.USB {
			line(u.Path, "%s", withParts(product(u.Identity), fwText(u.Firmware)))
		}
	}

	fmt.Fprintf(&b, "\n%d PCI devices, %d sensor chips. Full detail (serials, drivers, metrics) is in the JSON/YAML output.\n", len(r.PCI), len(r.Sensors))
	if len(attention) > 0 {
		// The reasons are data from the capture, which may be a file
		// someone else made; say where they come from.
		section("Needs attention (as recorded in this capture)")
		for _, a := range attention {
			fmt.Fprintf(&b, "  - %s\n", a)
		}
	}
	if len(r.Warnings) > 0 {
		section("Not captured")
		for _, warn := range r.Warnings {
			fmt.Fprintf(&b, "  - %s\n", warn)
		}
	}
	_, err := io.WriteString(w, terminalSafe(b.String()))
	return err
}

// product is "vendor model", without repeating the vendor when the model
// already starts with the vendor's brand as a whole word, ignoring case:
// "HP HP EliteDesk" and "Dell Inc. Dell G15" print the brand once. Vendor "HP"
// with model "HPE ProLiant" keeps both: they're different makers.
func product(id *report.Identity) string {
	if id == nil {
		return "unknown"
	}
	if startsWithWord(strings.TrimSpace(id.Model), brand(id.Vendor)) {
		return join(id.Model)
	}
	return join(id.Vendor, id.Model)
}

// brand is the vendor's first word without a trailing '.' or ',': "Dell" for
// "Dell Inc.", "VMware" for "VMware, Inc.". Models name the brand, not the
// company ("Dell G15 5520", "VMware Virtual Platform").
func brand(vendor string) string {
	f := strings.Fields(vendor)
	if len(f) == 0 {
		return ""
	}
	return strings.TrimRight(f[0], ".,")
}

// startsWithWord reports whether s starts with word, ignoring case, and the
// match ends at a word boundary: "HP EliteDesk" starts with "hp", "HPE" doesn't.
func startsWithWord(s, word string) bool {
	if word == "" || len(s) < len(word) || !strings.EqualFold(s[:len(word)], word) {
		return false
	}
	rest := s[len(word):]
	if rest == "" {
		return true
	}
	next, _ := utf8.DecodeRuneInString(rest)
	return !unicode.IsLetter(next) && !unicode.IsDigit(next)
}

func model(id *report.Identity) string {
	if id == nil || id.Model == "" {
		return product(id)
	}
	return id.Model
}

func fwText(fw *report.Firmware) string {
	if fw == nil {
		return ""
	}
	return "fw " + fw.Version
}

// driverText names the driver and flags what matters for maintenance:
// drivers outside the kernel tree, proprietary ones.
func driverText(d *report.Driver) string {
	if d == nil {
		return ""
	}
	var flags []string
	if d.Builtin {
		flags = append(flags, "built in")
	}
	if d.InTree != nil && !*d.InTree {
		flags = append(flags, "out-of-tree")
	}
	if d.Proprietary != nil && *d.Proprietary {
		flags = append(flags, "proprietary")
	}
	if d.Version != "" {
		flags = append(flags, d.Version)
	}
	if len(flags) == 0 {
		return d.Name
	}
	return d.Name + " " + strings.Join(flags, ", ")
}

// healthText is the full summary: status, wear, and the key metrics.
func healthText(h *report.Health) string {
	if h == nil {
		return ""
	}
	parts := []string{"health " + strings.ToUpper(h.Status)}
	if h.LifeUsedPercent != nil {
		s := fmt.Sprintf("%.0f%% worn", *h.LifeUsedPercent)
		if h.LifeRemainingPercent != nil {
			s += fmt.Sprintf(" (%.0f%% life left)", *h.LifeRemainingPercent)
		}
		parts = append(parts, s)
	}
	for _, m := range []struct{ key, format string }{
		{report.MetricPowerOnHours, "%.0f h on"},
		{report.MetricCycleCount, "%.0f cycles"},
		{report.MetricThrottleEvents, "%.0f throttle events"},
	} {
		if v, ok := h.Metrics[m.key]; ok {
			parts = append(parts, fmt.Sprintf(m.format, v))
		}
	}
	if h.Estimate != nil {
		parts = append(parts, fmt.Sprintf("~%s %s", trimFloat(h.Estimate.Value), h.Estimate.What))
	}
	return strings.Join(parts, ", ")
}

// healthShort only mentions health when it isn't OK.
func healthShort(h *report.Health) string {
	if h == nil || h.Status == report.StatusOK || h.Status == report.StatusUnknown {
		return ""
	}
	return "health " + strings.ToUpper(h.Status)
}

func chips(dram, module string) string {
	if dram == "" || strings.EqualFold(dram, module) {
		return ""
	}
	return dram + " chips"
}

func sizeOrEmpty(n uint64) string {
	if n == 0 {
		return ""
	}
	return bytesStr(n)
}

// withParts appends non-empty parts after a comma.
func withParts(s string, parts ...string) string {
	for _, p := range parts {
		if p != "" {
			s += ", " + p
		}
	}
	return s
}

func trimFloat(v float64) string {
	if v == math.Trunc(v) {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.1f", v)
}

// terminalSafe drops control characters other than newlines, so names from
// devices, captures or ID databases can't carry terminal escape sequences
// (clearing the screen, rewriting the clipboard via OSC 52, ...).
func terminalSafe(s string) string {
	s = strings.ToValidUTF8(s, "�")
	return strings.Map(func(r rune) rune {
		if r != '\n' && unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

func join(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return "unknown"
	}
	return strings.Join(out, " ")
}

func mts(configured, max int) string {
	switch {
	case configured > 0 && max > 0 && configured != max:
		return fmt.Sprintf("%d MT/s (rated %d)", configured, max)
	case configured > 0:
		return fmt.Sprintf("%d MT/s", configured)
	case max > 0:
		return fmt.Sprintf("%d MT/s", max)
	}
	return ""
}

func cacheSuffix(t string) string {
	switch t {
	case "Data":
		return "d"
	case "Instruction":
		return "i"
	}
	return ""
}

// bytesStr formats sizes with binary units (KiB, MiB, GiB, TiB).
func bytesStr(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	v := float64(n) / float64(div)
	s := fmt.Sprintf("%.1f", v)
	s = strings.TrimSuffix(s, ".0")
	return s + " " + []string{"KiB", "MiB", "GiB", "TiB", "PiB"}[exp]
}
