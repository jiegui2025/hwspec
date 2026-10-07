package collect

import (
	"fmt"
	"strings"

	"github.com/jiegui2025/hwspec/internal/report"
)

// processorGraphicsSources are the processor generations, by x86 family,
// model and stepping (as tools/genids/cpu-curated.ids and the kernel's
// intel-family.h name them), whose Intel datasheet places the processor's
// graphics at PCI bus 0, device 2, function 0. Before Clarkdale and Sandy
// Bridge that address was the chipset's graphics, soldered on the board,
// so a generation is listed only with its datasheet cited.
var processorGraphicsSources = []struct {
	model, minStepping, maxStepping int
	name, source                    string
}{
	{0x9e, 10, 13, "Coffee Lake", "Intel 337345-002 (8th and 9th Generation Core, formerly Coffee Lake), datasheet vol. 2, §2.2"},
	{0x7e, 0, 255, "Ice Lake", "Intel 341078-004 (10th Generation, formerly Ice Lake), datasheet vol. 2, §2.2"},
}

// processorGraphics marks an Intel processor's own graphics as part of
// the CPU (#137): the firmware lists it onboard ("Onboard IGD"), but it
// changes with the processor, socketed or not. It applies to a
// display-class Intel device at 00:02.0 when the CPU is of a generation
// whose datasheet says that's the processor's graphics; a discrete GPU,
// other vendors' integrated graphics, and other or unknown generations
// keep what the records say.
func (c *collector) processorGraphics() {
	source := ""
	if cpu := c.r.CPU; cpu.Identity != nil && cpu.Identity.Vendor == "GenuineIntel" && cpu.Family == 6 {
		for _, g := range processorGraphicsSources {
			if cpu.ModelID == g.model && cpu.Stepping >= g.minStepping && cpu.Stepping <= g.maxStepping {
				source = fmt.Sprintf("the processor (family 6, model %d, stepping %d: %s) has its graphics at PCI bus 0, device 2, function 0 (%s)",
					cpu.ModelID, cpu.Stepping, g.name, g.source)
				break
			}
		}
	}
	if source == "" {
		return
	}
	for i := range c.r.PCI {
		d := &c.r.PCI[i]
		if !strings.EqualFold(d.VendorID, "8086") || !strings.HasPrefix(d.ClassCode, "03") || !strings.HasSuffix(d.Address, ":00:02.0") {
			continue
		}
		m := &report.Mounting{Kind: report.MountedInCPU, Confidence: "high", Evidence: []string{source}}
		for _, p := range c.r.CPU.Packages {
			if p.Populated && p.Package != "" {
				m.Package = p.Package
				m.Evidence = append(m.Evidence, "the processor's package is "+p.Package+" (SMBIOS type 4)")
				break
			}
		}
		if d.Mounting != nil {
			m.Evidence = append(m.Evidence, d.Mounting.Evidence...)
		}
		d.Mounting = m
	}
}
