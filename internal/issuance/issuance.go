// Package issuance mints the NATS operator + account trust chain from the B1
// account model (operator keypair + JWT, per-account keypairs + operator-signed
// account JWTs), writing a resolver-ready credential bundle. Keypairs are
// mint-once (reused if their seed exists); account JWTs are re-signed every run.
// Only the .nkey seed files are secret (written 0600); the JWTs, accounts.index,
// and resolver.conf are public material (public keys + signed claims). Callers
// must still keep the whole bundle out of version control since it contains the
// seeds.
package issuance

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"

	"github.com/Vikasa2M/vikasa-infra/internal/accounts"
	"github.com/Vikasa2M/vikasa-infra/internal/fleet"
	"github.com/Vikasa2M/vikasa-infra/internal/naming"
	"github.com/Vikasa2M/vikasa-infra/internal/pki"
	"github.com/Vikasa2M/vikasa-infra/internal/topology"
)

// userJWTValidity is the lifetime of a per-cabinet user JWT. It mirrors
// pki.leafValidity (the 90-day client cert) so a cabinet's identity JWT and its
// transport cert expire — and are re-minted by `cmd/issue -rotate` — together.
const userJWTValidity = 90 * 24 * time.Hour

// AccountResult records the outcome for one account.
type AccountResult struct {
	Name           string
	Pub            string
	Minted         bool   // keypair newly minted this run (vs reused)
	SigningPub     string // account signing-key pub; signs user JWTs for this account
	SigningMinted  bool   // true if the account signing key was minted this run (vs reused)
	SigningRotated bool   // true if the account signing key was force-rotated this run
}

// CabinetResult records the outcome for one cabinet credential.
type CabinetResult struct {
	ID         string
	District   string
	Minted     bool
	CertMinted bool // client-cert leaf key newly minted this run
}

// DMZConsumerResult reports one minted DMZ external-consumer credential.
type DMZConsumerResult struct {
	Consumer string
	Minted   bool     // true if the user keypair was minted this run (vs reused)
	Subjects []string // subscribe scope (sorted)
}

// Result summarizes an Issue run.
type Result struct {
	OperatorPub            string
	OperatorMinted         bool            // true if the operator keypair was minted this run (vs reused)
	OperatorSigningPub     string          // operator signing-key pub; signs account JWTs
	OperatorSigningMinted  bool            // true if the operator signing key was minted this run (vs reused)
	OperatorSigningRotated bool            // true if the operator signing key was force-rotated this run
	Accounts               []AccountResult // in model order
	Cabinets               []CabinetResult
	CACreated              bool                // true if the cabinet client CA was minted this run
	Rotated                []string            // cabinet ids re-keyed this run (rotation)
	Revoked                int                 // user pubkeys folded into account-JWT Revocations this run
	DMZConsumers           []DMZConsumerResult // external-consumer creds minted this run (sorted by Consumer)
}

// RotationSpec selects which credentials IssueWithRotation force-regenerates.
// The zero value rotates nothing (a plain re-issue).
type RotationSpec struct {
	Cabinets   []string // cabinet ids to re-key (capture old user pub + cert serial first)
	OperatorSK bool     // force-rotate the operator signing key (immediate swap)
	AccountSK  []string // account names whose signing key to force-rotate (immediate swap)
}

// Issue mints (or reuses) the operator + account keypairs, signs the operator
// JWT and per-account JWTs from the model, and writes a resolver-ready bundle
// under dir: operator.nkey/jwt, accounts/<NAME>.nkey (seeds), resolver/<PUB>.jwt
// (account JWTs, full-resolver layout), accounts.index, resolver.conf. Keypairs
// are mint-once (reused if their seed exists). Use IssueWithRotation to re-key
// specific cabinets.
func Issue(model *accounts.Model, inv *fleet.Inventory, root *topology.Root, dir string) (*Result, error) {
	return IssueWithRotation(model, inv, root, dir, RotationSpec{})
}

// IssueWithRotation behaves like Issue but force-regenerates the user nkey and
// client cert key for every cabinet id in rotate (instead of reusing them),
// recording each retired cabinet's old PUBLIC identifiers (user pubkey + cert
// serial) to revocations/retired.jsonl first. rotate must name cabinets present
// in inv (else an error); a non-empty rotate with a nil inv is an error.
func IssueWithRotation(model *accounts.Model, inv *fleet.Inventory, root *topology.Root, dir string, spec RotationSpec) (*Result, error) {
	if model == nil {
		return nil, fmt.Errorf("issuance.Issue: nil model")
	}
	rotateSet := make(map[string]struct{}, len(spec.Cabinets))
	for _, id := range spec.Cabinets {
		rotateSet[id] = struct{}{}
	}
	if len(rotateSet) > 0 && inv == nil {
		return nil, fmt.Errorf("issuance.IssueWithRotation: rotation requested but no cabinet inventory provided")
	}
	rotateAccountSKSet := make(map[string]struct{}, len(spec.AccountSK))
	for _, name := range spec.AccountSK {
		rotateAccountSKSet[name] = struct{}{}
	}
	if len(rotateAccountSKSet) > 0 {
		known := make(map[string]struct{}, len(model.Accounts))
		for _, a := range model.Accounts {
			known[a.Name] = struct{}{}
		}
		for name := range rotateAccountSKSet {
			if _, ok := known[name]; !ok {
				return nil, fmt.Errorf("issuance.IssueWithRotation: -rotate-account-sk account %q not in model", name)
			}
		}
	}
	accountsDir := filepath.Join(dir, "accounts")
	resolverDir := filepath.Join(dir, "resolver")
	caDir := filepath.Join(dir, "ca")
	cabinetsDir := filepath.Join(dir, "cabinets")
	for _, d := range []string{dir, accountsDir, resolverDir, caDir, cabinetsDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, fmt.Errorf("issuance.Issue: mkdir %s: %w", d, err)
		}
		// MkdirAll leaves a pre-existing directory's mode untouched; every
		// seed/key below is 0600, but a group/world-accessible tree exposes
		// listings and invites mis-permissioned copies. Fail closed before
		// any secret material is written.
		if err := requireOwnerOnly(d); err != nil {
			return nil, err
		}
	}

	opPub, opMinted, skKP, skPub, skMinted, err := ensureOperator(dir, model.DOT, spec.OperatorSK)
	if err != nil {
		return nil, err
	}
	// The operator signing key signs every account JWT (writeResolverBundle),
	// so it lives for the whole run; the operator root key never leaves
	// ensureOperator.
	defer skKP.Wipe()

	accts, pubByName, err := ensureAccounts(dir, accountsDir, model, rotateAccountSKSet)
	if err != nil {
		return nil, err
	}
	// Account root + signing keys are the trust-chain signers and must not
	// linger: the signing keys sign user JWTs in the cabinet loop, so the
	// sweep runs at function exit (success or error).
	defer func() {
		for _, a := range accts {
			a.kp.Wipe()
			a.skKP.Wipe()
		}
	}()

	res := &Result{OperatorPub: opPub, OperatorMinted: opMinted, OperatorSigningPub: skPub, OperatorSigningMinted: skMinted, OperatorSigningRotated: spec.OperatorSK}

	skByName := make(map[string]nkeys.KeyPair, len(accts))
	for _, a := range accts {
		skByName[a.def.Name] = a.skKP
	}
	if inv != nil {
		if err := mintCabinets(res, model, inv, root, dir, rotateSet, skByName, pubByName); err != nil {
			return nil, err
		}
	}
	if dmzSK, ok := skByName[naming.DMZAccountName()]; ok {
		if err := mintDMZConsumers(res, model, dir, dmzSK, pubByName[naming.DMZAccountName()]); err != nil {
			return nil, err
		}
	}

	if err := writeResolverBundle(res, accts, pubByName, skKP, dir, resolverDir); err != nil {
		return nil, err
	}
	return res, nil
}

// acct is one account's minted key material plus its model definition.
type acct struct {
	def       accounts.Account
	kp        nkeys.KeyPair
	pub       string
	minted    bool
	skKP      nkeys.KeyPair
	skPub     string
	skMinted  bool
	skRotated bool
}

// ensureOperator mints/loads the operator root + signing keys and writes
// operator.jwt. The root key signs only the operator JWT and is wiped before
// returning; the signing key is returned live — the caller owns its wipe
// (it signs every account JWT later in the run).
func ensureOperator(dir, dot string, rotateSK bool) (opPub string, opMinted bool, skKP nkeys.KeyPair, skPub string, skMinted bool, err error) {
	opKP, opMinted, err := ensureKeypair(filepath.Join(dir, "operator.nkey"), nkeys.CreateOperator, false)
	if err != nil {
		return "", false, nil, "", false, fmt.Errorf("issuance.Issue: operator key: %w", err)
	}
	defer opKP.Wipe()
	opPub, err = opKP.PublicKey()
	if err != nil {
		return "", false, nil, "", false, fmt.Errorf("issuance.Issue: operator root key pub: %w", err)
	}
	if rotateSK {
		if err := captureRetiredOperatorSK(dir, time.Now()); err != nil {
			return "", false, nil, "", false, fmt.Errorf("issuance.IssueWithRotation: capture retired operator sk: %w", err)
		}
	}
	skKP, skMinted, err = ensureKeypair(filepath.Join(dir, "operator-sk.nkey"), nkeys.CreateOperator, rotateSK)
	if err != nil {
		return "", false, nil, "", false, fmt.Errorf("issuance.Issue: operator signing key: %w", err)
	}
	skPub, err = skKP.PublicKey()
	if err != nil {
		skKP.Wipe()
		return "", false, nil, "", false, fmt.Errorf("issuance.Issue: operator signing key pub: %w", err)
	}
	oc := jwt.NewOperatorClaims(opPub)
	oc.Name = naming.OperatorName(dot)
	oc.SigningKeys.Add(skPub)
	opJWT, err := oc.Encode(opKP)
	if err != nil {
		skKP.Wipe()
		return "", false, nil, "", false, fmt.Errorf("issuance.Issue: encode operator jwt: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "operator.jwt"), []byte(opJWT), 0o644); err != nil {
		skKP.Wipe()
		return "", false, nil, "", false, err
	}
	return opPub, opMinted, skKP, skPub, skMinted, nil
}

// ensureAccounts mints/loads every account's root + signing keypairs. On error
// each keypair minted so far is wiped before returning; on success the CALLER
// owns the wipes — the signing keys must survive to sign user JWTs.
func ensureAccounts(dir, accountsDir string, model *accounts.Model, rotateAccountSKSet map[string]struct{}) ([]acct, map[string]string, error) {
	accts := make([]acct, 0, len(model.Accounts))
	pubByName := make(map[string]string, len(model.Accounts))
	wipeAll := func() {
		for _, a := range accts {
			a.kp.Wipe()
			a.skKP.Wipe()
		}
	}
	for _, a := range model.Accounts {
		kp, minted, err := ensureKeypair(filepath.Join(accountsDir, a.Name+".nkey"), nkeys.CreateAccount, false)
		if err != nil {
			wipeAll()
			return nil, nil, fmt.Errorf("issuance.Issue: account %s key: %w", a.Name, err)
		}
		pub, err := kp.PublicKey()
		if err != nil {
			kp.Wipe()
			wipeAll()
			return nil, nil, err
		}
		_, rotatingSK := rotateAccountSKSet[a.Name]
		if rotatingSK {
			if err := captureRetiredAccountSK(dir, a.Name, pub, time.Now()); err != nil {
				kp.Wipe()
				wipeAll()
				return nil, nil, fmt.Errorf("issuance.IssueWithRotation: account %s capture retired sk: %w", a.Name, err)
			}
		}
		skKP, skMinted, err := ensureKeypair(filepath.Join(accountsDir, a.Name+"-sk.nkey"), nkeys.CreateAccount, rotatingSK)
		if err != nil {
			kp.Wipe()
			wipeAll()
			return nil, nil, fmt.Errorf("issuance.Issue: account %s signing key: %w", a.Name, err)
		}
		skPub, err := skKP.PublicKey()
		if err != nil {
			kp.Wipe()
			skKP.Wipe()
			wipeAll()
			return nil, nil, fmt.Errorf("issuance.Issue: account %s signing key pub: %w", a.Name, err)
		}
		accts = append(accts, acct{def: a, kp: kp, pub: pub, minted: minted, skKP: skKP, skPub: skPub, skMinted: skMinted, skRotated: rotatingSK})
		pubByName[a.Name] = pub
	}
	return accts, pubByName, nil
}

// mintCabinets validates the inventory against the topology and mints each
// cabinet's credential set, folding results into res.
func mintCabinets(res *Result, model *accounts.Model, inv *fleet.Inventory, root *topology.Root, dir string, rotateSet map[string]struct{}, skByName map[string]nkeys.KeyPair, pubByName map[string]string) error {
	if inv.DOT != model.DOT {
		return fmt.Errorf("issuance.Issue: cabinet inventory dot %q does not match model dot %q", inv.DOT, model.DOT)
	}
	if root == nil || root.Topology == nil {
		return fmt.Errorf("issuance.Issue: cabinets supplied but nil topology")
	}
	known := make(map[string]struct{}, len(inv.Cabinets))
	for _, c := range inv.Cabinets {
		known[c.ID] = struct{}{}
	}
	for id := range rotateSet {
		if _, ok := known[id]; !ok {
			return fmt.Errorf("issuance.IssueWithRotation: -rotate cabinet %q not in inventory", id)
		}
	}
	partToDistrict, err := root.Topology.PartitionIndex()
	if err != nil {
		return fmt.Errorf("issuance.Issue: %w", err)
	}
	cabinetsDir := filepath.Join(dir, "cabinets")
	ca, caCreated, err := pki.LoadOrCreateCA(
		filepath.Join(dir, "ca", "cabinet-ca.crt"),
		filepath.Join(dir, "ca", "cabinet-ca.key"),
	)
	if err != nil {
		return fmt.Errorf("issuance.Issue: cabinet CA: %w", err)
	}
	var signer pki.Signer = ca
	defer signer.Wipe()
	res.CACreated = caCreated

	for _, c := range inv.Cabinets {
		if c.Filter == "" {
			return fmt.Errorf("issuance.Issue: cabinet %q: filter (subject scope) required to mint a scoped credential", c.ID)
		}
		distID, ok := partToDistrict[c.Partition]
		if !ok {
			return fmt.Errorf("issuance.Issue: cabinet %q: partition %q not defined", c.ID, c.Partition)
		}
		// Same boundary rule as plan.AttachCabinets: the filter becomes the
		// JWT's pub+sub allow verbatim, so it must lie inside the cabinet's
		// district subject space — the two tools must agree on inventories.
		space, ok := naming.FilterUnderDistrict(model.DOT, distID, root.Topology.District[distID].SubjectPrefix, c.Filter)
		if !ok {
			return fmt.Errorf("issuance.Issue: cabinet %q: filter %q is outside district prefix %q", c.ID, c.Filter, space)
		}
		acctName := naming.DistrictAccountName(distID)
		acctSKKP, ok := skByName[acctName]
		if !ok {
			return fmt.Errorf("issuance.Issue: cabinet %q: account %q not found in model", c.ID, acctName)
		}
		distDir := filepath.Join(cabinetsDir, distID)
		if err := os.MkdirAll(distDir, 0o700); err != nil {
			return fmt.Errorf("issuance.Issue: mkdir %s: %w", distDir, err)
		}
		if err := requireOwnerOnly(distDir); err != nil {
			return err
		}
		_, rotating := rotateSet[c.ID]
		if rotating {
			rec, rerr := captureRetired(distDir, c.ID, distID, acctName, time.Now())
			if rerr != nil {
				return fmt.Errorf("issuance.IssueWithRotation: cabinet %q capture retired: %w", c.ID, rerr)
			}
			if rec != nil {
				if werr := appendAuditRecord(dir, retiredLogFile, rec); werr != nil {
					return fmt.Errorf("issuance.IssueWithRotation: cabinet %q record retired: %w", c.ID, werr)
				}
			}
			res.Rotated = append(res.Rotated, c.ID)
		}
		minted, certMinted, err := mintCabinet(c, distDir, pubByName[acctName], acctSKKP, signer, rotating)
		if err != nil {
			return err
		}
		res.Cabinets = append(res.Cabinets, CabinetResult{ID: c.ID, District: distID, Minted: minted, CertMinted: certMinted})
	}
	return nil
}

// mintCabinet mints one cabinet's user nkey, scoped JWT, creds file, leaf key,
// and client cert. The user keypair is wiped on every path via a single defer.
func mintCabinet(c fleet.Cabinet, distDir, issuerAccountPub string, acctSK nkeys.KeyPair, signer pki.Signer, rotating bool) (minted, certMinted bool, err error) {
	userKP, minted, err := ensureKeypair(filepath.Join(distDir, c.ID+".nkey"), nkeys.CreateUser, rotating)
	if err != nil {
		return false, false, fmt.Errorf("issuance.Issue: cabinet %q key: %w", c.ID, err)
	}
	defer userKP.Wipe()
	userPub, err := userKP.PublicKey()
	if err != nil {
		return false, false, err
	}
	uc := jwt.NewUserClaims(userPub)
	uc.Name = c.ID
	uc.IssuerAccount = issuerAccountPub
	uc.Permissions = jwt.Permissions{
		Pub: jwt.Permission{Allow: []string{c.Filter}},
		Sub: jwt.Permission{Allow: []string{c.Filter}},
	}
	uc.Expires = time.Now().Add(userJWTValidity).Unix()
	uJWT, err := uc.Encode(acctSK)
	if err != nil {
		return false, false, fmt.Errorf("issuance.Issue: encode cabinet %q jwt: %w", c.ID, err)
	}
	userSeed, err := userKP.Seed()
	if err != nil {
		return false, false, err
	}
	creds, err := jwt.FormatUserConfig(uJWT, userSeed)
	// userSeed is userKP's internal buffer (Seed() returns it directly);
	// FormatUserConfig has copied it into `creds`, so zero it now. userKP
	// must not be used for signing after this.
	wipeBytes(userSeed)
	if err != nil {
		return false, false, fmt.Errorf("issuance.Issue: format cabinet %q creds: %w", c.ID, err)
	}
	defer wipeBytes(creds) // creds embeds the decorated plaintext seed; scrub after the write
	if werr := os.WriteFile(filepath.Join(distDir, c.ID+".creds"), creds, 0o600); werr != nil {
		return false, false, werr
	}
	keyPEM, certMinted, err := ensureLeafKey(filepath.Join(distDir, c.ID+".key"), rotating)
	if err != nil {
		return false, false, fmt.Errorf("issuance.Issue: cabinet %q leaf key: %w", c.ID, err)
	}
	csrDER, err := pki.ClientCSR(c.ID, keyPEM)
	wipeBytes(keyPEM) // key persisted to <id>.key; drop the in-memory copy
	if err != nil {
		return false, false, fmt.Errorf("issuance.Issue: cabinet %q csr: %w", c.ID, err)
	}
	certPEM, err := signer.SignClientCert(csrDER, c.ID)
	if err != nil {
		return false, false, fmt.Errorf("issuance.Issue: cabinet %q cert: %w", c.ID, err)
	}
	if err := os.WriteFile(filepath.Join(distDir, c.ID+".crt"), certPEM, 0o644); err != nil {
		return false, false, err
	}
	return minted, certMinted, nil
}

// mintDMZConsumers mints one subscribe-only, subject-scoped user credential per
// DMZ external consumer, signed by the DMZ account signing key. JWT-only (no
// cert). No-op when the model has no DMZ consumers. Fails closed on a
// group/world-accessible dmz/ dir before writing any secret.
func mintDMZConsumers(res *Result, model *accounts.Model, dir string, dmzSK nkeys.KeyPair, dmzPub string) error {
	consumers := aggregateDMZConsumers(model)
	if len(consumers) == 0 {
		return nil
	}
	dmzDir := filepath.Join(dir, "dmz")
	if err := os.MkdirAll(dmzDir, 0o700); err != nil {
		return fmt.Errorf("issuance.Issue: mkdir %s: %w", dmzDir, err)
	}
	if err := requireOwnerOnly(dmzDir); err != nil {
		return err
	}
	for _, c := range consumers {
		if strings.ContainsAny(c.Name, `/\`) || c.Name == "." || c.Name == ".." {
			return fmt.Errorf("issuance.Issue: dmz consumer %q is not a safe file name", c.Name)
		}
		minted, err := mintDMZConsumer(c, dmzDir, dmzPub, dmzSK)
		if err != nil {
			return err
		}
		res.DMZConsumers = append(res.DMZConsumers, DMZConsumerResult{Consumer: c.Name, Minted: minted, Subjects: c.SubAllow})
	}
	return nil
}

// mintDMZConsumer mints one external consumer's subscribe-only user nkey +
// JWT (signed by the DMZ account signing key) and writes the decorated creds
// file. Unlike mintCabinet there is no client cert: DMZ consumers authenticate
// with the JWT/nkey alone.
func mintDMZConsumer(c dmzConsumer, dmzDir, issuerAccountPub string, acctSK nkeys.KeyPair) (minted bool, err error) {
	if len(c.PubDeny) == 0 {
		return false, fmt.Errorf("issuance.Issue: dmz consumer %q has no publish deny; refusing to mint an allow-all-publish external credential", c.Name)
	}
	userKP, minted, err := ensureKeypair(filepath.Join(dmzDir, c.Name+".nkey"), nkeys.CreateUser, false)
	if err != nil {
		return false, fmt.Errorf("issuance.Issue: dmz consumer %q key: %w", c.Name, err)
	}
	defer userKP.Wipe()
	userPub, err := userKP.PublicKey()
	if err != nil {
		return false, err
	}
	uc := jwt.NewUserClaims(userPub)
	uc.Name = c.Name
	uc.IssuerAccount = issuerAccountPub
	uc.Permissions = jwt.Permissions{
		Sub: jwt.Permission{Allow: c.SubAllow},
		Pub: jwt.Permission{Deny: c.PubDeny},
	}
	uc.Expires = time.Now().Add(userJWTValidity).Unix()
	uJWT, err := uc.Encode(acctSK)
	if err != nil {
		return false, fmt.Errorf("issuance.Issue: encode dmz consumer %q jwt: %w", c.Name, err)
	}
	userSeed, err := userKP.Seed()
	if err != nil {
		return false, err
	}
	creds, err := jwt.FormatUserConfig(uJWT, userSeed)
	wipeBytes(userSeed)
	if err != nil {
		return false, fmt.Errorf("issuance.Issue: format dmz consumer %q creds: %w", c.Name, err)
	}
	defer wipeBytes(creds)
	if werr := os.WriteFile(filepath.Join(dmzDir, c.Name+".creds"), creds, 0o600); werr != nil {
		return false, werr
	}
	return minted, nil
}

// writeResolverBundle folds revocations into each account's JWT, signs it with
// the operator signing key, and writes the resolver directory, accounts.index,
// and resolver.conf.
func writeResolverBundle(res *Result, accts []acct, pubByName map[string]string, operatorSK nkeys.KeyPair, dir, resolverDir string) error {
	revoked, err := loadRevocations(auditLogPath(dir, retiredLogFile))
	if err != nil {
		return fmt.Errorf("issuance.Issue: load revocations: %w", err)
	}
	indexLines := make([]string, 0, len(accts))
	for _, a := range accts {
		ac := jwt.NewAccountClaims(a.pub)
		ac.Name = a.def.Name
		ac.SigningKeys.Add(a.skPub)
		for _, e := range a.def.Exports {
			ac.Exports.Add(&jwt.Export{Name: e.Subject, Subject: jwt.Subject(e.Subject), Type: jwt.Stream})
		}
		for _, im := range a.def.Imports {
			fromPub, ok := pubByName[im.FromAccount]
			if !ok {
				return fmt.Errorf("issuance.Issue: account %q imports from unknown account %q", a.def.Name, im.FromAccount)
			}
			ac.Imports.Add(&jwt.Import{
				Name:    im.Subject,
				Subject: jwt.Subject(im.Subject),
				Account: fromPub,
				Type:    jwt.Stream,
			})
		}
		if a.def.JetStream {
			ac.Limits.JetStreamLimits.MemoryStorage = -1
			ac.Limits.JetStreamLimits.DiskStorage = -1
		}
		for _, e := range revoked[a.def.Name] {
			if e.At.IsZero() {
				ac.Revoke(e.Pub)
			} else {
				ac.RevokeAt(e.Pub, e.At)
			}
			res.Revoked++
		}
		acJWT, err := ac.Encode(operatorSK)
		if err != nil {
			return fmt.Errorf("issuance.Issue: encode account %s jwt: %w", a.def.Name, err)
		}
		if err := os.WriteFile(filepath.Join(resolverDir, a.pub+".jwt"), []byte(acJWT), 0o644); err != nil {
			return err
		}
		res.Accounts = append(res.Accounts, AccountResult{Name: a.def.Name, Pub: a.pub, Minted: a.minted, SigningPub: a.skPub, SigningMinted: a.skMinted, SigningRotated: a.skRotated})
		indexLines = append(indexLines, a.def.Name+" "+a.pub)
	}

	sort.Strings(indexLines)
	if err := os.WriteFile(filepath.Join(dir, "accounts.index"), []byte(strings.Join(indexLines, "\n")+"\n"), 0o644); err != nil {
		return err
	}

	resolverConf := "# AUTOGENERATED by vikasa-infra/cmd/issue — operator-mode trust config.\n" +
		"# Paths are relative to this file's directory.\n" +
		"operator: operator.jwt\n" +
		"resolver: {\n  type: full\n  dir: resolver\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "resolver.conf"), []byte(resolverConf), 0o644); err != nil {
		return err
	}
	return nil
}

// ensureKeypair loads the keypair from seedPath if present (mint-once), else
// mints one via create() and writes the seed 0600. Returns (kp, minted).
// If force is true the reuse branch is skipped and a new keypair is always minted.
func ensureKeypair(seedPath string, create func() (nkeys.KeyPair, error), force bool) (nkeys.KeyPair, bool, error) {
	if !force {
		data, err := os.ReadFile(seedPath)
		switch {
		case err == nil:
			kp, perr := nkeys.FromSeed(data)
			if perr != nil {
				wipeBytes(data)
				return nil, false, fmt.Errorf("parse seed %s: %w", seedPath, perr)
			}
			wipeBytes(data)
			return kp, false, nil
		case !errors.Is(err, os.ErrNotExist):
			return nil, false, fmt.Errorf("read seed %s: %w", seedPath, err)
		}
	}
	kp, err := create()
	if err != nil {
		return nil, false, err
	}
	seedRef, err := kp.Seed()
	if err != nil {
		return nil, false, err
	}
	// kp.Seed() returns the keypair's internal buffer directly; copy before
	// writing so we can zero the copy without corrupting kp.
	seedCopy := append([]byte{}, seedRef...)
	if err := os.WriteFile(seedPath, seedCopy, 0o600); err != nil {
		wipeBytes(seedCopy)
		return nil, false, fmt.Errorf("write seed %s: %w", seedPath, err)
	}
	wipeBytes(seedCopy)
	return kp, true, nil
}

// ensureLeafKey loads the cabinet leaf key PEM at path (mint-once), or generates
// one via pki.NewClientKey and writes it 0600. Returns (keyPEM, minted). The
// returned slice is a standalone copy the caller may wipe.
// If force is true the reuse branch is skipped and a new key is always generated.
func ensureLeafKey(path string, force bool) (keyPEM []byte, minted bool, err error) {
	if !force {
		data, rerr := os.ReadFile(path)
		switch {
		case rerr == nil:
			return data, false, nil
		case !errors.Is(rerr, os.ErrNotExist):
			return nil, false, fmt.Errorf("read leaf key %s: %w", path, rerr)
		}
	}
	keyPEM, err = pki.NewClientKey()
	if err != nil {
		return nil, false, err
	}
	if werr := os.WriteFile(path, keyPEM, 0o600); werr != nil {
		wipeBytes(keyPEM)
		return nil, false, fmt.Errorf("write leaf key %s: %w", path, werr)
	}
	return keyPEM, true, nil
}

// retiredRecord is one rotated-out cabinet credential's public identifiers,
// appended to revocations/retired.jsonl for the revocation slice (B4c-3).
type retiredRecord struct {
	Cabinet       string `json:"cabinet"`
	District      string `json:"district"`
	Account       string `json:"account"`
	OldUserPub    string `json:"old_user_pubkey"`
	OldCertSerial string `json:"old_cert_serial"`
	RotatedAt     string `json:"rotated_at"`
}

// captureRetired reads the cabinet's existing PUBLIC identifiers — the old user
// pubkey (the .creds JWT subject) and the old cert serial (.crt) — before they
// are overwritten by rotation. It never reads a seed. Returns nil if neither
// artifact exists yet (a first-ever mint has nothing to retire).
func captureRetired(distDir, cabinet, district, account string, now time.Time) (*retiredRecord, error) {
	rec := &retiredRecord{
		Cabinet:   cabinet,
		District:  district,
		Account:   account,
		RotatedAt: now.UTC().Format(time.RFC3339),
	}

	credsPath := filepath.Join(distDir, cabinet+".creds")
	switch data, err := os.ReadFile(credsPath); {
	case err == nil:
		tok, perr := jwt.ParseDecoratedJWT(data)
		if perr != nil {
			return nil, fmt.Errorf("parse %s: %w", credsPath, perr)
		}
		uc, derr := jwt.DecodeUserClaims(tok)
		if derr != nil {
			return nil, fmt.Errorf("decode %s: %w", credsPath, derr)
		}
		rec.OldUserPub = uc.Subject
	case !errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("read %s: %w", credsPath, err)
	}

	crtPath := filepath.Join(distDir, cabinet+".crt")
	switch data, err := os.ReadFile(crtPath); {
	case err == nil:
		blk, _ := pem.Decode(data)
		if blk == nil || blk.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("parse %s: invalid certificate PEM", crtPath)
		}
		cert, perr := x509.ParseCertificate(blk.Bytes)
		if perr != nil {
			return nil, fmt.Errorf("parse %s: %w", crtPath, perr)
		}
		rec.OldCertSerial = cert.SerialNumber.Text(16)
	case !errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("read %s: %w", crtPath, err)
	}

	if rec.OldUserPub == "" && rec.OldCertSerial == "" {
		return nil, nil
	}
	return rec, nil
}

// appendJSONLine appends v as one JSON line to path (creating its dir + file, 0644).
func appendJSONLine(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	line, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("append %s: %w", path, err)
	}
	return nil
}

// revokedEntry is one retired user pubkey to fold into its account's Revocations.
type revokedEntry struct {
	Pub string
	At  time.Time
}

// Filenames (under dir's revocations/ directory) for the three append-only
// audit logs rotation writes to. Each log has its own record shape — only
// the path layout and append mechanics are shared, via auditLogPath and
// appendAuditRecord below.
const (
	retiredLogFile           = "retired.jsonl"
	retiredOperatorSKLogFile = "retired-operator-sk.jsonl"
	retiredAccountSKLogFile  = "retired-account-sk.jsonl"
)

// auditLogPath is the single source of truth for the revocations/ log
// layout: every retired-credential/retired-signing-key audit log lives at
// dir/revocations/<name>.
func auditLogPath(dir, name string) string {
	return filepath.Join(dir, "revocations", name)
}

// appendAuditRecord appends rec as one JSON line to the named audit log
// under dir's revocations/ directory (creating the directory + file, 0644).
// It is the single write path shared by the three retired-* audit logs
// (retired cabinet credentials, retired operator signing keys, retired
// account signing keys); each retains its own record type so its on-disk
// JSON shape is unchanged.
func appendAuditRecord(dir, name string, rec any) error {
	return appendJSONLine(auditLogPath(dir, name), rec)
}

// retiredSKRecord is one rotated-out operator signing key's PUBLIC id, appended
// to revocations/retired-operator-sk.jsonl for forensics. Audit-only: an operator
// signing key is untrusted by being dropped from the operator JWT's SigningKeys,
// not via any revocation list — nothing consumes this log.
type retiredSKRecord struct {
	OldPub    string `json:"old_operator_sk_pubkey"`
	RotatedAt string `json:"rotated_at"`
}

// captureRetiredOperatorSK reads the CURRENT operator.jwt (public material only,
// never a seed) and appends each declared signing-key pub to the audit log before
// the key is force-regenerated. A missing operator.jwt (first run) retires nothing.
// A malformed operator.jwt is tolerated (skipped) so a corrupt prior artifact can
// never block a rotation.
func captureRetiredOperatorSK(dir string, now time.Time) error {
	data, err := os.ReadFile(filepath.Join(dir, "operator.jwt"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read operator.jwt: %w", err)
	}
	oc, derr := jwt.DecodeOperatorClaims(string(data))
	if derr != nil {
		// Tolerate a malformed prior operator JWT — never block a rotation —
		// but say so: silently retiring nothing would leave the old signing
		// key out of the revocation record with no audit trail.
		fmt.Fprintf(os.Stderr, "issuance: warning: operator.jwt is malformed (%v); no prior signing key retired\n", derr)
		return nil
	}
	ts := now.UTC().Format(time.RFC3339)
	for _, pub := range oc.SigningKeys {
		if werr := appendAuditRecord(dir, retiredOperatorSKLogFile, retiredSKRecord{OldPub: pub, RotatedAt: ts}); werr != nil {
			return fmt.Errorf("record retired operator sk: %w", werr)
		}
	}
	return nil
}

// retiredAccountSKRecord is one rotated-out account signing key's PUBLIC id,
// appended to revocations/retired-account-sk.jsonl for forensics. Audit-only: an
// account signing key is untrusted by being dropped from its account JWT's
// SigningKeys, not via any revocation list — nothing consumes this log.
type retiredAccountSKRecord struct {
	Account   string `json:"account"`
	OldPub    string `json:"old_account_sk_pubkey"`
	RotatedAt string `json:"rotated_at"`
}

// captureRetiredAccountSK reads the CURRENT account JWT (resolver/<pub>.jwt,
// public material only, never a seed) and appends each declared signing-key pub
// to the audit log before the key is force-regenerated. A missing JWT (first run)
// retires nothing. A malformed JWT is tolerated (skipped) so a corrupt prior
// artifact can never block a rotation.
// AccountClaims.SigningKeys is a map[string]Scope, so its pubkeys are the map
// KEYS — read them via .Keys(). A `for _, v := range` would iterate Scope
// VALUES, not pubkeys (the operator path differs: its SigningKeys is a StringList).
func captureRetiredAccountSK(dir, account, accountPub string, now time.Time) error {
	data, err := os.ReadFile(filepath.Join(dir, "resolver", accountPub+".jwt"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read account jwt %s: %w", account, err)
	}
	ac, derr := jwt.DecodeAccountClaims(string(data))
	if derr != nil {
		// Tolerate a malformed prior account JWT — never block a rotation —
		// but leave an audit trail for the silently-skipped retirement.
		fmt.Fprintf(os.Stderr, "issuance: warning: account jwt %s is malformed (%v); no prior signing key retired\n", account, derr)
		return nil
	}
	ts := now.UTC().Format(time.RFC3339)
	for _, pub := range ac.SigningKeys.Keys() {
		if werr := appendAuditRecord(dir, retiredAccountSKLogFile, retiredAccountSKRecord{Account: account, OldPub: pub, RotatedAt: ts}); werr != nil {
			return fmt.Errorf("record retired account sk: %w", werr)
		}
	}
	return nil
}

// loadRevocations reads the append-only retired-credentials log and returns the
// retired user pubkeys grouped by issuing account name. It is tolerant of a
// blank or malformed line (e.g. a crash-truncated trailing line from
// appendAuditRecord, which is not crash-atomic) — such lines are skipped, not fatal.
// A missing log yields an empty map. Reads only public material.
func loadRevocations(path string) (map[string][]revokedEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string][]revokedEntry{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	out := map[string][]revokedEntry{}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec retiredRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue // tolerate a malformed/truncated line
		}
		if rec.OldUserPub == "" {
			continue
		}
		e := revokedEntry{Pub: rec.OldUserPub}
		if t, perr := time.Parse(time.RFC3339, rec.RotatedAt); perr == nil {
			e.At = t
		}
		out[rec.Account] = append(out[rec.Account], e)
	}
	return out, nil
}

// requireOwnerOnly fails when the directory grants any group/other access —
// the credentials tree must be private to the issuing user.
func requireOwnerOnly(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("issuance.Issue: stat %s: %w", dir, err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("issuance.Issue: credentials dir %s is group/world-accessible (%04o); chmod 700 it before issuing", dir, perm)
	}
	return nil
}

// dmzConsumer is one external consumer's aggregated, scoped subscribe grant.
type dmzConsumer struct {
	Name     string
	SubAllow []string // union of the consumer's share Subscribe subjects (sorted, deduped)
	PubDeny  []string // union of the consumer's PublishDeny (sorted, deduped)
}

// aggregateDMZConsumers groups the DMZ account's per-share user templates by
// consumer (Label), unioning Subscribe → SubAllow and PublishDeny → PubDeny.
// Consumers are sorted by Name; each list sorted+deduped. Returns nil when the
// model has no DMZ account or that account has no users. Aggregating here keeps
// accounts.Build (and its accounts.conf golden) unchanged.
//
// By contract, DMZ consumers are subscribe-allow + publish-deny ONLY: the
// template's Publish and SubscribeDeny fields are intentionally NOT carried into
// the minted credential. Dropping Publish is a protective invariant (an external
// consumer must never gain a publish allow); do not wire those fields in without
// revisiting that guarantee. mintDMZConsumer additionally fails closed if PubDeny
// is empty.
func aggregateDMZConsumers(model *accounts.Model) []dmzConsumer {
	var dmz *accounts.Account
	for i := range model.Accounts {
		if model.Accounts[i].Name == naming.DMZAccountName() {
			dmz = &model.Accounts[i]
			break
		}
	}
	if dmz == nil || len(dmz.Users) == 0 {
		return nil
	}
	subs := map[string]map[string]struct{}{}
	deny := map[string]map[string]struct{}{}
	add := func(m map[string]map[string]struct{}, label string, vals []string) {
		set := m[label]
		if set == nil {
			set = map[string]struct{}{}
			m[label] = set
		}
		for _, v := range vals {
			set[v] = struct{}{}
		}
	}
	for _, u := range dmz.Users {
		add(subs, u.Label, u.Subscribe)
		add(deny, u.Label, u.PublishDeny)
	}
	sortedKeys := func(set map[string]struct{}) []string {
		out := make([]string, 0, len(set))
		for k := range set {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	out := make([]dmzConsumer, 0, len(subs))
	for name := range subs {
		out = append(out, dmzConsumer{Name: name, SubAllow: sortedKeys(subs[name]), PubDeny: sortedKeys(deny[name])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// wipeBytes zeroes a byte slice holding sensitive material (a seed).
func wipeBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
