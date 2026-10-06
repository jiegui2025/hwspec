package collect

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
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

// socketCalls are the syscalls that move packets or accept them; a socket
// that is only opened (ethtool's AF_INET ioctl socket) and written to (the
// Bluetooth management socket, a kernel interface) sends nothing.
var socketCalls = map[string]bool{
	"Connect": true, "Sendto": true, "Sendmsg": true, "SendmsgN": true, "Sendmmsg": true,
	"Listen": true, "Accept": true, "Accept4": true,
}

// socketFamilies are the address families collect may open: kernel
// interfaces, and AF_INET only for ethtool's ioctls.
var socketFamilies = map[string]bool{"AF_BLUETOOTH": true, "AF_NETLINK": true, "AF_UNIX": true, "AF_INET": true}

// sendsPackets lists the ways f could put packets on a network through
// syscall or x/sys/unix, which depguard can't tell apart from the kernel
// interfaces collect reads.
func sendsPackets(fset *token.FileSet, f *ast.File) []string {
	pkgs := map[string]bool{}
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		if path != "syscall" && path != "golang.org/x/sys/unix" {
			continue
		}
		local := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			local = imp.Name.Name
		}
		pkgs[local] = true
	}
	var out []string
	sel := func(e ast.Expr) (string, bool) {
		s, ok := e.(*ast.SelectorExpr)
		if !ok {
			return "", false
		}
		x, ok := s.X.(*ast.Ident)
		return s.Sel.Name, ok && pkgs[x.Name]
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			name, ok := sel(n.Fun)
			switch {
			case !ok:
			case socketCalls[name]:
				out = append(out, fmt.Sprintf("%s: calls %s", fset.Position(n.Pos()), name))
			case name == "Socket" && len(n.Args) > 0:
				if family, ok := sel(n.Args[0]); !ok || !socketFamilies[family] {
					out = append(out, fmt.Sprintf("%s: opens a socket of a family collect doesn't use", fset.Position(n.Pos())))
				}
			}
		case *ast.CompositeLit:
			if name, ok := sel(n.Type); ok && (strings.HasPrefix(name, "SockaddrInet") || name == "SockaddrLinklayer") {
				out = append(out, fmt.Sprintf("%s: builds a network address (%s)", fset.Position(n.Pos()), name))
			}
		}
		return true
	})
	if pkgs["."] {
		out = append(out, "dot-imports syscall or x/sys/unix, which hides what it calls")
	}
	return out
}

// collect opens sockets only to talk to the kernel (Bluetooth management,
// ethtool ioctls): nothing in it connects, sends, listens or builds a
// network address. With depguard (no net import) and the ids test, no
// capture can reach the network.
func TestCollectSendsNoPackets(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, p := range sendsPackets(fset, f) {
			t.Error(p)
		}
	}
	if checked == 0 {
		t.Fatal("no source files checked")
	}
}

func TestSendsPacketsFindsEachWay(t *testing.T) {
	for src, want := range map[string]string{
		`package x; import "golang.org/x/sys/unix"; func f(fd int) { unix.Connect(fd, &unix.SockaddrInet4{Port: 80}) }`:         "calls Connect",
		`package x; import "syscall"; func f(fd int) { syscall.Sendto(fd, nil, 0, nil) }`:                                       "calls Sendto",
		`package x; import u "golang.org/x/sys/unix"; func f() { u.Socket(u.AF_PACKET, u.SOCK_RAW, 0) }`:                        "family collect doesn't use",
		`package x; import "golang.org/x/sys/unix"; func f(d int) { unix.Socket(d, 0, 0) }`:                                     "family collect doesn't use",
		`package x; import "golang.org/x/sys/unix"; var _ = &unix.SockaddrInet6{}`:                                              "builds a network address",
		`package x; import "golang.org/x/sys/unix"; var _ = unix.SockaddrLinklayer{}`:                                           "builds a network address",
		`package x; import . "golang.org/x/sys/unix"; func f() { Socket(AF_INET, SOCK_STREAM, 0) }`:                             "dot-imports",
		`package x; import "golang.org/x/sys/unix"; func f() { unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0) }`:                 "",
		`package x; import "golang.org/x/sys/unix"; func f(fd int) { unix.Bind(fd, &unix.SockaddrHCI{}); unix.Write(fd, nil) }`: "",
	} {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "x.go", src, 0)
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Join(sendsPackets(fset, f), "; ")
		if want == "" && got != "" || want != "" && !strings.Contains(got, want) {
			t.Errorf("%s:\n  found %q, want %q", src, got, want)
		}
	}
}
