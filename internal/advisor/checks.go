package advisor

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jiegui2025/hwspec/internal/kb"
	"github.com/jiegui2025/hwspec/internal/report"
)

// pciWithoutDriver finds PCI devices of the rule's classes that no kernel
// driver is bound to. Bridges and other plumbing often have no driver by
// design, so the rule must name the classes it cares about. What to do is
// the rule's: its commands may name the device's {modalias}.
func pciWithoutDriver(in *Input, rule *kb.Rule) ([]hit, error) {
	if len(rule.Match.PCIClass) == 0 {
		return nil, errors.New("match.pci_class is empty: name the device classes that need a driver")
	}
	var hits []hit
	for i, d := range in.Report.PCI {
		if d.Driver != nil || !hasAnyPrefix(strings.ToLower(d.ClassCode), rule.Match.PCIClass) {
			continue
		}
		name := d.Class
		if d.Identity != nil && d.Identity.Model != "" {
			name = d.Identity.Model
		}
		h := hit{
			device: &DeviceRef{Kind: "pci", Key: d.Address, Name: name},
			evidence: []Evidence{
				Present(fmt.Sprintf("pci[%d].class_code", i), d.ClassCode),
				Absent(fmt.Sprintf("pci[%d].driver", i)),
			},
		}
		if m, ok := pciModalias(d); ok {
			h.vars = map[string]string{"modalias": m}
		}
		hits = append(hits, h)
	}
	return hits, nil
}

var (
	hex4 = regexp.MustCompile(`^[0-9a-fA-F]{4}$`)
	hex6 = regexp.MustCompile(`^[0-9a-fA-F]{6}$`)
)

// pciModalias is the device's modalias as the kernel writes it in sysfs
// (pci:v…d…sv…sd…bc…sc…i…), built from the capture's IDs only when each
// is hex of the right length: it goes into a suggested command, and a
// capture may be someone else's file.
func pciModalias(d report.PCIDevice) (string, bool) {
	for _, id := range []string{d.VendorID, d.DeviceID, d.SubVendorID, d.SubDeviceID} {
		if !hex4.MatchString(id) {
			return "", false
		}
	}
	if !hex6.MatchString(d.ClassCode) {
		return "", false
	}
	c := strings.ToUpper(d.ClassCode)
	return fmt.Sprintf("pci:v0000%sd0000%ssv0000%ssd0000%sbc%ssc%si%s", strings.ToUpper(d.VendorID), strings.ToUpper(d.DeviceID),
		strings.ToUpper(d.SubVendorID), strings.ToUpper(d.SubDeviceID), c[0:2], c[2:4], c[4:6]), true
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
