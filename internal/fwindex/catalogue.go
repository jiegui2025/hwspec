package fwindex

import (
	"bytes"
	"cmp"
	"crypto/x509"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/jiegui2025/hwspec/internal/trust"
)

// Catalogue is what hwspec reads of LVFS's catalogue: each firmware
// component, with the GUIDs it is for, its releases and requirements.
// Descriptions, download locations and checksums aren't kept.
type Catalogue struct {
	Components []Component
	byGUID     map[string][]int
}

// Component is one firmware stream (an AppStream <component>).
type Component struct {
	ID        string // e.g. com.lenovo.PM981.256GB.firmware
	Name      string
	Developer string   // developer_name: who published it
	GUIDs     []string // lower case
	// VersionFormat is LVFS::VersionFormat ("triplet", "quad", ...); empty
	// when the component doesn't say.
	VersionFormat string
	Releases      []Release // newest first
	Requires      []Requirement
}

// Release is one firmware version LVFS offers.
type Release struct {
	Version string
	Date    time.Time // when it was published; zero if not given
	Urgency string    // low, medium, high, critical, or empty
	CVEs    []string
}

// Requirement is one <requires> child: Kind is its element (firmware, id,
// hardware, not_hardware, client), Text what it names (empty for the
// device's own firmware), Compare and Version the test.
type Requirement struct {
	Kind, Compare, Version, Text string
}

// NewCatalogue builds a catalogue from components, as ParseCatalogue
// would from their XML.
func NewCatalogue(components ...Component) *Catalogue {
	c := &Catalogue{byGUID: map[string][]int{}}
	for _, comp := range components {
		c.add(comp)
	}
	return c
}

func (c *Catalogue) add(comp Component) {
	for _, g := range comp.GUIDs {
		c.byGUID[g] = append(c.byGUID[g], len(c.Components))
	}
	c.Components = append(c.Components, comp)
}

// ByGUID returns the components for a GUID.
func (c *Catalogue) ByGUID(guid string) []*Component {
	var out []*Component
	for _, i := range c.byGUID[strings.ToLower(guid)] {
		out = append(out, &c.Components[i])
	}
	return out
}

type xmlText struct {
	Lang  string `xml:"http://www.w3.org/XML/1998/namespace lang,attr"`
	Value string `xml:",chardata"`
}

type xmlComponent struct {
	Type      string    `xml:"type,attr"`
	ID        string    `xml:"id"`
	Names     []xmlText `xml:"name"`
	Developer []xmlText `xml:"developer_name"`
	Provides  []struct {
		Type string `xml:"type,attr"`
		GUID string `xml:",chardata"`
	} `xml:"provides>firmware"`
	Custom []struct {
		Key   string `xml:"key,attr"`
		Value string `xml:",chardata"`
	} `xml:"custom>value"`
	Releases []struct {
		Version   string `xml:"version,attr"`
		Timestamp int64  `xml:"timestamp,attr"`
		Urgency   string `xml:"urgency,attr"`
		Issues    []struct {
			Type string `xml:"type,attr"`
			ID   string `xml:",chardata"`
		} `xml:"issues>issue"`
	} `xml:"releases>release"`
	Requires struct {
		Any []struct {
			XMLName xml.Name
			Compare string `xml:"compare,attr"`
			Version string `xml:"version,attr"`
			Text    string `xml:",chardata"`
		} `xml:",any"`
	} `xml:"requires"`
}

// untranslated is the text without a language, else the first.
func untranslated(texts []xmlText) string {
	for _, t := range texts {
		if t.Lang == "" {
			return strings.TrimSpace(t.Value)
		}
	}
	if len(texts) > 0 {
		return strings.TrimSpace(texts[0].Value)
	}
	return ""
}

// ParseCatalogue reads a decoded catalogue (DecodeCatalogue's XML). Only
// firmware components that provide a GUID are kept.
func ParseCatalogue(xmlData []byte) (*Catalogue, error) {
	c := NewCatalogue()
	dec := xml.NewDecoder(bytes.NewReader(xmlData))
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("catalogue: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "component" {
			continue
		}
		var x xmlComponent
		if err := dec.DecodeElement(&x, &se); err != nil {
			return nil, fmt.Errorf("catalogue: %w", err)
		}
		if comp, ok := component(x); ok {
			c.add(comp)
		}
	}
	return c, nil
}

func component(x xmlComponent) (Component, bool) {
	if x.Type != "firmware" {
		return Component{}, false
	}
	comp := Component{ID: strings.TrimSpace(x.ID), Name: untranslated(x.Names), Developer: untranslated(x.Developer)}
	for _, p := range x.Provides {
		if g := strings.ToLower(strings.TrimSpace(p.GUID)); g != "" && !slices.Contains(comp.GUIDs, g) {
			comp.GUIDs = append(comp.GUIDs, g)
		}
	}
	if len(comp.GUIDs) == 0 {
		return Component{}, false
	}
	// Some components give two formats (quad, then dell-bios): the last
	// wins, as fwupd has it (fu_device_ensure_from_component_verfmt reads
	// them last first).
	for _, v := range x.Custom {
		if v.Key == "LVFS::VersionFormat" {
			comp.VersionFormat = strings.TrimSpace(v.Value)
		}
	}
	for _, r := range x.Releases {
		rel := Release{Version: strings.TrimSpace(r.Version), Urgency: strings.TrimSpace(r.Urgency)}
		if rel.Version == "" {
			continue
		}
		if r.Timestamp > 0 {
			rel.Date = time.Unix(r.Timestamp, 0).UTC()
		}
		for _, i := range r.Issues {
			if id := strings.TrimSpace(i.ID); i.Type == "cve" && id != "" && !slices.Contains(rel.CVEs, id) {
				rel.CVEs = append(rel.CVEs, id)
			}
		}
		comp.Releases = append(comp.Releases, rel)
	}
	// Newest first, as LVFS lists them; the order is by date, not version.
	slices.SortStableFunc(comp.Releases, func(a, b Release) int { return cmp.Compare(b.Date.Unix(), a.Date.Unix()) })
	for _, q := range x.Requires.Any {
		comp.Requires = append(comp.Requires, Requirement{
			Kind: q.XMLName.Local, Compare: strings.TrimSpace(q.Compare), Version: strings.TrimSpace(q.Version), Text: strings.TrimSpace(q.Text),
		})
	}
	return comp, true
}

// FwupdCatalogue is where fwupd keeps LVFS's catalogue; its .jcat is
// beside it. hwspec uses it when it has none of its own, or fwupd's is
// newer (ADR 0012), after the same verification.
const FwupdCatalogue = "/var/lib/fwupd/metadata/lvfs/" + CatalogueName

// CatalogueFile is a catalogue on disk whose signature verified: a cache
// is user-writable, so it is checked as a download would be, then parsed
// only if chosen (Parse).
type CatalogueFile struct {
	SignedAt   time.Time // from the signature
	SHA256     string    // of the catalogue
	JcatSHA256 string    // of its signature file
	zst        []byte
}

// OpenCatalogue reads a catalogue and its .jcat as regular files within
// the caps and verifies them (VerifyCatalogue).
func OpenCatalogue(path string, now time.Time) (*CatalogueFile, error) {
	roots, err := lvfsRoots()
	if err != nil {
		return nil, err
	}
	return openCatalogue(path, roots, now)
}

func openCatalogue(path string, roots *x509.CertPool, now time.Time) (*CatalogueFile, error) {
	zst, err := trust.ReadRegular(path, maxCatalogueBytes)
	if err != nil {
		return nil, err
	}
	jcat, err := trust.ReadRegular(path+".jcat", maxJcatBytes)
	if err != nil {
		return nil, err
	}
	signedAt, err := verifyCatalogue(zst, jcat, roots, now)
	if err != nil {
		return nil, err
	}
	return &CatalogueFile{SignedAt: signedAt, SHA256: sha(zst), JcatSHA256: sha(jcat), zst: zst}, nil
}

// Signed is when the catalogue was signed.
func (f *CatalogueFile) Signed() time.Time { return f.SignedAt }

// Matches says whether this is the catalogue an update installed: the
// manifest's checksums and signing time. One that differs is an older
// copy put back, or an update that didn't finish.
func (f *CatalogueFile) Matches(s *Source) error {
	if s == nil || f.SHA256 != s.Files[CatalogueName] || f.JcatSHA256 != s.Files[JcatName] || !f.SignedAt.Equal(s.SignedAt) {
		return errors.New("it isn't the catalogue the last `hwspec firmware update` installed")
	}
	return nil
}

// Parse decodes the catalogue (zstd, capped) and reads it, refusing one
// with fewer components than LVFS's complete catalogue has.
func (f *CatalogueFile) Parse() (*Catalogue, error) {
	xmlData, n, err := DecodeCatalogue(f.zst)
	if err != nil {
		return nil, err
	}
	if n < minComponents {
		return nil, fmt.Errorf("lists %d components, fewer than the %d of LVFS's complete catalogue", n, minComponents)
	}
	return ParseCatalogue(xmlData)
}
