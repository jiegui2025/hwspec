package collect

import (
	"errors"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// The architecture rules a capture must keep (ARCHITECTURE.md "Rules"),
// checked by behaviour. depguard (.golangci.yml) checks the imports.

// noNetwork replaces the transport every http.Client without its own uses
// (internal/ids' downloads among them) with one that records the request
// and fails it.
func noNetwork(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var asked []string
	old := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, r.URL.String())
		return nil, errors.New("a capture must not touch the network")
	})
	t.Cleanup(func() { http.DefaultTransport = old })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), asked...)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// "Captures never touch the network": a whole capture of every recorded
// machine makes no HTTP request.
func TestCapturesNeverTouchTheNetwork(t *testing.T) {
	asked := noNetwork(t)
	dirs, _ := filepath.Glob("testdata/machines/*")
	if len(dirs) == 0 {
		t.Fatal("no recorded machines")
	}
	for _, dir := range dirs {
		if _, err := CollectRecorded(dir, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if got := asked(); len(got) > 0 {
		t.Errorf("a capture made HTTP requests: %v", got)
	}
}

// The check above must notice a request (it would pass vacuously if the
// transport weren't the one requests go through).
func TestTheNetworkCheckSeesRequests(t *testing.T) {
	asked := noNetwork(t)
	if resp, err := http.Get("http://127.0.0.1:9/"); err == nil { //nolint:noctx // a test request that must fail
		resp.Body.Close()
		t.Error("the request wasn't stopped")
	}
	if got := asked(); len(got) != 1 {
		t.Errorf("recorded %v, want the one request", got)
	}
}

// "No external tools in the capture path, except smartctl": a root
// capture of a machine with SATA and USB drives runs nothing else.
func TestCapturesRunNoProgramButSmartctl(t *testing.T) {
	file, link := fakeRoot(t)
	asMachine(t, "x86_64", 0)
	for name, devicePath := range map[string]string{
		"sda": "pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0",
		"sdb": "pci0000:00/0000:00:14.0/usb2/2-1/2-1:1.0/host1/target1:0:0/1:0:0:0",
	} {
		dev := "/sys/devices/" + devicePath + "/block/" + name
		link("/sys/block/"+name, "../devices/"+devicePath+"/block/"+name)
		file(dev+"/size", "2000000")
		file(dev+"/queue/rotational", "0")
		link(dev+"/device", "../..")
		file("/sys/devices/"+devicePath+"/model", "Disk "+name)
	}
	const smartctl = "/usr/sbin/smartctl"
	findSmartctl = func() string { return smartctl }
	var mu sync.Mutex
	var ran []string
	runCommand = func(_ time.Duration, name string, _ ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		ran = append(ran, name)
		return []byte(`{"smart_status":{"passed":true}}`), nil
	}
	r := Collect("test")
	if len(r.Storage) != 2 {
		t.Fatalf("storage = %+v, want sda and sdb", r.Storage)
	}
	if len(ran) == 0 {
		t.Fatal("smartctl never ran: the check would pass vacuously")
	}
	for _, name := range ran {
		if name != smartctl {
			t.Errorf("a capture ran %q", name)
		}
	}
}
