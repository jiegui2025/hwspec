package fwindex

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Whence is linux-firmware's WHENCE: which driver each firmware file is
// for, and its version where the file states one.
type Whence struct {
	Files map[string]WhenceFile // by path in the linux-firmware tree
	Links map[string]string     // a link's path → the file it points to
}

type WhenceFile struct {
	Driver  string
	Version string
}

// minWhenceFiles is far below the thousands a real WHENCE lists (4,602 at
// tag 20260916): fewer means a truncated or wrong file.
const minWhenceFiles = 1000

const maxWhenceBytes = 4 << 20

// ParseWhence reads WHENCE. Sections are separated by lines of dashes;
// each names a "Driver:" and lists "File:" or "RawFile:" entries (quoted
// when they hold spaces) and "Link: link -> target". A "Version:" belongs
// to the nearest File above it in its section only: when a group of files
// shares one (iwlwifi's .pnvm files before a .ucode), the others stay
// unversioned rather than be given another file's version.
func ParseWhence(b []byte) (*Whence, error) {
	if len(b) > maxWhenceBytes {
		return nil, fmt.Errorf("WHENCE larger than %d bytes", maxWhenceBytes)
	}
	w := &Whence{Files: map[string]WhenceFile{}, Links: map[string]string{}}
	var driver, last string
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		if len(line) >= 10 && strings.Trim(line, "-") == "" {
			driver, last = "", ""
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "Driver":
			driver, _, _ = strings.Cut(value, " - ")
		case "File", "RawFile":
			last = unquote(value)
			if last != "" {
				w.Files[last] = WhenceFile{Driver: driver}
			}
		case "Link":
			link, target, ok := strings.Cut(value, " -> ")
			if ok {
				link = unquote(link)
				w.Links[link] = path.Join(path.Dir(link), unquote(target))
			}
		case "Version":
			if f, ok := w.Files[last]; ok && last != "" {
				f.Version = value
				w.Files[last] = f
			}
			last = ""
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("WHENCE: %w", err)
	}
	if len(w.Files) < minWhenceFiles {
		return nil, fmt.Errorf("WHENCE lists %d files, fewer than the %d a complete one has", len(w.Files), minWhenceFiles)
	}
	return w, nil
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if u, err := strconv.Unquote(s); err == nil && strings.HasPrefix(s, `"`) {
		return u
	}
	return s
}

const maxRefsBytes = 1 << 20

// releaseTag matches linux-firmware's release tags, which are dates.
var releaseTag = regexp.MustCompile(`^[0-9]{8}$`)
var commitID = regexp.MustCompile(`^[0-9a-f]{40}$`)

// latestRelease picks the newest release tag from a git info/refs listing
// ("<id>\trefs/tags/<tag>", with "^{}" lines giving the commit an
// annotated tag points to) and returns it with its commit. A tag that
// isn't a date up to now (and a day) is skipped: one dated in the future
// would make every real release after it look older.
func latestRelease(refs []byte, now time.Time) (tag, commit string, err error) {
	if len(refs) > maxRefsBytes {
		return "", "", fmt.Errorf("tag list larger than %d bytes", maxRefsBytes)
	}
	commits := map[string]string{}
	for line := range strings.Lines(string(refs)) {
		id, ref, ok := strings.Cut(strings.TrimSpace(line), "\t")
		name, isTag := strings.CutPrefix(ref, "refs/tags/")
		if !ok || !isTag || !commitID.MatchString(id) {
			continue
		}
		name, peeled := strings.CutSuffix(name, "^{}")
		date, bad := time.Parse("20060102", name)
		if !releaseTag.MatchString(name) || bad != nil || date.After(now.Add(24*time.Hour)) {
			continue
		}
		if _, seen := commits[name]; !seen || peeled {
			commits[name] = id
		}
		if name > tag {
			tag = name
		}
	}
	if tag == "" {
		return "", "", errors.New("no release tags listed")
	}
	return tag, commits[tag], nil
}
