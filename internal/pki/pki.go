// Package pki issues X.509 client certificates for cabinet transport mTLS.
// The CA lives behind the Signer interface (CSR-signing) so a production backend
// (e.g. Vault PKI) can replace the bundled self-signed CA without changing
// callers. The bundled SelfSignedCA is a bootstrap posture only — the same
// caveat as the locally-minted operator nkey.
package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

const (
	caValidity   = 10 * 365 * 24 * time.Hour // ~10 years (mint-once root of trust)
	leafValidity = 90 * 24 * time.Hour       // 90 days (re-signed each run)
	clockSkew    = 5 * time.Minute           // backdate NotBefore for clock skew
)

// Signer signs a client CSR as a clientAuth leaf certificate for exactly the
// given identity. The CA fixes the EKU, key usage, validity window, and
// serial; implementations must refuse a CSR whose Subject CN differs from id —
// the caller's inventory, not the CSR, is the authority on identity. This is
// the seam a Vault-PKI backend implements later. Wipe releases any in-memory
// secret the signer holds (best-effort); a backend with no local key material
// may implement it as a no-op.
type Signer interface {
	SignClientCert(csrDER []byte, id string) (certPEM []byte, err error)
	Wipe()
}

// NewClientKey generates an ECDSA P-256 private key, PEM-encoded (PKCS#8).
func NewClientKey() (keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("pki: generate client key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("pki: marshal client key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// ClientCSR builds a PKCS#10 CSR (DER) for keyPEM with Subject CN = id.
func ClientCSR(id string, keyPEM []byte) (csrDER []byte, err error) {
	key, err := parseECKey(keyPEM)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.CertificateRequest{Subject: pkix.Name{CommonName: id}}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		return nil, fmt.Errorf("pki: create CSR for %q: %w", id, err)
	}
	return der, nil
}

// SelfSignedCA is a bundled, self-signed CA implementing Signer.
type SelfSignedCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

// SignClientCert implements Signer.
func (ca *SelfSignedCA) SignClientCert(csrDER []byte, id string) ([]byte, error) {
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return nil, fmt.Errorf("pki: parse CSR: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("pki: CSR signature: %w", err)
	}
	if csr.Subject.CommonName != id {
		return nil, fmt.Errorf("pki: CSR CN %q does not match expected identity %q", csr.Subject.CommonName, id)
	}
	serial, err := randSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now().Add(-clockSkew)
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: id},
		NotBefore:    now,
		NotAfter:     now.Add(leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, csr.PublicKey, ca.key)
	if err != nil {
		return nil, fmt.Errorf("pki: sign client cert for %q: %w", csr.Subject.CommonName, err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// Wipe best-effort zeroes the in-memory CA private key (process hygiene only —
// it does NOT remove the persisted key file on disk). ECDSA keys are not a fixed
// byte buffer like nkey seeds, and big.Int.SetInt64 may reallocate rather than
// scrub the original heap bytes, so this is best-effort, not a guarantee.
func (ca *SelfSignedCA) Wipe() {
	if ca == nil || ca.key == nil {
		return
	}
	// SA1019: PrivateKey.D is deprecated because mutating it can invalidate
	// the key — invalidating the key is exactly the point here.
	//lint:ignore SA1019 deliberate key invalidation (best-effort wipe)
	if d := ca.key.D; d != nil {
		d.SetInt64(0)
	}
}

// LoadOrCreateCA returns the CA at certPath/keyPath, minting a self-signed one
// (cert 0644, key 0600, dir 0700) if both files are absent. The bool is true
// when a new CA was created. If exactly one file is present (partial CA on
// disk), LoadOrCreateCA returns an error rather than silently re-rooting trust.
func LoadOrCreateCA(certPath, keyPath string) (*SelfSignedCA, bool, error) {
	certPEM, certErr := os.ReadFile(certPath)
	keyPEM, keyErr := os.ReadFile(keyPath)

	// Surface any non-ErrNotExist read error first.
	if certErr != nil && !errors.Is(certErr, os.ErrNotExist) {
		return nil, false, fmt.Errorf("pki: read CA cert %s: %w", certPath, certErr)
	}
	if keyErr != nil && !errors.Is(keyErr, os.ErrNotExist) {
		return nil, false, fmt.Errorf("pki: read CA key %s: %w", keyPath, keyErr)
	}

	certExists := certErr == nil
	keyExists := keyErr == nil

	switch {
	case certExists && keyExists:
		ca, err := parseCA(certPEM, keyPEM)
		if err != nil {
			return nil, false, err
		}
		return ca, false, nil
	case certExists != keyExists: // exactly one present
		present, missing := certPath, keyPath
		if keyExists {
			present, missing = keyPath, certPath
		}
		return nil, false, fmt.Errorf("pki: partial CA on disk: %s exists but %s is missing (refusing to re-root trust); remove both to regenerate, or restore the missing file", present, missing)
	}
	// Both absent — fall through to mint.

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, false, fmt.Errorf("pki: generate CA key: %w", err)
	}
	serial, err := randSerial()
	if err != nil {
		return nil, false, err
	}
	now := time.Now().Add(-clockSkew)
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "vikasa-cabinet-ca"},
		NotBefore:             now,
		NotAfter:              now.Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, false, fmt.Errorf("pki: create CA cert: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, false, fmt.Errorf("pki: marshal CA key: %w", err)
	}
	outCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	outKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return nil, false, fmt.Errorf("pki: mkdir CA dir: %w", err)
	}
	if err := os.WriteFile(certPath, outCertPEM, 0o644); err != nil {
		return nil, false, fmt.Errorf("pki: write CA cert: %w", err)
	}
	if err := os.WriteFile(keyPath, outKeyPEM, 0o600); err != nil {
		return nil, false, fmt.Errorf("pki: write CA key: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, false, fmt.Errorf("pki: parse new CA cert: %w", err)
	}
	return &SelfSignedCA{cert: cert, key: key}, true, nil
}

func parseCA(certPEM, keyPEM []byte) (*SelfSignedCA, error) {
	blk, _ := pem.Decode(certPEM)
	if blk == nil || blk.Type != "CERTIFICATE" {
		return nil, errors.New("pki: CA cert PEM invalid")
	}
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("pki: parse CA cert: %w", err)
	}
	key, err := parseECKey(keyPEM)
	if err != nil {
		return nil, err
	}
	return &SelfSignedCA{cert: cert, key: key}, nil
}

func parseECKey(keyPEM []byte) (*ecdsa.PrivateKey, error) {
	blk, _ := pem.Decode(keyPEM)
	if blk == nil || blk.Type != "PRIVATE KEY" {
		return nil, errors.New("pki: key PEM invalid")
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("pki: parse key: %w", err)
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("pki: key is not ECDSA")
	}
	return ec, nil
}

func randSerial() (*big.Int, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("pki: serial: %w", err)
	}
	return serial, nil
}
