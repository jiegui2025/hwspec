package collect

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jaypipes/ghw"

	"github.com/jiegui2025/hwspec/internal/report"
)

const cpuDir = "/sys/devices/system/cpu/"

func (c *collector) cpu() {
	out := &c.r.CPU
	var model, vendor string
	info, err := ghw.CPU(ghw.WithChroot(root), ghw.WithDisableWarnings(), ghw.WithDisableTools())
	if err != nil {
		c.warn("cpu: %v", err)
	} else {
		out.Sockets = len(info.Processors)
		out.Cores = int(info.TotalCores)
		out.Threads = int(info.TotalHardwareThreads)
		if len(info.Processors) > 0 {
			p0 := info.Processors[0]
			model, vendor = p0.Model, p0.Vendor
			out.Flags = p0.Capabilities
		}
	}
	if model == "" {
		// ARM kernels often have no "model name". "Processor" (older arm32)
		// and "Hardware" (the SoC) describe the CPU; "Model" is the board,
		// which belongs to System, so it isn't used here.
		for _, k := range []string{"model name", "cpu model", "Processor", "Hardware"} {
			if v := c.cpuinfoField(k); v != "" {
				model = v
				break
			}
		}
	}
	if vendor == "" {
		vendor = c.cpuinfoField("vendor_id")
	}
	if model != "" || vendor != "" {
		out.Identity = &report.Identity{Vendor: vendor, Model: model}
	}
	out.Firmware = firmwareOrUnknown(firmwareVersion(c.cpuinfoField("microcode"), report.FirmwareFromMicrocode), microcodeReason())
	out.Family, _ = strconv.Atoi(c.cpuinfoField("cpu family"))
	out.ModelID, _ = strconv.Atoi(c.cpuinfoField("model"))
	out.Stepping, _ = strconv.Atoi(c.cpuinfoField("stepping"))
	if out.Flags == nil {
		out.Flags = strings.Fields(c.cpuinfoField("flags"))
	}
	sort.Strings(out.Flags)
	for _, f := range out.Flags {
		if f == "vmx" || f == "svm" {
			out.Virtualization = f
		}
	}

	if khz, ok := readInt32(cpuDir + "cpu0/cpufreq/cpuinfo_min_freq"); ok {
		out.MinFreqMHz = khz / 1000
	}
	// On hybrid CPUs cpu0 may be an E-core, so take the highest max.
	for _, cpu := range cpuDirs() {
		if khz, ok := readInt32(cpuDir + cpu + "/cpufreq/cpuinfo_max_freq"); ok && khz/1000 > out.MaxFreqMHz {
			out.MaxFreqMHz = khz / 1000
		}
	}
	// The frequency-scaling driver (intel_pstate, amd-pstate-epp,
	// acpi-cpufreq…) is usually built in; it has no device to bind to.
	if name := readStr(cpuDir + "cpu0/cpufreq/scaling_driver"); name != "" {
		out.Driver = &report.Driver{Name: name}
		if mod := strings.ReplaceAll(name, "-", "_"); exists("/sys/module/" + mod + "/initstate") {
			out.Driver.Module = mod
		} else {
			out.Driver.Builtin = true
		}
	}
	out.Health = cpuHealth()
	out.Governor = readStr(cpuDir + "cpu0/cpufreq/scaling_governor")

	// Intel hybrid: the kernel registers separate PMUs for each core type.
	for _, t := range []struct{ pmu, name string }{
		{"cpu_core", "performance"},
		{"cpu_atom", "efficiency"},
	} {
		if list := readStr("/sys/devices/" + t.pmu + "/cpus"); list != "" {
			out.CoreTypes = append(out.CoreTypes, report.CoreType{Name: t.name, Threads: countCPUList(list)})
		}
	}

	out.Caches = c.caches()
}

// cpuHealth counts thermal throttling since boot (Intel exposes the
// counters). Laptops reach their thermal limit under sustained load by
// design, so a count alone doesn't make the CPU unhealthy; it is recorded
// for the maintenance advice, which can compare it with the machine's age
// and load.
func cpuHealth() *report.Health {
	var events uint64
	found := false
	// Each counter repeats on every logical CPU that shares it: the core
	// counter on hyper-thread siblings, the package counter on every CPU of
	// the package. Count each once.
	seen := map[string]bool{}
	for _, cpu := range cpuDirs() {
		topo := cpuDir + cpu + "/topology/"
		pkg := readStr(topo + "physical_package_id")
		for f, owner := range map[string]string{
			"core_throttle_count":    pkg + "/" + readStr(topo+"core_id"),
			"package_throttle_count": pkg,
		} {
			v, err := strconv.ParseUint(readStr(cpuDir+cpu+"/thermal_throttle/"+f), 10, 64)
			if err != nil {
				continue
			}
			found = true
			if key := f + "|" + owner; !seen[key] {
				seen[key] = true
				events += v
			}
		}
	}
	if !found {
		return nil
	}
	h := &report.Health{Status: report.StatusOK, Source: report.HealthFromThermalThrottle}
	metric(h, report.MetricThrottleEvents, float64(events), true)
	if events > 0 {
		h.Reasons = append(h.Reasons, fmt.Sprintf("thermal throttling %d times since boot: expected under heavy load on thin laptops; at light load, check cooling (dust, thermal paste or pad, fan)", events))
	}
	return h
}

func cpuDirs() []string {
	var out []string
	for _, n := range list(cpuDir) {
		if strings.HasPrefix(n, "cpu") {
			if _, err := strconv.Atoi(n[3:]); err == nil {
				out = append(out, n)
			}
		}
	}
	return out
}

// caches walks every CPU's cache entries and counts each distinct cache
// once (caches are shared, e.g. L3 by all cores, so they appear under
// several CPUs with the same shared_cpu_list).
func (c *collector) caches() []report.Cache {
	type key struct {
		level int
		typ   string
		size  uint64
	}
	seen := map[string]bool{}
	counts := map[key]int{}
	for _, cpu := range cpuDirs() {
		base := cpuDir + cpu + "/cache/"
		for _, idx := range list(base) {
			if !strings.HasPrefix(idx, "index") {
				continue
			}
			d := base + idx + "/"
			level, _ := readInt32(d + "level")
			typ := readStr(d + "type")
			size := parseSize(readStr(d + "size"))
			id := strings.Join([]string{strconv.Itoa(level), typ, readStr(d + "shared_cpu_list")}, "|")
			if seen[id] || size == 0 {
				continue
			}
			seen[id] = true
			counts[key{level, typ, size}]++
		}
	}
	out := []report.Cache{}
	for k, n := range counts {
		out = append(out, report.Cache{Level: k.level, Type: k.typ, SizeBytes: k.size, Instances: n})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Level != b.Level {
			return a.Level < b.Level
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		return a.SizeBytes > b.SizeBytes
	})
	return out
}

// parseSize handles sysfs cache sizes like "32K", "1024K", "16M".
func parseSize(s string) uint64 {
	if s == "" {
		return 0
	}
	mult := uint64(1)
	switch s[len(s)-1] {
	case 'K':
		mult, s = 1<<10, s[:len(s)-1]
	case 'M':
		mult, s = 1<<20, s[:len(s)-1]
	case 'G':
		mult, s = 1<<30, s[:len(s)-1]
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return v * mult
}

// countCPUList counts the CPUs in a kernel cpu list like "0-11,16,18-19".
func countCPUList(s string) int {
	n := 0
	for part := range strings.SplitSeq(strings.TrimSpace(s), ",") {
		if part == "" {
			continue
		}
		lo, hi, isRange := strings.Cut(part, "-")
		if !isRange {
			n++
			continue
		}
		a, err1 := strconv.Atoi(lo)
		b, err2 := strconv.Atoi(hi)
		if err1 == nil && err2 == nil && b >= a {
			n += b - a + 1
		}
	}
	return n
}

// microcodeReason says why the microcode revision is missing: the kernel
// reports it in /proc/cpuinfo on x86 only.
func microcodeReason() string {
	switch _, arch := uname(); arch {
	case "x86_64", "i386", "i486", "i586", "i686":
		return "the kernel's /proc/cpuinfo gives no microcode revision"
	default:
		return "the kernel doesn't report CPU microcode on " + arch
	}
}
