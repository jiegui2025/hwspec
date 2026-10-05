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

// Metric names come from the capture file too: a crafted key must not
// carry escapes or bidi overrides into JSON or YAML output.
func TestSanitizeCleansMetricNames(t *testing.T) {
	r := &Report{Batteries: []Battery{{Health: &Health{Metrics: map[string]float64{"a\x1b[31m\u202eRED": 1}}}}}
	r.Sanitize()
	if v, ok := r.Batteries[0].Health.Metrics["a[31mRED"]; !ok || v != 1 || len(r.Batteries[0].Health.Metrics) != 1 {
		t.Errorf("metrics = %v", r.Batteries[0].Health.Metrics)
	}
}
