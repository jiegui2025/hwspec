package fwindex

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// CatalogueName is the LVFS catalogue hwspec uses: the .zst, the only one
// with every component and several releases each (ADR 0012).
const CatalogueName = "firmware.xml.zst"

const (
	maxCatalogueBytes   = 16 << 20 // compressed, as downloaded
	maxCatalogueDecoded = 64 << 20

	// maxSignatures is how many PKCS#7 signatures in a jcat are tried
	// (LVFS sends two: RSA and ML-DSA).
	maxSignatures = 4

	// minComponents is far below the 4,413 components of LVFS's stable
	// catalogue (2026-10-06) and above its other signed catalogues
	// (firmware-testing: 934). LVFS's signature covers a file's digest,
	// not its name, so a CDN could pass off another genuine catalogue as
	// this one; its size tells them apart.
	minComponents = 2000

	// maxShrink is how much smaller than the cached catalogue a new one
	// may be without --allow-older, as ids refuses a bundle 5% smaller.
	maxShrink = 0.05
)

// VerifyCatalogue checks LVFS's catalogue against its .jcat signature
// file at time now: an RSA PKCS#7 signature in it must chain to a built-in
// LVFS CA for code signing and sign the catalogue's SHA-256. Checksums and
// other signatures in the file are ignored (the ML-DSA one binds no
// content). It returns when the catalogue was signed.
func VerifyCatalogue(catalogue, jcat []byte, now time.Time) (time.Time, error) {
	roots, err := lvfsRoots()
	if err != nil {
		return time.Time{}, err
	}
	return verifyCatalogue(catalogue, jcat, roots, now)
}

func verifyCatalogue(catalogue, jcat []byte, roots *x509.CertPool, now time.Time) (time.Time, error) {
	j, err := parseJcat(jcat)
	if err != nil {
		return time.Time{}, err
	}
	blobs, found := j.blobs(CatalogueName)
	if !found {
		return time.Time{}, fmt.Errorf("the signature file has no entry for %s", CatalogueName)
	}
	digest := sha256.Sum256(catalogue)
	var reasons []string
	tried := 0
	for _, b := range blobs {
		if b.Kind != blobPKCS7 || tried == maxSignatures {
			continue
		}
		tried++
		der, err := b.der()
		if err == nil {
			var sig signature
			if sig, err = verifyPKCS7(der, digest, roots, now); err == nil {
				return sig.signedAt, nil
			}
		}
		if !slices.Contains(reasons, err.Error()) {
			reasons = append(reasons, err.Error())
		}
	}
	if tried == 0 {
		return time.Time{}, errors.New("the signature file has no PKCS#7 signature")
	}
	return time.Time{}, fmt.Errorf("no signature verifies: %s (if LVFS changed how it signs, update hwspec)", strings.Join(reasons, "; "))
}

// DecodeCatalogue decompresses a verified catalogue, refusing more than
// 64 MiB, and checks it is an AppStream components document. It returns
// the XML and how many components it lists. Decode only what
// VerifyCatalogue accepted: the zstd decoder is third-party code.
func DecodeCatalogue(zst []byte) ([]byte, int, error) {
	return decodeCatalogue(zst, maxCatalogueDecoded)
}

func decodeCatalogue(zst []byte, limit int64) ([]byte, int, error) {
	d, err := zstd.NewReader(bytes.NewReader(zst),
		zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(uint64(limit)))
	if err != nil {
		return nil, 0, fmt.Errorf("catalogue: %w", err)
	}
	defer d.Close()
	xmlData, err := io.ReadAll(io.LimitReader(d, limit+1))
	switch {
	case int64(len(xmlData)) > limit || errors.Is(err, zstd.ErrDecoderSizeExceeded):
		return nil, 0, fmt.Errorf("catalogue decodes to more than %d bytes", limit)
	case err != nil:
		return nil, 0, fmt.Errorf("catalogue: %w", err)
	}
	n, err := countComponents(xmlData)
	if err != nil {
		return nil, 0, err
	}
	return xmlData, n, nil
}

// countComponents checks the root is <components> and counts the
// <component> elements in it.
func countComponents(xmlData []byte) (int, error) {
	dec := xml.NewDecoder(bytes.NewReader(xmlData))
	n, depth, root := 0, 0, false
	for {
		tok, err := dec.RawToken()
		if errors.Is(err, io.EOF) && root && depth == 0 {
			return n, nil
		}
		if err != nil {
			return 0, fmt.Errorf("catalogue isn't XML: %w", err)
		}
		switch el := tok.(type) {
		case xml.StartElement:
			depth++
			switch {
			case depth == 1 && (root || el.Name.Local != "components"):
				return 0, fmt.Errorf("catalogue's root element is <%s>, not one <components>", el.Name.Local)
			case depth == 1:
				root = true
			case depth == 2 && el.Name.Local == "component":
				n++
			}
		case xml.EndElement:
			depth--
		}
	}
}
