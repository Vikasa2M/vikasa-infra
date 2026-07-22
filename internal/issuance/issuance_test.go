package issuance_test

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"

	"github.com/Vikasa2M/vikasa-infra/internal/accounts"
	"github.com/Vikasa2M/vikasa-infra/internal/fleet"
	"github.com/Vikasa2M/vikasa-infra/internal/issuance"
	"github.com/Vikasa2M/vikasa-infra/internal/topology"
)

func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

func issueExdot(t *testing.T, dir string) (*issuance.Result, *accounts.Model) {
	t.Helper()
	root, err := topology.Load("../../examples/exdot-shared.json")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	m, err := accounts.Build(root)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	res, err := issuance.Issue(m, nil, nil, dir)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	return res, m
}

// dmzModelRoot builds the account model + topology root for the real DMZ
// example (two external consumers: research-aggregate, peer-neighbor-corridor).
func dmzModelRoot(t *testing.T) (*accounts.Model, *topology.Root) {
	t.Helper()
	root, err := topology.Load("../../examples/exdot-dmz.json")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	m, err := accounts.Build(root)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return m, root
}

func TestIssue_DMZConsumerCreds(t *testing.T) {
	dir := credsDir(t)
	m, root := dmzModelRoot(t)
	if _, err := issuance.Issue(m, nil, root, dir); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	creds := readBytes(t, filepath.Join(dir, "dmz", "research-aggregate.creds"))
	tok, err := jwt.ParseDecoratedJWT(creds)
	if err != nil {
		t.Fatal(err)
	}
	uc, err := jwt.DecodeUserClaims(tok)
	if err != nil {
		t.Fatal(err)
	}
	if len(uc.Sub.Allow) != 1 || uc.Sub.Allow[0] != "vikasa.exdot.share.research.>" {
		t.Errorf("consumer subscribe scope = %v, want [vikasa.exdot.share.research.>]", uc.Sub.Allow)
	}
	if len(uc.Pub.Deny) != 1 || uc.Pub.Deny[0] != ">" {
		t.Errorf("consumer must deny all publish, got Pub.Deny=%v", uc.Pub.Deny)
	}
	if len(uc.Pub.Allow) != 0 {
		t.Errorf("consumer must have no publish allow, got %v", uc.Pub.Allow)
	}
}

func TestIssue_DMZConsumerIdempotent(t *testing.T) {
	dir := credsDir(t)
	m, root := dmzModelRoot(t)
	r1, err := issuance.Issue(m, nil, root, dir)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := issuance.Issue(m, nil, root, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(r1.DMZConsumers) == 0 || len(r2.DMZConsumers) != len(r1.DMZConsumers) {
		t.Fatalf("consumer counts: %d then %d", len(r1.DMZConsumers), len(r2.DMZConsumers))
	}
	for _, c := range r2.DMZConsumers {
		if c.Minted {
			t.Errorf("consumer %q re-minted on second run (should reuse nkey)", c.Consumer)
		}
	}
}

func TestIssue_DMZConsumerFailsClosedLooseDir(t *testing.T) {
	dir := credsDir(t)
	if err := os.MkdirAll(filepath.Join(dir, "dmz"), 0o750); err != nil {
		t.Fatal(err)
	}
	m, root := dmzModelRoot(t)
	_, err := issuance.Issue(m, nil, root, dir)
	if err == nil || !strings.Contains(err.Error(), "group/world-accessible") {
		t.Fatalf("expected fail-closed on loose dmz/ dir, got: %v", err)
	}
}

func TestIssue_TrustChain(t *testing.T) {
	dir := credsDir(t)
	res, _ := issueExdot(t, dir)

	pub := map[string]string{}
	for _, a := range res.Accounts {
		pub[a.Name] = a.Pub
	}
	if pub["DISTRICT_D7"] == "" || pub["CENTRAL"] == "" || pub["SYSTEM"] == "" {
		t.Fatalf("missing account pubkeys: %+v", res.Accounts)
	}

	d7, err := jwt.DecodeAccountClaims(string(readBytes(t, filepath.Join(dir, "resolver", pub["DISTRICT_D7"]+".jwt"))))
	if err != nil {
		t.Fatalf("decode d7: %v", err)
	}
	if d7.Name != "DISTRICT_D7" {
		t.Errorf("d7 name = %q, want DISTRICT_D7", d7.Name)
	}
	if d7.Issuer != res.OperatorSigningPub {
		t.Errorf("d7 issuer = %q, want operator signing key %q", d7.Issuer, res.OperatorSigningPub)
	}
	if len(d7.Exports) != 1 || string(d7.Exports[0].Subject) != "vikasa.exdot.d7.>" {
		t.Errorf("d7 exports = %+v, want one export of vikasa.exdot.d7.>", d7.Exports)
	}
	if !d7.Limits.IsJSEnabled() {
		t.Error("DISTRICT_D7 JWT: JetStream should be enabled")
	}

	central, err := jwt.DecodeAccountClaims(string(readBytes(t, filepath.Join(dir, "resolver", pub["CENTRAL"]+".jwt"))))
	if err != nil {
		t.Fatalf("decode central: %v", err)
	}
	if len(central.Imports) != 1 || central.Imports[0].Account != pub["DISTRICT_D7"] || string(central.Imports[0].Subject) != "vikasa.exdot.d7.>" {
		t.Errorf("central imports = %+v, want one import of vikasa.exdot.d7.> from %s", central.Imports, pub["DISTRICT_D7"])
	}
	if !central.Limits.IsJSEnabled() {
		t.Error("CENTRAL JWT: JetStream should be enabled")
	}

	for _, f := range []string{"operator.jwt", "operator.nkey", "operator-sk.nkey", "resolver.conf", "accounts.index"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
	if !res.OperatorSigningMinted {
		t.Error("operator signing key should report Minted=true on first run")
	}
	for _, name := range []string{"operator.nkey", "operator-sk.nkey"} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", name, fi.Mode().Perm())
		}
	}

	opClaims, err := jwt.DecodeOperatorClaims(string(readBytes(t, filepath.Join(dir, "operator.jwt"))))
	if err != nil {
		t.Fatalf("decode operator: %v", err)
	}
	if opClaims.Issuer != res.OperatorPub {
		t.Errorf("operator JWT issuer = %q, want operator root %q", opClaims.Issuer, res.OperatorPub)
	}
	if res.OperatorSigningPub == "" || res.OperatorSigningPub == res.OperatorPub {
		t.Errorf("signing key %q must be set and distinct from root %q", res.OperatorSigningPub, res.OperatorPub)
	}
	foundSK := false
	for _, k := range opClaims.SigningKeys {
		if k == res.OperatorSigningPub {
			foundSK = true
		}
	}
	if !foundSK {
		t.Errorf("operator JWT SigningKeys %v missing signing key %q", opClaims.SigningKeys, res.OperatorSigningPub)
	}
	if !opClaims.DidSign(d7) {
		t.Error("operator claims did not sign the DISTRICT_D7 account JWT (trust chain broken)")
	}
}

func TestIssue_Idempotent(t *testing.T) {
	dir := credsDir(t)
	issueExdot(t, dir)
	opSeed1 := readBytes(t, filepath.Join(dir, "operator.nkey"))
	d7Seed1 := readBytes(t, filepath.Join(dir, "accounts", "DISTRICT_D7.nkey"))
	d7SKSeed1 := readBytes(t, filepath.Join(dir, "accounts", "DISTRICT_D7-sk.nkey"))
	skSeed1 := readBytes(t, filepath.Join(dir, "operator-sk.nkey"))

	res2, _ := issueExdot(t, dir)
	opSeed2 := readBytes(t, filepath.Join(dir, "operator.nkey"))
	d7Seed2 := readBytes(t, filepath.Join(dir, "accounts", "DISTRICT_D7.nkey"))
	d7SKSeed2 := readBytes(t, filepath.Join(dir, "accounts", "DISTRICT_D7-sk.nkey"))
	skSeed2 := readBytes(t, filepath.Join(dir, "operator-sk.nkey"))

	if !bytes.Equal(opSeed1, opSeed2) {
		t.Error("operator seed changed across runs (must be mint-once)")
	}
	if !bytes.Equal(skSeed1, skSeed2) {
		t.Error("operator signing-key seed changed across runs (must be mint-once)")
	}
	if !bytes.Equal(d7Seed1, d7Seed2) {
		t.Error("DISTRICT_D7 seed changed across runs (must be mint-once)")
	}
	if !bytes.Equal(d7SKSeed1, d7SKSeed2) {
		t.Error("DISTRICT_D7 account SK seed changed across runs (must be mint-once)")
	}
	for _, a := range res2.Accounts {
		if a.Minted {
			t.Errorf("account %s reported Minted=true on second run (should be reused)", a.Name)
		}
		if a.SigningMinted {
			t.Errorf("account %s SK reported SigningMinted=true on second run (should be reused)", a.Name)
		}
	}
	if res2.OperatorSigningMinted {
		t.Error("operator signing key reported Minted=true on second run (should be reused)")
	}
}

func TestIssue_Growth(t *testing.T) {
	dir := credsDir(t)
	m1 := &accounts.Model{DOT: "exdot", Accounts: []accounts.Account{
		{Name: "CENTRAL", JetStream: true},
		{Name: "DISTRICT_D7", JetStream: true, Exports: []accounts.Export{{Subject: "vikasa.exdot.d7.>"}}},
		{Name: "SYSTEM"},
	}}
	if _, err := issuance.Issue(m1, nil, nil, dir); err != nil {
		t.Fatalf("issue m1: %v", err)
	}
	d7Seed := readBytes(t, filepath.Join(dir, "accounts", "DISTRICT_D7.nkey"))
	d7SKSeed := readBytes(t, filepath.Join(dir, "accounts", "DISTRICT_D7-sk.nkey"))

	m2 := &accounts.Model{DOT: "exdot", Accounts: append(append([]accounts.Account{}, m1.Accounts...),
		accounts.Account{Name: "DISTRICT_D8", JetStream: true, Exports: []accounts.Export{{Subject: "vikasa.exdot.d8.>"}}})}
	res2, err := issuance.Issue(m2, nil, nil, dir)
	if err != nil {
		t.Fatalf("issue m2: %v", err)
	}

	if !bytes.Equal(d7Seed, readBytes(t, filepath.Join(dir, "accounts", "DISTRICT_D7.nkey"))) {
		t.Error("DISTRICT_D7 seed changed after adding D8 (must be reused)")
	}
	if !bytes.Equal(d7SKSeed, readBytes(t, filepath.Join(dir, "accounts", "DISTRICT_D7-sk.nkey"))) {
		t.Error("DISTRICT_D7 account SK seed changed across growth run (must be mint-once)")
	}
	for _, a := range res2.Accounts {
		if a.Name == "DISTRICT_D8" && !a.Minted {
			t.Error("DISTRICT_D8 should be Minted on first appearance")
		}
		if a.Name == "DISTRICT_D8" && !a.SigningMinted {
			t.Error("DISTRICT_D8 SK should be SigningMinted on first appearance")
		}
		if a.Name == "DISTRICT_D7" && a.Minted {
			t.Error("DISTRICT_D7 should be reused, not minted")
		}
		if a.Name == "DISTRICT_D7" && a.SigningMinted {
			t.Error("DISTRICT_D7 SK should be reused, not SigningMinted")
		}
	}
}

func TestIssue_UnknownImportAccount(t *testing.T) {
	m := &accounts.Model{DOT: "exdot", Accounts: []accounts.Account{
		{Name: "CENTRAL", JetStream: true, Imports: []accounts.Import{{FromAccount: "DISTRICT_NOPE", Subject: "vikasa.nope.>"}}},
	}}
	_, err := issuance.Issue(m, nil, nil, credsDir(t))
	if err == nil {
		t.Fatal("expected error for import from unknown account, got nil")
	}
	if !strings.Contains(err.Error(), "DISTRICT_NOPE") {
		t.Errorf("expected error to name the unknown account DISTRICT_NOPE, got: %v", err)
	}
}

func TestIssue_CabinetCreds(t *testing.T) {
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
		t.Fatalf("Issue: %v", err)
	}

	var d7Pub string
	for _, a := range res.Accounts {
		if a.Name == "DISTRICT_D7" {
			d7Pub = a.Pub
		}
	}
	if d7Pub == "" {
		t.Fatal("DISTRICT_D7 account not minted")
	}
	if len(res.Cabinets) != 1 || res.Cabinets[0].ID != "cab-a" || res.Cabinets[0].District != "d7" {
		t.Fatalf("unexpected cabinet results: %+v", res.Cabinets)
	}

	credsBytes, err := os.ReadFile(filepath.Join(dir, "cabinets", "d7", "cab-a.creds"))
	if err != nil {
		t.Fatalf("read creds: %v", err)
	}
	jwtStr, err := jwt.ParseDecoratedJWT(credsBytes)
	if err != nil {
		t.Fatalf("parse decorated jwt: %v", err)
	}
	uc, err := jwt.DecodeUserClaims(jwtStr)
	if err != nil {
		t.Fatalf("decode user claims: %v", err)
	}
	if uc.IssuerAccount != d7Pub {
		t.Errorf("user JWT IssuerAccount = %q, want DISTRICT_D7 %q", uc.IssuerAccount, d7Pub)
	}
	if len(uc.Permissions.Pub.Allow) != 1 || uc.Permissions.Pub.Allow[0] != "vikasa.exdot.d7.a.>" {
		t.Errorf("pub allow = %v, want the cabinet filter", uc.Permissions.Pub.Allow)
	}
	if len(uc.Permissions.Sub.Allow) != 1 || uc.Permissions.Sub.Allow[0] != "vikasa.exdot.d7.a.>" {
		t.Errorf("sub allow = %v, want the cabinet filter", uc.Permissions.Sub.Allow)
	}
}

// TestIssue_CredsFileStillValidAfterSeedWipe is a behavior-lock: mintCabinet
// now wipes the `creds` buffer (which embeds a copy of the decorated seed)
// after the file write via `defer wipeBytes(creds)`. Since Go's GC is
// non-moving, wiping that local slice in place cannot alter what was already
// flushed to disk — this asserts the on-disk .creds is unchanged: still a
// valid decorated JWT + seed pair.
func TestIssue_CredsFileStillValidAfterSeedWipe(t *testing.T) {
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
	if _, err := issuance.Issue(m, inv, root, dir); err != nil {
		t.Fatalf("Issue: %v", err)
	}

	credsBytes := readBytes(t, filepath.Join(dir, "cabinets", "d7", "cab-a.creds"))
	creds := string(credsBytes)
	if !strings.Contains(creds, "-----BEGIN NATS USER JWT-----") ||
		!strings.Contains(creds, "-----BEGIN USER NKEY SEED-----") {
		t.Errorf(".creds missing JWT or seed block after wipe change:\n%s", creds)
	}
}

func TestIssue_CabinetNoFilterFailsClosed(t *testing.T) {
	dir := credsDir(t)
	root, _ := topology.Load("../../examples/exdot-shared.json")
	m, _ := accounts.Build(root)
	inv := &fleet.Inventory{DOT: "exdot", Cabinets: []fleet.Cabinet{{ID: "cab-x", Partition: "d7/0"}}}
	_, err := issuance.Issue(m, inv, root, dir)
	if err == nil || !strings.Contains(err.Error(), "cab-x") || !strings.Contains(err.Error(), "filter") {
		t.Fatalf("expected fail-closed error naming cab-x + filter, got %v", err)
	}
}

func TestIssue_CabinetOrphanPartition(t *testing.T) {
	dir := credsDir(t)
	root, _ := topology.Load("../../examples/exdot-shared.json")
	m, _ := accounts.Build(root)
	inv := &fleet.Inventory{DOT: "exdot", Cabinets: []fleet.Cabinet{{ID: "cab-x", Partition: "d9/9", Filter: "vikasa.exdot.x.>"}}}
	_, err := issuance.Issue(m, inv, root, dir)
	if err == nil || !strings.Contains(err.Error(), "d9/9") {
		t.Fatalf("expected orphan-partition error naming d9/9, got %v", err)
	}
}

func TestIssue_CabinetAccountMissingFailsClosed(t *testing.T) {
	dir := credsDir(t)
	root, err := topology.Load("../../examples/exdot-shared.json")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// A model that does NOT contain DISTRICT_D7 (only SYSTEM), so a cabinet whose
	// partition resolves to district d7 has no account key to sign with.
	m := &accounts.Model{DOT: "exdot", Accounts: []accounts.Account{{Name: "SYSTEM"}}}
	inv := &fleet.Inventory{DOT: "exdot", Cabinets: []fleet.Cabinet{
		{ID: "cab-a", Partition: "d7/0", Filter: "vikasa.exdot.d7.a.>"},
	}}
	_, err = issuance.Issue(m, inv, root, dir)
	if err == nil || !strings.Contains(err.Error(), "DISTRICT_D7") || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected fail-closed error naming DISTRICT_D7 not found, got %v", err)
	}
}

func TestIssue_CabinetIdempotent(t *testing.T) {
	dir := credsDir(t)
	root, _ := topology.Load("../../examples/exdot-shared.json")
	m, _ := accounts.Build(root)
	inv := &fleet.Inventory{DOT: "exdot", Cabinets: []fleet.Cabinet{{ID: "cab-a", Partition: "d7/0", Filter: "vikasa.exdot.d7.a.>"}}}
	if _, err := issuance.Issue(m, inv, root, dir); err != nil {
		t.Fatalf("issue 1: %v", err)
	}
	seed1 := readBytes(t, filepath.Join(dir, "cabinets", "d7", "cab-a.nkey"))
	res2, err := issuance.Issue(m, inv, root, dir)
	if err != nil {
		t.Fatalf("issue 2: %v", err)
	}
	seed2 := readBytes(t, filepath.Join(dir, "cabinets", "d7", "cab-a.nkey"))
	if !bytes.Equal(seed1, seed2) {
		t.Error("cabinet user seed changed across runs (must be mint-once)")
	}
	if len(res2.Cabinets) != 1 || res2.Cabinets[0].Minted {
		t.Errorf("expected cab-a reused on second run, got %+v", res2.Cabinets)
	}
}

func TestIssue_CabinetClientCert(t *testing.T) {
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
		t.Fatalf("Issue: %v", err)
	}
	if !res.CACreated {
		t.Error("first run should report CACreated=true")
	}
	if len(res.Cabinets) != 1 || !res.Cabinets[0].CertMinted {
		t.Fatalf("expected cab-a CertMinted=true, got %+v", res.Cabinets)
	}

	leafPEM := readBytes(t, filepath.Join(dir, "cabinets", "d7", "cab-a.crt"))
	blk, _ := pem.Decode(leafPEM)
	if blk == nil {
		t.Fatal("leaf cert PEM did not decode")
	}
	leaf, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	if leaf.Subject.CommonName != "cab-a" {
		t.Errorf("CN = %q, want cab-a", leaf.Subject.CommonName)
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
	caPEM := readBytes(t, filepath.Join(dir, "ca", "cabinet-ca.crt"))
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("append CA failed")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Errorf("leaf does not verify against cabinet CA: %v", err)
	}

	if fi, _ := os.Stat(filepath.Join(dir, "cabinets", "d7", "cab-a.key")); fi.Mode().Perm() != 0o600 {
		t.Errorf("leaf key mode = %v, want 0600", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Join(dir, "ca", "cabinet-ca.key")); fi.Mode().Perm() != 0o600 {
		t.Errorf("CA key mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestIssue_CabinetCertIdempotent(t *testing.T) {
	dir := credsDir(t)
	root, _ := topology.Load("../../examples/exdot-shared.json")
	m, _ := accounts.Build(root)
	inv := &fleet.Inventory{DOT: "exdot", Cabinets: []fleet.Cabinet{
		{ID: "cab-a", Partition: "d7/0", Filter: "vikasa.exdot.d7.a.>"},
	}}
	if _, err := issuance.Issue(m, inv, root, dir); err != nil {
		t.Fatalf("issue 1: %v", err)
	}
	caKey1 := readBytes(t, filepath.Join(dir, "ca", "cabinet-ca.key"))
	leafKey1 := readBytes(t, filepath.Join(dir, "cabinets", "d7", "cab-a.key"))

	res2, err := issuance.Issue(m, inv, root, dir)
	if err != nil {
		t.Fatalf("issue 2: %v", err)
	}
	if res2.CACreated {
		t.Error("second run should report CACreated=false")
	}
	if len(res2.Cabinets) != 1 || res2.Cabinets[0].CertMinted {
		t.Errorf("expected cab-a CertMinted=false on re-run, got %+v", res2.Cabinets)
	}
	if !bytes.Equal(caKey1, readBytes(t, filepath.Join(dir, "ca", "cabinet-ca.key"))) {
		t.Error("CA key changed across runs (must be mint-once)")
	}
	if !bytes.Equal(leafKey1, readBytes(t, filepath.Join(dir, "cabinets", "d7", "cab-a.key"))) {
		t.Error("leaf key changed across runs (must be mint-once)")
	}
}

func userPubFromCreds(t *testing.T, dir, district, id string) string {
	t.Helper()
	data := readBytes(t, filepath.Join(dir, "cabinets", district, id+".creds"))
	tok, err := jwt.ParseDecoratedJWT(data)
	if err != nil {
		t.Fatalf("parse creds: %v", err)
	}
	uc, err := jwt.DecodeUserClaims(tok)
	if err != nil {
		t.Fatalf("decode user: %v", err)
	}
	return uc.Subject
}

func certSerial(t *testing.T, dir, district, id string) string {
	t.Helper()
	data := readBytes(t, filepath.Join(dir, "cabinets", district, id+".crt"))
	blk, _ := pem.Decode(data)
	if blk == nil {
		t.Fatal("cert pem decode failed")
	}
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return cert.SerialNumber.Text(16)
}

func readRetired(t *testing.T, dir string) []map[string]string {
	t.Helper()
	data := readBytes(t, filepath.Join(dir, "revocations", "retired.jsonl"))
	var out []map[string]string
	for ln := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		if ln == "" {
			continue
		}
		var m map[string]string
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Fatalf("unmarshal retired line: %v", err)
		}
		out = append(out, m)
	}
	return out
}

func TestIssue_RotateGeneratesNewCreds(t *testing.T) {
	dir := credsDir(t)
	root, _ := topology.Load("../../examples/exdot-shared.json")
	m, _ := accounts.Build(root)
	inv := &fleet.Inventory{DOT: "exdot", Cabinets: []fleet.Cabinet{
		{ID: "cab-a", Partition: "d7/0", Filter: "vikasa.exdot.d7.a.>"},
		{ID: "cab-b", Partition: "d7/0", Filter: "vikasa.exdot.d7.b.>"},
	}}
	if _, err := issuance.Issue(m, inv, root, dir); err != nil {
		t.Fatalf("issue: %v", err)
	}
	oldPubA := userPubFromCreds(t, dir, "d7", "cab-a")
	oldSerialA := certSerial(t, dir, "d7", "cab-a")
	nkeyA1 := readBytes(t, filepath.Join(dir, "cabinets", "d7", "cab-a.nkey"))
	nkeyB1 := readBytes(t, filepath.Join(dir, "cabinets", "d7", "cab-b.nkey"))

	res, err := issuance.IssueWithRotation(m, inv, root, dir, issuance.RotationSpec{Cabinets: []string{"cab-a"}})
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if len(res.Rotated) != 1 || res.Rotated[0] != "cab-a" {
		t.Errorf("Rotated = %v, want [cab-a]", res.Rotated)
	}
	if bytes.Equal(nkeyA1, readBytes(t, filepath.Join(dir, "cabinets", "d7", "cab-a.nkey"))) {
		t.Error("cab-a nkey unchanged after rotation (should be new)")
	}
	if userPubFromCreds(t, dir, "d7", "cab-a") == oldPubA {
		t.Error("cab-a user pubkey unchanged after rotation")
	}
	if certSerial(t, dir, "d7", "cab-a") == oldSerialA {
		t.Error("cab-a cert serial unchanged after rotation")
	}
	if !bytes.Equal(nkeyB1, readBytes(t, filepath.Join(dir, "cabinets", "d7", "cab-b.nkey"))) {
		t.Error("cab-b nkey changed (rotation must be targeted)")
	}

	lines := readRetired(t, dir)
	if len(lines) != 1 {
		t.Fatalf("retired.jsonl has %d lines, want 1", len(lines))
	}
	if lines[0]["cabinet"] != "cab-a" || lines[0]["old_user_pubkey"] != oldPubA || lines[0]["old_cert_serial"] != oldSerialA {
		t.Errorf("retired record mismatch: %+v (want old pub %s serial %s)", lines[0], oldPubA, oldSerialA)
	}
	if lines[0]["account"] != "DISTRICT_D7" {
		t.Errorf("retired account = %v, want DISTRICT_D7", lines[0]["account"])
	}
}

func TestIssue_RotateUnknownCabinetFailsClosed(t *testing.T) {
	dir := credsDir(t)
	root, _ := topology.Load("../../examples/exdot-shared.json")
	m, _ := accounts.Build(root)
	inv := &fleet.Inventory{DOT: "exdot", Cabinets: []fleet.Cabinet{
		{ID: "cab-a", Partition: "d7/0", Filter: "vikasa.exdot.d7.a.>"},
	}}
	_, err := issuance.IssueWithRotation(m, inv, root, dir, issuance.RotationSpec{Cabinets: []string{"nope"}})
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("expected error naming nope, got %v", err)
	}
}

func TestIssue_RotateRequiresInventory(t *testing.T) {
	dir := credsDir(t)
	root, _ := topology.Load("../../examples/exdot-shared.json")
	m, _ := accounts.Build(root)
	if _, err := issuance.IssueWithRotation(m, nil, nil, dir, issuance.RotationSpec{Cabinets: []string{"cab-a"}}); err == nil {
		t.Fatal("expected error rotating with no inventory")
	}
}

func TestIssue_RotateAppendsAcrossRuns(t *testing.T) {
	dir := credsDir(t)
	root, _ := topology.Load("../../examples/exdot-shared.json")
	m, _ := accounts.Build(root)
	inv := &fleet.Inventory{DOT: "exdot", Cabinets: []fleet.Cabinet{
		{ID: "cab-a", Partition: "d7/0", Filter: "vikasa.exdot.d7.a.>"},
	}}
	if _, err := issuance.Issue(m, inv, root, dir); err != nil {
		t.Fatalf("issue: %v", err)
	}
	pub0 := userPubFromCreds(t, dir, "d7", "cab-a")
	if _, err := issuance.IssueWithRotation(m, inv, root, dir, issuance.RotationSpec{Cabinets: []string{"cab-a"}}); err != nil {
		t.Fatalf("rotate 1: %v", err)
	}
	pub1 := userPubFromCreds(t, dir, "d7", "cab-a")
	if _, err := issuance.IssueWithRotation(m, inv, root, dir, issuance.RotationSpec{Cabinets: []string{"cab-a"}}); err != nil {
		t.Fatalf("rotate 2: %v", err)
	}
	lines := readRetired(t, dir)
	if len(lines) != 2 {
		t.Fatalf("retired.jsonl has %d lines, want 2", len(lines))
	}
	if lines[0]["old_user_pubkey"] != pub0 {
		t.Errorf("line 0 old pub = %s, want original %s", lines[0]["old_user_pubkey"], pub0)
	}
	if lines[1]["old_user_pubkey"] != pub1 {
		t.Errorf("line 1 old pub = %s, want first-rotation pub %s", lines[1]["old_user_pubkey"], pub1)
	}
}

func accountPub(t *testing.T, res *issuance.Result, name string) string {
	t.Helper()
	for _, a := range res.Accounts {
		if a.Name == name {
			return a.Pub
		}
	}
	t.Fatalf("account %s not in result", name)
	return ""
}

func accountRevocations(t *testing.T, dir, accountPub string) jwt.RevocationList {
	t.Helper()
	ac, err := jwt.DecodeAccountClaims(string(readBytes(t, filepath.Join(dir, "resolver", accountPub+".jwt"))))
	if err != nil {
		t.Fatalf("decode account jwt: %v", err)
	}
	return ac.Revocations
}

func TestIssue_RotateRevokesOldCred(t *testing.T) {
	dir := credsDir(t)
	root, _ := topology.Load("../../examples/exdot-shared.json")
	m, _ := accounts.Build(root)
	inv := &fleet.Inventory{DOT: "exdot", Cabinets: []fleet.Cabinet{
		{ID: "cab-a", Partition: "d7/0", Filter: "vikasa.exdot.d7.a.>"},
	}}
	if _, err := issuance.Issue(m, inv, root, dir); err != nil {
		t.Fatalf("issue: %v", err)
	}
	oldPub := userPubFromCreds(t, dir, "d7", "cab-a")

	res, err := issuance.IssueWithRotation(m, inv, root, dir, issuance.RotationSpec{Cabinets: []string{"cab-a"}})
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if res.Revoked < 1 {
		t.Errorf("Revoked = %d, want >= 1", res.Revoked)
	}
	d7Pub := accountPub(t, res, "DISTRICT_D7")
	revs := accountRevocations(t, dir, d7Pub)
	if _, ok := revs[oldPub]; !ok {
		t.Errorf("account revocations %v missing old pubkey %s", revs, oldPub)
	}
	newPub := userPubFromCreds(t, dir, "d7", "cab-a")
	if _, ok := revs[newPub]; ok {
		t.Errorf("new cred pubkey %s must NOT be revoked", newPub)
	}
}

func TestIssue_RevocationPersistsAcrossRuns(t *testing.T) {
	dir := credsDir(t)
	root, _ := topology.Load("../../examples/exdot-shared.json")
	m, _ := accounts.Build(root)
	inv := &fleet.Inventory{DOT: "exdot", Cabinets: []fleet.Cabinet{
		{ID: "cab-a", Partition: "d7/0", Filter: "vikasa.exdot.d7.a.>"},
	}}
	if _, err := issuance.Issue(m, inv, root, dir); err != nil {
		t.Fatalf("issue: %v", err)
	}
	oldPub := userPubFromCreds(t, dir, "d7", "cab-a")
	if _, err := issuance.IssueWithRotation(m, inv, root, dir, issuance.RotationSpec{Cabinets: []string{"cab-a"}}); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	res, err := issuance.Issue(m, inv, root, dir) // plain re-issue
	if err != nil {
		t.Fatalf("re-issue: %v", err)
	}
	d7Pub := accountPub(t, res, "DISTRICT_D7")
	if _, ok := accountRevocations(t, dir, d7Pub)[oldPub]; !ok {
		t.Errorf("revocation for %s not persisted on plain re-issue", oldPub)
	}
}

func TestIssue_TolerateMalformedRetiredLine(t *testing.T) {
	dir := credsDir(t)
	root, _ := topology.Load("../../examples/exdot-shared.json")
	m, _ := accounts.Build(root)
	inv := &fleet.Inventory{DOT: "exdot", Cabinets: []fleet.Cabinet{
		{ID: "cab-a", Partition: "d7/0", Filter: "vikasa.exdot.d7.a.>"},
	}}
	if _, err := issuance.Issue(m, inv, root, dir); err != nil {
		t.Fatalf("issue: %v", err)
	}
	oldPub := userPubFromCreds(t, dir, "d7", "cab-a")
	if _, err := issuance.IssueWithRotation(m, inv, root, dir, issuance.RotationSpec{Cabinets: []string{"cab-a"}}); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "revocations", "retired.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open retired: %v", err)
	}
	if _, err := f.WriteString(`{ "cabinet": "trunc`); err != nil {
		t.Fatalf("write: %v", err)
	}
	f.Close()

	res, err := issuance.Issue(m, inv, root, dir)
	if err != nil {
		t.Fatalf("re-issue with malformed line must not fail: %v", err)
	}
	d7Pub := accountPub(t, res, "DISTRICT_D7")
	if _, ok := accountRevocations(t, dir, d7Pub)[oldPub]; !ok {
		t.Errorf("valid revocation %s lost due to malformed line", oldPub)
	}
}

func TestIssue_NoRevocationsWhenNoLog(t *testing.T) {
	dir := credsDir(t)
	root, _ := topology.Load("../../examples/exdot-shared.json")
	m, _ := accounts.Build(root)
	inv := &fleet.Inventory{DOT: "exdot", Cabinets: []fleet.Cabinet{
		{ID: "cab-a", Partition: "d7/0", Filter: "vikasa.exdot.d7.a.>"},
	}}
	res, err := issuance.Issue(m, inv, root, dir)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if res.Revoked != 0 {
		t.Errorf("Revoked = %d, want 0 (no retired log)", res.Revoked)
	}
	d7Pub := accountPub(t, res, "DISTRICT_D7")
	if n := len(accountRevocations(t, dir, d7Pub)); n != 0 {
		t.Errorf("expected empty revocations, got %d", n)
	}
}

func TestIssue_RotateOperatorSK(t *testing.T) {
	dir := credsDir(t)
	root, err := topology.Load("../../examples/exdot-shared.json")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	m, err := accounts.Build(root)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	res1, err := issuance.Issue(m, nil, nil, dir)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	oldSKSeed := readBytes(t, filepath.Join(dir, "operator-sk.nkey"))
	oldSKPub := res1.OperatorSigningPub

	res2, err := issuance.IssueWithRotation(m, nil, nil, dir, issuance.RotationSpec{OperatorSK: true})
	if err != nil {
		t.Fatalf("IssueWithRotation rotate-sk: %v", err)
	}
	if !res2.OperatorSigningRotated {
		t.Error("OperatorSigningRotated = false, want true")
	}
	if bytes.Equal(oldSKSeed, readBytes(t, filepath.Join(dir, "operator-sk.nkey"))) {
		t.Error("operator-sk.nkey seed unchanged after rotation (must be re-keyed)")
	}
	if res2.OperatorSigningPub == oldSKPub {
		t.Errorf("operator SK pub unchanged after rotation: %q", oldSKPub)
	}

	opClaims, err := jwt.DecodeOperatorClaims(string(readBytes(t, filepath.Join(dir, "operator.jwt"))))
	if err != nil {
		t.Fatalf("decode operator: %v", err)
	}
	if !opClaims.SigningKeys.Contains(res2.OperatorSigningPub) {
		t.Error("operator JWT SigningKeys missing the new SK")
	}
	if opClaims.SigningKeys.Contains(oldSKPub) {
		t.Error("operator JWT still lists the retired old SK (immediate swap expected)")
	}

	var d7Pub string
	for _, a := range res2.Accounts {
		if a.Name == "DISTRICT_D7" {
			d7Pub = a.Pub
		}
	}
	d7, err := jwt.DecodeAccountClaims(string(readBytes(t, filepath.Join(dir, "resolver", d7Pub+".jwt"))))
	if err != nil {
		t.Fatalf("decode d7: %v", err)
	}
	if d7.Issuer != res2.OperatorSigningPub {
		t.Errorf("account issuer = %q, want new SK %q", d7.Issuer, res2.OperatorSigningPub)
	}
	if !opClaims.DidSign(d7) {
		t.Error("rotated operator claims did not sign the account JWT")
	}

	logData := readBytes(t, filepath.Join(dir, "revocations", "retired-operator-sk.jsonl"))
	lines := strings.Split(strings.TrimSpace(string(logData)), "\n")
	if len(lines) != 1 {
		t.Fatalf("retired-operator-sk.jsonl: got %d lines, want 1: %q", len(lines), logData)
	}
	var rec map[string]string
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("parse audit line: %v (line=%q)", err, lines[0])
	}
	if rec["old_operator_sk_pubkey"] != oldSKPub {
		t.Errorf("audit old_operator_sk_pubkey = %q, want %q", rec["old_operator_sk_pubkey"], oldSKPub)
	}
	if _, err := time.Parse(time.RFC3339, rec["rotated_at"]); err != nil {
		t.Errorf("audit rotated_at %q not RFC3339: %v", rec["rotated_at"], err)
	}
}

func TestIssue_RotateOperatorSK_FirstRunSafe(t *testing.T) {
	dir := credsDir(t)
	root, err := topology.Load("../../examples/exdot-shared.json")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	m, err := accounts.Build(root)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	res, err := issuance.IssueWithRotation(m, nil, nil, dir, issuance.RotationSpec{OperatorSK: true})
	if err != nil {
		t.Fatalf("first-run rotate-sk should succeed: %v", err)
	}
	if res.OperatorSigningPub == "" {
		t.Error("no SK minted on first-run rotation")
	}
	if _, err := os.Stat(filepath.Join(dir, "revocations", "retired-operator-sk.jsonl")); err == nil {
		t.Error("retired-operator-sk.jsonl should not exist on first-run rotation (nothing to retire)")
	}
}

func TestIssue_RotateOperatorSK_AppendsAcrossRuns(t *testing.T) {
	dir := credsDir(t)
	root, err := topology.Load("../../examples/exdot-shared.json")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	m, err := accounts.Build(root)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	res0, err := issuance.Issue(m, nil, nil, dir)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	skA := res0.OperatorSigningPub

	res1, err := issuance.IssueWithRotation(m, nil, nil, dir, issuance.RotationSpec{OperatorSK: true})
	if err != nil {
		t.Fatalf("rotate 1: %v", err)
	}
	skB := res1.OperatorSigningPub

	if _, err := issuance.IssueWithRotation(m, nil, nil, dir, issuance.RotationSpec{OperatorSK: true}); err != nil {
		t.Fatalf("rotate 2: %v", err)
	}

	logData := readBytes(t, filepath.Join(dir, "revocations", "retired-operator-sk.jsonl"))
	lines := strings.Split(strings.TrimSpace(string(logData)), "\n")
	if len(lines) != 2 {
		t.Fatalf("audit log: got %d lines, want 2 (one per rotation): %q", len(lines), logData)
	}
	var got []string
	for _, ln := range lines {
		var rec map[string]string
		if err := json.Unmarshal([]byte(ln), &rec); err != nil {
			t.Fatalf("parse %q: %v", ln, err)
		}
		got = append(got, rec["old_operator_sk_pubkey"])
	}
	if got[0] != skA || got[1] != skB {
		t.Errorf("retired pubs = %v, want [%s %s] (the two superseded keys, in order)", got, skA, skB)
	}
}

func TestIssue_CabinetCredExpiry(t *testing.T) {
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
	before := time.Now()
	res, err := issuance.Issue(m, inv, root, dir)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	after := time.Now()

	// User (cabinet) JWT must now carry a ~90-day expiry.
	credsBytes, err := os.ReadFile(filepath.Join(dir, "cabinets", "d7", "cab-a.creds"))
	if err != nil {
		t.Fatalf("read creds: %v", err)
	}
	jwtStr, err := jwt.ParseDecoratedJWT(credsBytes)
	if err != nil {
		t.Fatalf("parse decorated jwt: %v", err)
	}
	uc, err := jwt.DecodeUserClaims(jwtStr)
	if err != nil {
		t.Fatalf("decode user claims: %v", err)
	}
	if uc.Expires == 0 {
		t.Fatal("user JWT has no expiry; want ~90 days")
	}
	const ttl = 90 * 24 * time.Hour
	lo := before.Add(ttl).Unix()
	hi := after.Add(ttl).Unix()
	if uc.Expires < lo || uc.Expires > hi {
		t.Errorf("user JWT Expires = %d, want within [%d, %d] (~now+90d)", uc.Expires, lo, hi)
	}

	// The trust chain must stay non-expiring: operator + the DISTRICT_D7 account JWT.
	opClaims, err := jwt.DecodeOperatorClaims(string(readBytes(t, filepath.Join(dir, "operator.jwt"))))
	if err != nil {
		t.Fatalf("decode operator: %v", err)
	}
	if opClaims.Expires != 0 {
		t.Errorf("operator JWT Expires = %d, want 0 (non-expiring)", opClaims.Expires)
	}
	var d7Pub string
	for _, a := range res.Accounts {
		if a.Name == "DISTRICT_D7" {
			d7Pub = a.Pub
		}
	}
	if d7Pub == "" {
		t.Fatal("DISTRICT_D7 account not minted")
	}
	acClaims, err := jwt.DecodeAccountClaims(string(readBytes(t, filepath.Join(dir, "resolver", d7Pub+".jwt"))))
	if err != nil {
		t.Fatalf("decode account: %v", err)
	}
	if acClaims.Expires != 0 {
		t.Errorf("account JWT Expires = %d, want 0 (non-expiring)", acClaims.Expires)
	}
}

func TestIssue_RotateAccountSK(t *testing.T) {
	dir := credsDir(t)
	root, _ := topology.Load("../../examples/exdot-shared.json")
	m, _ := accounts.Build(root)
	inv := &fleet.Inventory{DOT: "exdot", Cabinets: []fleet.Cabinet{
		{ID: "cab-a", Partition: "d7/0", Filter: "vikasa.exdot.d7.a.>"},
	}}
	res1, err := issuance.Issue(m, inv, root, dir)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	// Record account D7's SK pub + seed, and another account's SK seed (scoping).
	var d7Pub, d7SK1, otherName, otherPub, otherSK1 string
	for _, a := range res1.Accounts {
		if a.Name == "DISTRICT_D7" {
			d7Pub, d7SK1 = a.Pub, a.SigningPub
		} else if otherName == "" {
			otherName, otherPub, otherSK1 = a.Name, a.Pub, a.SigningPub
		}
	}
	if d7Pub == "" || otherName == "" {
		t.Fatalf("test requires DISTRICT_D7 and at least one other account, got %+v", res1.Accounts)
	}
	d7SKSeed1 := readBytes(t, filepath.Join(dir, "accounts", "DISTRICT_D7-sk.nkey"))
	otherSKSeed1 := readBytes(t, filepath.Join(dir, "accounts", otherName+"-sk.nkey"))
	userIssuer1 := userIssuerFromCreds(t, dir, "d7", "cab-a")
	if userIssuer1 != d7SK1 {
		t.Fatalf("precondition: user JWT issuer %q != D7 SK %q", userIssuer1, d7SK1)
	}

	res2, err := issuance.IssueWithRotation(m, inv, root, dir, issuance.RotationSpec{AccountSK: []string{"DISTRICT_D7"}})
	if err != nil {
		t.Fatalf("rotate-account-sk: %v", err)
	}

	// (a) D7's SK re-keyed; the other account's SK untouched (scoped).
	var d7SK2 string
	var d7Rotated, d7Minted, otherRotated bool
	for _, a := range res2.Accounts {
		switch a.Name {
		case "DISTRICT_D7":
			d7SK2, d7Rotated, d7Minted = a.SigningPub, a.SigningRotated, a.SigningMinted
		case otherName:
			otherRotated = a.SigningRotated
		}
	}
	if !d7Rotated {
		t.Error("DISTRICT_D7 SigningRotated = false, want true")
	}
	if !d7Minted {
		t.Error("DISTRICT_D7 SigningMinted = false, want true (force-mint re-keys = minted)")
	}
	if otherRotated {
		t.Errorf("%s SigningRotated = true, want false (rotation must be scoped)", otherName)
	}
	if bytes.Equal(d7SKSeed1, readBytes(t, filepath.Join(dir, "accounts", "DISTRICT_D7-sk.nkey"))) {
		t.Error("DISTRICT_D7-sk.nkey seed unchanged after rotation (must be re-keyed)")
	}
	if d7SK2 == d7SK1 {
		t.Errorf("DISTRICT_D7 SK pub unchanged after rotation: %q", d7SK1)
	}
	if !bytes.Equal(otherSKSeed1, readBytes(t, filepath.Join(dir, "accounts", otherName+"-sk.nkey"))) {
		t.Errorf("%s SK seed changed (rotation must be targeted)", otherName)
	}

	// (b) D7 account JWT lists only the new SK (immediate swap) and revokes nothing.
	d7 := decodeAccount(t, dir, d7Pub)
	if !d7.SigningKeys.Contains(d7SK2) {
		t.Error("D7 account JWT SigningKeys missing the new SK")
	}
	if d7.SigningKeys.Contains(d7SK1) {
		t.Error("D7 account JWT still lists the retired old SK (immediate swap expected)")
	}
	if len(d7.Revocations) != 0 {
		t.Errorf("account-SK rotation must not revoke users; Revocations = %v", d7.Revocations)
	}

	// (c) D7's user JWT re-signed by the new SK and validates under the rotated account.
	userIssuer2 := userIssuerFromCreds(t, dir, "d7", "cab-a")
	if userIssuer2 != d7SK2 {
		t.Errorf("user JWT issuer = %q, want new SK %q", userIssuer2, d7SK2)
	}
	uc := userClaimsFromCreds(t, dir, "d7", "cab-a")
	if !d7.DidSign(uc) {
		t.Error("rotated D7 account claims did not sign the user JWT")
	}

	// (d) Audit log has exactly D7's old SK pub.
	logData := readBytes(t, filepath.Join(dir, "revocations", "retired-account-sk.jsonl"))
	lines := strings.Split(strings.TrimSpace(string(logData)), "\n")
	if len(lines) != 1 {
		t.Fatalf("retired-account-sk.jsonl: got %d lines, want 1: %q", len(lines), logData)
	}
	var rec map[string]string
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("parse audit line: %v (line=%q)", err, lines[0])
	}
	if rec["account"] != "DISTRICT_D7" || rec["old_account_sk_pubkey"] != d7SK1 {
		t.Errorf("audit record = %+v, want account DISTRICT_D7 old SK %s", rec, d7SK1)
	}
	if _, err := time.Parse(time.RFC3339, rec["rotated_at"]); err != nil {
		t.Errorf("audit rotated_at %q not RFC3339: %v", rec["rotated_at"], err)
	}
	otherAcct := decodeAccount(t, dir, otherPub)
	if !otherAcct.SigningKeys.Contains(otherSK1) {
		t.Errorf("%s account JWT no longer lists its original SK %q (untargeted account must be unchanged)", otherName, otherSK1)
	}
}

// userIssuerFromCreds returns the issuer (signing key pub) of a cabinet's user JWT.
func userIssuerFromCreds(t *testing.T, dir, district, id string) string {
	t.Helper()
	return userClaimsFromCreds(t, dir, district, id).Issuer
}

// userClaimsFromCreds decodes a cabinet's user JWT from its .creds file.
func userClaimsFromCreds(t *testing.T, dir, district, id string) *jwt.UserClaims {
	t.Helper()
	data := readBytes(t, filepath.Join(dir, "cabinets", district, id+".creds"))
	tok, err := jwt.ParseDecoratedJWT(data)
	if err != nil {
		t.Fatalf("parse creds: %v", err)
	}
	uc, err := jwt.DecodeUserClaims(tok)
	if err != nil {
		t.Fatalf("decode user: %v", err)
	}
	return uc
}

// decodeAccount decodes an account JWT from the resolver dir by account pub.
func decodeAccount(t *testing.T, dir, pub string) *jwt.AccountClaims {
	t.Helper()
	ac, err := jwt.DecodeAccountClaims(string(readBytes(t, filepath.Join(dir, "resolver", pub+".jwt"))))
	if err != nil {
		t.Fatalf("decode account %s: %v", pub, err)
	}
	return ac
}

func TestIssue_RotateAccountSK_FirstRunSafe(t *testing.T) {
	dir := credsDir(t)
	root, _ := topology.Load("../../examples/exdot-shared.json")
	m, _ := accounts.Build(root)
	res, err := issuance.IssueWithRotation(m, nil, nil, dir, issuance.RotationSpec{AccountSK: []string{"DISTRICT_D7"}})
	if err != nil {
		t.Fatalf("first-run rotate-account-sk should succeed: %v", err)
	}
	var rotated bool
	for _, a := range res.Accounts {
		if a.Name == "DISTRICT_D7" {
			rotated = a.SigningRotated
		}
	}
	if !rotated {
		t.Error("DISTRICT_D7 SigningRotated = false on first-run rotation")
	}
	if _, err := os.Stat(filepath.Join(dir, "revocations", "retired-account-sk.jsonl")); err == nil {
		t.Error("retired-account-sk.jsonl should not exist on first-run rotation (nothing to retire)")
	}
}

func TestIssue_RotateAccountSK_AppendsAcrossRuns(t *testing.T) {
	dir := credsDir(t)
	root, _ := topology.Load("../../examples/exdot-shared.json")
	m, _ := accounts.Build(root)

	res0, err := issuance.Issue(m, nil, nil, dir)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	var skA string
	for _, a := range res0.Accounts {
		if a.Name == "DISTRICT_D7" {
			skA = a.SigningPub
		}
	}
	res1, err := issuance.IssueWithRotation(m, nil, nil, dir, issuance.RotationSpec{AccountSK: []string{"DISTRICT_D7"}})
	if err != nil {
		t.Fatalf("rotate 1: %v", err)
	}
	var skB string
	for _, a := range res1.Accounts {
		if a.Name == "DISTRICT_D7" {
			skB = a.SigningPub
		}
	}
	if _, err := issuance.IssueWithRotation(m, nil, nil, dir, issuance.RotationSpec{AccountSK: []string{"DISTRICT_D7"}}); err != nil {
		t.Fatalf("rotate 2: %v", err)
	}

	logData := readBytes(t, filepath.Join(dir, "revocations", "retired-account-sk.jsonl"))
	lines := strings.Split(strings.TrimSpace(string(logData)), "\n")
	if len(lines) != 2 {
		t.Fatalf("audit log: got %d lines, want 2 (one per rotation): %q", len(lines), logData)
	}
	var got [2]map[string]string
	for i, ln := range lines {
		if err := json.Unmarshal([]byte(ln), &got[i]); err != nil {
			t.Fatalf("parse audit line %d: %v (line=%q)", i, err, ln)
		}
	}
	if got[0]["old_account_sk_pubkey"] != skA || got[1]["old_account_sk_pubkey"] != skB {
		t.Errorf("retired pubs = [%s %s], want [%s %s] (append-only, in rotation order)",
			got[0]["old_account_sk_pubkey"], got[1]["old_account_sk_pubkey"], skA, skB)
	}
	if got[0]["account"] != "DISTRICT_D7" || got[1]["account"] != "DISTRICT_D7" {
		t.Errorf("retired accounts = [%s %s], want both DISTRICT_D7", got[0]["account"], got[1]["account"])
	}
}

func TestIssue_RotateAccountSK_UnknownNameFailsClosed(t *testing.T) {
	dir := credsDir(t)
	root, _ := topology.Load("../../examples/exdot-shared.json")
	m, _ := accounts.Build(root)
	_, err := issuance.IssueWithRotation(m, nil, nil, dir, issuance.RotationSpec{AccountSK: []string{"DISTRICT_NOPE"}})
	if err == nil || !strings.Contains(err.Error(), "DISTRICT_NOPE") {
		t.Fatalf("expected error naming DISTRICT_NOPE, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "revocations", "retired-account-sk.jsonl")); statErr == nil {
		t.Error("unknown-name rotation must mutate nothing, but wrote the audit log")
	}
}

func TestIssue_AccountSigningKey(t *testing.T) {
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
		t.Fatalf("Issue: %v", err)
	}

	// Locate the DISTRICT_D7 account: root pub, SK pub, mint flag.
	var rootPub, skPub string
	var skMinted bool
	for _, a := range res.Accounts {
		if a.Name == "DISTRICT_D7" {
			rootPub, skPub, skMinted = a.Pub, a.SigningPub, a.SigningMinted
		}
	}
	if rootPub == "" {
		t.Fatal("DISTRICT_D7 account not minted")
	}
	if skPub == "" || skPub == rootPub {
		t.Fatalf("account SK pub %q must be set and distinct from root %q", skPub, rootPub)
	}
	if !skMinted {
		t.Error("account SK should report SigningMinted=true on first run")
	}

	// SK seed file exists, 0600, and is distinct from the root seed file.
	skSeedPath := filepath.Join(dir, "accounts", "DISTRICT_D7-sk.nkey")
	fi, err := os.Stat(skSeedPath)
	if err != nil {
		t.Fatalf("stat %s: %v", skSeedPath, err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("%s mode = %v, want 0600", skSeedPath, fi.Mode().Perm())
	}

	// Account JWT (operator-SK-signed) declares the account SK in SigningKeys.
	acct, err := jwt.DecodeAccountClaims(string(readBytes(t, filepath.Join(dir, "resolver", rootPub+".jwt"))))
	if err != nil {
		t.Fatalf("decode account: %v", err)
	}
	if acct.Issuer != res.OperatorSigningPub {
		t.Errorf("account issuer = %q, want operator SK %q (account JWT must stay operator-SK-signed)", acct.Issuer, res.OperatorSigningPub)
	}
	if !acct.SigningKeys.Contains(skPub) {
		t.Errorf("account JWT SigningKeys %v missing account SK %q", acct.SigningKeys, skPub)
	}

	// User JWT is signed by the account SK, names the root as IssuerAccount,
	// and the account claims accept it (valid trust chain).
	credsBytes := readBytes(t, filepath.Join(dir, "cabinets", "d7", "cab-a.creds"))
	tok, err := jwt.ParseDecoratedJWT(credsBytes)
	if err != nil {
		t.Fatalf("parse decorated jwt: %v", err)
	}
	uc, err := jwt.DecodeUserClaims(tok)
	if err != nil {
		t.Fatalf("decode user claims: %v", err)
	}
	if uc.Issuer != skPub {
		t.Errorf("user JWT issuer = %q, want account SK %q (root must sign nothing)", uc.Issuer, skPub)
	}
	if uc.Issuer == rootPub {
		t.Error("user JWT signed by account ROOT key; want account SK")
	}
	if uc.IssuerAccount != rootPub {
		t.Errorf("user JWT IssuerAccount = %q, want account root %q", uc.IssuerAccount, rootPub)
	}
	if !acct.DidSign(uc) {
		t.Error("account claims did not accept the user JWT (trust chain broken)")
	}

	// Mint-once: a second run reuses the SK seed unchanged.
	res2, err := issuance.Issue(m, inv, root, dir)
	if err != nil {
		t.Fatalf("re-Issue: %v", err)
	}
	for _, a := range res2.Accounts {
		if a.Name == "DISTRICT_D7" {
			if a.SigningMinted {
				t.Error("account SK should report SigningMinted=false on re-run (mint-once)")
			}
			if a.SigningPub != skPub {
				t.Errorf("account SK pub changed across runs: %q != %q", a.SigningPub, skPub)
			}
		}
	}
}

func TestIssue_CabinetFilterOutsideDistrictFailsClosed(t *testing.T) {
	// The issuer must apply the same subject-space boundary as
	// plan.AttachCabinets: a tampered or typo'd inventory filter must never
	// mint a credential wider than the cabinet's district space.
	dir := credsDir(t)
	root, _ := topology.Load("../../examples/exdot-shared.json")
	m, _ := accounts.Build(root)
	for _, filter := range []string{
		">",                     // account-wide (incl. $JS.API.>)
		"vikasa.>",              // every DOT
		"vikasa.exdot.>",        // whole DOT space
		"vikasa.exdot.d8.a.>",   // another district
		"vikasa.exdot.d70.a.>",  // token-boundary sibling of d7
		"vikasa.exdot.atl.d7.>", // outside the default d7 space
	} {
		inv := &fleet.Inventory{DOT: "exdot", Cabinets: []fleet.Cabinet{{ID: "cab-x", Partition: "d7/0", Filter: filter}}}
		if _, err := issuance.Issue(m, inv, root, dir); err == nil || !strings.Contains(err.Error(), "outside district prefix") {
			t.Fatalf("filter %q: expected outside-district-prefix error, got %v", filter, err)
		}
	}
	// An in-scope filter still mints.
	inv := &fleet.Inventory{DOT: "exdot", Cabinets: []fleet.Cabinet{{ID: "cab-x", Partition: "d7/0", Filter: "vikasa.exdot.d7.x.>"}}}
	if _, err := issuance.Issue(m, inv, root, dir); err != nil {
		t.Fatalf("in-scope filter rejected: %v", err)
	}
}

func TestIssue_RejectsLooseCredsDirPerms(t *testing.T) {
	// Every seed below dir is written 0600, but a pre-existing group/world-
	// accessible tree exposes listings and invites mis-permissioned copies:
	// fail closed before writing any secret material.
	dir := filepath.Join(t.TempDir(), "creds")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	root, _ := topology.Load("../../examples/exdot-shared.json")
	m, _ := accounts.Build(root)
	_, err := issuance.Issue(m, nil, root, dir)
	if err == nil || !strings.Contains(err.Error(), "chmod 700") {
		t.Fatalf("expected loose-permissions rejection naming chmod 700, got: %v", err)
	}
	// Nothing may have been written into the loose tree.
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		t.Fatalf("readdir: %v", rerr)
	}
	for _, e := range entries {
		if !e.IsDir() {
			t.Errorf("secret-bearing file %q written despite loose dir perms", e.Name())
		}
	}
	// Tightening the tree makes the same call succeed.
	for _, d := range []string{dir, filepath.Join(dir, "accounts"), filepath.Join(dir, "resolver")} {
		_ = os.Chmod(d, 0o700)
	}
	if _, err := issuance.Issue(m, nil, root, dir); err != nil {
		t.Fatalf("Issue after chmod 700: %v", err)
	}
}

func TestIssue_RejectsLooseCADir(t *testing.T) {
	dir := credsDir(t)
	// Pre-create a group-accessible ca/ so MkdirAll won't tighten it.
	if err := os.MkdirAll(filepath.Join(dir, "ca"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	root, err := topology.Load("../../examples/exdot-shared.json")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	m, err := accounts.Build(root)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	_, err = issuance.Issue(m, nil, nil, dir)
	if err == nil || !strings.Contains(err.Error(), "group/world-accessible") {
		t.Fatalf("expected fail-closed on loose ca/ dir, got: %v", err)
	}
}

func TestIssue_RejectsLooseCabinetsDir(t *testing.T) {
	dir := credsDir(t)
	// Pre-create a group-accessible cabinets/ so MkdirAll won't tighten it.
	if err := os.MkdirAll(filepath.Join(dir, "cabinets"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	root, err := topology.Load("../../examples/exdot-shared.json")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	m, err := accounts.Build(root)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	_, err = issuance.Issue(m, nil, nil, dir)
	if err == nil || !strings.Contains(err.Error(), "group/world-accessible") {
		t.Fatalf("expected fail-closed on loose cabinets/ dir, got: %v", err)
	}
}

// credsDir returns a temp dir tightened to 0700: issuance refuses to write
// secret material into a group/world-accessible tree, and t.TempDir()'s
// per-test subdirectories are created 0755.
func credsDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	if err := os.Chmod(d, 0o700); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	return d
}
