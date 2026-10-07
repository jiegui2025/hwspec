package fwindex

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"slices"
)

// Jcat blob kinds and flags, as libjcat numbers them (jcat-blob.h).
const (
	blobPKCS7    = 3
	blobFlagUTF8 = 1 << 0
)

const (
	maxJcatBytes   = 1 << 20 // the signature file as downloaded
	maxJcatDecoded = 4 << 20 // its JSON
)

// jcatFile is a .jcat signature file: gzip-compressed JSON, which LVFS's
// CDN follows with a few bytes of its own ("IHATECDN"). Each item names a
// file and carries blobs: checksums and signatures of it. Nothing in the
// JSON is signed itself, so only what a signature covers is trusted.
type jcatFile struct {
	Major int        `json:"JcatVersionMajor"`
	Items []jcatItem `json:"Items"`
}

type jcatItem struct {
	ID      string     `json:"Id"`
	Aliases []string   `json:"AliasIds"`
	Blobs   []jcatBlob `json:"Blobs"`
}

type jcatBlob struct {
	Kind  int    `json:"Kind"`
	Flags int    `json:"Flags"`
	Data  string `json:"Data"`
}

func parseJcat(b []byte) (*jcatFile, error) {
	if len(b) > maxJcatBytes {
		return nil, fmt.Errorf("signature file larger than %d bytes", maxJcatBytes)
	}
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("signature file: %w", err)
	}
	zr.Multistream(false) // the bytes after the gzip member aren't part of it
	js, err := io.ReadAll(io.LimitReader(zr, maxJcatDecoded+1))
	if err != nil {
		return nil, fmt.Errorf("signature file: %w", err)
	}
	if len(js) > maxJcatDecoded {
		return nil, fmt.Errorf("signature file decodes to more than %d bytes", maxJcatDecoded)
	}
	var j jcatFile
	if err := json.Unmarshal(js, &j); err != nil {
		return nil, fmt.Errorf("signature file: %w", err)
	}
	if j.Major != 0 {
		return nil, fmt.Errorf("signature file format %d is newer than this hwspec reads; update hwspec", j.Major)
	}
	return &j, nil
}

// blobs returns the blobs of every entry for the file called name, by its
// ID or an alias. Entries aren't signed, so a bogus one placed first
// mustn't hide the real one.
func (j *jcatFile) blobs(name string) (blobs []jcatBlob, found bool) {
	for _, it := range j.Items {
		if it.ID == name || slices.Contains(it.Aliases, name) {
			blobs, found = append(blobs, it.Blobs...), true
		}
	}
	return blobs, found
}

// der returns a PKCS#7 blob's DER: PEM when flagged as text, else base64.
func (b jcatBlob) der() ([]byte, error) {
	if b.Flags&blobFlagUTF8 == 0 {
		return base64.StdEncoding.DecodeString(b.Data)
	}
	block, _ := pem.Decode([]byte(b.Data))
	if block == nil || block.Type != "PKCS7" && block.Type != "CMS" {
		return nil, errors.New("not a PEM-encoded PKCS#7 signature")
	}
	return block.Bytes, nil
}
