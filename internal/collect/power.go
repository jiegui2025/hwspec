package collect

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jiegui2025/hwspec/internal/report"
)

// noRating is why a capture has no supply rating: nothing in power_supply
// gives one, as on a desktop whose barrel-jack adapter the kernel doesn't
// see. It isn't a warning: nothing failed to read.
const noRating = "no supply in power_supply reports a rating (a barrel-jack adapter isn't exposed to the kernel)"

// power records what can supply the machine and what its parts may draw
// (#105): external supplies (power_supply Mains and USB), the RAPL limits
// (powercap) and the GPUs' hwmon limits. Facts only: whether a supply is
// enough is the advisor's. It runs after usbCPorts, whose ports carry a
// charger's PD objects, and after gpus.
func (c *collector) power() {
	p := &report.Power{Supplies: []report.PowerSupply{}, Limits: []report.PowerLimit{}}
	c.supplies(p)
	c.raplLimits(p)
	c.gpuLimits(p)
	p.RatingUnknown = noRating
	for _, s := range p.Supplies {
		switch {
		case s.RatedMaxMW > 0:
			p.RatingUnknown = ""
			c.r.Power = p
			return
		case s.Type == report.SupplyMains:
			p.RatingUnknown = "the AC supply (power_supply Mains) reports no rating"
		case s.Online != nil && *s.Online && p.RatingUnknown == noRating:
			p.RatingUnknown = "the supply powering the machine reports no rating"
		}
	}
	c.r.Power = p
}

// supplies lists the Mains and USB supplies, with the contract in force
// and a rating where one is known; a supply scoped to a device (a mouse's
// battery charger) isn't the machine's.
func (c *collector) supplies(p *report.Power) {
	for _, name := range list("/sys/class/power_supply") {
		dir := "/sys/class/power_supply/" + name + "/"
		typ := map[string]string{"Mains": report.SupplyMains, "USB": report.SupplyUSB}[readStr(dir+"type")]
		if typ == "" || readStr(dir+"scope") == "Device" {
			continue
		}
		s := report.PowerSupply{Name: name, Type: typ, USBType: bracketed(readStr(dir + "usb_type"))}
		// online is 0 offline, 1 a fixed supply, 2 a programmable one (a
		// PPS or AVS contract), 3 a programmable one at fixed output.
		programmable := false
		if v, err := readStrErr(dir + "online"); err == nil {
			online := v != "0"
			s.Online = &online
			programmable = v == "2" || v == "3"
		} else if !errors.Is(err, fs.ErrNotExist) {
			c.warn("power supply %s: online: %v", name, err)
		}
		programmable = programmable || s.USBType == "PD_PPS" || s.USBType == "PD_SPR_AVS"
		if s.Online != nil && *s.Online {
			s.ContractMV = c.micro(name, dir+"voltage_now")
			s.ContractMA = c.micro(name, dir+"current_now")
		}
		if id := (report.Identity{Vendor: readStr(dir + "manufacturer"), Model: readStr(dir + "model_name"), Serial: readStr(dir + "serial_number")}); id != (report.Identity{}) {
			s.Identity = &id
		}
		port := c.supplyPort(dir)
		if port != "" {
			s.Port = port
		}
		// Only a supply powering the machine is rated: UCSI registers a
		// partner's source PDOs whatever the power direction, so a phone on
		// a source-only port offers them too.
		if s.Online != nil && *s.Online {
			for i := range c.r.USBC {
				if c.r.USBC[i].Name == port {
					c.ratingFromPDOs(&s, c.r.USBC[i].PartnerSourcePDOs, programmable)
				}
			}
			if s.RatedMaxMW == 0 {
				if mw := c.micro(name, dir+"input_power_limit"); mw > 0 {
					s.RatedMaxMW, s.RatingSource = mw, report.RatingFromInputLimit
				}
			}
		}
		p.Supplies = append(p.Supplies, s)
	}
}

// bracketed is the selected value in a sysfs choice list ("C [PD] PD_PPS"
// gives PD), or the value when there's no choice.
func bracketed(s string) string {
	if i := strings.Index(s, "["); i >= 0 {
		if j := strings.Index(s[i:], "]"); j > 0 {
			return s[i+1 : i+j]
		}
		return ""
	}
	if strings.ContainsRune(s, ' ') {
		return ""
	}
	return s
}

// micro reads a value in micro-units (µV, µA, µW) as milli-units; absent
// is 0, an unreadable or malformed value a warning and 0.
func (c *collector) micro(who, path string) int {
	s, err := readStrErr(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0
	}
	if err == nil {
		var n int64
		if n, err = strconv.ParseInt(s, 10, 64); err == nil {
			return int((n + 500) / 1000)
		}
	}
	c.warn("power %s: %s: %v", who, filepath.Base(path), err)
	return 0
}

// supplyPort names the USB-C port a UCSI or TCPM supply belongs to: the
// one Type-C port on the same controller device, counted in sysfs (a port
// left out of usb_c_ports still counts). With several ports on it, which
// is the supply's isn't told by sysfs, so none is named.
func (c *collector) supplyPort(dir string) string {
	dev := realPath(dir + "device")
	if dev == "" {
		return ""
	}
	found := ""
	for _, name := range list("/sys/class/typec") {
		if !typecPort.MatchString(name) || realPath("/sys/class/typec/"+name+"/device") != dev {
			continue
		}
		if found != "" {
			return ""
		}
		found = name
	}
	return found
}

// ratingFromPDOs rates a USB-C supply by the charger's largest fixed PDO
// (exact: a PPS or AVS range's top isn't promised at every voltage), and
// names the PDO a fixed contract uses when exactly one fixed PDO has its
// voltage; a programmable contract's voltage is the APDO's output, so it
// names none.
func (c *collector) ratingFromPDOs(s *report.PowerSupply, pdos []report.PDO, programmable bool) {
	var match []int
	for _, o := range pdos {
		if o.Type != report.PDOFixed {
			continue
		}
		if o.PowerMW > s.RatedMaxMW {
			s.RatedMaxMW, s.RatingSource = o.PowerMW, report.RatingFromPDOs
		}
		if o.MaxVoltageMV == s.ContractMV {
			match = append(match, o.Position)
		}
	}
	if len(match) == 1 && !programmable {
		s.SelectedPDO = match[0]
	}
}

// raplDomains maps RAPL zone names to domains; package zones are
// "package-N" or "package-N-die-M".
var raplDomains = map[string]string{"psys": report.DomainPlatform, "core": report.DomainCPUCore,
	"uncore": report.DomainCPUUncore, "dram": report.DomainDRAM}

// raplLimits reads the Intel RAPL zones' constraints (intel-rapl, MSR or
// TPMI, and intel-rapl-mmio), each zone told by its name, not its index
// (Documentation/ABI/testing/sysfs-class-powercap). A limit of 0 isn't
// one. AMD's RAPL has no limit registers, so it gives none.
func (c *collector) raplLimits(p *report.Power) {
	for _, zone := range list("/sys/class/powercap") {
		source, _, ok := strings.Cut(zone, ":")
		if !ok || source != "intel-rapl" && source != "intel-rapl-mmio" {
			continue
		}
		dir := "/sys/class/powercap/" + zone + "/"
		name := readStr(dir + "name")
		domain := raplDomains[name]
		if strings.HasPrefix(name, "package-") {
			domain = report.DomainCPUPackage
		}
		if domain == "" {
			continue
		}
		var enabled *bool
		if v, err := readStrErr(dir + "enabled"); err == nil {
			e := v == "1"
			enabled = &e
		}
		for i := 0; ; i++ {
			at := fmt.Sprintf("%sconstraint_%d_", dir, i)
			cname, err := readStrErr(at + "name")
			if errors.Is(err, fs.ErrNotExist) {
				break
			}
			if err != nil {
				c.warn("power %s: constraint_%d_name: %v", zone, i, err)
				continue
			}
			mw := c.micro(zone, at+"power_limit_uw")
			if mw <= 0 {
				continue
			}
			l := report.PowerLimit{Domain: domain, Zone: name, Name: cname, LimitMW: mw, Source: source}
			if cname == "long_term" {
				l.Enabled = enabled // the zone's enable bit is PL1's
			}
			if us, err := strconv.ParseInt(readStr(at+"time_window_us"), 10, 64); err == nil {
				l.TimeWindowUS = us
			}
			p.Limits = append(p.Limits, l)
		}
	}
}

// gpuLimitFiles are the GPU drivers' hwmon power limits: amdgpu's cap
// and its maximum (amdgpu_pm.c), i915's and xe's PL1 (max, 0 when
// disabled), default (rated_max) and critical limits; xe's second channel
// is the package.
var gpuLimitFiles = []string{"cap", "cap_max", "max", "rated_max", "crit"}

// gpuLimits reads each GPU's hwmon power limits, linked to it by PCI
// address.
func (c *collector) gpuLimits(p *report.Power) {
	for _, g := range c.r.GPUs {
		dir := "/sys/bus/pci/devices/" + g.PCIAddress + "/hwmon/"
		for _, h := range list(dir) {
			hdir := dir + h + "/"
			driver := readStr(hdir + "name")
			for ch := 1; ch <= 2; ch++ {
				for _, f := range gpuLimitFiles {
					mw := c.micro(driver, fmt.Sprintf("%spower%d_%s", hdir, ch, f))
					if mw <= 0 {
						continue
					}
					name := f
					if ch > 1 {
						name = fmt.Sprintf("power%d_%s", ch, f)
					}
					p.Limits = append(p.Limits, report.PowerLimit{Domain: report.DomainGPU, Zone: driver, Device: g.PCIAddress,
						Name: name, LimitMW: mw, Source: driver})
				}
			}
		}
	}
}
