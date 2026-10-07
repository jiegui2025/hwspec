package advisor

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jiegui2025/hwspec/internal/kb"
	"github.com/jiegui2025/hwspec/internal/report"
)

// Firmware LVFS doesn't carry (#227, part 6 of #10): the latest a vendor
// lists on its own page, from the model's knowledge-base entry, always
// with the date it was checked: hwspec can't see what came after.

// vendorFirmwareData is a model's vendor_firmware group, read strictly.
type vendorFirmwareData struct {
	// SystemBIOS is the system firmware: its family (HP's "R21") and the
	// latest release the vendor's page lists.
	SystemBIOS *struct {
		Family claims[string]        `json:"family"`
		Latest claims[vendorRelease] `json:"latest"`
	} `json:"system_bios"`
}

type vendorRelease struct {
	Version string `json:"version"`
	Date    string `json:"date"` // YYYY-MM-DD, as the vendor's page gives it
}

var (
	biosFamilyRe  = regexp.MustCompile(`^[A-Z][0-9]{2}$`)
	biosVersionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
)

func decodeVendorFirmware(raw json.RawMessage) (*vendorFirmwareData, error) {
	d, err := decodeData[vendorFirmwareData](raw)
	if err != nil {
		return nil, err
	}
	b := d.SystemBIOS
	if b == nil || len(b.Family) == 0 || len(b.Latest) == 0 {
		return nil, errors.New("system_bios needs a family and the latest release")
	}
	var errs []error
	for _, c := range b.Family {
		if !biosFamilyRe.MatchString(c.Value) {
			errs = append(errs, fmt.Errorf("system_bios.family: %q isn't a family such as R21", c.Value))
		}
	}
	for _, c := range b.Latest {
		if !biosVersionRe.MatchString(c.Value.Version) {
			errs = append(errs, fmt.Errorf("system_bios.latest: version %q isn't three numbers such as 02.27.00", c.Value.Version))
		}
		if _, err := time.Parse(time.DateOnly, c.Value.Date); err != nil {
			errs = append(errs, fmt.Errorf("system_bios.latest: version %s needs its release date as YYYY-MM-DD (got %q)", c.Value.Version, c.Value.Date))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return &d, nil
}

func init() {
	register("vendor-firmware", check{
		run:   vendorFirmware,
		needs: []string{"system.firmware.version"},
		available: func(in *Input) bool {
			return in.Report.System.Firmware.Known()
		},
		example: func() (kb.Rule, *report.Report) {
			return kb.Rule{ID: "firmware.vendor-only", Check: "vendor-firmware", Category: "firmware", Severity: "info",
					Title: "Firmware only on the vendor's site", Src: []string{"capture"}},
				&report.Report{System: report.System{Identity: &report.Identity{Vendor: "HP", Model: "HP EliteDesk 800 G5 Desktop Mini"},
					Firmware: &report.Firmware{Vendor: "HP", Version: "R21 Ver. 02.26.00", Source: "dmi"}}}
		},
		exampleData: func() ([]kb.Source, []kb.Model) {
			return []kb.Source{{ID: "example-notes", URL: "https://example.com/sp1.html", Retrieved: "2026-10-07",
					Licence: "proprietary", Confidence: "oem-doc", LinkOnly: true, Locator: "VERSION, EFFECTIVE DATE"}},
				[]kb.Model{{ID: "hp.example", Match: kb.ModelMatch{SysVendor: "HP", ProductName: "HP EliteDesk 800 G5 Desktop Mini"},
					Data: map[string]json.RawMessage{"vendor_firmware": json.RawMessage(`{"system_bios": {
						"family": [{"value": "R21", "src": "example-notes"}],
						"latest": [{"value": {"version": "02.27.00", "date": "2026-08-11"}, "src": "example-notes"}]}}`)}}}
		},
	})
}

// vendorFirmware compares the BIOS with the latest release the model's
// entry cites: "check manually", with the page and when it was read. It
// stands down when LVFS carries the machine's system firmware (an ESRT
// entry LVFS has a component for): the LVFS finding speaks for it then.
func vendorFirmware(in *Input, _ *kb.Rule) ([]hit, error) {
	m := modelFor(in.KB, in.Report)
	if m == nil {
		return nil, nil
	}
	raw, ok := m.Data["vendor_firmware"]
	if !ok || lvfsCarriesSystemFirmware(in) {
		return nil, nil
	}
	d, err := decodeVendorFirmware(raw)
	if err != nil {
		return nil, fmt.Errorf("this build can't read the knowledge base's vendor firmware for this model (update hwspec): %w", err)
	}
	r := in.Report
	installed := r.System.Firmware.Version
	vendor := ""
	if r.System.Identity != nil {
		vendor = r.System.Identity.Vendor
	}
	// The entry's family must be this BIOS's (HP's "R21" in "R21 Ver.
	// 02.27.00"): another family's page is no advice for this one. A
	// version of a form hwspec can't read is compared, which says so.
	family, familySrc := d.SystemBIOS.Family.first()
	if f, _, _ := strings.Cut(installed, " "); biosFamilyRe.MatchString(f) {
		i := slices.IndexFunc(d.SystemBIOS.Family, func(c claim[string]) bool { return c.Value == f })
		if i < 0 {
			return nil, nil
		}
		family, familySrc = f, d.SystemBIOS.Family[i].Src
	}
	h := hit{device: &DeviceRef{Kind: "system", Key: "firmware", Name: family + " BIOS"},
		evidence: []Evidence{Present("system.firmware.version", installed)}, used: []string{familySrc}}
	for _, c := range d.SystemBIOS.Latest {
		checked := ""
		if src := in.KB.Source(c.Src); src != nil {
			checked = src.Retrieved
		}
		h.used = append(h.used, c.Src)
		var part string
		switch order, err := kb.CompareBIOS(vendor, installed, family+" Ver. "+c.Value.Version); {
		case err != nil:
			part = fmt.Sprintf("this machine runs %s, which hwspec can't compare with it (%v)", installed, err)
		case order < 0:
			part = fmt.Sprintf("this machine runs %s, older: the page has the update", installed)
		case order == 0:
			part = fmt.Sprintf("this machine runs %s, the same", installed)
		default:
			part = fmt.Sprintf("this machine runs %s, newer than that", installed)
		}
		h.answers = append(h.answers, Answer{Topic: "vendor_firmware", Known: true, Claims: []AnswerClaim{{Value: c.Value.Version, Src: c.Src, Published: c.Value.Date}},
			Text: fmt.Sprintf("%s lists %s %s (%s) as the latest, as of %s; %s. When the page was read the vendor published it only on its own site, not on LVFS, so releases since aren't known here: check the page",
				c.Src, family, c.Value.Version, c.Value.Date, checked, part)})
	}
	return []hit{h}, nil
}

// lvfsCarriesSystemFirmware says whether LVFS's catalogue has a component
// for one of the machine's system-firmware ESRT entries.
func lvfsCarriesSystemFirmware(in *Input) bool {
	if in.LVFS == nil || in.Report.System.ESRT == nil {
		return false
	}
	return slices.ContainsFunc(in.Report.System.ESRT.Entries, func(e report.ESRTEntry) bool {
		return e.FWType == "system" && len(in.LVFS.Components[strings.ToLower(e.FWClass)]) > 0
	})
}

// validateVendorFirmware is decodeVendorFirmware for genkb, which also
// knows the sources: a release can't be dated after its page was read.
func validateVendorFirmware(raw json.RawMessage, source func(string) *kb.Source) error {
	d, err := decodeVendorFirmware(raw)
	if err != nil || source == nil {
		return err
	}
	var errs []error
	for _, c := range d.SystemBIOS.Latest {
		if s := source(c.Src); s != nil && c.Value.Date > s.Retrieved {
			errs = append(errs, fmt.Errorf("system_bios.latest: version %s is dated %s, after its source %s was read (%s)", c.Value.Version, c.Value.Date, c.Src, s.Retrieved))
		}
	}
	return errors.Join(errs...)
}
