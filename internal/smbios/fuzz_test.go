package smbios

import (
	"testing"

	"github.com/jiegui2025/hwspec/internal/smbios/smbiostest"
)

// The SMBIOS table comes from firmware, so parsing it and every decoder
// built on it must handle anything without panicking.
func FuzzParse(f *testing.F) {
	f.Add(smbiostest.EliteDesk800G5Mini())
	f.Add(append(smbiostest.MemoryArray(0x1000, 32<<20, 2, 3), smbiostest.End()...))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, table []byte) {
		structs := Parse(table)
		_ = MemoryArrays(structs)
		_ = MemoryDevices(structs)
		_ = Processors(structs)
		_ = Slots(structs)
		_ = OnboardDevices(structs)
	})
}
