package collect

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jiegui2025/hwspec/internal/report"
)

// fixedPDO is a fixed supply at a position.
func fixedPDO(pos, mv, ma int) report.PDO {
	return report.PDO{Position: pos, Type: "fixed", MinVoltageMV: mv, MaxVoltageMV: mv, CurrentMA: ma, PowerMW: mv * ma / 1000}
}

// ucsiSupply writes a UCSI supply on USBC000:00 and links port0 to the
// same controller.
func ucsiSupply(file func(string, string), link func(string, string), online string) {
	file("/sys/devices/platform/USBC000:00/uevent", "")
	d := "/sys/class/power_supply/ucsi-source-psy-USBC000:001/"
	file(d+"type", "USB")
	file(d+"scope", "System")
	file(d+"usb_type", "C [PD] PD_PPS")
	file(d+"online", online)
	file(d+"voltage_now", "20000000")
	file(d+"current_now", "3250000")
	link(d+"device", "../../../devices/platform/USBC000:00")
	link("/sys/class/typec/port0/device", "../../../devices/platform/USBC000:00")
	// The partner is another entry under the class, not another port.
	link("/sys/class/typec/port0-partner/device", "../../../devices/platform/USBC000:00")
}

// #105's synthetic supplies: a UCSI charger rated by its largest fixed PDO
// (65 W, not a PPS range's top), with the contract and the PDO it uses;
// a laptop's AC adapter, online, unrated; a battery and a device-scoped
// supply left out.
func TestPowerSupplies(t *testing.T) {
	file, link := fakeRoot(t)
	ucsiSupply(file, link, "1")
	file("/sys/class/power_supply/ucsi-source-psy-USBC000:001/input_power_limit", "15000000")
	file("/sys/class/power_supply/AC/type", "Mains")
	file("/sys/class/power_supply/AC/online", "1")
	file("/sys/class/power_supply/BAT0/type", "Battery")
	file("/sys/class/power_supply/hidpp_battery_0/type", "USB")
	file("/sys/class/power_supply/hidpp_battery_0/scope", "Device")
	col := &collector{r: &report.Report{USBC: []report.USBCPort{{Name: "port0", PartnerSourcePDOs: []report.PDO{
		fixedPDO(1, 5000, 3000), fixedPDO(2, 9000, 3000), fixedPDO(3, 15000, 3000), fixedPDO(4, 20000, 3250),
		{Position: 5, Type: "pps", MinVoltageMV: 3300, MaxVoltageMV: 21000, CurrentMA: 5000, PowerMW: 105000}}}}}}
	col.power()
	p := col.r.Power
	if len(p.Supplies) != 2 || len(col.r.Warnings) != 0 || p.RatingUnknown != "" {
		t.Fatalf("supplies %+v, warnings %q, rating unknown %q", p.Supplies, col.r.Warnings, p.RatingUnknown)
	}
	ac, usb := p.Supplies[0], p.Supplies[1]
	if ac.Name != "AC" || ac.Type != "mains" || ac.Online == nil || !*ac.Online || ac.RatedMaxMW != 0 || ac.Port != "" {
		t.Errorf("AC %+v", ac)
	}
	if usb.Type != "usb" || usb.USBType != "PD" || usb.Port != "port0" || usb.RatedMaxMW != 65000 || usb.RatingSource != "usb-pd-source-capabilities" ||
		usb.ContractMV != 20000 || usb.ContractMA != 3250 || usb.SelectedPDO != 4 {
		t.Errorf("UCSI %+v", usb)
	}

	// Offline, its contract isn't read; unrated: the reason.
	file, link = fakeRoot(t)
	ucsiSupply(file, link, "0")
	file("/sys/class/power_supply/AC/type", "Mains")
	file("/sys/class/power_supply/AC/online", "1")
	col = &collector{r: &report.Report{USBC: []report.USBCPort{{Name: "port0"}}}}
	col.power()
	if s := col.r.Power.Supplies[1]; s.ContractMV != 0 || s.Online == nil || *s.Online || col.r.Power.RatingUnknown != "the AC supply (power_supply Mains) reports no rating" {
		t.Errorf("offline %+v, %q", s, col.r.Power.RatingUnknown)
	}

	// #274 round 1, I1: an offline supply isn't rated from the partner's
	// PDOs (a phone on a source-only port offers them too), nor from its
	// input_power_limit; with nothing online and no Mains, the barrel-jack
	// reason stands.
	file, link = fakeRoot(t)
	ucsiSupply(file, link, "0")
	file("/sys/class/power_supply/ucsi-source-psy-USBC000:001/input_power_limit", "15000000")
	col = &collector{r: &report.Report{USBC: []report.USBCPort{{Name: "port0", PartnerSourcePDOs: []report.PDO{fixedPDO(1, 5000, 3000)}}}}}
	col.power()
	if s := col.r.Power.Supplies[0]; s.RatedMaxMW != 0 || s.Port != "port0" || col.r.Power.RatingUnknown != noRating {
		t.Errorf("offline with a phone: %+v, %q", s, col.r.Power.RatingUnknown)
	}

	// I3: a programmable contract (online 2, or usb_type PD_PPS) names no
	// fixed PDO, though one has its voltage; its rating stands.
	for _, c := range []struct{ online, usbType string }{{"2", "C [PD] PD_PPS"}, {"3", "C [PD] PD_PPS"}, {"1", "C PD [PD_PPS]"}, {"1", "C PD [PD_SPR_AVS]"}} {
		file, link = fakeRoot(t)
		ucsiSupply(file, link, c.online)
		file("/sys/class/power_supply/ucsi-source-psy-USBC000:001/usb_type", c.usbType)
		file("/sys/class/power_supply/ucsi-source-psy-USBC000:001/voltage_now", "9000000")
		col = &collector{r: &report.Report{USBC: []report.USBCPort{{Name: "port0", PartnerSourcePDOs: []report.PDO{fixedPDO(1, 5000, 3000), fixedPDO(2, 9000, 3000),
			{Position: 3, Type: "pps", MinVoltageMV: 3300, MaxVoltageMV: 11000, CurrentMA: 3000, PowerMW: 33000}}}}}}
		col.power()
		if s := col.r.Power.Supplies[0]; s.SelectedPDO != 0 || s.RatedMaxMW != 27000 || s.ContractMV != 9000 {
			t.Errorf("programmable %v: %+v", c, s)
		}
	}

	// An online supply with no rating anywhere: that supply reports none;
	// with an AC supply too, the AC one is named.
	file, link = fakeRoot(t)
	ucsiSupply(file, link, "1")
	col = &collector{r: &report.Report{USBC: []report.USBCPort{{Name: "port0"}}}}
	col.power()
	if col.r.Power.RatingUnknown != "the supply powering the machine reports no rating" {
		t.Errorf("online, unrated: %q", col.r.Power.RatingUnknown)
	}
	file("/sys/class/power_supply/AC/type", "Mains")
	col = &collector{r: &report.Report{USBC: []report.USBCPort{{Name: "port0"}}}}
	col.power()
	if col.r.Power.RatingUnknown != "the AC supply (power_supply Mains) reports no rating" {
		t.Errorf("AC and online USB, unrated: %q", col.r.Power.RatingUnknown)
	}
}

// The voltage_max × current_max trap: 5 V 3 A and 20 V 2.25 A are 45 W,
// not 60 W. A contract voltage two fixed PDOs share names neither.
func TestPowerRatingFromPDOs(t *testing.T) {
	for _, c := range []struct {
		name     string
		pdos     []report.PDO
		mv       int
		rated    int
		selected int
	}{
		{"5 V 3 A and 20 V 2.25 A", []report.PDO{fixedPDO(1, 5000, 3000), fixedPDO(2, 20000, 2250)}, 20000, 45000, 2},
		{"two PDOs at 20 V", []report.PDO{fixedPDO(1, 20000, 2250), fixedPDO(2, 20000, 3000)}, 20000, 60000, 0},
		{"a PPS contract", []report.PDO{fixedPDO(1, 5000, 3000), {Position: 2, Type: "pps", MaxVoltageMV: 11000, CurrentMA: 3000, PowerMW: 33000}}, 9200, 15000, 0},
		{"no contract", []report.PDO{fixedPDO(1, 5000, 3000)}, 0, 15000, 0},
	} {
		s := report.PowerSupply{ContractMV: c.mv}
		(&collector{}).ratingFromPDOs(&s, c.pdos, false)
		if s.RatedMaxMW != c.rated || s.SelectedPDO != c.selected || s.RatingSource != "usb-pd-source-capabilities" {
			t.Errorf("%s: %+v", c.name, s)
		}
	}
}

// A TCPM supply without the partner's PD object is rated by its driver's
// input_power_limit; a supply whose controller has two ports is linked to
// neither; identity strings are kept; bad values are warnings.
func TestPowerSupplyEdges(t *testing.T) {
	file, link := fakeRoot(t)
	d := "/sys/class/power_supply/tcpm-source-psy-i2c-fusb302/"
	file(d+"type", "USB")
	file(d+"online", "1")
	file(d+"input_power_limit", "10000000")
	file(d+"voltage_now", "15000000")
	file(d+"current_now", "lots")
	file(d+"manufacturer", "ACME")
	file(d+"model_name", "Brick 60")
	file("/sys/devices/i2c/fusb302/uevent", "")
	link(d+"device", "../../../devices/i2c/fusb302")
	link("/sys/class/typec/port0/device", "../../../devices/i2c/fusb302")
	link("/sys/class/typec/port1/device", "../../../devices/i2c/fusb302")
	file("/sys/class/power_supply/odd/type", "Mains")
	file("/sys/class/power_supply/odd/online/x", "")
	// port1 couldn't be read, so it isn't in usb_c_ports; it still counts.
	col := &collector{r: &report.Report{USBC: []report.USBCPort{{Name: "port0"}}}}
	col.power()
	s := col.r.Power.Supplies[1]
	if s.Port != "" || s.RatedMaxMW != 10000 || s.RatingSource != "input_power_limit" || s.ContractMV != 15000 || s.ContractMA != 0 ||
		s.Identity == nil || *s.Identity != (report.Identity{Vendor: "ACME", Model: "Brick 60"}) {
		t.Errorf("TCPM %+v", s)
	}
	if o := col.r.Power.Supplies[0]; o.Online != nil || o.Identity != nil {
		t.Errorf("odd %+v", o)
	}
	w := strings.Join(col.r.Warnings, "\n")
	for _, want := range []string{"power tcpm-source-psy-i2c-fusb302: current_now:", "power supply odd: online:"} {
		if !strings.Contains(w, want) {
			t.Errorf("warnings %q lack %q", w, want)
		}
	}

	// Neither the supply nor the port has a device link: not the same.
	file, _ = fakeRoot(t)
	file("/sys/class/power_supply/usb/type", "USB")
	col = &collector{r: &report.Report{USBC: []report.USBCPort{{Name: "port0"}}}}
	col.power()
	if s := col.r.Power.Supplies[0]; s.Port != "" {
		t.Errorf("no device links: %+v", s)
	}

	for in, want := range map[string]string{"C [PD] PD_PPS": "PD", "[C] PD": "C", "PD": "PD", "C PD": "", "": "", "[broken": ""} {
		if got := bracketed(in); got != want {
			t.Errorf("bracketed(%q) = %q", in, got)
		}
	}
}

// RAPL zones by name, whatever their index; limits of 0, unknown zones
// and other control types left out; an unreadable limit or constraint
// name is a warning naming the file. AMD's RAPL zones have no constraint
// files: no CPU limits.
func TestRAPLLimits(t *testing.T) {
	file, _ := fakeRoot(t)
	z := func(zone, name, enabled string, constraints ...[3]string) {
		d := "/sys/class/powercap/" + zone + "/"
		file(d+"name", name)
		if enabled != "" {
			file(d+"enabled", enabled)
		}
		for i, c := range constraints {
			n := string(rune('0' + i))
			if c[0] != "" {
				file(d+"constraint_"+n+"_name", c[0])
			}
			if c[1] == "/x" {
				file(d+"constraint_"+n+"_power_limit_uw/x", "")
			} else {
				file(d+"constraint_"+n+"_power_limit_uw", c[1])
			}
			if c[2] != "" {
				file(d+"constraint_"+n+"_time_window_us", c[2])
			}
		}
	}
	file("/sys/class/powercap/intel-rapl/enabled", "1")
	z("intel-rapl:1", "package-0-die-0", "1", [3]string{"long_term", "28000000", "28000000"}, [3]string{"short_term", "64000000", ""}, [3]string{"peak_power", "/x", ""})
	z("intel-rapl:0", "psys", "0", [3]string{"long_term", "60999600", "bad"})
	z("intel-rapl:0:0", "core", "0", [3]string{"long_term", "400", "976"})
	z("intel-rapl-mmio:0", "package-0", "", [3]string{"long_term", "15000000", ""})
	z("intel-rapl:2", "mystery", "1", [3]string{"long_term", "1000000", ""})
	z("dtpm:0", "package-0", "1", [3]string{"long_term", "1000000", ""})
	file("/sys/class/powercap/intel-rapl:3/name", "package-1")
	file("/sys/class/powercap/intel-rapl:3/constraint_0_name/x", "")
	col := &collector{r: &report.Report{}}
	col.power()
	on, off := true, false
	want := []report.PowerLimit{
		{Domain: "cpu-package", Zone: "package-0", Name: "long_term", LimitMW: 15000, Source: "intel-rapl-mmio"},
		{Domain: "platform", Zone: "psys", Name: "long_term", LimitMW: 61000, Enabled: &off, Source: "intel-rapl"},
		{Domain: "cpu-package", Zone: "package-0-die-0", Name: "long_term", LimitMW: 28000, TimeWindowUS: 28000000, Enabled: &on, Source: "intel-rapl"},
		{Domain: "cpu-package", Zone: "package-0-die-0", Name: "short_term", LimitMW: 64000, Source: "intel-rapl"},
	}
	got := col.r.Power.Limits
	if len(got) != len(want) {
		t.Fatalf("limits %+v", got)
	}
	for i := range want {
		if !sameLimit(got[i], want[i]) {
			t.Errorf("limit %d: %+v, want %+v", i, got[i], want[i])
		}
	}
	w := strings.Join(col.r.Warnings, "\n")
	for _, s := range []string{"power intel-rapl:1: constraint_2_power_limit_uw: read", "power intel-rapl:3: constraint_0_name: read"} {
		if !strings.Contains(w, s) {
			t.Errorf("warnings %q lack %q", w, s)
		}
	}

	// AMD: a package zone without constraints, and amdgpu's cap.
	file, _ = fakeRoot(t)
	z("intel-rapl:0", "package-0", "0")
	h := "/sys/bus/pci/devices/0000:03:00.0/hwmon/hwmon5/"
	file(h+"name", "amdgpu")
	file(h+"power1_cap", "120000000")
	file(h+"power1_cap_max", "150000000")
	col = &collector{r: &report.Report{GPUs: []report.GPU{{PCIAddress: "0000:03:00.0"}}}}
	col.power()
	got = col.r.Power.Limits
	if len(got) != 2 || got[0] != (report.PowerLimit{Domain: "gpu", Zone: "amdgpu", Device: "0000:03:00.0", Name: "cap", LimitMW: 120000, Source: "amdgpu"}) ||
		got[1].Name != "cap_max" || got[1].LimitMW != 150000 {
		t.Errorf("AMD limits %+v", got)
	}
}

// Intel discrete GPUs: i915's PL1 of 0 (disabled) left out, its rated
// maximum kept; xe's second channel (the package) named as such.
func TestGPUPowerLimits(t *testing.T) {
	file, _ := fakeRoot(t)
	h := "/sys/bus/pci/devices/0000:03:00.0/hwmon/hwmon6/"
	file(h+"name", "i915")
	file(h+"power1_max", "0")
	file(h+"power1_rated_max", "190000000")
	file(h+"power1_crit", "400000000")
	x := "/sys/bus/pci/devices/0000:04:00.0/hwmon/hwmon7/"
	file(x+"name", "xe")
	file(x+"power2_max", "35000000")
	col := &collector{r: &report.Report{GPUs: []report.GPU{{PCIAddress: "0000:03:00.0"}, {PCIAddress: "0000:04:00.0"}}}}
	col.power()
	var names []string
	for _, l := range col.r.Power.Limits {
		names = append(names, fmt.Sprintf("%s %s %s %s %d", l.Domain, l.Source, l.Device, l.Name, l.LimitMW))
	}
	if got := strings.Join(names, "; "); got != "gpu i915 0000:03:00.0 rated_max 190000; gpu i915 0000:03:00.0 crit 400000; gpu xe 0000:04:00.0 power2_max 35000" {
		t.Errorf("limits %q", got)
	}
}

// A capture from before the power section (no field) and one with an
// empty one are told apart in JSON.
func TestPowerAbsentOrEmpty(t *testing.T) {
	old, _ := json.Marshal(report.Report{})
	empty, _ := json.Marshal(report.Report{Power: &report.Power{Supplies: []report.PowerSupply{}, Limits: []report.PowerLimit{}}})
	if strings.Contains(string(old), `"power"`) || !strings.Contains(string(empty), `"power":{"supplies":[],"limits":[]}`) {
		t.Errorf("old %s\nempty %s", old, empty)
	}
}
