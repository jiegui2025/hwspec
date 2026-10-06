package collect

import (
	"testing"

	"github.com/jiegui2025/hwspec/internal/report"
)

// The kernel version comes from /proc, or from uname when /proc doesn't
// have it (a restricted container); with neither, the capture says so.
func TestKernelVersionFallsBackToUname(t *testing.T) {
	file, _ := fakeRoot(t)
	uname = func() (string, string) { return "6.12.0-uname", "x86_64" }
	c := &collector{r: &report.Report{}}
	c.osInfo()
	if c.r.OS.Kernel != "6.12.0-uname" || c.r.OS.Arch != "x86_64" {
		t.Errorf("without /proc: kernel %q, arch %q", c.r.OS.Kernel, c.r.OS.Arch)
	}
	file("/proc/sys/kernel/osrelease", "6.12.0-proc\n")
	c = &collector{r: &report.Report{}}
	c.osInfo()
	if c.r.OS.Kernel != "6.12.0-proc" {
		t.Errorf("with /proc: kernel %q", c.r.OS.Kernel)
	}
	uname = func() (string, string) { return "", "" }
	fakeRoot(t)
	c = &collector{r: &report.Report{}}
	c.osInfo()
	found := false
	for _, w := range c.r.Warnings {
		found = found || w == "kernel version: /proc/sys/kernel/osrelease unreadable and uname failed"
	}
	if c.r.OS.Kernel != "" || !found {
		t.Errorf("neither: kernel %q, warnings %v", c.r.OS.Kernel, c.r.Warnings)
	}
}
