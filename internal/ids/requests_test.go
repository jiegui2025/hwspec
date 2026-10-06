package ids

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// httpNames are the only net/http names this package may use: none of
// them can open a connection except through http.DefaultTransport.
var httpNames = map[string]bool{
	"Client": true, "NewRequestWithContext": true, "NewRequest": true,
	"MethodGet": true, "StatusOK": true, "Response": true, "Request": true, "Header": true,
}

// Every network request leaves through http.DefaultTransport, the one
// collect's TestCapturesNeverTouchTheNetwork replaces with a transport that
// fails the test. depguard lets only this package import net (in
// sync.go); this test closes the other ways out: dialing directly (net,
// a net.Dialer), a transport or round tripper of its own, or a client
// that names one.
func TestRequestsOnlyGoThroughTheDefaultTransport(t *testing.T) {
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
		for _, problem := range escapes(fset, f) {
			t.Error(problem)
		}
	}
	if checked == 0 {
		t.Fatal("no source files checked")
	}
}

// escapes lists the ways f could reach the network other than through
// http.DefaultTransport.
func escapes(fset *token.FileSet, f *ast.File) []string {
	var out []string
	httpName := ""
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		local := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			local = imp.Name.Name
		}
		switch {
		case path == "net/http" && local == ".":
			out = append(out, fmt.Sprintf("%s: imports net/http with a dot, which hides what it uses", fset.Position(imp.Pos())))
		case path == "net/http":
			httpName = local
		case path == "syscall" || path == "unsafe" || path == "golang.org/x/sys/unix":
			out = append(out, fmt.Sprintf("%s: imports %s, which can open sockets past http.DefaultTransport", fset.Position(imp.Pos()), path))
		case path == "net" || strings.HasPrefix(path, "net/") && path != "net/url":
			out = append(out, fmt.Sprintf("%s: imports %s, which can dial past http.DefaultTransport", fset.Position(imp.Pos()), path))
		}
	}
	if httpName == "" {
		return out
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			if x, ok := n.X.(*ast.Ident); ok && x.Name == httpName && !httpNames[n.Sel.Name] {
				out = append(out, fmt.Sprintf("%s: uses http.%s; requests must go through http.DefaultTransport", fset.Position(n.Pos()), n.Sel.Name))
			}
		case *ast.CompositeLit:
			if sel, ok := n.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "Client" {
				for _, e := range n.Elts {
					if kv, ok := e.(*ast.KeyValueExpr); ok {
						if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Transport" {
							out = append(out, fmt.Sprintf("%s: an http.Client with its own Transport", fset.Position(kv.Pos())))
						}
					}
				}
			}
		case *ast.AssignStmt:
			for _, l := range n.Lhs {
				if sel, ok := l.(*ast.SelectorExpr); ok && sel.Sel.Name == "Transport" {
					out = append(out, fmt.Sprintf("%s: sets a client's Transport", fset.Position(sel.Pos())))
				}
			}
		}
		return true
	})
	return out
}

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
		got := strings.Join(escapes(fset, f), "; ")
		if want == "" && got != "" || want != "" && !strings.Contains(got, want) {
			t.Errorf("%s:\n  found %q, want %q", src, got, want)
		}
	}
}
