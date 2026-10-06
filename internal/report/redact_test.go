package report

import (
	"fmt"
	"testing"
	"time"
)

func TestRedactRemovesPersonalPathsAndMACDerivedNames(t *testing.T) {
	r := &Report{
		Hostname: "alice-laptop",
		System:   System{Identity: &Identity{Model: "ThinkPad", Serial: "PF1234"}, UUID: "u-u-i-d"},
		Memory:   Memory{Modules: []MemoryModule{{Identity: &Identity{PartNumber: "M471", Serial: "1234ABCD"}}}},
		USB:      []USBDevice{{Identity: &Identity{Model: "Receiver", Serial: "XYZ"}}},
		Storage: []Disk{{Name: "sda", Identity: &Identity{Model: "SSD", Serial: "S1"}, Partitions: []Partition{
			{MountPoint: "/"}, {MountPoint: "/home"}, {MountPoint: "/home/alice"},
			{MountPoint: "/run/media/alice/Backup", Label: "Backup", UUID: "u"},
		}}},
		Network: []NIC{
			{Name: "enx00e04c680123", MAC: "00:e0:4c:68:01:23"},
			{Name: "wlp2s0", MAC: "aa:bb:cc:dd:ee:ff"},
		},
		Bluetooth: []BluetoothController{{Address: "60:F2:62:12:34:56", LocalName: "alice-laptop"}},
	}
	r.Redact()
	parts := r.Storage[0].Partitions
	want := []string{"/", "/home", "", ""}
	for i, p := range parts {
		if p.MountPoint != want[i] {
			t.Errorf("partition %d mount point = %q, want %q", i, p.MountPoint, want[i])
		}
	}
	if parts[3].Label != "" || parts[3].UUID != "" {
		t.Errorf("partition label/UUID kept: %+v", parts[3])
	}
	if r.Network[0].Name != "enxxxxxxxxxxxxx" || r.Network[0].MAC != "" || r.Network[1].Name != "wlp2s0" {
		t.Errorf("network = %+v", r.Network)
	}
	// Every serial, wherever it lives, is gone; models stay.
	for name, id := range map[string]*Identity{"system": r.System.Identity, "memory": r.Memory.Modules[0].Identity, "usb": r.USB[0].Identity, "disk": r.Storage[0].Identity} {
		if id.Serial != "" || id.Model == "" && id.PartNumber == "" {
			t.Errorf("%s identity after redaction: %+v", name, id)
		}
	}
	if r.System.UUID != "" {
		t.Error("system UUID kept")
	}
	if r.Bluetooth[0].Address != "" || r.Bluetooth[0].LocalName != "" || r.Hostname != "" || !r.Redacted {
		t.Errorf("identifiers kept: %+v", r)
	}
}

// A manufacture day plus a model narrows a part to one production batch;
// redacted captures keep the month.
func TestRedactKeepsManufactureDatesToTheMonth(t *testing.T) {
	r := &Report{
		Batteries: []Battery{{Identity: &Identity{Model: "5B10W13930", ManufactureDate: "2021-03-17"}}},
		Displays:  []Display{{Identity: &Identity{ManufactureDate: "2020-W38"}}},
		Memory:    Memory{Modules: []MemoryModule{{Identity: &Identity{ManufactureDate: "2020-W05"}}}},
	}
	r.Redact()
	if got := r.Batteries[0].Identity.ManufactureDate; got != "2021-03" {
		t.Errorf("battery date = %q", got)
	}
	// Week 38 of 2020 is Mon 14 to Sun 20 September.
	if got := r.Displays[0].Identity.ManufactureDate; got != "2020-09" {
		t.Errorf("display date = %q, want 2020-09", got)
	}
	// Week 5 of 2020 is Mon 27 January to Sun 2 February: its Thursday is in January.
	if got := r.Memory.Modules[0].Identity.ManufactureDate; got != "2020-01" {
		t.Errorf("memory module date = %q, want 2020-01", got)
	}
}

func TestMonthOf(t *testing.T) {
	for d, want := range map[string]string{
		"2021-03-17": "2021-03",
		"2020-W38":   "2020-09",
		"2020-W01":   "2020-01", // begins Mon 30 December 2019
		"2019-W01":   "2019-01", // begins Mon 31 December 2018
		"2020-W05":   "2020-01", // Thursday 30 January, ends in February
		"2020-W06":   "2020-02",
		"2026-W53":   "2026-12", // 2026 has 53 weeks; Thursday 31 December
		"2025-W53":   "2025-12", // 2025 has 52: not the next year's January
		// Already a month or coarser, or malformed: unchanged.
		"2021-03": "2021-03", "2020": "2020", "": "",
		"2020-W00": "2020-W00", "2020-W54": "2020-W54", "2020-W1": "2020-W1",
		"2020-Wxx": "2020-Wxx", "20a0-W10": "20a0-W10", "2020/W10": "2020/W10",
	} {
		if got := monthOf(d); got != want {
			t.Errorf("monthOf(%q) = %q, want %q", d, got, want)
		}
	}
}

// Against Go's own ISO week numbering: every week maps to the month of its
// Thursday.
func TestMonthOfAgreesWithISOWeek(t *testing.T) {
	for day := time.Date(1990, 1, 4, 0, 0, 0, 0, time.UTC); day.Year() < 2040; day = day.AddDate(0, 0, 7) {
		if day.Weekday() != time.Thursday {
			day = day.AddDate(0, 0, (int(time.Thursday)-int(day.Weekday())+7)%7)
		}
		year, week := day.ISOWeek()
		d := fmt.Sprintf("%04d-W%02d", year, week)
		if got := monthOf(d); got != day.Format("2006-01") {
			t.Fatalf("monthOf(%q) = %q, want %q (its Thursday is %s)", d, got, day.Format("2006-01"), day.Format("2006-01-02"))
		}
	}
}
