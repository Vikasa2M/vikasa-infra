package issuance_test

import (
	"crypto/x509"
	"encoding/pem"
	"path/filepath"
	"testing"

	"github.com/nats-io/jwt/v2"

	"github.com/Vikasa2M/vikasa-infra/internal/accounts"
	"github.com/Vikasa2M/vikasa-infra/internal/fleet"
	"github.com/Vikasa2M/vikasa-infra/internal/issuance"
	"github.com/Vikasa2M/vikasa-infra/internal/naming"
	"github.com/Vikasa2M/vikasa-infra/internal/topology"
)

// TestIssue_FullTrustChainVerifies walks the entire issued trust chain end to end on a
// freshly minted bundle and verifies every link cross-checks against the previous one:
//
//	operator root -> operator JWT (self-signed anchor)
//	  -> operator SK (declared in operator JWT SigningKeys)
//	    -> account JWT (signed by operator SK)
//	      -> account SK (declared in account JWT SigningKeys)
//	        -> user JWT (signed by account SK, IssuerAccount = account root)
//	          -> mTLS client cert (chains to the cabinet CA, same cabinet identity)
//
// Each level is unit-tested in isolation elsewhere; this is the composition guard — it
// fails if any future change breaks the chain as a whole (e.g. signs the user JWT with
// the wrong key, drops the SK from a SigningKeys list, or re-roots the cert CA).
func TestIssue_FullTrustChainVerifies(t *testing.T) {
	dir := credsDir(t)
	root, err := topology.Load("../../examples/exdot-shared.json")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	m, err := accounts.Build(root)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	inv := &fleet.Inventory{DOT: "exdot", Cabinets: []fleet.Cabinet{
		{ID: "cab-a", Partition: "d7/0", Filter: "vikasa.exdot.d7.a.>"},
	}}
	res, err := issuance.Issue(m, inv, root, dir)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	// Link 1: the operator root self-signs the operator JWT (the offline anchor).
	oc, err := jwt.DecodeOperatorClaims(string(readBytes(t, filepath.Join(dir, "operator.jwt"))))
	if err != nil {
		t.Fatalf("decode operator jwt: %v", err)
	}
	if oc.Subject != res.OperatorPub {
		t.Errorf("operator JWT Subject = %q, want operator root %q", oc.Subject, res.OperatorPub)
	}
	if oc.Issuer != res.OperatorPub {
		t.Errorf("operator JWT Issuer = %q, want self-signed by root %q", oc.Issuer, res.OperatorPub)
	}

	// Link 2: the operator JWT declares the operator signing key.
	if !oc.SigningKeys.Contains(res.OperatorSigningPub) {
		t.Errorf("operator JWT SigningKeys does not declare operator SK %q", res.OperatorSigningPub)
	}

	// Locate the account that owns cab-a (partition d7/0 -> DISTRICT_D7).
	var d7Pub, d7SKPub string
	for _, a := range res.Accounts {
		if a.Name == "DISTRICT_D7" {
			d7Pub, d7SKPub = a.Pub, a.SigningPub
		}
	}
	if d7Pub == "" {
		t.Fatalf("DISTRICT_D7 not in issued accounts: %+v", res.Accounts)
	}

	// Link 3: the account JWT is signed by the operator SK, and the operator claims sign it.
	ac := decodeAccount(t, dir, d7Pub)
	if ac.Issuer != res.OperatorSigningPub {
		t.Errorf("account JWT Issuer = %q, want operator SK %q", ac.Issuer, res.OperatorSigningPub)
	}
	if !oc.DidSign(ac) {
		t.Error("operator claims did not sign the account JWT (chain broken at operator->account)")
	}

	// Link 4: the account JWT declares the account signing key.
	if !ac.SigningKeys.Contains(d7SKPub) {
		t.Errorf("account JWT SigningKeys does not declare account SK %q", d7SKPub)
	}

	// Link 5: the user JWT is signed by the account SK, with IssuerAccount = account root.
	uc := userClaimsFromCreds(t, dir, "d7", "cab-a")
	if uc.Issuer != d7SKPub {
		t.Errorf("user JWT Issuer = %q, want account SK %q", uc.Issuer, d7SKPub)
	}
	if uc.IssuerAccount != d7Pub {
		t.Errorf("user JWT IssuerAccount = %q, want account root %q", uc.IssuerAccount, d7Pub)
	}
	if !ac.DidSign(uc) {
		t.Error("account claims did not sign the user JWT (chain broken at account->user)")
	}

	// Link 6: the cabinet mTLS client cert chains to the cabinet CA.
	caCert := parseCertPEM(t, readBytes(t, filepath.Join(dir, "ca", "cabinet-ca.crt")))
	leaf := parseCertPEM(t, readBytes(t, filepath.Join(dir, "cabinets", "d7", "cab-a.crt")))
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Errorf("cabinet client cert does not chain to the cabinet CA: %v", err)
	}

	// Link 7: both identity planes (NATS JWT and mTLS cert) name the same cabinet.
	if leaf.Subject.CommonName != "cab-a" {
		t.Errorf("cert CN = %q, want cabinet id cab-a", leaf.Subject.CommonName)
	}
	if uc.Name != "cab-a" {
		t.Errorf("user JWT Name = %q, want cabinet id cab-a", uc.Name)
	}
}

// TestIssue_DMZConsumerTrustChain mirrors the cabinet trust-chain checks
// above for a DMZ external-consumer credential: the consumer JWT must be
// issued by the DMZ account's signing key, name the DMZ account root as
// IssuerAccount, and validate under the DMZ account claims — JWT-only, no
// cert (external consumers never get an mTLS leaf).
func TestIssue_DMZConsumerTrustChain(t *testing.T) {
	dir := credsDir(t)
	m, root := dmzModelRoot(t)
	res, err := issuance.Issue(m, nil, root, dir)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	var dmzPub, dmzSKPub string
	for _, a := range res.Accounts {
		if a.Name == naming.DMZAccountName() {
			dmzPub, dmzSKPub = a.Pub, a.SigningPub
		}
	}
	if dmzPub == "" {
		t.Fatalf("DMZ account not in issued accounts: %+v", res.Accounts)
	}
	dmzAcct := decodeAccount(t, dir, dmzPub)
	if dmzAcct.Issuer != res.OperatorSigningPub {
		t.Errorf("DMZ account JWT Issuer = %q, want operator SK %q", dmzAcct.Issuer, res.OperatorSigningPub)
	}

	creds := readBytes(t, filepath.Join(dir, "dmz", "research-aggregate.creds"))
	tok, err := jwt.ParseDecoratedJWT(creds)
	if err != nil {
		t.Fatalf("parse creds: %v", err)
	}
	uc, err := jwt.DecodeUserClaims(tok)
	if err != nil {
		t.Fatalf("decode user claims: %v", err)
	}
	if uc.Issuer != dmzSKPub {
		t.Errorf("consumer JWT Issuer = %q, want DMZ account SK %q", uc.Issuer, dmzSKPub)
	}
	if uc.IssuerAccount != dmzPub {
		t.Errorf("consumer JWT IssuerAccount = %q, want DMZ account root %q", uc.IssuerAccount, dmzPub)
	}
	if !dmzAcct.DidSign(uc) {
		t.Error("DMZ account claims did not sign the consumer JWT (trust chain broken)")
	}
}

// parseCertPEM decodes a single PEM CERTIFICATE block into an *x509.Certificate.
func parseCertPEM(t *testing.T, pemBytes []byte) *x509.Certificate {
	t.Helper()
	blk, _ := pem.Decode(pemBytes)
	if blk == nil || blk.Type != "CERTIFICATE" {
		t.Fatal("expected a PEM CERTIFICATE block")
	}
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return cert
}
