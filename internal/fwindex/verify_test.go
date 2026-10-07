package fwindex

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

var catalogue = []byte("<?xml version=\"1.0\"?>\n<components origin=\"lvfs\"/>\n")

func pkcs7Blob(der []byte) jcatBlob {
	return jcatBlob{Kind: blobPKCS7, Flags: blobFlagUTF8, Data: pemPKCS7(der)}
}

func shaBlob(b []byte) jcatBlob {
	s := sha256.Sum256(b)
	return jcatBlob{Kind: 1, Flags: blobFlagUTF8, Data: hex.EncodeToString(s[:])}
}

// A good signature verifies and gives its signing time; each way it can be
// wrong is refused with a reason that says which.
func TestVerifyCatalogue(t *testing.T) {
	p := newPKI(t, nil)
	good := cms{}.build(t, p, catalogue)
	signedAt, err := verifyCatalogue(catalogue, jcatOf(t, CatalogueName, shaBlob(catalogue), pkcs7Blob(good)), p.roots, testNow)
	if err != nil || !signedAt.Equal(testNow.Add(-time.Hour)) {
		t.Fatalf("good signature: %v, %v", signedAt, err)
	}
	// Base64 DER (a blob not flagged as text) and a SubjectKeyIdentifier
	// signer verify too.
	skid := asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, Bytes: p.cert.SubjectKeyId}
	byKeyID := cms{sid: &skid}.build(t, p, catalogue)
	if _, err := verifyCatalogue(catalogue, jcatOf(t, CatalogueName, jcatBlob{Kind: blobPKCS7, Data: base64.StdEncoding.EncodeToString(byKeyID)}), p.roots, testNow); err != nil {
		t.Errorf("base64 blob, key-ID signer: %v", err)
	}

	changed := []byte(strings.Replace(string(catalogue), "lvfs", "lvfz", 1))
	other := []byte("another file")
	expired := newPKI(t, func(c *x509.Certificate) { c.NotAfter = testNow.AddDate(0, 0, -1) })
	notCodeSigning := newPKI(t, func(c *x509.Certificate) { c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth} })
	ecKey := newPKI(t, func(c *x509.Certificate) { c.PublicKey = ecdsaPublic(t) })
	sum := sha256.Sum256(catalogue)
	shake256 := asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 12}
	mldsa87 := asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 3, 19}
	wrongSID := asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, Bytes: []byte{1, 2, 3}}
	oddSID := asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 3, Bytes: []byte{1}}
	// Another CA: its own key and name.
	otherCA := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "Other CA"},
		NotBefore: testNow.AddDate(-1, 0, 0), NotAfter: testNow.AddDate(1, 0, 0),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	foreign := x509.NewCertPool()
	foreign.AddCert(mustCert(t, otherCA, otherCA, &rsaKey(2).PublicKey, rsaKey(2)))
	attrs := func(edit func([]attribute) []attribute) []attribute {
		return edit(lvfsAttrs(t, sum[:], testNow.Add(-time.Hour)))
	}

	for _, c := range []struct {
		name    string
		content []byte
		jcat    []byte
		roots   *x509.CertPool
		want    string
	}{
		{"one changed byte", changed, jcatOf(t, CatalogueName, pkcs7Blob(good)), nil, "for another file"},
		{"a signature for another file", catalogue, jcatOf(t, CatalogueName, pkcs7Blob(cms{}.build(t, p, other))), nil, "for another file"},
		{"a jcat for another file", catalogue, jcatOf(t, "firmware.xml.xz", pkcs7Blob(good)), nil, "no entry for firmware.xml.zst"},
		{"an expired certificate", catalogue, jcatOf(t, CatalogueName, pkcs7Blob(cms{}.build(t, expired, catalogue))), expired.roots, "expired"},
		{"a foreign certificate", catalogue, jcatOf(t, CatalogueName, pkcs7Blob(good)), foreign, "unknown authority"},
		{"the PQ blob alone", catalogue, jcatOf(t, CatalogueName, pkcs7Blob(cms{digestAlg: shake256, sigAlg: mldsa87, digest: []byte{}}.build(t, p, catalogue))), nil, "only SHA-256"},
		{"no PKCS#7 blob", catalogue, jcatOf(t, CatalogueName, shaBlob(catalogue)), nil, "no PKCS#7 signature"},
		{"an ML-DSA signature over SHA-256", catalogue, jcatOf(t, CatalogueName, pkcs7Blob(cms{sigAlg: mldsa87}.build(t, p, catalogue))), nil, "only RSA"},
		{"no signed attributes", catalogue, jcatOf(t, CatalogueName, pkcs7Blob(cms{noAttrs: true}.build(t, p, catalogue))), nil, "no signed attributes"},
		{"only a content type", catalogue, jcatOf(t, CatalogueName, pkcs7Blob(cms{attrs: attrs(func(a []attribute) []attribute { return a[:1:1] })}.build(t, p, catalogue))), nil, "lack the message digest"},
		{"signing time missing", catalogue, jcatOf(t, CatalogueName, pkcs7Blob(cms{attrs: attrs(func(a []attribute) []attribute { return []attribute{a[0], a[2]} })}.build(t, p, catalogue))), nil, "lack the signing time"},
		{"content type not data", catalogue, jcatOf(t, CatalogueName, pkcs7Blob(cms{attrs: attrs(func(a []attribute) []attribute {
			a[0].Values = setOf(t, oidSignedData)
			return a
		})}.build(t, p, catalogue))), nil, "is not data"},
		{"an attribute twice", catalogue, jcatOf(t, CatalogueName, pkcs7Blob(cms{attrs: attrs(func(a []attribute) []attribute { return append(a, a[2]) })}.build(t, p, catalogue))), nil, "appears twice"},
		{"two digests in one attribute", catalogue, jcatOf(t, CatalogueName, pkcs7Blob(cms{attrs: attrs(func(a []attribute) []attribute {
			a[2].Values = setOf(t, sum[:], sum[:])
			return a
		})}.build(t, p, catalogue))), nil, "more than one value"},
		{"a digest that isn't an octet string", catalogue, jcatOf(t, CatalogueName, pkcs7Blob(cms{attrs: attrs(func(a []attribute) []attribute {
			a[2].Values = setOf(t, 7)
			return a
		})}.build(t, p, catalogue))), nil, "structure error"},
		{"signed in the future", catalogue, jcatOf(t, CatalogueName, pkcs7Blob(cms{attrs: lvfsAttrs(t, sum[:], testNow.Add(48*time.Hour))}.build(t, p, catalogue))), nil, "signed in the future"},
		{"a signature by another key", catalogue, jcatOf(t, CatalogueName, pkcs7Blob(cms{key: rsaKey(2)}.build(t, p, catalogue))), nil, "doesn't verify"},
		{"a certificate not for code signing", catalogue, jcatOf(t, CatalogueName, pkcs7Blob(cms{}.build(t, notCodeSigning, catalogue))), notCodeSigning.roots, "not for code signing"},
		{"a certificate without an RSA key", catalogue, jcatOf(t, CatalogueName, pkcs7Blob(cms{}.build(t, ecKey, catalogue))), ecKey.roots, "no RSA key"},
		{"the signing certificate missing", catalogue, jcatOf(t, CatalogueName, pkcs7Blob(cms{certs: []*x509.Certificate{p.ca}}.build(t, p, catalogue))), nil, "isn't in the signature"},
		{"a signer identifier matching nothing", catalogue, jcatOf(t, CatalogueName, pkcs7Blob(cms{sid: &wrongSID}.build(t, p, catalogue))), nil, "isn't in the signature"},
		{"a signer identifier of another form", catalogue, jcatOf(t, CatalogueName, pkcs7Blob(cms{sid: &oddSID}.build(t, p, catalogue))), nil, "unknown form"},
		{"an attached signature", catalogue, jcatOf(t, CatalogueName, pkcs7Blob(cms{eContent: true}.build(t, p, catalogue))), nil, "not a detached signature"},
		{"two signers", catalogue, jcatOf(t, CatalogueName, pkcs7Blob(cms{signers: 2}.build(t, p, catalogue))), nil, "2 signers"},
		{"not signed data", catalogue, jcatOf(t, CatalogueName, pkcs7Blob(cms{contentType: oidData}.build(t, p, catalogue))), nil, "is not signed data"},
		{"not CMS", catalogue, jcatOf(t, CatalogueName, pkcs7Blob([]byte("junk"))), nil, "not a CMS structure"},
		{"trailing bytes after the CMS", catalogue, jcatOf(t, CatalogueName, pkcs7Blob(append(slices.Clip(good), 0))), nil, "trailing data"},
		{"a PEM block of another type", catalogue, jcatOf(t, CatalogueName, jcatBlob{Kind: blobPKCS7, Flags: blobFlagUTF8, Data: strings.ReplaceAll(pemPKCS7(good), "PKCS7", "CERTIFICATE")}), nil, "not a PEM-encoded PKCS#7"},
		{"bad base64", catalogue, jcatOf(t, CatalogueName, jcatBlob{Kind: blobPKCS7, Data: "!!"}), nil, "illegal base64"},
		{"not gzip", catalogue, []byte(`{"JcatVersionMajor":0,"Items":[]}`), nil, "signature file: gzip: invalid header"},
		{"not JSON", catalogue, append(gzipped(t, []byte("{")), "IHATECDN"...), nil, "signature file: unexpected end"},
		{"a newer jcat format", catalogue, gzipped(t, []byte(`{"JcatVersionMajor":1}`)), nil, "newer than this hwspec reads"},
		{"a truncated jcat", catalogue, gzipped(t, []byte(`{"Items":[]}`))[:20], nil, "signature file:"},
		{"a jcat too large", catalogue, make([]byte, maxJcatBytes+1), nil, "larger than"},
		{"a jcat that decodes too large", catalogue, gzipped(t, make([]byte, maxJcatDecoded+1)), nil, "decodes to more than"},
	} {
		roots := c.roots
		if roots == nil {
			roots = p.roots
		}
		_, err := verifyCatalogue(c.content, c.jcat, roots, testNow)
		if !contains(err, c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}

// Several signatures: one that verifies is enough (the ML-DSA blob beside
// the RSA one is skipped), and when none does, each reason is given.
func TestVerifyCatalogueTriesEachSignature(t *testing.T) {
	p := newPKI(t, nil)
	pq := cms{digestAlg: asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 12}}.build(t, p, catalogue)
	good := cms{}.build(t, p, catalogue)
	if _, err := verifyCatalogue(catalogue, jcatOf(t, CatalogueName, pkcs7Blob(pq), pkcs7Blob(good)), p.roots, testNow); err != nil {
		t.Errorf("PQ then RSA: %v", err)
	}
	other := cms{}.build(t, p, []byte("x"))
	_, err := verifyCatalogue(catalogue, jcatOf(t, CatalogueName, pkcs7Blob(pq), pkcs7Blob(other)), p.roots, testNow)
	if !contains(err, "only SHA-256") || !contains(err, "for another file") || !contains(err, "update hwspec") {
		t.Errorf("neither verifies: %v", err)
	}
}

// The built-in CA is LVFS's, pinned: an edited copy isn't trusted, and a
// signature from another CA isn't accepted by the public function.
func TestBuiltInCA(t *testing.T) {
	pool, err := lvfsRoots()
	if err != nil {
		t.Fatal(err)
	}
	if n := len(pool.Subjects()); n != len(trustedCAs) { //nolint:staticcheck // counting, not trusting, the subjects
		t.Errorf("%d CAs in the pool", n)
	}
	saved := trustedCAs[0]
	t.Cleanup(func() { trustedCAs[0] = saved })
	trustedCAs[0].sha256 = strings.Repeat("0", 64)
	if _, err := lvfsRoots(); !contains(err, "fingerprint") {
		t.Errorf("wrong pin: %v", err)
	}
	if _, err := VerifyCatalogue(catalogue, nil, testNow); !contains(err, "fingerprint") {
		t.Errorf("VerifyCatalogue with a wrong pin: %v", err)
	}
	trustedCAs[0].pem = "not PEM"
	if _, err := lvfsRoots(); !contains(err, "not PEM") {
		t.Errorf("not PEM: %v", err)
	}
	trustedCAs[0] = saved
	bad := saved
	bad.pem = strings.Replace(saved.pem, "MII", "MIA", 1)
	trustedCAs[0] = bad
	if _, err := lvfsRoots(); err == nil {
		t.Error("corrupt certificate accepted")
	}
	// A pinned PEM that isn't a certificate.
	trustedCAs[0].pem = "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"
	trustedCAs[0].sha256 = sha([]byte{0, 0, 0})
	if _, err := lvfsRoots(); !contains(err, "built-in LVFS CA:") {
		t.Errorf("pinned non-certificate: %v", err)
	}
	trustedCAs[0] = saved

	p := newPKI(t, nil)
	_, err = VerifyCatalogue(catalogue, jcatOf(t, CatalogueName, pkcs7Blob(cms{}.build(t, p, catalogue))), testNow)
	if !contains(err, "unknown authority") {
		t.Errorf("a test CA's signature: %v", err)
	}
}

// fwupd's cached catalogue verifies against the built-in CA as it is, and
// not after one changed byte.
func TestHostVerifiesFwupdsCache(t *testing.T) {
	hostTest(t)
	zst, err1 := os.ReadFile("/var/lib/fwupd/metadata/lvfs/" + CatalogueName)
	jcat, err2 := os.ReadFile("/var/lib/fwupd/metadata/lvfs/" + JcatName)
	if err1 != nil || err2 != nil {
		t.Skip("fwupd hasn't cached LVFS's catalogue here")
	}
	signedAt, err := VerifyCatalogue(zst, jcat, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("signed %s", signedAt)
	if _, n, err := DecodeCatalogue(zst); err != nil || n < minComponents {
		t.Errorf("%d components, %v", n, err)
	}
	zst[len(zst)/2] ^= 1
	if _, err := VerifyCatalogue(zst, jcat, time.Now()); !contains(err, "for another file") {
		t.Errorf("one changed byte: %v", err)
	}
}

func hostTest(t *testing.T) {
	t.Helper()
	if !strings.HasPrefix(t.Name(), "TestHost") {
		t.Fatalf("%s depends on this machine, so it must be named TestHost*: CI runs host tests by that prefix (-run '^TestHost')", t.Name())
	}
	if os.Getenv("HWSPEC_SKIP_HOST_TESTS") != "" {
		t.Skip("depends on this machine (HWSPEC_SKIP_HOST_TESTS is set)")
	}
}

// Malformed parts inside an otherwise well-formed signature are refused,
// and a CA in the signature serves as an intermediate, not a root.
func TestVerifyPKCS7MalformedParts(t *testing.T) {
	p := newPKI(t, nil)
	digest := sha256.Sum256(catalogue)
	if _, err := verifyPKCS7(cms{certs: []*x509.Certificate{p.ca, p.cert}}.build(t, p, catalogue), digest, p.roots, testNow); err != nil {
		t.Errorf("CA in the signature: %v", err)
	}
	ci := func(content []byte) []byte {
		der, err := asn1.Marshal(contentInfo{ContentType: oidSignedData, Content: asn1.RawValue{FullBytes: explicit0(t, content)}})
		if err != nil {
			t.Fatal(err)
		}
		return der
	}
	junkSID := asn1.RawValue{Class: asn1.ClassUniversal, Tag: asn1.TagSequence, IsCompound: true, Bytes: []byte{0x05, 0x00}}
	badCert := *p.cert
	badCert.Raw = []byte{0x30, 0x03, 0x02, 0x01, 0x01}
	for _, c := range []struct {
		name string
		der  []byte
		want string
	}{
		{"signed data that isn't", ci([]byte{0x30, 0x03, 0x02, 0x01, 0x01}), "CMS signed data unreadable"},
		{"a certificate that isn't", cms{certs: []*x509.Certificate{&badCert}}.build(t, p, catalogue), "certificates:"},
		{"a signer identifier that isn't", cms{sid: &junkSID}.build(t, p, catalogue), "signer identifier unreadable"},
		{"signed attributes that aren't", cms{rawAttrs: []byte{0x02, 0x01, 0x01}}.build(t, p, catalogue), "signed attributes unreadable"},
	} {
		if _, err := verifyPKCS7(c.der, digest, p.roots, testNow); !contains(err, c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}

// Unsigned parts can't hide a genuine signature: a forged certificate
// with the signer's issuer and serial listed first, a bogus jcat entry for
// the file listed first. Junk signatures are tried at most maxSignatures
// times, and each reason is given once.
func TestVerifyCatalogueIgnoresDecoys(t *testing.T) {
	p := newPKI(t, nil)
	forgedCA := &x509.Certificate{
		SerialNumber: p.ca.SerialNumber, Subject: p.ca.Subject,
		NotBefore: testNow.AddDate(-1, 0, 0), NotAfter: testNow.AddDate(1, 0, 0),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	forged := mustCert(t, &x509.Certificate{
		SerialNumber: p.cert.SerialNumber, Subject: p.cert.Subject,
		NotBefore: p.cert.NotBefore, NotAfter: p.cert.NotAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
	}, forgedCA, &rsaKey(2).PublicKey, rsaKey(2))
	good := cms{certs: []*x509.Certificate{forged, p.cert}}.build(t, p, catalogue)
	if _, err := verifyCatalogue(catalogue, jcatOf(t, CatalogueName, pkcs7Blob(good)), p.roots, testNow); err != nil {
		t.Errorf("forged certificate first: %v", err)
	}

	js, _ := json.Marshal(map[string]any{"Items": []map[string]any{
		{"Id": CatalogueName, "Blobs": []jcatBlob{{Kind: blobPKCS7, Flags: blobFlagUTF8, Data: "junk"}}},
		{"Id": "other.xml.zst", "AliasIds": []string{CatalogueName}, "Blobs": []jcatBlob{pkcs7Blob(good)}},
	}})
	if _, err := verifyCatalogue(catalogue, gzipped(t, js), p.roots, testNow); err != nil {
		t.Errorf("bogus entry first: %v", err)
	}

	junk := jcatBlob{Kind: blobPKCS7, Flags: blobFlagUTF8, Data: "junk"}
	_, err := verifyCatalogue(catalogue, jcatOf(t, CatalogueName, junk, junk, junk, junk, pkcs7Blob(good)), p.roots, testNow)
	if !contains(err, "not a PEM-encoded PKCS#7") || strings.Count(err.Error(), "not a PEM") != 1 {
		t.Errorf("four junk signatures, then a good one: %v", err)
	}
}

// LVFS's key is 3072 bits; one under 2048 isn't accepted.
func TestVerifyCatalogueRefusesAWeakKey(t *testing.T) {
	weak, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	p := newPKI(t, func(c *x509.Certificate) { c.PublicKey = &weak.PublicKey })
	_, err = verifyCatalogue(catalogue, jcatOf(t, CatalogueName, pkcs7Blob(cms{key: weak}.build(t, p, catalogue))), p.roots, testNow)
	if !contains(err, "1024-bit RSA key") {
		t.Errorf("1024-bit key: %v", err)
	}
}

// A signature whose signer doesn't verify is refused for that, not for
// what its unsigned attributes claim (such as a future signing time).
func TestVerifyPKCS7ChecksTheSignerFirst(t *testing.T) {
	p := newPKI(t, nil)
	sum := sha256.Sum256(catalogue)
	future := cms{attrs: lvfsAttrs(t, sum[:], testNow.Add(48*time.Hour)), key: rsaKey(2)}.build(t, p, catalogue)
	if _, err := verifyPKCS7(future, sum, p.roots, testNow); !contains(err, "doesn't verify") {
		t.Errorf("unsigned future time: %v", err)
	}
}
