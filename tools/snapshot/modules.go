package main

import (
	"bytes"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// keepOthers is how many index entries that match none of the machine's
// devices a recording keeps, so a replay also sees aliases that don't
// match.
const keepOthers = 3

// trimModuleIndex cuts the recorded module index (modules.alias, 1.7 MB,
// and modules.builtin.modinfo) down to the aliases that match the
// recorded PCI devices' modaliases, plus a few that don't: a capture of
// the recording finds the same candidates.
func trimModuleIndex(build string) error {
	modaliases, err := filepath.Glob(filepath.Join(build, "sys/bus/pci/devices/*/modalias"))
	if err != nil {
		return err
	}
	var have []string
	for _, f := range modaliases {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		have = append(have, strings.TrimSpace(string(b)))
	}
	matches := func(pattern string) bool {
		for _, m := range have {
			if ok, _ := path.Match(pattern, m); ok {
				return true
			}
		}
		return false
	}
	for _, dir := range []string{"lib/modules", "usr/lib/modules", "run/booted-system/kernel-modules/lib/modules"} {
		files, err := filepath.Glob(filepath.Join(build, dir, "*", "modules.*"))
		if err != nil {
			return err
		}
		// Through a /lib symlink a file is found twice; trimming it again
		// keeps the same lines.
		for _, f := range files {
			var out []byte
			switch filepath.Base(f) {
			case "modules.alias":
				out = trimLines(f, matches)
			case "modules.builtin.modinfo":
				out = trimModinfo(f, matches)
			default:
				continue
			}
			if out == nil {
				continue
			}
			if err := os.WriteFile(f, out, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// trimLines keeps the "alias <glob> <module>" lines that match, and the
// first few that don't.
func trimLines(file string, matches func(string) bool) []byte {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var out bytes.Buffer
	others := 0
	for line := range strings.Lines(string(b)) {
		f := strings.Fields(line)
		if len(f) != 3 || f[0] != "alias" {
			continue
		}
		if matches(f[1]) || others < keepOthers {
			if !matches(f[1]) {
				others++
			}
			out.WriteString(strings.Join(f, " ") + "\n")
		}
	}
	return out.Bytes()
}

// trimModinfo keeps the "<module>.alias=<glob>" entries that match, and
// the first few that don't; other keys are dropped.
func trimModinfo(file string, matches func(string) bool) []byte {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var out bytes.Buffer
	others := 0
	for entry := range bytes.SplitSeq(b, []byte{0}) {
		k, v, ok := strings.Cut(string(entry), "=")
		if !ok || !strings.HasSuffix(k, ".alias") {
			continue
		}
		if matches(v) || others < keepOthers {
			if !matches(v) {
				others++
			}
			out.WriteString(string(entry))
			out.WriteByte(0)
		}
	}
	return out.Bytes()
}
