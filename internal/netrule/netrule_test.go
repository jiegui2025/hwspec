package netrule

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var clientOnly = map[string]bool{"Client": true, "MethodGet": true}

// The check finds each escape (it would pass vacuously otherwise). An
// unkeyed http.Client{rt} is left to go vet's composites check.
func TestEscapesAreFound(t *testing.T) {
	for src, want := range map[string]string{
		`package x; import "net"; func f() { net.Dial("tcp", "x:1") }`:                       "imports net,",
		`package x; import "net/http"; var c = &http.Client{Transport: &http.Transport{}}`:   "http.Transport",
		`package x; import "net/http"; var c = &http.Client{Transport: nil}`:                 "its own Transport",
		`package x; import "net/http"; func f(c *http.Client) { c.Transport = nil }`:         "sets a client's Transport",
		`package x; import h "net/http"; var t h.RoundTripper`:                               "http.RoundTripper",
		`package x; import "net/http"; var _ = http.DefaultTransport`:                        "http.DefaultTransport",
		`package x; import "net/http/httptrace"; var _ httptrace.ClientTrace`:                "imports net/http/httptrace",
		`package x; import "syscall"; func f() { syscall.Socket(2, 1, 0) }`:                  "imports syscall,",
		`package x; import "golang.org/x/sys/unix"; func f() { unix.Socket(2, 1, 0) }`:       "imports golang.org/x/sys/unix,",
		`package x; import "unsafe"; var _ unsafe.Pointer`:                                   "imports unsafe,",
		`package x; import . "net/http"; var c = &Client{Transport: &Transport{}}`:           "with a dot",
		`package x; import "net/http"; var c = &http.Client{}; var _, _ = http.MethodGet, c`: "",
		`package x; import "net/url"; var _, _ = url.Parse("https://example.org")`:           "",
	} {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "x.go", src, 0)
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Join(Escapes(fset, f, clientOnly), "; ")
		if want == "" && got != "" || want != "" && !strings.Contains(got, want) {
			t.Errorf("%s:\n  found %q, want %q", src, got, want)
		}
	}
}

// Check reads a directory's source files, skips its tests, and fails on a
// directory with nothing to check or a file that doesn't parse.
func TestCheck(t *testing.T) {
	dir := t.TempDir()
	if _, err := Check(dir, clientOnly); err == nil {
		t.Error("empty directory: no error")
	}
	write := func(name, src string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("a_test.go", `package x; import "net"; var _ net.Conn`)
	write("a.go", `package x; import "net/http"; var _ = http.Get`)
	got, err := Check(dir, clientOnly)
	if err != nil || len(got) != 1 || !strings.Contains(got[0], "a.go") || !strings.Contains(got[0], "http.Get") {
		t.Errorf("Check = %q, %v; want one problem in a.go", got, err)
	}
	write("b.go", `package x; func {`)
	if _, err := Check(dir, clientOnly); err == nil {
		t.Error("unparsable file: no error")
	}
	if _, err := Check("[", clientOnly); err == nil {
		t.Error("bad pattern: no error")
	}
}
