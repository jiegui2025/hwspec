package fwindex

import (
	"crypto/sha256"
	"testing"
)

// The signature file and the CMS inside it are downloaded, so neither
// parser may panic on any input.

func FuzzParseJcat(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("{}"))
	f.Add(append(gzipped(f, []byte(`{"JcatVersionMajor":0,"Items":[{"Id":"firmware.xml.zst","Blobs":[{"Kind":3,"Flags":1,"Data":"-----BEGIN PKCS7-----\nMAA=\n-----END PKCS7-----\n"}]}]}`)), "IHATECDN"...))
	f.Fuzz(func(t *testing.T, b []byte) {
		j, err := parseJcat(b)
		if err != nil {
			return
		}
		blobs, _ := j.blobs(CatalogueName)
		for _, blob := range blobs {
			_, _ = blob.der()
		}
	})
}

func FuzzVerifyPKCS7(f *testing.F) {
	p := newPKI(f, nil)
	f.Add(cms{}.build(f, p, catalogue))
	f.Add(cms{noAttrs: true}.build(f, p, catalogue))
	f.Add([]byte{0x30, 0x03, 0x06, 0x01, 0x00})
	digest := sha256.Sum256(catalogue)
	f.Fuzz(func(t *testing.T, der []byte) {
		_, _ = verifyPKCS7(der, digest, p.roots, testNow)
	})
}

func FuzzParseWhence(f *testing.F) {
	f.Add([]byte("Driver: x - y\nFile: a\nLink: b -> a\nVersion: 1\n------------\n"))
	f.Add([]byte(`File: "a b"` + "\nRawFile: c\n"))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = ParseWhence(b)
		_, _, _ = latestRelease(b, testNow)
	})
}
