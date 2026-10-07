package fwindex

import (
	"crypto/sha256"
	"crypto/x509"
	_ "embed"
	"encoding/hex"
	"encoding/pem"
	"fmt"
)

// lvfsCA is the LVFS CA certificate, as fwupd ships it
// (data/pki/LVFS-CA.pem; self-signed, valid 2017-08-01 to 2047-08-01).
//
//go:embed lvfs-ca.pem
var lvfsCA []byte

// trustedCAs are the CAs whose signatures on LVFS's catalogue hwspec
// accepts, each pinned by the SHA-256 of its certificate. When LVFS adds a
// CA, a release adds it here (as internal/ids's trustedKeys does for the
// ID bundle); until then the refusal says to update hwspec.
var trustedCAs = []struct {
	name, pem, sha256 string
}{
	{"LVFS CA", string(lvfsCA), "c4dee98e36f76a4b2e586fb07c802b96651a25cc50990e20cf70c000c43d39e4"},
}

// lvfsRoots returns the pool of trusted CAs, checking each against its pin.
func lvfsRoots() (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	for _, ca := range trustedCAs {
		block, _ := pem.Decode([]byte(ca.pem))
		if block == nil {
			return nil, fmt.Errorf("built-in %s: not PEM", ca.name)
		}
		if sum := sha256.Sum256(block.Bytes); hex.EncodeToString(sum[:]) != ca.sha256 {
			return nil, fmt.Errorf("built-in %s: fingerprint %x, want %s", ca.name, sum, ca.sha256)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("built-in %s: %w", ca.name, err)
		}
		pool.AddCert(cert)
	}
	return pool, nil
}
