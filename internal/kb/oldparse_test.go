package kb

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// parseV1Rules is Parse as released before the data sections (#128), frozen
// at 5d2246d: an older binary's reader. The test that the sections are
// skipped by older binaries, with a warning, runs it on a new file. Only
// this function is frozen: it calls today's helpers (decodeRule,
// validateRule, Source.validate, nameOf), so it shows what an older binary
// does with the sections, not how it reads a changed rule.
func parseV1Rules(gz []byte) (*KB, error) {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, fmt.Errorf("knowledge base: %w", err)
	}
	raw, err := io.ReadAll(io.LimitReader(zr, maxSize+1))
	if err != nil {
		return nil, fmt.Errorf("knowledge base: %w", err)
	}
	if len(raw) > maxSize {
		return nil, fmt.Errorf("knowledge base: larger than %d bytes", maxSize)
	}
	// The format first: another format's sections may not fit at all.
	var sections map[string]json.RawMessage
	if err := json.Unmarshal(raw, &sections); err != nil {
		return nil, fmt.Errorf("knowledge base: %w", err)
	}
	var format int
	if err := json.Unmarshal(sections["format"], &format); err != nil || format != Format {
		got := string(sections["format"])
		if got == "" {
			got = "missing"
		}
		return nil, fmt.Errorf("knowledge base: format %s, this build reads %d", got, Format)
	}
	var file struct {
		Format  int               `json:"format"`
		Version string            `json:"version"`
		Sources []json.RawMessage `json:"sources"`
		Rules   []json.RawMessage `json:"rules"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("knowledge base: %w", err)
	}
	if !isDate(file.Version, day) {
		return nil, fmt.Errorf("knowledge base: version %q isn't YYYY-MM-DD", file.Version)
	}
	k := &KB{Format: file.Format, Version: file.Version, Sources: []Source{}, Rules: []Rule{}}
	// A newer knowledge base may add whole sections (#25's models, #10's
	// firmware): this build can't use them, and says so.
	for _, name := range slices.Sorted(maps.Keys(sections)) {
		if !slices.Contains([]string{"format", "version", "sources", "rules"}, name) {
			k.Skipped = append(k.Skipped, fmt.Sprintf("section %s skipped: this build of hwspec doesn't use it", strconv.Quote(name)))
		}
	}
	for i, js := range file.Sources {
		var s Source
		err := json.Unmarshal(js, &s)
		if err == nil {
			err = errors.Join(s.validate()...)
		}
		if err == nil && slices.ContainsFunc(k.Sources, func(o Source) bool { return o.ID == s.ID }) {
			err = errors.New("duplicate id")
		}
		if err != nil {
			k.Skipped = append(k.Skipped, fmt.Sprintf("source %s skipped: %v", nameOf(s.ID, i), err))
			continue
		}
		k.Sources = append(k.Sources, s)
	}
	slices.SortFunc(k.Sources, func(a, b Source) int { return strings.Compare(a.ID, b.ID) })
	seen := map[string]bool{}
	for i, js := range file.Rules {
		r, err := decodeRule(js)
		if err == nil {
			err = errors.Join(k.validateRule(&r, false)...)
		}
		if err == nil && seen[r.ID] {
			err = errors.New("duplicate id")
		}
		if err != nil {
			k.Skipped = append(k.Skipped, fmt.Sprintf("rule %s skipped: %v", nameOf(r.ID, i), err))
			k.SkippedRules++
			continue
		}
		seen[r.ID] = true
		k.Rules = append(k.Rules, r)
	}
	return k, nil
}
