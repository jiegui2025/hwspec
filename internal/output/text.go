package output

import (
	"fmt"
	"io"
	"strings"

	"github.com/jiegui2025/hwspec/internal/report"
)

func writeText(w io.Writer, r *report.Report) error {
	var b strings.Builder
	line := func(label, format string, args ...any) {
		fmt.Fprintf(&b, "  %-10s %s\n", label, fmt.Sprintf(format, args...))
	}
	section := func(name string) { fmt.Fprintf(&b, "\n%s\n", name) }

	fmt.Fprintf(&b, "%s %s  ·  captured %s", r.Tool.Name, r.Tool.Version, r.CapturedAt.Format("2006-01-02 15:04 MST"))
	if r.Hostname != "" {
		fmt.Fprintf(&b, " on %s", r.Hostname)
	}
	if !r.Privileged {
		b.WriteString("  ·  limited (not root)")
	}
	b.WriteString("\n")

	section("System")
	line("Machine", "%s", join(vendorUnlessInProduct(r.System.Vendor, r.System.Product), r.System.Product, r.System.Version))
	if r.System.ChassisType != "" {
		line("Chassis", "%s", r.System.ChassisType)
	}
	line("Board", "%s", join(r.Board.Vendor, r.Board.Product, r.Board.Version))
	line("BIOS", "%s", join(r.BIOS.Vendor, r.BIOS.Version, r.BIOS.Date))
	sb := ""
	if r.OS.SecureBoot != nil {
		sb = map[bool]string{true: ", Secure Boot on", false: ", Secure Boot off"}[*r.OS.SecureBoot]
	}
	line("OS", "%s, kernel %s (%s, %s%s)", r.OS.PrettyName, r.OS.Kernel, r.OS.Arch, r.OS.BootMode, sb)
	if r.OS.Virtualization != "none" {
		line("Runs in", "%s", r.OS.Virtualization)
	}

	section("CPU")
	line("Model", "%s", r.CPU.Model)
	if r.CPU.Codename != "" {
		arch := r.CPU.Codename
		if r.CPU.Microarchitecture != "" && r.CPU.Microarchitecture != r.CPU.Codename {
			arch += " (" + r.CPU.Microarchitecture + " cores)"
		}
		line("Codename", "%s", arch)
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
		line("Clock", "%d–%d MHz", r.CPU.MinFreqMHz, r.CPU.MaxFreqMHz)
	}
	var caches []string
	for _, c := range r.CPU.Caches {
		caches = append(caches, fmt.Sprintf("L%d%s %s×%d", c.Level, cacheSuffix(c.Type), bytesStr(c.SizeBytes), c.Instances))
	}
	if len(caches) > 0 {
		line("Cache", "%s", strings.Join(caches, ", "))
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
		line(m.Locator, "%s", join(bytesStr(m.SizeBytes), m.Type, m.FormFactor, mts(m.ConfiguredMTs, m.SpeedMTs), m.Manufacturer, m.PartNumber))
	}

	if len(r.Storage) > 0 {
		section("Storage")
		for _, d := range r.Storage {
			s := join(d.Model, bytesStr(d.SizeBytes), d.Type)
			if d.Transport != d.Type {
				s += " via " + d.Transport
			}
			if h := d.Health; h != nil {
				if h.Passed != nil {
					s += map[bool]string{true: ", health OK", false: ", HEALTH FAILING"}[*h.Passed]
				}
				if h.PercentageUsed != nil {
					s += fmt.Sprintf(", %d%% worn", *h.PercentageUsed)
				}
				if h.PowerOnHours != nil {
					s += fmt.Sprintf(", %d h on", *h.PowerOnHours)
				}
			}
			line(d.Name, "%s", s)
		}
	}

	if len(r.GPUs) > 0 || len(r.Displays) > 0 {
		section("Graphics")
		for _, g := range r.GPUs {
			s := join(g.Vendor, g.Model)
			if g.VRAMBytes > 0 {
				s += ", " + bytesStr(g.VRAMBytes) + " VRAM"
			}
			if g.Driver != "" {
				s += " (" + g.Driver + ")"
			}
			line("GPU", "%s", s)
		}
		for _, d := range r.Displays {
			s := join(d.Manufacturer, d.Model)
			if d.NativeWidth > 0 {
				s += fmt.Sprintf(", %d×%d @ %.0f Hz", d.NativeWidth, d.NativeHeight, d.NativeRefreshHz)
			}
			if d.DiagonalIn > 0 {
				s += fmt.Sprintf(", %.1f\"", d.DiagonalIn)
			}
			line("Display", "%s (%s)", s, d.Connector)
		}
	}

	if len(r.Network) > 0 {
		section("Network")
		for _, n := range r.Network {
			s := join(n.Vendor, n.Model) + ", " + n.Type + ", " + n.State
			if n.SpeedMbps > 0 {
				s += fmt.Sprintf(", %d Mb/s", n.SpeedMbps)
			}
			line(n.Name, "%s", s)
		}
	}

	if len(r.Bluetooth) > 0 {
		section("Bluetooth")
		for _, b := range r.Bluetooth {
			s := join(b.Vendor, b.Model)
			if b.Version != "" {
				s += ", Bluetooth " + b.Version
			}
			if b.Manufacturer != "" && !strings.Contains(s, strings.Fields(b.Manufacturer)[0]) {
				s += ", chip by " + b.Manufacturer
			}
			if b.Powered != nil && !*b.Powered {
				s += ", off"
			}
			line(b.Name, "%s", s)
		}
	}

	if len(r.Audio) > 0 {
		section("Audio")
		for _, a := range r.Audio {
			s := fmt.Sprintf("%s (%s)", a.Name, a.Driver)
			var names []string
			for _, c := range a.Codecs {
				names = append(names, c.Name)
			}
			if len(names) > 0 {
				s += ": " + strings.Join(names, ", ")
			}
			line(fmt.Sprintf("card %d", a.Index), "%s", s)
		}
	}

	if len(r.Batteries) > 0 {
		section("Battery")
		for _, bt := range r.Batteries {
			line(bt.Name, "%s, %.1f of %.1f Wh design (%.0f%% health), %d cycles",
				join(bt.Manufacturer, bt.Model), bt.FullWh, bt.DesignWh, bt.HealthPercent, bt.CycleCount)
		}
	}

	if len(r.USB) > 0 {
		section("USB")
		for _, u := range r.USB {
			line(u.Path, "%s", join(u.Vendor, u.Product))
		}
	}

	fmt.Fprintf(&b, "\n%d PCI devices, %d sensor chips.", len(r.PCI), len(r.Sensors))
	b.WriteString(" Full detail is in the JSON/YAML output.\n")
	if len(r.Warnings) > 0 {
		section("Not captured")
		for _, warn := range r.Warnings {
			fmt.Fprintf(&b, "  - %s\n", warn)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// vendorUnlessInProduct returns vendor, or "" when product already starts with
// it (case-insensitively). Many OEMs repeat their name in the DMI product name
// ("HP" + "HP EliteDesk 800 G5 Desktop Mini"), and prefixing it again would
// print the vendor twice.
func vendorUnlessInProduct(vendor, product string) string {
	v, p := strings.TrimSpace(vendor), strings.TrimSpace(product)
	if v != "" && len(p) >= len(v) && strings.EqualFold(p[:len(v)], v) {
		return ""
	}
	return vendor
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
