package collect

import (
	"slices"
	"strings"
	"testing"

	"github.com/jiegui2025/hwspec/internal/report"
)

// coffeeLake is the reference machine's processor rule, as evidence.
const coffeeLake = "the processor (family 6, model 158, stepping 10: Coffee Lake) has its graphics at PCI bus 0, device 2, function 0 (Intel 337345-002 (8th and 9th Generation Core, formerly Coffee Lake), datasheet vol. 2, §2.2)"

// An Intel processor's graphics is part of the CPU, with the package when
// the firmware gives it and the firmware's own records kept as evidence
// (#137); a discrete GPU, another vendor's at the same address, or
// another class there keep what the records say.
func TestProcessorGraphics(t *testing.T) {
	onboard := &report.Mounting{Kind: report.MountedOnboard, Confidence: "high", Evidence: []string{`the kernel's label "Onboard IGD" comes from SMBIOS type 41`}}
	socket := []report.CPUPackage{{Designation: "U3E2", Package: "Socket LGA1150"}, {Designation: "U3E1", Package: "Socket LGA1151", Populated: true}}
	for _, c := range []struct {
		name     string
		dev      report.PCIDevice
		packages []report.CPUPackage
		want     *report.Mounting
	}{
		{"the reference GPU, socketed", report.PCIDevice{Address: "0000:00:02.0", VendorID: "8086", ClassCode: "030000", Mounting: onboard}, socket,
			&report.Mounting{Kind: report.MountedInCPU, Confidence: "high", Package: "Socket LGA1151", Evidence: []string{coffeeLake,
				"the processor's package is Socket LGA1151 (SMBIOS type 4)", `the kernel's label "Onboard IGD" comes from SMBIOS type 41`}}},
		{"no packages (no root)", report.PCIDevice{Address: "0000:00:02.0", VendorID: "8086", ClassCode: "038000"}, nil,
			&report.Mounting{Kind: report.MountedInCPU, Confidence: "high", Evidence: []string{coffeeLake}}},
		{"a package without a name", report.PCIDevice{Address: "0000:00:02.0", VendorID: "8086", ClassCode: "030000"}, []report.CPUPackage{{Populated: true}},
			&report.Mounting{Kind: report.MountedInCPU, Confidence: "high", Evidence: []string{coffeeLake}}},
		{"two populated sockets: the first", report.PCIDevice{Address: "0000:00:02.0", VendorID: "8086", ClassCode: "030000"},
			[]report.CPUPackage{{Package: "Socket LGA1151", Populated: true}, {Package: "Socket LGA1200", Populated: true}},
			&report.Mounting{Kind: report.MountedInCPU, Confidence: "high", Package: "Socket LGA1151", Evidence: []string{coffeeLake,
				"the processor's package is Socket LGA1151 (SMBIOS type 4)"}}},
		{"an Intel GPU behind a port", report.PCIDevice{Address: "0000:03:00.0", VendorID: "8086", ClassCode: "030000", Mounting: onboard}, socket, onboard},
		{"another vendor at 00:02.0", report.PCIDevice{Address: "0000:00:02.0", VendorID: "1002", ClassCode: "030000", Mounting: onboard}, socket, onboard},
		{"another class at 00:02.0", report.PCIDevice{Address: "0000:00:02.0", VendorID: "8086", ClassCode: "060400"}, socket, nil},
		{"function 1", report.PCIDevice{Address: "0000:00:02.1", VendorID: "8086", ClassCode: "030000", Mounting: onboard}, socket, onboard},
	} {
		col := &collector{r: &report.Report{PCI: []report.PCIDevice{c.dev}, CPU: report.CPU{Packages: c.packages,
			Identity: &report.Identity{Vendor: "GenuineIntel"}, Family: 6, ModelID: 0x9e, Stepping: 10}}}
		col.processorGraphics()
		got := col.r.PCI[0].Mounting
		if (got == nil) != (c.want == nil) || got != nil && (got.Kind != c.want.Kind || got.Package != c.want.Package ||
			got.Confidence != c.want.Confidence || !slices.Equal(got.Evidence, c.want.Evidence)) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

// Only a generation whose datasheet is cited (#269 round 1): a Core 2
// with G41 chipset graphics at 00:02.0, an unknown or uncited generation
// or stepping, another family or vendor, and no CPU identity keep what
// the records say; Coffee Lake's steppings and Ice Lake are the
// processor's.
func TestProcessorGraphicsGenerations(t *testing.T) {
	onboard := &report.Mounting{Kind: report.MountedOnboard, Confidence: "high", Evidence: []string{"x"}}
	for _, c := range []struct {
		name, vendor            string
		family, model, stepping int
		inCPU                   bool
		inName                  string
	}{
		{"Core 2 with G41 graphics (8086:2e22)", "GenuineIntel", 6, 0x17, 10, false, ""},
		{"no signature", "GenuineIntel", 0, 0, 0, false, ""},
		{"Kaby Lake desktop (stepping 9, not cited)", "GenuineIntel", 6, 0x9e, 9, false, ""},
		{"Comet Lake (no datasheet cited)", "GenuineIntel", 6, 0xa5, 3, false, ""},
		{"another family", "GenuineIntel", 15, 0x9e, 10, false, ""},
		{"another vendor's CPU", "AuthenticAMD", 6, 0x9e, 10, false, ""},
		{"no CPU identity", "", 6, 0x9e, 10, false, ""},
		{"Coffee Lake, stepping 13", "GenuineIntel", 6, 0x9e, 13, true, "stepping 13: Coffee Lake"},
		{"Coffee Lake, stepping 14 (not listed)", "GenuineIntel", 6, 0x9e, 14, false, ""},
		{"Ice Lake", "GenuineIntel", 6, 0x7e, 5, true, "model 126, stepping 5: Ice Lake"},
	} {
		cpu := report.CPU{Family: c.family, ModelID: c.model, Stepping: c.stepping}
		if c.vendor != "" {
			cpu.Identity = &report.Identity{Vendor: c.vendor}
		}
		col := &collector{r: &report.Report{CPU: cpu, PCI: []report.PCIDevice{{Address: "0000:00:02.0", VendorID: "8086", DeviceID: "2e22", ClassCode: "030000", Mounting: onboard}}}}
		col.processorGraphics()
		got := col.r.PCI[0].Mounting
		if c.inCPU != (got.Kind == report.MountedInCPU) || !c.inCPU && got != onboard {
			t.Errorf("%s: %+v", c.name, got)
		}
		if c.inCPU && !strings.Contains(got.Evidence[0], c.inName) {
			t.Errorf("%s: evidence %q", c.name, got.Evidence)
		}
	}
}
