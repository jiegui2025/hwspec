package collect

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/jiegui2025/hwspec/internal/report"
)

// network lists physical interfaces. Virtual ones (bridges, veth, VPN
// tunnels, containers) have no backing device and are skipped.
func (c *collector) network() {
	c.r.Network = []report.NIC{}
	for _, name := range list("/sys/class/net") {
		d := "/sys/class/net/" + name + "/"
		if !exists(d + "device") {
			continue
		}
		nic := report.NIC{
			Name:   name,
			Type:   "ethernet",
			MAC:    readStr(d + "address"),
			State:  readStr(d + "operstate"),
			Driver: driverAt(d + "device"),
		}
		// The driver reports the adapter's firmware (no root needed).
		if fw, err := ethtoolDrvinfo(name); err == nil {
			nic.Firmware = firmwareVersion(fw, "ethtool")
		}
		nic.Health = nicHealth(d + "statistics/")
		if exists(d+"wireless") || exists(d+"phy80211") {
			nic.Type = "wireless"
		} else if t := readStr(d + "type"); t != "1" {
			nic.Type = "other (" + t + ")"
		}
		nic.Bus, nic.BusAddress = busOf(d + "device")
		// speed and duplex are only meaningful while the link is up;
		// reading them on a down link fails or returns -1.
		if v, ok := readInt32(d + "speed"); ok && v > 0 {
			nic.SpeedMbps = v
			nic.Duplex = readStr(d + "duplex")
		}
		if v, ok := readInt32(d + "mtu"); ok {
			nic.MTU = v
		}
		c.r.Network = append(c.r.Network, nic)
	}
}

// nicHealth reports the interface's error and drop counters since boot. A
// link losing more than 1 in 1000 packets to errors is worth a look
// (cable, port, driver, interference).
func nicHealth(stats string) *report.Health {
	h := &report.Health{Status: report.StatusOK, Source: "statistics"}
	var total, bad float64
	for _, m := range []struct{ name, file string }{
		{report.MetricRxPackets, "rx_packets"}, {report.MetricTxPackets, "tx_packets"},
		{report.MetricRxErrors, "rx_errors"}, {report.MetricTxErrors, "tx_errors"},
		{report.MetricRxDropped, "rx_dropped"}, {report.MetricTxDropped, "tx_dropped"},
	} {
		v, err := strconv.ParseUint(readStr(stats+m.file), 10, 64)
		metric(h, m.name, float64(v), err == nil)
		switch {
		case err != nil:
		case strings.HasSuffix(m.file, "_packets"):
			total += float64(v)
		case strings.HasSuffix(m.file, "_errors"):
			bad += float64(v)
		}
	}
	if h.Metrics == nil {
		return nil
	}
	if total > 0 && bad/total > 0.001 {
		h.Status = report.StatusWarning
		h.Reasons = append(h.Reasons, fmt.Sprintf("%.2f%% of packets had errors since boot", bad/total*100))
	}
	return h
}

var asoundCard = regexp.MustCompile(`^\s*(\d+)\s+\[(.*?)\s*\]:\s*(.*?)\s+-\s+(.*)$`)

func (c *collector) audio() {
	c.r.Audio = []report.SoundCard{}
	f, err := os.Open(p("/proc/asound/cards"))
	if err != nil {
		return // no ALSA (headless server, container)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		m := asoundCard.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		idx, _ := strconv.Atoi(m[1])
		card := report.SoundCard{Index: idx, ID: m[2], Name: m[4]}
		card.Bus, card.BusAddress = busOf("/sys/class/sound/card" + m[1] + "/device")
		card.Driver = driverAt("/sys/class/sound/card" + m[1] + "/device")
		card.Codecs = codecs("/proc/asound/card" + m[1])
		c.r.Audio = append(c.r.Audio, card)
	}
	if err := sc.Err(); err != nil {
		c.warn("audio: reading /proc/asound/cards: %v", err)
	}
}

// codecs reads the HD Audio codec files the kernel writes for each card
// (/proc/asound/cardN/codec#M); the kernel already names the chip.
func codecs(cardDir string) []report.AudioCodec {
	var out []report.AudioCodec
	for _, f := range list(cardDir) {
		if !strings.HasPrefix(f, "codec#") {
			continue
		}
		var codec report.AudioCodec
		id := &report.Identity{}
		for _, line := range strings.Split(readStr(cardDir+"/"+f), "\n") {
			k, v, ok := strings.Cut(line, ":")
			if !ok || strings.HasPrefix(line, " ") {
				continue // indented lines describe widgets and pins
			}
			v = strings.TrimSpace(v)
			switch k {
			case "Codec":
				id.Model = v
			case "Vendor Id":
				codec.VendorID = hex4(v)
			case "Subsystem Id":
				codec.SubsystemID = hex4(v)
			case "Revision Id":
				id.Revision = hex4(v)
			}
		}
		if !id.Empty() {
			codec.Identity = id
		}
		if codec.Identity != nil || codec.VendorID != "" {
			out = append(out, codec)
		}
	}
	return out
}

func (c *collector) batteries() {
	c.r.Batteries = []report.Battery{}
	for _, n := range list("/sys/class/power_supply") {
		d := "/sys/class/power_supply/" + n + "/"
		// scope=Device marks peripheral batteries (mice, headsets).
		if readStr(d+"type") != "Battery" || readStr(d+"scope") == "Device" {
			continue
		}
		b := report.Battery{
			Name:       n,
			Technology: readStr(d + "technology"),
			Status:     readStr(d + "status"),
		}
		id := &report.Identity{
			Vendor: readStr(d + "manufacturer"),
			Model:  readStr(d + "model_name"),
			Serial: strings.TrimSpace(readStr(d + "serial_number")),
		}
		if y, ok := readInt32(d + "manufacture_year"); ok && y > 1990 {
			mo, _ := readInt32(d + "manufacture_month")
			day, _ := readInt32(d + "manufacture_day")
			id.ManufactureDate, id.ManufactureDateSource = isoDate(y, mo, day), "battery"
		}
		if !id.Empty() {
			b.Identity = id
		}
		if v, ok := readInt32(d + "capacity"); ok {
			b.CapacityPercent = v
		}
		b.Health = batteryHealth(d)
		c.r.Batteries = append(c.r.Batteries, b)
	}
}

// batteryHealth compares full-charge capacity with the design capacity.
// Below 80% a battery is conventionally worn out, so life used and
// remaining are both measured on that 100% → 80% range and add up to 100,
// like an SSD's; the raw capacity is the capacity_percent metric. The
// estimate of cycles left until 80% uses the wear per cycle measured so far.
func batteryHealth(d string) *report.Health {
	h := &report.Health{Status: report.StatusUnknown, Source: "power_supply"}
	// Energy in µWh, or charge in µAh × design voltage in µV.
	design, full := float64(readUint(d+"energy_full_design")), float64(readUint(d+"energy_full"))
	if design == 0 {
		volts := float64(readUint(d+"voltage_min_design")) / 1e6
		design = float64(readUint(d+"charge_full_design")) * volts
		full = float64(readUint(d+"charge_full")) * volts
	}
	metric(h, report.MetricDesignWh, round(design/1e6, 2), design > 0)
	metric(h, report.MetricFullWh, round(full/1e6, 2), full > 0)
	cycles, haveCycles := readInt32(d + "cycle_count")
	metric(h, report.MetricCycleCount, float64(cycles), haveCycles && cycles > 0)

	// Health needs both figures; a missing one must not read as 0%.
	if design > 0 && full > 0 {
		// A new battery can hold slightly more than its design capacity.
		capacity := full / design * 100
		metric(h, report.MetricCapacityPercent, round(capacity, 1), true)
		healthPct := math.Min(100, capacity)
		remaining := round(math.Max(0, healthPct-80)/20*100, 1)
		h.LifeRemainingPercent = &remaining
		h.LifeUsedPercent = ptr(round(100-remaining, 1))
		h.Status = report.StatusOK
		if healthPct < 80 {
			h.Status = report.StatusWarning
			h.Reasons = append(h.Reasons, fmt.Sprintf("holds %.0f%% of its design capacity (below 80%%): consider replacing it", healthPct))
		}
		if haveCycles && cycles > 0 && healthPct < 100 && healthPct > 80 {
			perCycle := (100 - healthPct) / float64(cycles)
			h.Estimate = &report.Estimate{
				What:   "charge cycles until 80% of design capacity",
				Value:  math.Round((healthPct - 80) / perCycle),
				Unit:   "cycles",
				Method: fmt.Sprintf("(%.1f%% health − 80%%) ÷ %.4f%% wear per cycle over %d cycles so far", healthPct, perCycle, cycles),
			}
		}
	}
	// Some drivers report a verdict of their own. Only faults of the cell
	// itself are failures; temperature states (a laptop left in a cold
	// car), charging timers and calibration are passing conditions.
	switch v := strings.ToLower(readStr(d + "health")); v {
	case "", "unknown", "good":
	case "dead", "over voltage", "over current", "unspecified failure":
		h.Status = report.StatusFailing
		h.Reasons = append(h.Reasons, "driver reports battery health: "+v)
	case "overheat", "hot", "warm", "cool", "cold":
		if h.Status == report.StatusOK || h.Status == report.StatusUnknown {
			h.Status = report.StatusWarning
		}
		h.Reasons = append(h.Reasons, "driver reports battery temperature: "+v+" (charging may pause until it passes)")
	default:
		if h.Status == report.StatusOK || h.Status == report.StatusUnknown {
			h.Status = report.StatusWarning
		}
		h.Reasons = append(h.Reasons, "driver reports battery health: "+v)
	}
	if h.Metrics == nil && h.Status == report.StatusUnknown {
		return nil
	}
	return h
}

func isoDate(y, m, d int) string {
	switch {
	case m >= 1 && m <= 12 && d >= 1 && d <= 31:
		return fmt.Sprintf("%04d-%02d-%02d", y, m, d)
	case m >= 1 && m <= 12:
		return fmt.Sprintf("%04d-%02d", y, m)
	}
	return strconv.Itoa(y)
}

func round(v float64, places int) float64 {
	m := math.Pow(10, float64(places))
	return math.Round(v*m) / m
}

var hwmonInput = regexp.MustCompile(`^(temp|fan|in|power|curr)(\d+)_(input|average)$`)

// sensors takes one reading of every hwmon sensor.
func (c *collector) sensors() {
	c.r.Sensors = []report.Sensor{}
	for _, h := range list("/sys/class/hwmon") {
		d := "/sys/class/hwmon/" + h + "/"
		s := report.Sensor{Chip: readStr(d + "name"), Readings: []report.SensorReading{}}
		_, s.Device = busOf(d + "device")
		seen := map[string]bool{}
		for _, f := range list(d) {
			m := hwmonInput.FindStringSubmatch(f)
			if m == nil || seen[m[1]+m[2]] {
				continue
			}
			seen[m[1]+m[2]] = true
			raw, ok := readInt(d + f)
			if !ok {
				continue
			}
			prefix := d + m[1] + m[2] + "_"
			label := readStr(prefix + "label")
			if label == "" {
				label = m[1] + m[2]
			}
			r := report.SensorReading{Label: label}
			scale := 1.0
			switch m[1] {
			case "temp":
				r.Kind, r.Unit, scale = "temperature", "C", 1000
			case "fan":
				r.Kind, r.Unit = "fan", "RPM"
			case "in":
				r.Kind, r.Unit, scale = "voltage", "V", 1000
			case "power":
				r.Kind, r.Unit, scale = "power", "W", 1e6
			case "curr":
				r.Kind, r.Unit, scale = "current", "A", 1000
			}
			r.Value = round(float64(raw)/scale, 3)
			if v, ok := readInt(prefix + "max"); ok {
				r.Max = round(float64(v)/scale, 3)
			}
			if v, ok := readInt(prefix + "crit"); ok {
				r.Crit = round(float64(v)/scale, 3)
			}
			s.Readings = append(s.Readings, r)
		}
		if len(s.Readings) > 0 {
			c.r.Sensors = append(c.r.Sensors, s)
		}
	}
}

// usb lists USB devices (not interfaces, not root hubs).
func (c *collector) usb() {
	c.r.USB = []report.USBDevice{}
	const dir = "/sys/bus/usb/devices/"
	for _, n := range list(dir) {
		if strings.Contains(n, ":") || strings.HasPrefix(n, "usb") {
			continue
		}
		d := dir + n + "/"
		vid, pid := readStr(d+"idVendor"), readStr(d+"idProduct")
		// The device's own strings; resolve replaces them with database
		// names where it has them, which are usually more consistent.
		dev := report.USBDevice{
			Path:       n,
			VendorID:   vid,
			ProductID:  pid,
			USBVersion: strings.TrimSpace(readStr(d + "version")),
			Firmware:   firmwareVersion(usbRelease(readStr(d+"bcdDevice")), "usb"),
		}
		if id := (&report.Identity{
			Vendor: readStr(d + "manufacturer"),
			Model:  readStr(d + "product"),
			Serial: readStr(d + "serial"),
		}); !id.Empty() {
			dev.Identity = id
		}
		if v, ok := readInt32(d + "busnum"); ok {
			dev.Bus = v
		}
		if v, ok := readInt32(d + "devnum"); ok {
			dev.Device = v
		}
		if v, err := strconv.ParseFloat(readStr(d+"speed"), 64); err == nil {
			dev.SpeedMbps = v
		}
		class := readStr(d + "bDeviceClass")
		seenDrv := map[string]bool{}
		for _, iface := range list(d) {
			if !strings.HasPrefix(iface, n+":") {
				continue
			}
			if class == "00" || class == "" {
				class = readStr(d + iface + "/bInterfaceClass")
			}
			if drv := driverAt(d + iface); drv != nil && !seenDrv[drv.Name] {
				seenDrv[drv.Name] = true
				dev.Drivers = append(dev.Drivers, *drv)
			}
		}
		dev.ClassCode = class
		c.r.USB = append(c.r.USB, dev)
	}
}
