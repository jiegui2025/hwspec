package collect

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// A recording that can't be read is an error, not an empty capture.
func TestCollectRecordedRefusesBrokenRecordings(t *testing.T) {
	dir := t.TempDir()
	if _, err := CollectRecorded(dir, "test"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("no machine.json: %v", err)
	}
	if err := os.WriteFile(dir+"/machine.json", []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CollectRecorded(dir, "test"); err == nil || !strings.Contains(err.Error(), "machine.json") {
		t.Errorf("bad machine.json: %v", err)
	}
	if err := os.WriteFile(dir+"/machine.json", []byte(`{"arch":"x86_64"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CollectRecorded(dir, "test"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("no root/: %v", err)
	}
}

// A recording whose Bluetooth answer can't be replayed faithfully is
// refused rather than guessed at.
func TestCollectRecordedRefusesUnknownBluetoothAnswers(t *testing.T) {
	for name, js := range map[string]string{
		"unknown version":   `{"arch":"x86_64","bluetooth":{"hci0":{"address":"00:1b:dc:00:00:01","version":"9.9","manufacturer_id":2}}}`,
		"manufacturer >16b": `{"arch":"x86_64","bluetooth":{"hci0":{"address":"00:1b:dc:00:00:01","version":"5.2","manufacturer_id":70000}}}`,
		"bad controller":    `{"arch":"x86_64","bluetooth":{"bt0":{"address":"00:1b:dc:00:00:01","version":"5.2"}}}`,
		"controller suffix": `{"arch":"x86_64","bluetooth":{"hci0x":{"address":"00:1b:dc:00:00:01","version":"5.2"}}}`,
		"misspelt field":    `{"arch":"x86_64","ethtol":{}}`,
		"no arch":           `{}`,
		"relative path":     `{"arch":"x86_64","empty_dirs":["sys/x"]}`,
		"bad errno":         `{"arch":"x86_64","unreadable":{"/sys/x":0}}`,
		"revision >16b":     `{"arch":"x86_64","bluetooth":{"hci0":{"address":"00:1b:dc:00:00:01","version":"5.2","firmware":{"hci_revision":70000,"lmp_subversion":1}}}}`,
	} {
		dir := t.TempDir()
		write(t, dir+"/machine.json", js)
		mkdir(t, dir+"/root")
		if _, err := CollectRecorded(dir, "test"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A controller's recorded HCI answer replays as its firmware; one
// recorded without it gets the reason and a warning.
func TestCollectRecordedReplaysBluetoothFirmware(t *testing.T) {
	dir := t.TempDir()
	write(t, dir+"/machine.json", `{"arch":"x86_64","bluetooth":{
		"hci0":{"address":"00:1b:dc:00:00:01","version":"5.2","firmware":{"hci_revision":4660,"lmp_subversion":22136}},
		"hci1":{"address":"00:1b:dc:00:00:02","version":"5.2"}}}`)
	mkdir(t, dir+"/root/sys/class/bluetooth/hci0")
	mkdir(t, dir+"/root/sys/class/bluetooth/hci1")
	r, err := CollectRecorded(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Bluetooth) != 2 {
		t.Fatalf("controllers %+v", r.Bluetooth)
	}
	if fw := r.Bluetooth[0].Firmware; !fw.Known() || fw.Version != "0x5678" || fw.Release != "0x1234" || fw.Source != "hci" {
		t.Errorf("hci0 firmware %+v", fw)
	}
	if fw := r.Bluetooth[1].Firmware; fw.Known() || !strings.Contains(strings.Join(r.Warnings, "\n"), "bluetooth hci1: firmware version: not recorded") {
		t.Errorf("hci1 firmware %+v, warnings %q", fw, r.Warnings)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}
