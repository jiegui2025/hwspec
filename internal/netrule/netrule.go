// Package netrule checks that a package's network requests can only leave
// through http.DefaultTransport, the one collect's
// TestCapturesNeverTouchTheNetwork replaces with a transport that fails the
// test. depguard decides which files may import net at all; this closes
// the other ways out: dialing directly (net, a net.Dialer), a transport or
// round tripper of its own, or a client that names one. Only tests use it.
package netrule

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
)

// Check parses the non-test Go files in dir and lists their escapes. HTTP
// names the net/http identifiers the package may use; none of them may be
// able to open a connection except through http.DefaultTransport.
func Check(dir string, http map[string]bool) (problems []string, err error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			return nil, err
		}
		checked++
		problems = append(problems, Escapes(fset, f, http)...)
	}
	if checked == 0 {
		return nil, fmt.Errorf("%s: no source files checked", dir)
	}
	return problems, nil
}

// Escapes lists the ways f could reach the network other than through
// http.DefaultTransport.
func Escapes(fset *token.FileSet, f *ast.File, http map[string]bool) []string {
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
			if x, ok := n.X.(*ast.Ident); ok && x.Name == httpName && !http[n.Sel.Name] {
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
