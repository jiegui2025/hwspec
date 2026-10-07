package main

import (
	"fmt"

	"github.com/jiegui2025/hwspec/internal/ids"
)

// knownAnswers are entries whose names upstream settled long ago, checked
// in every bundle before it's signed (#155). Counts can't catch a parser
// that misreads upstream: a reordered amd.c that maps Zen 4 models to
// "Zen 3", or a JEDEC bank cut short so every later ID shifts, keeps the
// counts. Each kind has entries from early and late in its file; the CPU
// ones come from the kernel's sources, not the curated list, which would
// mask a misread. When one fails, see CONTRIBUTING › ID databases.
var knownAnswers = map[ids.Kind][][2]string{
	ids.PCI: {{"8086", "Intel Corporation"}, {"10de", "NVIDIA Corporation"}, {"1002", "Advanced Micro Devices, Inc. [AMD/ATI]"}},
	ids.USB: {{"046d", "Logitech, Inc."}, {"1d6b", "Linux Foundation"}},
	ids.PNP: {{"DEL", "Dell Inc."}, {"SAM", "Samsung Electric Company"}},
	ids.OUI: {{"00000C", "Cisco Systems, Inc"}, {"000C29", "VMware, Inc."}, {"F4F5D8", "Google, Inc."}},
	ids.JEDEC: {{"1:2C", "Micron Technology"}, {"1:4E", "Samsung"}, {"1:2D", "SK Hynix (former Hyundai Electronics)"},
		{"6:77", "Avant Technology"}, {"12:01", "ABIT Electronics (Shenzhen) Co Ltd"}},
	ids.AMDGPU: {{"744c:c8", "AMD Radeon RX 7900 XTX"}},
	ids.BT:     {{"0002", "Intel Corp."}, {"004C", "Apple, Inc."}},
	ids.CPU: {{"intel:6:97", "Alder Lake\tGolden Cove / Gracemont"}, {"intel:6:8c", "Tiger Lake\tWillow Cove"},
		{"amd:19:22", "\tZen 3"}, {"amd:19:62", "\tZen 4"}},
}

// checkKnownAnswers checks a database's known answers.
func checkKnownAnswers(k ids.Kind, content []byte) error {
	names, err := ids.Names(k, content)
	if err != nil {
		return err
	}
	for _, a := range knownAnswers[k] {
		if got := names[a[0]]; got != a[1] {
			return fmt.Errorf("known answer %s %s is %q, want %q: upstream changed it, or genids misread upstream (CONTRIBUTING › ID databases)", k, a[0], got, a[1])
		}
	}
	return nil
}
