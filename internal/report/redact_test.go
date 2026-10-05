package report

import "testing"

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
		Bluetooth: []BluetoothController{{Address: "60:F2:62:15:AB:5C", LocalName: "alice-laptop"}},
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
