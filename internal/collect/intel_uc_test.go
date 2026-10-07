package collect

import (
	"strings"
	"testing"

	"github.com/jiegui2025/hwspec/internal/report"
)

// What i915 and xe print, as their dump functions write it (intel_uc_fw.c
// intel_uc_fw_dump, xe_uc_fw.c xe_uc_fw_print), with the reference
// machine's versions (UHD 630, i915: the kernel log's "GuC firmware
// i915/kbl_guc_70.1.1.bin version 70.1.1", "HuC … version 4.0.0").
const (
	i915GuC = "GuC firmware: i915/kbl_guc_70.1.1.bin\n\tstatus: RUNNING\n\tversion: found 70.1.1\n\tuCode: 336448 bytes\n\tRSA: 256 bytes\n" +
		"GuC status 0x8002f0ec:\n\tBootrom status = 0x76\n"
	i915HuC   = "HuC firmware: i915/kbl_huc_4.0.0.bin\n\tstatus: RUNNING\n\tversion: found 4.0.0\n\tuCode: 220032 bytes\n\tRSA: 256 bytes\nHuC status: 0x00006080\n"
	i915Older = "GuC firmware: i915/adlp_guc_62.0.3.bin\nGuC firmware wanted: i915/adlp_guc_70.bin\n\tstatus: RUNNING\n\tversion: wanted 70.1.1, found 62.0.3\n"
	xeGuC     = "GuC firmware: xe/lnl_guc_70.bin\n\tstatus: RUNNING\n\twanted release version 70.29.2\n\tfound release version 70.36.0\n\tfound compatibility version 1.17.0\n\tuCode: 405504 bytes\n"
	xeHuC     = "HuC firmware: xe/lnl_huc.bin\n\tstatus: RUNNING\n\twanted compatibility version 8.2.10\n\tfound compatibility version 8.2.10.3108\n"
)

func TestParseUCInfo(t *testing.T) {
	for name, c := range map[string]struct{ text, want string }{
		"i915 GuC":           {i915GuC, "GuC 70.1.1 debugfs"},
		"i915 HuC":           {i915HuC, "HuC 4.0.0 debugfs"},
		"i915 older file":    {i915Older, "GuC 62.0.3 debugfs"},
		"xe release":         {xeGuC, "GuC 70.36.0 debugfs"},
		"xe compatibility":   {xeHuC, "HuC 8.2.10.3108 debugfs"},
		"not loaded":         {"HuC firmware: i915/kbl_huc_4.0.0.bin\n\tstatus: DISABLED\n\tversion: found 0.0.0\n", "HuC unknown: the driver gives no version (status: DISABLED)"},
		"no version, status": {"GuC firmware: \n\tstatus: NOT_SUPPORTED\n", "GuC unknown: the driver gives no version (status: NOT_SUPPORTED)"},
		"no version at all":  {"GuC firmware: x\n", "GuC unknown: the driver gives no version"},
		"not a uC file":      {"something else\n\tversion: found 1.2.3\n", " unknown: the file doesn't start with a firmware line"},
		"empty":              {"", " unknown: the file doesn't start with a firmware line"},
	} {
		fw := parseUCInfo(c.text)
		got := fw.Name + " " + fw.Version + " " + fw.Source
		if !fw.Known() {
			got = fw.Name + " unknown: " + fw.Reason
		}
		if got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

// Under --full, an Intel GPU's GuC and HuC come from debugfs, one
// component each per GT, xe's gt symlinks read once; without root, one
// warning for every Intel GPU.
func TestIntelUCFirmware(t *testing.T) {
	gpu := func(addr, driver string) *report.GPU {
		return &report.GPU{PCIAddress: addr, Driver: &report.Driver{Name: driver}}
	}
	components := func(g *report.GPU) string {
		var out []string
		for _, fw := range g.FirmwareComponents {
			if fw.Known() {
				out = append(out, fw.Name+" "+fw.Version)
			} else {
				out = append(out, fw.Name+" unknown: "+fw.Reason)
			}
		}
		return strings.Join(out, "; ")
	}

	t.Run("i915", func(t *testing.T) {
		file, _ := fakeRoot(t)
		file("/sys/kernel/debug/dri/0000:00:02.0/gt0/uc/guc_info", i915GuC)
		file("/sys/kernel/debug/dri/0000:00:02.0/gt0/uc/huc_info", i915HuC)
		col := &collector{r: &report.Report{}, privileged: true}
		g := gpu("0000:00:02.0", "i915")
		col.intelUCFirmware(g)
		if got := components(g); got != "GuC 70.1.1; HuC 4.0.0" || len(col.r.Warnings) != 0 {
			t.Errorf("%q, warnings %q", got, col.r.Warnings)
		}
	})
	t.Run("xe, two GTs and the legacy links", func(t *testing.T) {
		file, link := fakeRoot(t)
		dir := "/sys/kernel/debug/dri/0000:00:02.0/"
		file(dir+"tile0/gt0/uc/guc_info", xeGuC)
		file(dir+"tile0/gt0/uc/huc_info", "HuC firmware: \n\tstatus: NOT_SUPPORTED\n")
		file(dir+"tile0/gt1/uc/guc_info", strings.Replace(xeGuC, "70.36.0", "70.36.1", 1))
		file(dir+"tile0/gt1/uc/huc_info", xeHuC)
		link(dir+"gt0", "tile0/gt0")
		link(dir+"gt1", "tile0/gt1")
		link(dir+"gt2", "tile1/gt2") // dangling
		col := &collector{r: &report.Report{}, privileged: true}
		g := gpu("0000:00:02.0", "xe")
		col.intelUCFirmware(g)
		want := "GuC 70.36.0; HuC unknown: the driver gives no version (status: NOT_SUPPORTED); GuC (gt1) 70.36.1; HuC (gt1) 8.2.10.3108"
		if got := components(g); got != want || len(col.r.Warnings) != 0 {
			t.Errorf("%q\nwant %q, warnings %q", got, want, col.r.Warnings)
		}
	})
	t.Run("xe without the legacy links, and a file that isn't one", func(t *testing.T) {
		file, _ := fakeRoot(t)
		dir := "/sys/kernel/debug/dri/0000:00:02.0/"
		file(dir+"tile0/gt0/uc/guc_info", xeGuC)
		file(dir+"tile0/gt1/uc/guc_info", "garbage\n")
		col := &collector{r: &report.Report{}, privileged: true}
		g := gpu("0000:00:02.0", "xe")
		col.intelUCFirmware(g)
		if got := components(g); got != "GuC 70.36.0;  unknown: the file doesn't start with a firmware line" {
			t.Errorf("%q", got)
		}
		if len(g.FirmwareComponents) == 2 && g.FirmwareComponents[1].Name != "" {
			t.Errorf("an unnamed file got a GT suffix: %q", g.FirmwareComponents[1].Name)
		}
	})
	t.Run("without root", func(t *testing.T) {
		fakeRoot(t)
		col := &collector{r: &report.Report{}}
		for _, g := range []*report.GPU{gpu("0000:00:02.0", "i915"), gpu("0000:03:00.0", "xe"), gpu("0000:04:00.0", "amdgpu")} {
			col.intelUCFirmware(g)
			if g.FirmwareComponents != nil {
				t.Errorf("%s: %+v", g.PCIAddress, g.FirmwareComponents)
			}
		}
		if len(col.r.Warnings) != 1 || col.r.Warnings[0] != "Intel GPU firmware (GuC, HuC): needs root (run with --full)" {
			t.Errorf("warnings %q", col.r.Warnings)
		}
	})
	t.Run("other drivers, and none", func(t *testing.T) {
		fakeRoot(t)
		col := &collector{r: &report.Report{}, privileged: true}
		for _, g := range []*report.GPU{gpu("0000:01:00.0", "amdgpu"), {PCIAddress: "0000:05:00.0"}} {
			col.intelUCFirmware(g)
			if g.FirmwareComponents != nil || len(col.r.Warnings) != 0 {
				t.Errorf("%s: %+v, warnings %q", g.PCIAddress, g.FirmwareComponents, col.r.Warnings)
			}
		}
	})
	t.Run("no uc directory, and an unreadable file", func(t *testing.T) {
		file, _ := fakeRoot(t)
		col := &collector{r: &report.Report{}, privileged: true}
		g := gpu("0000:00:02.0", "i915")
		col.intelUCFirmware(g)
		if g.FirmwareComponents != nil || strings.Join(col.r.Warnings, "\n") != "gpu 0000:00:02.0: debugfs isn't mounted (/sys/kernel/debug/dri/ is missing): GuC and HuC can't be read" {
			t.Errorf("not mounted: %+v, warnings %q", g.FirmwareComponents, col.r.Warnings)
		}
		file("/sys/kernel/debug/dri/0000:00:02.0/name", "i915") // mounted, but no gt*/uc
		col = &collector{r: &report.Report{}, privileged: true}
		col.intelUCFirmware(g)
		if g.FirmwareComponents != nil || !strings.Contains(strings.Join(col.r.Warnings, "\n"), "has no gt*/uc: GuC and HuC can't be read") {
			t.Errorf("mounted: %+v, warnings %q", g.FirmwareComponents, col.r.Warnings)
		}
		file("/sys/kernel/debug/dri/0000:00:02.0/gt0/uc/guc_info/x", "") // a directory: can't be read
		file("/sys/kernel/debug/dri/0000:00:02.0/gt0/uc/huc_info", i915HuC)
		col = &collector{r: &report.Report{}, privileged: true}
		col.intelUCFirmware(g)
		if got := components(g); got != "HuC 4.0.0" || !strings.Contains(strings.Join(col.r.Warnings, "\n"), "gpu 0000:00:02.0 gt0/uc/guc_info: ") {
			t.Errorf("%q, warnings %q", got, col.r.Warnings)
		}
	})
}

func TestListGlob(t *testing.T) {
	file, _ := fakeRoot(t)
	for _, f := range []string{"/d/tile1/gt0/x", "/d/tile0/gt1/x", "/d/tile0/gt0/x", "/d/tile0/other/x", "/d/gt0/x", "/d/notgt"} {
		file(f, "")
	}
	if got := strings.Join(listGlob("/d", "tile*", "gt*"), " "); got != "tile0/gt0 tile0/gt1 tile1/gt0" {
		t.Errorf("tile*/gt*: %q", got)
	}
	if got := strings.Join(listGlob("/d", "gt*"), " "); got != "gt0" {
		t.Errorf("gt*: %q", got)
	}
	if got := listGlob("/missing", "gt*"); len(got) != 0 {
		t.Errorf("missing: %q", got)
	}
}
