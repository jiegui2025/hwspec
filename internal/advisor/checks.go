package advisor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"regexp"
	"strings"

	"github.com/jiegui2025/hwspec/internal/kb"
	"github.com/jiegui2025/hwspec/internal/report"
)

func init() {
	register("pci-without-driver", check{
		run:       pciWithoutDriver,
		needs:     []string{"pci[].class_code", "pci[].driver"},
		available: func(in *Input) bool { return in.Report.PCI != nil },
		provides:  []string{"modalias"},
		matches:   []string{"pci_class"},
		// Bridges and other plumbing often have no driver by design, so the
		// rule must name the classes it cares about.
		requires: []string{"pci_class"},
		example: func() (kb.Rule, *report.Report) {
			return kb.Rule{ID: "pci.no-driver", Check: "pci-without-driver", Category: "needs-attention", Severity: "warning",
					Title: "No kernel driver", Match: kb.Match{PCIClass: []string{"02"}},
					Actions: []kb.Action{{Text: "Look it up", Commands: []string{"modprobe -R {modalias}"}}}, Src: []string{"kernel"}},
				&report.Report{PCI: []report.PCIDevice{
					{Address: "0000:00:1f.6", VendorID: "8086", DeviceID: "15bc", ClassCode: "020000", Class: "Ethernet controller", Driver: &report.Driver{Name: "e1000e"}},
					{Address: "0000:02:00.0", VendorID: "8086", DeviceID: "2723", SubVendorID: "8086", SubDeviceID: "0084",
						ClassCode: "028000", Class: "Network controller", Identity: &report.Identity{Model: "Wi-Fi 6 AX200"}},
				}}
		},
	})
}

// pciWithoutDriver finds PCI devices of the rule's classes that no kernel
// driver is bound to and no module claims (#214): a device with candidate
// modules gets the driver checks' answer instead (drivers.go), and while
// the running kernel's modules aren't installed, kernel-modules-missing's.
// A capture from before candidates were read gets this finding for every
// driverless device, as before. What to do is the rule's: its commands may
// name the device's {modalias}.
func pciWithoutDriver(in *Input, rule *kb.Rule) ([]hit, error) {
	if modulesMissing(in.Report) {
		return nil, nil
	}
	var hits []hit
	for m := range pciMatches(in.Report, rule.Match) {
		if m.dev.Driver != nil || len(m.dev.ModuleCandidates) > 0 {
			continue
		}
		h := hit{device: m.ref, evidence: append(m.evidence, Absent(m.path("driver")))}
		if m.dev.ModuleCandidates != nil {
			h.evidence = append(h.evidence, Present(m.path("module_candidates"), m.dev.ModuleCandidates))
		}
		if modalias, ok := pciModalias(*m.dev); ok {
			h.vars = map[string]string{"modalias": modalias}
		}
		hits = append(hits, h)
	}
	return hits, nil
}

// pciMatch is a PCI device a rule's common match keys selected, with its
// reference and the evidence of the match.
type pciMatch struct {
	index    int
	dev      *report.PCIDevice
	ref      *DeviceRef
	evidence []Evidence
}

// path is the capture path of one of the device's fields.
func (m pciMatch) path(field string) string { return fmt.Sprintf("pci[%d].%s", m.index, field) }

// pciMatches yields the capture's PCI devices that the PCI match keys
// (pci_class) select; with none set, every device.
func pciMatches(r *report.Report, match kb.Match) iter.Seq[pciMatch] {
	return func(yield func(pciMatch) bool) {
		for i := range r.PCI {
			d := &r.PCI[i]
			name := d.Class
			if d.Identity != nil && d.Identity.Model != "" {
				name = d.Identity.Model
			}
			m := pciMatch{index: i, dev: d, ref: &DeviceRef{Kind: "pci", Key: d.Address, Name: name}}
			if len(match.PCIClass) > 0 {
				if !hasAnyPrefix(strings.ToLower(d.ClassCode), match.PCIClass) {
					continue
				}
				m.evidence = append(m.evidence, Present(m.path("class_code"), d.ClassCode))
			}
			if !yield(m) {
				return
			}
		}
	}
}

// claims is one data value as the knowledge base gives it: the claims of
// its sources, each {value, src}.
type claims[V any] []claim[V]

type claim[V any] struct {
	Value V      `json:"value"`
	Src   string `json:"src"`
	Note  string `json:"note,omitempty"` // a caveat, printed with the source
}

// UnmarshalJSON reads a claim's value strictly and its source. Its other
// keys are descriptive (a note, a page: ADR 0009), so a newer knowledge
// base may add them without older binaries refusing the claim.
func (c *claim[V]) UnmarshalJSON(b []byte) error {
	var raw struct {
		Value json.RawMessage `json:"value"`
		Src   string          `json:"src"`
		Note  string          `json:"note"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if raw.Value == nil || raw.Src == "" {
		return errors.New("a claim is {value, src}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw.Value))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c.Value); err != nil {
		return err
	}
	c.Src, c.Note = raw.Src, raw.Note
	return nil
}

// first returns the first claim's value and the source it cites, which
// the finding then cites too (hit.used).
func (c claims[V]) first() (V, string) {
	var zero V
	if len(c) == 0 {
		return zero, ""
	}
	return c[0].Value, c[0].Src
}

// decodeData decodes a rule's data into T strictly: a key T doesn't have
// is refused, so a rule is never applied with part of its data ignored.
// A check's data func and its run use it the same way.
func decodeData[T any](raw json.RawMessage) (T, error) {
	var v T
	if len(raw) == 0 {
		return v, errors.New("no data")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return v, err
	}
	return v, nil
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
