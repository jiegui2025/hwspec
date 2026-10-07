package collect

import (
	"cmp"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/jiegui2025/hwspec/internal/report"
)

// An Intel GPU's GuC and HuC firmware versions (#204). i915 and xe print
// them in debugfs, which only root can read (on the reference machine
// /sys/kernel/debug is drwx------ root):
//
//   - i915: dri/<PCI address>/gt<N>/uc/{guc,huc}_info, written by
//     intel_uc_fw_dump (gt/uc/intel_uc_fw.c): "<GuC|HuC> firmware: <path>",
//     "\tstatus: <status>", then "\tversion: found X.Y.Z", or "\tversion:
//     wanted A.B.C, found X.Y.Z" when an older file was loaded.
//   - xe: dri/<PCI address>/tile<T>/gt<N>/uc/{guc,huc}_info (gt<N> is a
//     legacy symlink to it), written by xe_uc_fw_print (xe_uc_fw.c):
//     "<GuC|HuC> firmware: <path>", "\tstatus: <status>", "\twanted
//     <type> version …", then "\tfound release version X.Y.Z[.B]" and
//     "\tfound compatibility version …" for those the firmware gives.
//
// The found version is the one running; xe's release version names the
// firmware build, so it is preferred over its compatibility version.

var (
	ucHeader   = regexp.MustCompile(`^([A-Za-z]+) firmware: `)
	ucI915     = regexp.MustCompile(`^\tversion: (?:wanted [0-9.]+, )?found ([0-9]+\.[0-9]+\.[0-9]+)$`)
	ucXe       = regexp.MustCompile(`^\tfound (release|compatibility) version ([0-9]+\.[0-9]+\.[0-9]+(?:\.[0-9]+)?)$`)
	ucStatus   = regexp.MustCompile(`^\tstatus: (.+)$`)
	debugfsDRI = "/sys/kernel/debug/dri/"
)

// parseUCInfo reads a guc_info or huc_info file: the firmware's name (GuC,
// HuC) and its found version, or why there is none (no version found, as
// when the firmware isn't loaded: its status).
func parseUCInfo(text string) *report.Firmware {
	lines := strings.Split(text, "\n")
	name := ""
	if m := ucHeader.FindStringSubmatch(lines[0]); m != nil {
		name = m[1]
	}
	if name == "" {
		return &report.Firmware{Status: report.FirmwareUnknown, Reason: "the file doesn't start with a firmware line"}
	}
	status, release, compatibility := "", "", ""
	for _, l := range lines[1:] {
		if m := ucStatus.FindStringSubmatch(l); m != nil {
			status = m[1]
		}
		if m := ucI915.FindStringSubmatch(l); m != nil {
			release = m[1]
		}
		if m := ucXe.FindStringSubmatch(l); m != nil {
			if m[1] == "release" {
				release = m[2]
			} else {
				compatibility = m[2]
			}
		}
	}
	v := cmp.Or(release, compatibility)
	if v == "" || strings.Trim(v, "0.") == "" {
		reason := "the driver gives no version"
		if status != "" {
			reason += " (status: " + status + ")"
		}
		return &report.Firmware{Name: name, Status: report.FirmwareUnknown, Reason: reason}
	}
	return &report.Firmware{Name: name, Version: v, Source: report.FirmwareFromDebugfs}
}

// intelUCFirmware reads an i915 or xe GPU's GuC and HuC versions under
// --full, one component per microcontroller and GT; GTs after the first
// are named ("GuC (gt1)"). xe's gt<N> symlinks lead to the tile<T>/gt<N>
// directories already read, so each is read once.
func (c *collector) intelUCFirmware(gpu *report.GPU) {
	if gpu.Driver == nil || gpu.Driver.Name != "i915" && gpu.Driver.Name != "xe" {
		return
	}
	if !c.privileged {
		if w := "Intel GPU firmware (GuC, HuC): needs root (run with --full)"; !slices.Contains(c.r.Warnings, w) {
			c.warn("%s", w)
		}
		return
	}
	dir := debugfsDRI + gpu.PCIAddress
	var gts []string
	seen := map[string]bool{}
	for _, d := range append(listGlob(dir, "tile*", "gt*"), listGlob(dir, "gt*")...) {
		real := realPath(dir + "/" + d)
		if real == "" || seen[real] {
			continue // a dangling link has nothing to read
		}
		seen[real] = true
		gts = append(gts, d)
	}
	switch {
	case len(gts) > 0:
	case !exists(debugfsDRI):
		c.warn("gpu %s: debugfs isn't mounted (%s is missing): GuC and HuC can't be read", gpu.PCIAddress, debugfsDRI)
		return
	default:
		c.warn("gpu %s: the driver's debugfs directory %s has no gt*/uc: GuC and HuC can't be read", gpu.PCIAddress, dir)
		return
	}
	for _, gt := range gts {
		label := ""
		if n := path.Base(gt); n != "gt0" {
			label = " (" + n + ")"
		}
		for _, file := range []string{"guc_info", "huc_info"} {
			text, err := readStrErr(dir + "/" + gt + "/uc/" + file)
			if err != nil {
				c.warnRead(fmt.Sprintf("gpu %s %s/uc/%s", gpu.PCIAddress, gt, file), err)
				continue
			}
			fw := parseUCInfo(text)
			if fw.Name != "" {
				fw.Name += label
			}
			gpu.FirmwareComponents = append(gpu.FirmwareComponents, *fw)
		}
	}
}

// listGlob lists the entries of dir matching each pattern in turn, as
// relative paths ("tile0/gt0"), sorted (list sorts each level).
func listGlob(dir string, patterns ...string) []string {
	paths := []string{""}
	for _, p := range patterns {
		var next []string
		for _, base := range paths {
			for _, e := range list(dir + "/" + base) {
				if ok, _ := path.Match(p, e); ok {
					next = append(next, strings.TrimPrefix(base+"/"+e, "/"))
				}
			}
		}
		paths = next
	}
	return paths
}
