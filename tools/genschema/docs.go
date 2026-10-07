package main

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/jiegui2025/hwspec/internal/report"
)

// module is the import path prefix of the packages whose doc comments
// become descriptions; other types (time.Time) have none.
const module = "github.com/jiegui2025/hwspec/"

// pkgDocs is one package's doc comments: each type's, and each struct
// field's (its doc and line comments joined).
type pkgDocs struct {
	types  map[string]string
	fields map[string]map[string]fieldDoc
}

// fieldDoc is a struct field's doc comment, and whether it has a
// "Deprecated:" paragraph, Go's mark for a field on its way out.
type fieldDoc struct {
	text       string
	deprecated bool
}

var deprecatedParagraph = regexp.MustCompile(`(?m)^Deprecated: `)

// docs reads, and keeps, the doc comments of the packages a schema's
// types come from.
type docs map[string]*pkgDocs

func (d docs) pkg(path string) (*pkgDocs, error) {
	if p, ok := d[path]; ok {
		return p, nil
	}
	bp, err := build.Import(path, ".", build.FindOnly)
	if err != nil {
		return nil, err
	}
	files, err := filepath.Glob(filepath.Join(bp.Dir, "*.go"))
	if err != nil {
		return nil, err
	}
	files = slices.DeleteFunc(files, func(f string) bool { return strings.HasSuffix(f, "_test.go") })
	p, err := parseDocs(files)
	if err != nil {
		return nil, err
	}
	d[path] = p
	return p, nil
}

// parseDocs reads the doc comments of the types in files, one package's.
func parseDocs(files []string) (*pkgDocs, error) {
	p := &pkgDocs{types: map[string]string{}, fields: map[string]map[string]fieldDoc{}}
	fset := token.NewFileSet()
	for _, file := range files {
		f, err := parser.ParseFile(fset, file, nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts := spec.(*ast.TypeSpec)
				doc := ts.Doc
				if doc == nil && len(gd.Specs) == 1 {
					doc = gd.Doc
				}
				p.types[ts.Name.Name] = text(doc)
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				fields := map[string]fieldDoc{}
				for _, fl := range st.Fields.List {
					for _, n := range fl.Names {
						fields[n.Name] = fieldDoc{strings.TrimSpace(text(fl.Doc) + " " + text(fl.Comment)),
							fl.Doc != nil && deprecatedParagraph.MatchString(fl.Doc.Text())}
					}
				}
				p.fields[ts.Name.Name] = fields
			}
		}
	}
	return p, nil
}

// text is a comment's text on one line.
func text(c *ast.CommentGroup) string {
	if c == nil {
		return ""
	}
	return strings.Join(strings.Fields(c.Text()), " ")
}

// typeDoc is the doc comment of the named type t holds (through pointers,
// lists and maps), "" for one outside this module.
func (d docs) typeDoc(t reflect.Type) (string, error) {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array || t.Kind() == reflect.Map {
		t = t.Elem()
	}
	if t.Name() == "" || !strings.HasPrefix(t.PkgPath(), module) {
		return "", nil
	}
	p, err := d.pkg(t.PkgPath())
	if err != nil {
		return "", err
	}
	return p.types[t.Name()], nil
}

// jsonName is a struct field's key in the JSON, "" for one left out.
func jsonName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	switch name {
	case "-":
		return ""
	case "":
		return f.Name
	}
	return name
}

// vocabulary is the report.Vocabularies list of field f of t, if any.
func vocabulary(t reflect.Type, f reflect.StructField) []string {
	if t.PkgPath() != reflect.TypeFor[report.Report]().PkgPath() {
		return nil
	}
	return report.Vocabularies[t.Name()+"."+f.Name]
}

// describe gives every property below s, generated from t, its field's
// doc comment as its description, or, without one, its type's, and marks
// it deprecated when the comment says so. A field with a vocabulary lists
// it as examples: for a string its values, for a map its keys
// (propertyNames). None of these constrains a document.
func (d docs) describe(s *jsonschema.Schema, t reflect.Type) error {
	if s == nil {
		return nil
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		return d.describe(s.Items, t.Elem())
	case reflect.Map:
		return d.describe(s.AdditionalProperties, t.Elem())
	case reflect.Struct:
	default:
		return nil
	}
	if !strings.HasPrefix(t.PkgPath(), module) {
		return nil
	}
	p, err := d.pkg(t.PkgPath())
	if err != nil {
		return err
	}
	for f := range t.Fields() {
		name := jsonName(f)
		if !f.IsExported() || name == "" {
			continue
		}
		prop := s.Properties[name]
		if prop == nil {
			continue
		}
		doc := p.fields[t.Name()][f.Name]
		prop.Description, prop.Deprecated = doc.text, doc.deprecated
		if prop.Description == "" {
			if prop.Description, err = d.typeDoc(f.Type); err != nil {
				return err
			}
		}
		if v := vocabulary(t, f); v != nil {
			examples := make([]any, len(v))
			for i, x := range v {
				examples[i] = x
			}
			switch f.Type.Kind() {
			case reflect.String:
				prop.Examples = examples
			case reflect.Map:
				prop.PropertyNames = &jsonschema.Schema{Examples: examples}
			default:
				return fmt.Errorf("%s.%s: a vocabulary is for a string or a map's keys", t.Name(), f.Name)
			}
		}
		if err := d.describe(prop, f.Type); err != nil {
			return err
		}
	}
	return nil
}
