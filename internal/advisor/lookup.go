package advisor

import (
	"strings"

	"github.com/jiegui2025/hwspec/internal/kb"
	"github.com/jiegui2025/hwspec/internal/report"
)

// Lookups of the knowledge base's data sections (#25) for a capture, by
// raw IDs only. Checks use them; no entry means the check answers from
// the capture alone, and says what it can't know.

// modelFor returns the model entry for the capture's system, or nil. An
// entry that names the board or SKU must match them too, and the most
// specific matching entry wins. Two equally specific ones (genkb refuses
// them) give nil: which one applies can't be told.
func modelFor(k *kb.KB, r *report.Report) *kb.Model {
	id := r.System.Identity
	if id == nil {
		return nil
	}
	board := ""
	if r.Board.Identity != nil {
		board = r.Board.Identity.Model
	}
	var best *kb.Model
	bestScore, tie := -1, false
	for i := range k.Models {
		m := &k.Models[i]
		if m.Match.SysVendor != id.Vendor || m.Match.ProductName != id.Model ||
			m.Match.BoardName != "" && m.Match.BoardName != board || m.Match.SKU != "" && m.Match.SKU != id.PartNumber {
			continue
		}
		score := 0
		if m.Match.BoardName != "" {
			score++
		}
		if m.Match.SKU != "" {
			score++
		}
		switch {
		case score > bestScore:
			best, bestScore, tie = m, score, false
		case score == bestScore:
			tie = true
		}
	}
	if tie {
		return nil
	}
	return best
}

// pciDeviceFor returns the device entry for a PCI device, preferring one
// that names its subsystem over one for every board using the chip.
func pciDeviceFor(k *kb.KB, d *report.PCIDevice) *kb.Device {
	chip := strings.ToLower(d.VendorID + ":" + d.DeviceID)
	full := chip + ":" + strings.ToLower(d.SubVendorID+":"+d.SubDeviceID)
	var generic *kb.Device
	for i := range k.Devices {
		e := &k.Devices[i]
		switch {
		case e.Match.Bus != "pci":
		case e.Match.ID == full:
			return e
		case e.Match.ID == chip:
			generic = e
		}
	}
	return generic
}

// cpuFor returns the CPU entry for the capture's processor, matched by
// the processor number in its brand string.
func cpuFor(k *kb.KB, r *report.Report) *kb.CPU {
	id := r.CPU.Identity
	if id == nil || id.Vendor != "GenuineIntel" {
		return nil
	}
	n := kb.ProcessorNumber(id.Model)
	if n == "" {
		return nil
	}
	for i := range k.CPUs {
		if c := &k.CPUs[i]; c.Match.Vendor == "intel" && c.Match.Processor == n {
			return c
		}
	}
	return nil
}

// allowlistsFor returns the vendor firmware policies for the capture's
// model (its vendor, and one of the listed products, its family or its
// board): those whose BIOS range covers its version, and, apart, those
// whose range can't be judged (no version, or one the vendor's comparator
// can't read or compare). A check says "can't tell" for the latter,
// never "no policy".
func allowlistsFor(k *kb.KB, r *report.Report) (applies, undetermined []*kb.Allowlist) {
	id := r.System.Identity
	if id == nil {
		return nil, nil
	}
	board, bios := "", ""
	if r.Board.Identity != nil {
		board = r.Board.Identity.Model
	}
	if r.System.Firmware != nil {
		bios = r.System.Firmware.Version
	}
	for i := range k.Allowlists {
		a := &k.Allowlists[i]
		m := a.Match
		if m.SysVendor != id.Vendor {
			continue
		}
		if !contains(m.ProductName, id.Model) && (m.Family == "" || m.Family != r.System.Family) && !contains(m.BoardName, board) {
			continue
		}
		if m.BIOSVersion != nil {
			in, err := m.BIOSVersion.Contains(m.SysVendor, bios)
			if err != nil {
				undetermined = append(undetermined, a)
			}
			if err != nil || !in {
				continue
			}
		}
		applies = append(applies, a)
	}
	return applies, undetermined
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s && s != "" {
			return true
		}
	}
	return false
}
