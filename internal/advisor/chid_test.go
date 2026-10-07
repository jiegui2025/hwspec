package advisor

import (
	"maps"
	"slices"
	"testing"

	"github.com/jiegui2025/hwspec/internal/report"
)

// referenceIdentity is the reference machine's SMBIOS identity, as its
// capture holds it.
func referenceIdentity() *report.Report {
	return &report.Report{
		System: report.System{
			Identity:    &report.Identity{Vendor: "HP", Model: "HP EliteDesk 800 G5 Desktop Mini", PartNumber: "7LL88UT#ABA"},
			Family:      "103C_53307F HP EliteDesk",
			ChassisType: "Mini Tower",
			Firmware:    &report.Firmware{Vendor: "HP", Version: "R21 Ver. 02.27.00", Release: "27.0"},
		},
		Board: report.Board{Identity: &report.Identity{Vendor: "HP", Model: "8595"}},
	}
}

// #237's acceptance: the reference machine's 15 CHIDs equal fwupd's
// (`fwupdmgr hwids`, fwupd 2.1.8, 2026-10-07).
func TestReferenceMachineCHIDs(t *testing.T) {
	want := []string{
		"7c86573a-7a8b-5ea9-aa8c-3a89b3af2839", "b9152c66-5743-50fc-8b99-badff6bf4407", "ddeaa439-1703-5f99-aeb3-05ddd011e7d1",
		"18285c29-9034-565b-86c1-7f891159ea9a", "94d996ce-4c75-5095-ac40-8ddb05acc8e2", "62b727b9-d42c-5a93-a882-e596d83c0658",
		"cc5b85af-d653-53da-bc11-7bb08ed48001", "3dd83b34-531f-5439-b70d-8700ae6d4c83", "3d5b8825-7220-5b98-aed6-1932cd992432",
		"cce6dc5f-b2c4-52c7-8150-2ec6d5bb44d9", "6afe5781-1390-5764-9cce-ac3a5e9a3a00", "8088aaaf-32f5-5946-a74e-087f8875184a",
		"d6b934f5-cfb3-515e-9f3f-59b9f9c3dc11", "33d09d20-0420-5109-bf31-298ce02c0ee0", "93f84748-c854-5d6b-b78a-13c2361e0758",
	}
	ids, missing := machineCHIDs(referenceIdentity())
	if got := slices.Sorted(maps.Keys(ids)); !slices.Equal(got, slices.Sorted(slices.Values(want))) || missing != nil {
		t.Errorf("CHIDs %q, missing %q", got, missing)
	}
}

// A field the capture lacks leaves out every CHID that needs it, and is
// named; values are trimmed; an unreadable BIOS release or an unknown
// chassis type counts as missing.
func TestCHIDsWithMissingFields(t *testing.T) {
	r := referenceIdentity()
	r.System.Identity.PartNumber = " "
	r.System.Firmware.Release = "unknown"
	r.System.ChassisType = "Spaceship"
	r.Board.Identity = nil
	ids, missing := machineCHIDs(r)
	// Left: Manufacturer+Family+ProductName, Manufacturer+ProductName,
	// Manufacturer+Family, Manufacturer.
	if len(ids) != 4 || !ids["62b727b9-d42c-5a93-a882-e596d83c0658"] || !ids["93f84748-c854-5d6b-b78a-13c2361e0758"] {
		t.Errorf("CHIDs %v", ids)
	}
	if want := []string{"ProductSku", "BiosMajorRelease", "BaseboardManufacturer", "EnclosureKind"}; !slices.Equal(missing, want) {
		t.Errorf("missing %q, want %q", missing, want)
	}
	r = referenceIdentity()
	r.System.Identity.Vendor = "  HP  "
	if ids, _ := machineCHIDs(r); !ids["93f84748-c854-5d6b-b78a-13c2361e0758"] {
		t.Error("an untrimmed vendor changed the CHID")
	}
	for _, release := range []string{"256.0", "1.-1", "27"} {
		r = referenceIdentity()
		r.System.Firmware.Release = release
		if _, missing := machineCHIDs(r); !slices.Contains(missing, "BiosMajorRelease") {
			t.Errorf("release %q: missing %q", release, missing)
		}
	}
	if ids, missing := machineCHIDs(&report.Report{}); len(ids) != 0 || len(missing) == 0 {
		t.Errorf("an empty capture: %v, %q", ids, missing)
	}
}
