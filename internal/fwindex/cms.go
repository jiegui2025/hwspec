package fwindex

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"time"
)

// A detached CMS SignedData (RFC 5652), as LVFS signs its catalogue: one
// signer, an RSA key, SHA-256, and signed attributes carrying the file's
// digest. Go's standard library has no CMS package; this reads only that
// shape and refuses the rest.

var (
	oidSignedData    = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidData          = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
	oidContentType   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 3}
	oidMessageDigest = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}
	oidSigningTime   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 5}
	oidSHA256        = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
	oidRSA           = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}
	oidRSASHA256     = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 11}
)

type contentInfo struct {
	ContentType asn1.ObjectIdentifier
	Content     asn1.RawValue `asn1:"explicit,tag:0"`
}

type signedData struct {
	Version          int
	DigestAlgorithms asn1.RawValue
	EncapContentInfo encapContentInfo
	Certificates     asn1.RawValue `asn1:"optional,tag:0"`
	CRLs             asn1.RawValue `asn1:"optional,tag:1"`
	SignerInfos      []signerInfo  `asn1:"set"`
}

type encapContentInfo struct {
	EContentType asn1.ObjectIdentifier
	EContent     asn1.RawValue `asn1:"optional,explicit,tag:0"`
}

type signerInfo struct {
	Version            int
	SID                asn1.RawValue
	DigestAlgorithm    pkix.AlgorithmIdentifier
	SignedAttrs        asn1.RawValue `asn1:"optional,tag:0"`
	SignatureAlgorithm pkix.AlgorithmIdentifier
	Signature          []byte
	UnsignedAttrs      asn1.RawValue `asn1:"optional,tag:1"`
}

type issuerAndSerial struct {
	Issuer asn1.RawValue
	Serial *big.Int
}

type attribute struct {
	Type   asn1.ObjectIdentifier
	Values asn1.RawValue `asn1:"set"`
}

// signature is what a verified signature vouches for.
type signature struct {
	signedAt time.Time // the signer's signingTime attribute
	signer   string    // the signing certificate's subject
}

// verifyPKCS7 checks that der is a detached signature over content whose
// SHA-256 is digest, by a certificate that chains to roots for code
// signing at time now.
func verifyPKCS7(der []byte, digest [sha256.Size]byte, roots *x509.CertPool, now time.Time) (signature, error) {
	var ci contentInfo
	if rest, err := asn1.Unmarshal(der, &ci); err != nil || len(rest) > 0 {
		return signature{}, fmt.Errorf("not a CMS structure (%w)", errOrTrailing(err))
	}
	if !ci.ContentType.Equal(oidSignedData) {
		return signature{}, fmt.Errorf("CMS content %v is not signed data", ci.ContentType)
	}
	var sd signedData
	if rest, err := asn1.Unmarshal(ci.Content.Bytes, &sd); err != nil || len(rest) > 0 { // Bytes: inside the [0]
		return signature{}, fmt.Errorf("CMS signed data unreadable (%w)", errOrTrailing(err))
	}
	if !sd.EncapContentInfo.EContentType.Equal(oidData) || len(sd.EncapContentInfo.EContent.FullBytes) > 0 {
		return signature{}, errors.New("not a detached signature over a file")
	}
	if len(sd.SignerInfos) != 1 {
		return signature{}, fmt.Errorf("%d signers, want 1", len(sd.SignerInfos))
	}
	si := sd.SignerInfos[0]
	if !si.DigestAlgorithm.Algorithm.Equal(oidSHA256) {
		return signature{}, fmt.Errorf("digest algorithm %v: only SHA-256 is accepted", si.DigestAlgorithm.Algorithm)
	}
	if alg := si.SignatureAlgorithm.Algorithm; !alg.Equal(oidRSA) && !alg.Equal(oidRSASHA256) {
		return signature{}, fmt.Errorf("signature algorithm %v: only RSA is accepted", alg)
	}

	if len(si.SignedAttrs.FullBytes) == 0 {
		return signature{}, errors.New("no signed attributes, so nothing ties the signature to the file")
	}
	// The signature covers the attributes' DER with the SET tag, not the
	// [0] IMPLICIT tag they carry inside SignerInfo (RFC 5652 §5.4).
	set := bytes.Clone(si.SignedAttrs.FullBytes)
	set[0] = 0x31

	// Who signed, before what they signed: nothing unsigned decides a
	// message (such as one about the clock).
	certs, err := x509.ParseCertificates(sd.Certificates.Bytes)
	if err != nil {
		return signature{}, fmt.Errorf("certificates: %w", err)
	}
	candidates, err := signerCerts(si.SID, certs)
	if err != nil {
		return signature{}, err
	}
	var leaf *x509.Certificate
	for _, c := range candidates { // a forged certificate named the same can't hide the real one
		if err = checkSigner(c, certs, set, si.Signature, roots, now); err == nil {
			leaf = c
			break
		}
	}
	if leaf == nil {
		return signature{}, err
	}

	signedAt, err := checkAttributes(set, digest)
	if err != nil {
		return signature{}, err
	}
	if signedAt.After(now.Add(24 * time.Hour)) {
		return signature{}, fmt.Errorf("signed in the future (%s); check the system clock", signedAt.UTC().Format(time.RFC3339))
	}
	return signature{signedAt: signedAt, signer: leaf.Subject.CommonName}, nil
}

// minRSABits is the smallest key accepted (LVFS's is 3072 bits).
const minRSABits = 2048

// checkSigner checks that leaf chains to roots for code signing at time
// now, through the signature's other certificates, and that its RSA key
// made sig over the signed attributes set.
func checkSigner(leaf *x509.Certificate, certs []*x509.Certificate, set, sig []byte, roots *x509.CertPool, now time.Time) error {
	if !slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageCodeSigning) {
		return fmt.Errorf("certificate %q is not for code signing", leaf.Subject.CommonName)
	}
	intermediates := x509.NewCertPool()
	for _, c := range certs {
		if c != leaf {
			intermediates.AddCert(c)
		}
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots: roots, Intermediates: intermediates, CurrentTime: now,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
	}); err != nil {
		return fmt.Errorf("certificate %q: %w", leaf.Subject.CommonName, err)
	}
	pub, ok := leaf.PublicKey.(*rsa.PublicKey)
	if !ok {
		return fmt.Errorf("certificate %q has no RSA key", leaf.Subject.CommonName)
	}
	if pub.N.BitLen() < minRSABits {
		return fmt.Errorf("certificate %q has a %d-bit RSA key, fewer than %d", leaf.Subject.CommonName, pub.N.BitLen(), minRSABits)
	}
	sum := sha256.Sum256(set)
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		return fmt.Errorf("RSA signature by %q doesn't verify", leaf.Subject.CommonName)
	}
	return nil
}

// checkAttributes reads the signed attributes (as a SET): the content
// type must be data, the message digest the file's, and a signing time
// present. Each may appear once, with one value.
func checkAttributes(set []byte, digest [sha256.Size]byte) (time.Time, error) {
	var attrs []attribute
	if rest, err := asn1.UnmarshalWithParams(set, &attrs, "set"); err != nil || len(rest) > 0 {
		return time.Time{}, fmt.Errorf("signed attributes unreadable (%w)", errOrTrailing(err))
	}
	var (
		signedAt time.Time
		seen     = map[string]bool{}
	)
	for _, a := range attrs {
		if seen[a.Type.String()] {
			return time.Time{}, fmt.Errorf("signed attribute %v appears twice", a.Type)
		}
		seen[a.Type.String()] = true
		var err error
		switch {
		case a.Type.Equal(oidContentType):
			var ct asn1.ObjectIdentifier
			if err = single(a, &ct); err == nil && !ct.Equal(oidData) {
				err = fmt.Errorf("signed content type %v is not data", ct)
			}
		case a.Type.Equal(oidMessageDigest):
			var md []byte
			if err = single(a, &md); err == nil && !bytes.Equal(md, digest[:]) {
				err = errors.New("the signature is for another file (message digest differs from the file's SHA-256)")
			}
		case a.Type.Equal(oidSigningTime):
			err = single(a, &signedAt)
		}
		if err != nil {
			return time.Time{}, err
		}
	}
	for _, need := range []struct {
		oid  asn1.ObjectIdentifier
		name string
	}{{oidContentType, "content type"}, {oidMessageDigest, "message digest"}, {oidSigningTime, "signing time"}} {
		if !seen[need.oid.String()] {
			return time.Time{}, fmt.Errorf("signed attributes lack the %s", need.name)
		}
	}
	return signedAt, nil
}

// single decodes an attribute's only value into v.
func single(a attribute, v any) error {
	rest, err := asn1.Unmarshal(a.Values.Bytes, v)
	if err != nil {
		return fmt.Errorf("signed attribute %v: %w", a.Type, err)
	}
	if len(rest) > 0 {
		return fmt.Errorf("signed attribute %v has more than one value", a.Type)
	}
	return nil
}

// signerCerts finds the certificates a SignerInfo names, by issuer and
// serial number or ([0]) by subject key identifier.
func signerCerts(sid asn1.RawValue, certs []*x509.Certificate) ([]*x509.Certificate, error) {
	var match func(*x509.Certificate) bool
	switch {
	case sid.Class == asn1.ClassUniversal && sid.Tag == asn1.TagSequence:
		var is issuerAndSerial
		if _, err := asn1.Unmarshal(sid.FullBytes, &is); err != nil {
			return nil, fmt.Errorf("signer identifier unreadable: %w", err)
		}
		match = func(c *x509.Certificate) bool {
			return bytes.Equal(c.RawIssuer, is.Issuer.FullBytes) && c.SerialNumber.Cmp(is.Serial) == 0
		}
	case sid.Class == asn1.ClassContextSpecific && sid.Tag == 0:
		match = func(c *x509.Certificate) bool { return bytes.Equal(c.SubjectKeyId, sid.Bytes) }
	default:
		return nil, errors.New("signer identifier of an unknown form")
	}
	var found []*x509.Certificate
	for _, c := range certs {
		if match(c) {
			found = append(found, c)
		}
	}
	if len(found) == 0 {
		return nil, errors.New("the signing certificate isn't in the signature")
	}
	return found, nil
}

func errOrTrailing(err error) error {
	if err == nil {
		return errors.New("trailing data")
	}
	return err
}
