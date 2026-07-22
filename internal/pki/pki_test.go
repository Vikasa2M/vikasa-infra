package pki_test

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Vikasa2M/vikasa-infra/internal/pki"
)

func TestLoadOrCreateCA_MintAndReuse(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "ca", "cabinet-ca.crt")
	keyPath := filepath.Join(dir, "ca", "cabinet-ca.key")

	ca1, created, err := pki.LoadOrCreateCA(certPath, keyPath)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !created {
		t.Error("first call should report created=true")
	}
	if ca1 == nil {
		t.Fatal("nil CA")
	}

	// Cert parses and is a CA.
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read cert: %v", err)
	}
	blk, _ := pem.Decode(certPEM)
	if blk == nil {
		t.Fatal("cert PEM did not decode")
	}
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	if !cert.IsCA {
		t.Error("CA cert: IsCA should be true")
	}

	// File modes.
	if fi, _ := os.Stat(keyPath); fi.Mode().Perm() != 0o600 {
		t.Errorf("CA key mode = %v, want 0600", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(certPath); fi.Mode().Perm() != 0o644 {
		t.Errorf("CA cert mode = %v, want 0644", fi.Mode().Perm())
	}

	keyBytes1, _ := os.ReadFile(keyPath)
	_, created2, err := pki.LoadOrCreateCA(certPath, keyPath)
	if err != nil {
		t.Fatalf("reuse: %v", err)
	}
	if created2 {
		t.Error("second call should report created=false")
	}
	keyBytes2, _ := os.ReadFile(keyPath)
	if string(keyBytes1) != string(keyBytes2) {
		t.Error("CA key changed across runs (must be mint-once)")
	}
}

func TestSignClientCert_ChainAndEKU(t *testing.T) {
	dir := t.TempDir()
	ca, _, err := pki.LoadOrCreateCA(filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key"))
	if err != nil {
		t.Fatalf("ca: %v", err)
	}

	keyPEM, err := pki.NewClientKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	csr, err := pki.ClientCSR("exdot-d7a-cab-001", keyPEM)
	if err != nil {
		t.Fatalf("csr: %v", err)
	}
	certPEM, err := ca.SignClientCert(csr, "exdot-d7a-cab-001")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	blk, _ := pem.Decode(certPEM)
	leaf, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	if leaf.Subject.CommonName != "exdot-d7a-cab-001" {
		t.Errorf("CN = %q, want exdot-d7a-cab-001", leaf.Subject.CommonName)
	}
	hasClientAuth := false
	for _, eku := range leaf.ExtKeyUsage {
		if eku == x509.ExtKeyUsageClientAuth {
			hasClientAuth = true
		}
	}
	if !hasClientAuth {
		t.Error("leaf EKU missing clientAuth")
	}

	// Chains to the CA.
	caPEM, _ := os.ReadFile(filepath.Join(dir, "ca.crt"))
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("append CA to pool failed")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Errorf("leaf does not verify against CA: %v", err)
	}
}

func TestLoadOrCreateCA_PartialOnDiskFailsLoud(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "ca.crt")
	keyPath := filepath.Join(dir, "ca.key")
	if _, _, err := pki.LoadOrCreateCA(certPath, keyPath); err != nil {
		t.Fatalf("initial mint: %v", err)
	}
	// Delete the key, keep the cert: must fail loud, not silently re-root.
	if err := os.Remove(keyPath); err != nil {
		t.Fatalf("remove key: %v", err)
	}
	_, _, err := pki.LoadOrCreateCA(certPath, keyPath)
	if err == nil {
		t.Fatal("expected error for partial CA on disk, got nil")
	}
	if !strings.Contains(err.Error(), "partial CA") {
		t.Errorf("error should mention 'partial CA', got: %v", err)
	}
}

// TestWipe_NilSafety pins that Wipe never panics on degenerate receivers.
// Wipe is called via defer throughout issuance; a panic here would abort the
// key-hygiene path mid-flight.
func TestWipe_NilSafety(t *testing.T) {
	tests := []struct {
		name string
		ca   *pki.SelfSignedCA
	}{
		{"nil *SelfSignedCA", nil},
		{"zero-value CA (nil key)", &pki.SelfSignedCA{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.ca.Wipe() // must not panic
			// Also via the Signer interface, as issuance defers it.
			var s pki.Signer = tc.ca
			s.Wipe()
		})
	}
}

// TestWipe_FreshCA pins Wipe on a real CA: it is idempotent (safe to call
// twice) and actually neutralizes the in-memory private scalar — signing
// afterwards must not succeed.
func TestWipe_FreshCA(t *testing.T) {
	dir := t.TempDir()
	ca, _, err := pki.LoadOrCreateCA(filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key"))
	if err != nil {
		t.Fatalf("ca: %v", err)
	}

	ca.Wipe()
	ca.Wipe() // idempotent: second wipe must not panic

	keyPEM, err := pki.NewClientKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	csr, err := pki.ClientCSR("exdot-d7a-cab-001", keyPEM)
	if err != nil {
		t.Fatalf("csr: %v", err)
	}
	// Pinned contract: the wiped key (D=0) is rejected by crypto/ecdsa, so
	// signing fails rather than emitting a cert under a destroyed key.
	if certPEM, err := ca.SignClientCert(csr, "exdot-d7a-cab-001"); err == nil {
		t.Errorf("SignClientCert after Wipe should fail, got cert:\n%s", certPEM)
	}
}

func TestSignClientCert_RejectsBadCSR(t *testing.T) {
	dir := t.TempDir()
	ca, _, err := pki.LoadOrCreateCA(filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key"))
	if err != nil {
		t.Fatalf("ca: %v", err)
	}
	if _, err := ca.SignClientCert([]byte("not-a-csr"), "x"); err == nil {
		t.Error("expected error signing a malformed CSR, got nil")
	}
}

func TestSignClientCert_PinsCN(t *testing.T) {
	// The issuance host's inventory — not the CSR — is the authority on
	// identity: a CSR whose CN differs from the expected id must be refused,
	// or a compromised key-generation step could mint a cert impersonating
	// any other cabinet.
	dir := t.TempDir()
	ca, _, err := pki.LoadOrCreateCA(filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key"))
	if err != nil {
		t.Fatalf("ca: %v", err)
	}
	keyPEM, err := pki.NewClientKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	csr, err := pki.ClientCSR("exdot-d7a-cab-002", keyPEM)
	if err != nil {
		t.Fatalf("csr: %v", err)
	}
	if _, err := ca.SignClientCert(csr, "exdot-d7a-cab-001"); err == nil {
		t.Fatal("CSR CN exdot-d7a-cab-002 signed for expected id exdot-d7a-cab-001")
	} else if !strings.Contains(err.Error(), "exdot-d7a-cab-002") || !strings.Contains(err.Error(), "exdot-d7a-cab-001") {
		t.Errorf("mismatch error should name both identities: %v", err)
	}
	if _, err := ca.SignClientCert(csr, "exdot-d7a-cab-002"); err != nil {
		t.Fatalf("matching id must sign: %v", err)
	}
}
