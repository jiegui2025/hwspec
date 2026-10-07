package fwindex

import (
	"bytes"
	"compress/gzip"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// Synthetic signatures in the shape LVFS uses (RSA, SHA-256, detached CMS
// with signed attributes, the signer issued by a CA), from keys made here:
// LVFS's own data isn't committed (ADR 0012), so TestHostVerifiesFwupdsCache
// checks the real thing where fwupd has cached it.

var testNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

type testPKI struct {
	caKey, key *rsa.PrivateKey
	ca, cert   *x509.Certificate
	roots      *x509.CertPool
}

var (
	pkiOnce sync.Once
	pkiKeys [3]*rsa.PrivateKey
)

func rsaKey(i int) *rsa.PrivateKey {
	pkiOnce.Do(func() {
		for n := range pkiKeys {
			k, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				panic(err)
			}
			pkiKeys[n] = k
		}
	})
	return pkiKeys[i]
}

// newPKI makes a CA and a code-signing certificate it issued; edit, if
// given, changes the signing certificate's template first.
func newPKI(t testing.TB, edit func(*x509.Certificate)) *testPKI {
	t.Helper()
	p := &testPKI{caKey: rsaKey(0), key: rsaKey(1)}
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test CA", Organization: []string{"hwspec tests"}},
		NotBefore: testNow.AddDate(-5, 0, 0), NotAfter: testNow.AddDate(20, 0, 0),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	p.ca = mustCert(t, caTmpl, caTmpl, &p.caKey.PublicKey, p.caKey)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(0x58eab0bb), Subject: pkix.Name{CommonName: "fwupd.org test"},
		NotBefore: testNow.AddDate(-1, 0, 0), NotAfter: testNow.AddDate(9, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
	}
	var pub any = &p.key.PublicKey
	if edit != nil {
		edit(tmpl)
		if tmpl.PublicKey != nil {
			pub = tmpl.PublicKey
		}
	}
	p.cert = mustCert(t, tmpl, p.ca, pub, p.caKey)
	p.roots = x509.NewCertPool()
	p.roots.AddCert(p.ca)
	return p
}

func mustCert(t testing.TB, tmpl, parent *x509.Certificate, pub any, key crypto.Signer) *x509.Certificate {
	t.Helper()
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func ecdsaPublic(t testing.TB) any {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &k.PublicKey
}

// cms describes a signature to build; the zero value of each field means
// LVFS's choice.
type cms struct {
	digest            []byte // messageDigest; default: the content's SHA-256
	attrs             []attribute
	rawAttrs          []byte // the SET's content as given, instead of attrs
	noAttrs           bool
	digestAlg, sigAlg asn1.ObjectIdentifier
	key               *rsa.PrivateKey
	certs             []*x509.Certificate
	sid               *asn1.RawValue
	eContent          bool
	signers           int
	contentType       asn1.ObjectIdentifier // of the ContentInfo
}

func setOf(t testing.TB, vals ...any) asn1.RawValue {
	t.Helper()
	var b []byte
	for _, v := range vals {
		e, err := asn1.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		b = append(b, e...)
	}
	return asn1.RawValue{Class: asn1.ClassUniversal, Tag: asn1.TagSet, IsCompound: true, Bytes: b}
}

// lvfsAttrs are the signed attributes LVFS sends, in its order.
func lvfsAttrs(t testing.TB, digest []byte, signedAt time.Time) []attribute {
	return []attribute{
		{oidContentType, setOf(t, oidData)},
		{oidSigningTime, setOf(t, signedAt.UTC())},
		{oidMessageDigest, setOf(t, digest)},
	}
}

func (c cms) build(t testing.TB, p *testPKI, content []byte) []byte {
	t.Helper()
	sum := sha256.Sum256(content)
	if c.digest == nil {
		c.digest = sum[:]
	}
	if c.attrs == nil {
		c.attrs = lvfsAttrs(t, c.digest, testNow.Add(-time.Hour))
	}
	if c.digestAlg == nil {
		c.digestAlg = oidSHA256
	}
	if c.sigAlg == nil {
		c.sigAlg = oidRSA
	}
	if c.key == nil {
		c.key = p.key
	}
	if c.certs == nil {
		c.certs = []*x509.Certificate{p.cert}
	}
	if c.signers == 0 {
		c.signers = 1
	}
	if c.contentType == nil {
		c.contentType = oidSignedData
	}
	si := signerInfo{
		Version:            1,
		DigestAlgorithm:    pkix.AlgorithmIdentifier{Algorithm: c.digestAlg},
		SignatureAlgorithm: pkix.AlgorithmIdentifier{Algorithm: c.sigAlg},
	}
	if c.sid != nil {
		si.SID = *c.sid
	} else {
		is, err := asn1.Marshal(issuerAndSerial{Issuer: asn1.RawValue{FullBytes: p.cert.RawIssuer}, Serial: p.cert.SerialNumber})
		if err != nil {
			t.Fatal(err)
		}
		si.SID = asn1.RawValue{FullBytes: is}
	}
	if !c.noAttrs {
		set, err := asn1.MarshalWithParams(c.attrs, "set")
		if c.rawAttrs != nil {
			set, err = asn1.Marshal(asn1.RawValue{Tag: asn1.TagSet, IsCompound: true, Bytes: c.rawAttrs})
		}
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(set)
		si.Signature, err = rsa.SignPKCS1v15(nil, c.key, crypto.SHA256, h[:])
		if err != nil {
			t.Fatal(err)
		}
		set[0] = 0xa0
		si.SignedAttrs = asn1.RawValue{FullBytes: set}
	} else {
		si.Signature, _ = rsa.SignPKCS1v15(nil, c.key, crypto.SHA256, sum[:])
	}
	var certs []byte
	for _, cert := range c.certs {
		certs = append(certs, cert.Raw...)
	}
	sd := signedData{
		Version:          1,
		DigestAlgorithms: setOf(t, pkix.AlgorithmIdentifier{Algorithm: c.digestAlg}),
		EncapContentInfo: encapContentInfo{EContentType: oidData},
		Certificates:     asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: certs},
	}
	if c.eContent {
		inner, _ := asn1.Marshal(content)
		sd.EncapContentInfo.EContent = asn1.RawValue{FullBytes: explicit0(t, inner)}
	}
	for range c.signers {
		sd.SignerInfos = append(sd.SignerInfos, si)
	}
	sdDER, err := asn1.Marshal(sd)
	if err != nil {
		t.Fatal(err)
	}
	der, err := asn1.Marshal(contentInfo{ContentType: c.contentType, Content: asn1.RawValue{FullBytes: explicit0(t, sdDER)}})
	if err != nil {
		t.Fatal(err)
	}
	return der
}

// explicit0 wraps DER in an explicit [0] tag (a RawValue's FullBytes is
// written as it is, whatever the field's tag).
func explicit0(t testing.TB, der []byte) []byte {
	t.Helper()
	b, err := asn1.Marshal(asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: der})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func pemPKCS7(der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "PKCS7", Bytes: der}))
}

// jcatOf builds a .jcat as LVFS's CDN serves it: gzip JSON, then IHATECDN.
func jcatOf(t testing.TB, id string, blobs ...jcatBlob) []byte {
	t.Helper()
	type blob struct {
		Kind, Flags int
		Timestamp   int64
		Data        string
	}
	var bs []blob
	for _, b := range blobs {
		bs = append(bs, blob{b.Kind, b.Flags, testNow.Unix(), b.Data})
	}
	js, err := json.Marshal(map[string]any{
		"JcatVersionMajor": 0, "JcatVersionMinor": 1,
		"Items": []map[string]any{{"Id": "firmware-09109-stable.xml.zst", "AliasIds": []string{id}, "Blobs": bs}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return append(gzipped(t, js), "IHATECDN"...)
}

func gzipped(t testing.TB, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func write(t testing.TB, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func contains(err error, want string) bool {
	return err != nil && strings.Contains(err.Error(), want)
}
