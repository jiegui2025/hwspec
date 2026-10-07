package ids

import "testing"

// ID files come from distributions, users and the synced bundle, so every
// database's parser must handle anything without panicking, and the
// manifest parser too.
func FuzzValidate(f *testing.F) {
	for _, seed := range []string{
		"8086  Intel Corporation\n\t3e92  CoffeeLake-S GT2\n\t\t103c 8595  EliteDesk\nC 03  Display controller\n",
		"046d  Logitech, Inc.\n\tc52b  Unifying Receiver\n",
		"DEL\tDell Inc.\n",
		"00-1B-DC   (hex)\t\tVencer\n001BDC     (base 16)\t\tVencer Co., Ltd.\n",
		"1:CE\tSamsung\n",
		"1002:73BF:C1,\tAMD Radeon RX 6800 XT\n",
		"0x0002\tIntel Corp.\n",
		"intel:6:9e\tCoffee Lake\tSkylake\n",
		"",
	} {
		for k := range specs {
			f.Add(string(k), []byte(seed))
		}
	}
	f.Fuzz(func(t *testing.T, kind string, content []byte) {
		k := Kind(kind)
		if _, ok := specs[k]; !ok {
			return
		}
		_, _ = Validate(k, content)
	})
}

func FuzzParseManifest(f *testing.F) {
	f.Add([]byte(`{"format":1,"generated_at":"2026-10-06T00:00:00Z","files":{"pci.ids.gz":{"sha256":"00","size":1,"date":"2026-10-01","entries":1}}}`))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = ParseManifest(b)
	})
}
