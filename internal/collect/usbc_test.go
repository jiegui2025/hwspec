package collect

import (
	"slices"
	"strings"
	"testing"

	"github.com/jiegui2025/hwspec/internal/report"
)

// typecFiles writes a Type-C port with its PD objects under the fake root.
func typecFiles(file func(string, string), port, roles string, pdos map[string]string) {
	dir := "/sys/class/typec/" + port + "/"
	file(dir+"power_role", roles)
	file(dir+"usb_power_delivery_revision", "3.0")
	file(dir+"usb_typec_revision", "1.3")
	for path, v := range pdos {
		file(dir+path, v)
	}
}

// #115's acceptance, synthetic: a dual-role port that sinks up to 20 V /
// 3.25 A can charge, up to 65 W, with every PDO type read in position
// order; a source-only port can't; a connected charger's offer is read.
func TestUSBCPorts(t *testing.T) {
	file, _ := fakeRoot(t)
	typecFiles(file, "port0", "source [sink]\n", map[string]string{
		"physical_location/panel": "left", "physical_location/horizontal_position": "center", "physical_location/vertical_position": "lower",
		"physical_location/dock": "no", "physical_location/lid": "yes",
		"usb_power_delivery/sink-capabilities/2:fixed_supply/voltage":                                       "20000mV",
		"usb_power_delivery/sink-capabilities/2:fixed_supply/operational_current":                           "3250mA",
		"usb_power_delivery/sink-capabilities/1:fixed_supply/voltage":                                       "5000mV",
		"usb_power_delivery/sink-capabilities/1:fixed_supply/operational_current":                           "3000mA",
		"usb_power_delivery/sink-capabilities/10:variable_supply/minimum_voltage":                           "5000mV",
		"usb_power_delivery/sink-capabilities/10:variable_supply/maximum_voltage":                           "12000mV",
		"usb_power_delivery/sink-capabilities/10:variable_supply/operational_current":                       "1000mA",
		"usb_power_delivery/sink-capabilities/3:battery/minimum_voltage":                                    "5000mV",
		"usb_power_delivery/sink-capabilities/3:battery/maximum_voltage":                                    "20000mV",
		"usb_power_delivery/sink-capabilities/3:battery/operational_power":                                  "45000mW",
		"usb_power_delivery/sink-capabilities/4:programmable_supply/minimum_voltage":                        "3300mV",
		"usb_power_delivery/sink-capabilities/4:programmable_supply/maximum_voltage":                        "11000mV",
		"usb_power_delivery/sink-capabilities/4:programmable_supply/maximum_current":                        "3000mA",
		"usb_power_delivery/sink-capabilities/5:mystery_supply/voltage":                                     "1mV",
		"usb_power_delivery/source-capabilities/1:fixed_supply/voltage":                                     "5000mV",
		"usb_power_delivery/source-capabilities/1:fixed_supply/maximum_current":                             "1500mA",
		"usb_power_delivery/source-capabilities/2:battery/minimum_voltage":                                  "5000mV",
		"usb_power_delivery/source-capabilities/2:battery/maximum_voltage":                                  "20000mV",
		"usb_power_delivery/source-capabilities/2:battery/maximum_power":                                    "10000mW",
		"usb_power_delivery/source-capabilities/3:spr_adjustable_voltage_supply/maximum_current_15V_to_20V": "2250mA",
	})
	file("/sys/class/typec/port0-partner/usb_power_delivery/source-capabilities/1:fixed_supply/voltage", "20000mV")
	file("/sys/class/typec/port0-partner/usb_power_delivery/source-capabilities/1:fixed_supply/maximum_current", "4500mA")
	file("/sys/class/typec/port0-cable/x", "")
	// A source-only port that lists sink PDOs anyway still can't charge.
	typecFiles(file, "port1", "[source]", map[string]string{
		"usb_power_delivery/sink-capabilities/1:fixed_supply/voltage":             "5000mV",
		"usb_power_delivery/sink-capabilities/1:fixed_supply/operational_current": "900mA",
	})
	typecFiles(file, "port2", "[source] sink", nil)
	col := &collector{r: &report.Report{}}
	col.usbCPorts()
	if w := col.r.Warnings; len(w) != 0 {
		t.Errorf("warnings %q", w)
	}
	if len(col.r.USBC) != 3 {
		t.Fatalf("ports %+v", col.r.USBC)
	}
	p := col.r.USBC[0]
	wantSink := []report.PDO{
		{Position: 1, Type: "fixed", MinVoltageMV: 5000, MaxVoltageMV: 5000, CurrentMA: 3000, PowerMW: 15000},
		{Position: 2, Type: "fixed", MinVoltageMV: 20000, MaxVoltageMV: 20000, CurrentMA: 3250, PowerMW: 65000},
		{Position: 3, Type: "battery", MinVoltageMV: 5000, MaxVoltageMV: 20000, PowerMW: 45000},
		{Position: 4, Type: "pps", MinVoltageMV: 3300, MaxVoltageMV: 11000, CurrentMA: 3000, PowerMW: 33000},
		{Position: 10, Type: "variable", MinVoltageMV: 5000, MaxVoltageMV: 12000, CurrentMA: 1000, PowerMW: 12000},
	}
	wantSource := []report.PDO{
		{Position: 1, Type: "fixed", MinVoltageMV: 5000, MaxVoltageMV: 5000, CurrentMA: 1500, PowerMW: 7500},
		{Position: 2, Type: "battery", MinVoltageMV: 5000, MaxVoltageMV: 20000, PowerMW: 10000},
		{Position: 3, Type: "avs", MinVoltageMV: 9000, MaxVoltageMV: 20000, CurrentMA: 2250, PowerMW: 45000},
	}
	if p.Name != "port0" || !p.CanCharge || p.MaxChargeW != 65 || p.PowerRole != "sink" || !slices.Equal(p.PowerRoles, []string{"source", "sink"}) ||
		p.PDRevision != "3.0" || p.TypeCRevision != "1.3" || !slices.Equal(p.SinkPDOs, wantSink) || !slices.Equal(p.SourcePDOs, wantSource) ||
		*p.Location != (report.PortLocation{Panel: "left", Horizontal: "center", Vertical: "lower", Lid: true}) ||
		!slices.Equal(p.PartnerSourcePDOs, []report.PDO{{Position: 1, Type: "fixed", MinVoltageMV: 20000, MaxVoltageMV: 20000, CurrentMA: 4500, PowerMW: 90000}}) {
		t.Errorf("port0 %+v", p)
	}
	q := col.r.USBC[1]
	if q.Name != "port1" || q.CanCharge || q.MaxChargeW != 0 || q.PowerRole != "source" || q.Location != nil || len(q.SinkPDOs) != 1 {
		t.Errorf("port1 %+v", q)
	}
	if r := col.r.USBC[2]; !r.CanCharge || r.PowerRole != "source" || !slices.Equal(r.PowerRoles, []string{"source", "sink"}) {
		t.Errorf("port2 %+v", r)
	}
}

// No Type-C class: an empty list. A port whose roles can't be read is
// left out with a warning; a PDO with a missing or unreadable value is
// left out with a warning; a sink without PDOs can charge, its limit
// unknown; PD revision 0.0 is no PD.
func TestUSBCPortsEdges(t *testing.T) {
	fakeRoot(t)
	col := &collector{r: &report.Report{}}
	col.usbCPorts()
	if col.r.USBC == nil || len(col.r.USBC) != 0 || len(col.r.Warnings) != 0 {
		t.Errorf("no class: %+v %q", col.r.USBC, col.r.Warnings)
	}

	file, _ := fakeRoot(t)
	file("/sys/class/typec/port0/power_role/x", "")
	typecFiles(file, "port1", "[sink]", map[string]string{
		"usb_power_delivery/sink-capabilities/1:fixed_supply/voltage":             "5000mV",
		"usb_power_delivery/sink-capabilities/2:fixed_supply/voltage":             "9000mV",
		"usb_power_delivery/sink-capabilities/2:fixed_supply/operational_current": "lots",
	})
	typecFiles(file, "port2", "[sink]", nil)
	file("/sys/class/typec/port2/usb_power_delivery_revision", "0.0")
	col = &collector{r: &report.Report{}}
	col.usbCPorts()
	w := strings.Join(col.r.Warnings, "\n")
	for _, want := range []string{"usb-c port0: power_role: read", "usb-c port1: 1:fixed_supply: operational_current is missing",
		`usb-c port1: 2:fixed_supply: unexpected value "lots"`} {
		if !strings.Contains(w, want) {
			t.Errorf("warnings %q lack %q", w, want)
		}
	}
	if len(col.r.USBC) != 2 || col.r.USBC[0].Name != "port1" || !col.r.USBC[0].CanCharge || col.r.USBC[0].SinkPDOs != nil || col.r.USBC[0].MaxChargeW != 0 ||
		col.r.USBC[1].PDRevision != "" || col.r.USBC[1].TypeCRevision != "1.3" {
		t.Errorf("ports %+v", col.r.USBC)
	}
}
