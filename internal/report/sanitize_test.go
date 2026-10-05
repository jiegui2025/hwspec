package report

import "testing"

// Device and capture strings must not be able to inject terminal escapes,
// forge extra output lines, or reorder text with bidi overrides, in any
// output format.
func TestSanitizeCleansEveryStringInTheReport(t *testing.T) {
	evil := "OK\x1b[2J\n  Health    FAILED\u202e\u009bx\xff"
	r := &Report{
		Hostname: evil,
		CPU:      CPU{Identity: &Identity{Model: evil}, Flags: []string{evil}},
		Storage: []Disk{{Identity: &Identity{Model: evil}, Health: &Health{Source: evil, Reasons: []string{evil},
			Metrics: map[string]float64{MetricPowerOnHours: 7}}, Partitions: []Partition{{Label: evil}}}},
		Tool:     Tool{IDDatabases: map[string]string{"pci": evil}},
		Warnings: []string{evil},
	}
	r.Sanitize()
	want := "OK[2J  Health    FAILEDx�"
	for name, got := range map[string]string{
		"hostname": r.Hostname, "cpu": r.CPU.Identity.Model, "flag": r.CPU.Flags[0], "disk": r.Storage[0].Identity.Model,
		"reason": r.Storage[0].Health.Reasons[0],
		"health": r.Storage[0].Health.Source, "partition": r.Storage[0].Partitions[0].Label,
		"id_databases": r.Tool.IDDatabases["pci"], "warning": r.Warnings[0],
	} {
		if got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if r.Storage[0].Health.Metrics[MetricPowerOnHours] != 7 {
		t.Error("non-string fields changed")
	}
}
