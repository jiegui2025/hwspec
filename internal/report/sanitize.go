package report

import (
	"reflect"
	"strings"
	"unicode"
)

// Sanitize cleans every string in the report in place: control characters
// (escape sequences, newlines that could forge extra output lines) and
// invisible formatting characters (bidi overrides that reorder text) are
// removed, and invalid UTF-8 is replaced. Strings come from hardware,
// firmware and shared files, so none of them is trusted.
func (r *Report) Sanitize() {
	sanitizeValue(reflect.ValueOf(r).Elem())
}

// CleanString is the cleaning Sanitize applies to each string.
func CleanString(s string) string {
	s = strings.ToValidUTF8(s, "�")
	if strings.IndexFunc(s, unsafeRune) < 0 {
		return s
	}
	return strings.Map(func(r rune) rune {
		if unsafeRune(r) {
			return -1
		}
		return r
	}, s)
}

func unsafeRune(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
}

func sanitizeValue(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		if v.CanSet() {
			v.SetString(CleanString(v.String()))
		}
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			sanitizeValue(v.Elem())
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				sanitizeValue(v.Field(i))
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			sanitizeValue(v.Index(i))
		}
	case reflect.Map:
		if v.IsNil() || v.Type().Key().Kind() != reflect.String {
			return // a nil map stays nil, so a re-exported capture is unchanged
		}
		// Keys and string values are cleaned (Health.Metrics keys come from
		// shared files too); other values are copied as they are.
		clean := reflect.MakeMapWithSize(v.Type(), v.Len())
		for _, k := range v.MapKeys() {
			val := v.MapIndex(k)
			if val.Kind() == reflect.String {
				val = reflect.ValueOf(CleanString(val.String())).Convert(v.Type().Elem())
			}
			clean.SetMapIndex(reflect.ValueOf(CleanString(k.String())).Convert(v.Type().Key()), val)
		}
		v.Set(clean)
	}
}
