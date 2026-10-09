package collect

import (
	"errors"
	"io/fs"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jiegui2025/hwspec/internal/report"
)

// typecPort matches a Type-C port's name in /sys/class/typec; its
// partner, cable and plugs ("port0-partner") are other entries.
var typecPort = regexp.MustCompile(`^port[0-9]+$`)

// usbCPorts lists the Type-C ports the kernel's Type-C class manages, and
// what they can do with power (#115): the roles in power_role (the
// current one bracketed, "[source] sink"), the PD objects in their
// usb_power_delivery, and a connected partner's offer
// (Documentation/ABI/testing/sysfs-class-typec and
// sysfs-class-usb_power_delivery). All of it is world-readable.
func (c *collector) usbCPorts() {
	c.r.USBC = []report.USBCPort{}
	for _, name := range list("/sys/class/typec") {
		if !typecPort.MatchString(name) {
			continue
		}
		dir := "/sys/class/typec/" + name + "/"
		roles, err := readStrErr(dir + "power_role")
		if err != nil {
			c.warn("usb-c %s: power_role: %v", name, err)
			continue
		}
		port := report.USBCPort{Name: name, PowerRoles: []string{},
			PDRevision: readStr(dir + "usb_power_delivery_revision"), TypeCRevision: readStr(dir + "usb_typec_revision")}
		if port.PDRevision == "0.0" {
			port.PDRevision = "" // the kernel's "no PD"
		}
		for role := range strings.FieldsSeq(roles) {
			r := strings.Trim(role, "[]")
			if r != role {
				port.PowerRole = r
			}
			port.PowerRoles = append(port.PowerRoles, r)
		}
		port.CanCharge = slices.Contains(port.PowerRoles, report.PowerSink)
		port.Location = portLocation(dir + "physical_location/")
		pd := dir + "usb_power_delivery/"
		port.SourcePDOs = c.pdos(name, pd+"source-capabilities", false)
		port.SinkPDOs = c.pdos(name, pd+"sink-capabilities", true)
		port.PartnerSourcePDOs = c.pdos(name+" partner", "/sys/class/typec/"+name+"-partner/usb_power_delivery/source-capabilities", false)
		if port.CanCharge {
			most := 0
			for _, p := range port.SinkPDOs {
				most = max(most, p.PowerMW)
			}
			port.MaxChargeW = math.Round(float64(most)/100) / 10
		}
		c.r.USBC = append(c.r.USBC, port)
	}
}

// portLocation reads a port's physical_location, or nil without one.
func portLocation(dir string) *report.PortLocation {
	l := report.PortLocation{Panel: readStr(dir + "panel"), Horizontal: readStr(dir + "horizontal_position"),
		Vertical: readStr(dir + "vertical_position"), Dock: readStr(dir+"dock") == "yes", Lid: readStr(dir+"lid") == "yes"}
	if l == (report.PortLocation{}) {
		return nil
	}
	return &l
}

// pdoTypes maps the kernel's PDO directory names to report.PDO types.
var pdoTypes = map[string]string{"fixed_supply": report.PDOFixed, "variable_supply": report.PDOVariable,
	"battery": report.PDOBattery, "programmable_supply": report.PDOPPS, "spr_adjustable_voltage_supply": report.PDOAVS}

// pdos reads one capabilities directory's PDOs ("1:fixed_supply"...), in
// order; a sink's fixed, variable and battery PDOs give the operational
// current or power, a source's the maximum. No directory: none. A PDO
// type this build doesn't know is left out.
func (c *collector) pdos(who, dir string, sink bool) []report.PDO {
	entries := list(dir)
	slices.SortFunc(entries, func(a, b string) int { return position(a) - position(b) })
	var out []report.PDO
	for _, e := range entries {
		_, kind, ok := strings.Cut(e, ":")
		typ := pdoTypes[kind]
		if !ok || typ == "" {
			continue
		}
		d := dir + "/" + e + "/"
		var p report.PDO
		var errs []error
		val := func(file string) int {
			v, err := pdoValue(d + file)
			if err != nil {
				errs = append(errs, err)
			}
			return v
		}
		current, power := "maximum_current", "maximum_power"
		if sink {
			current, power = "operational_current", "operational_power"
		}
		p.Type, p.Position = typ, position(e)
		switch typ {
		case report.PDOFixed:
			p.MaxVoltageMV = val("voltage")
			p.MinVoltageMV, p.CurrentMA = p.MaxVoltageMV, val(current)
		case report.PDOVariable:
			p.MinVoltageMV, p.MaxVoltageMV, p.CurrentMA = val("minimum_voltage"), val("maximum_voltage"), val(current)
		case report.PDOBattery:
			p.MinVoltageMV, p.MaxVoltageMV, p.PowerMW = val("minimum_voltage"), val("maximum_voltage"), val(power)
		case report.PDOPPS:
			p.MinVoltageMV, p.MaxVoltageMV, p.CurrentMA = val("minimum_voltage"), val("maximum_voltage"), val("maximum_current")
		case report.PDOAVS:
			// SPR AVS: 9-20 V, its current given for 15-20 V.
			p.MinVoltageMV, p.MaxVoltageMV, p.CurrentMA = 9000, 20000, val("maximum_current_15V_to_20V")
		}
		if err := errors.Join(errs...); err != nil {
			c.warn("usb-c %s: %s: %v", who, e, err)
			continue
		}
		if p.PowerMW == 0 {
			p.PowerMW = p.MaxVoltageMV * p.CurrentMA / 1000
		}
		out = append(out, p)
	}
	return out
}

// position is a PDO directory's number ("2:variable_supply" is 2).
func position(entry string) int {
	n, _ := strconv.Atoi(strings.SplitN(entry, ":", 2)[0])
	return n
}

// pdoValue reads a PD attribute: a number with its unit, "5000mV".
func pdoValue(path string) (int, error) {
	s, err := readStrErr(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			err = errors.New(path[strings.LastIndex(path, "/")+1:] + " is missing")
		}
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimRight(s, "mAVWh"))
	if err != nil {
		return 0, errors.New("unexpected value " + strconv.Quote(s))
	}
	return n, nil
}
