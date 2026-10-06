package report

import "testing"

// A firmware block is known when it holds a version; an unknown one, or
// one with nothing in it, isn't.
func TestFirmwareKnown(t *testing.T) {
	var none *Firmware
	for fw, want := range map[*Firmware]bool{
		none:                      false,
		{}:                        false,
		{Version: "1.0"}:          true,
		UnknownFirmware("why"):    false,
		{Status: FirmwareUnknown}: false,
	} {
		if got := fw.Known(); got != want {
			t.Errorf("%+v: Known() = %v", fw, got)
		}
	}
	if fw := UnknownFirmware("needs --full"); fw.Status != "unknown" || fw.Reason != "needs --full" || fw.Version != "" || fw.Source != "" {
		t.Errorf("UnknownFirmware = %+v", fw)
	}
}
