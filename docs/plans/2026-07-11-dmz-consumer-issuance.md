# DMZ external-consumer issuance — Implementation Plan

**Goal:** `cmd/issue` mints one revocable, subscribe-only, subject-scoped `.creds` per DMZ external consumer (operator/JWT mode), signed by the DMZ account signing key.

**Architecture:** A second consumer of the existing mint path. Decision + rationale: `docs/decisions/2026-07-11-control-plane-issuance-decisions.md`. Driven by the DMZ account's `UserTemplate`s in `accounts.Model`; aggregated per consumer inside `issuance` (no `accounts.Build`/`accounts.conf` change). JWT-only, mint-only.

**Tech Stack:** Go, NATS `nkeys`/`jwt/v2`, existing `internal/issuance` helpers (`ensureKeypair`, `jwt.FormatUserConfig`, `wipeBytes`, `requireOwnerOnly`).

## Global Constraints

- **No `cmd/gen` golden impact** — this is `cmd/issue`/`internal/issuance` only. `make test` golden suite must stay green, `git status` clean.
- **Fail closed** — `dir/dmz` is created `0o700` and `requireOwnerOnly`-checked before any cred is written; `.creds`/`.nkey` are `0o600`.
- **Seed hygiene** — wipe the user seed AND the `creds` buffer after writing (same pattern as `mintCabinet`).
- **Determinism** — consumers sorted by name; each consumer's subject/deny lists sorted+deduped.
- **Honors the model's deny lists** — perms come from the `UserTemplate`, not hardcoded assumptions.
- **Error strings** — prefix `issuance.Issue:` like the surrounding code; no test currently asserts these new ones, but keep the convention.
- **`internal/naming` SSOT** — use `naming.DMZAccountName()` to find the DMZ account; never the literal `"DMZ"`.
- **TDD**; per-commit gate `make test && make lint && make staticcheck`.
- Branch: `dmz-consumer-issuance` (already created; ADR already committed at `b3c8d2f`).

---

### Task 1: Aggregate DMZ consumers from the model (pure helper)

**Files:**
- Modify: `internal/issuance/issuance.go` (new unexported helper + type)
- Test: `internal/issuance/issuance_test.go`

**Interfaces:**
- Produces:
  - `type dmzConsumer struct { Name string; SubAllow []string; PubDeny []string }`
  - `func aggregateDMZConsumers(model *accounts.Model) []dmzConsumer` — groups the DMZ account's `UserTemplate`s by `Label`, unions their `Subscribe` (→ `SubAllow`) and `PublishDeny` (→ `PubDeny`), both sorted+deduped; consumers sorted by `Name`. Returns `nil` if the model has no DMZ account or it has no users.

- [ ] **Step 1: Write the failing test**

Add to `internal/issuance/issuance_test.go`:

```go
func TestAggregateDMZConsumers(t *testing.T) {
	m := &accounts.Model{DOT: "exdot", Accounts: []accounts.Account{
		{Name: "CENTRAL"},
		{Name: naming.DMZAccountName(), Users: []accounts.UserTemplate{
			// consumer "research" appears in two shares → one aggregated cred
			{Label: "research", Subscribe: []string{"vikasa.exdot.share.corridor-b.>"}, PublishDeny: []string{">"}},
			{Label: "research", Subscribe: []string{"vikasa.exdot.share.corridor-a.>"}, PublishDeny: []string{">"}},
			{Label: "peer-neighbor", Subscribe: []string{"vikasa.peer.exdot.>"}, PublishDeny: []string{">"}},
		}},
	}}
	got := aggregateDMZConsumers(m)
	if len(got) != 2 {
		t.Fatalf("want 2 consumers, got %d: %+v", len(got), got)
	}
	// sorted by name: peer-neighbor, research
	if got[0].Name != "peer-neighbor" || got[1].Name != "research" {
		t.Fatalf("consumers not sorted by name: %+v", got)
	}
	// research's two share subjects unioned + sorted
	if len(got[1].SubAllow) != 2 ||
		got[1].SubAllow[0] != "vikasa.exdot.share.corridor-a.>" ||
		got[1].SubAllow[1] != "vikasa.exdot.share.corridor-b.>" {
		t.Errorf("research SubAllow not unioned+sorted: %v", got[1].SubAllow)
	}
	if len(got[1].PubDeny) != 1 || got[1].PubDeny[0] != ">" {
		t.Errorf("research PubDeny: %v", got[1].PubDeny)
	}
}

func TestAggregateDMZConsumers_NoDMZ(t *testing.T) {
	m := &accounts.Model{DOT: "exdot", Accounts: []accounts.Account{{Name: "CENTRAL"}}}
	if got := aggregateDMZConsumers(m); got != nil {
		t.Errorf("want nil for model without DMZ account, got %v", got)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/issuance/ -run TestAggregateDMZConsumers -v`
Expected: FAIL — `undefined: aggregateDMZConsumers` / `dmzConsumer`.

- [ ] **Step 3: Implement the helper**

Add to `internal/issuance/issuance.go` (ensure `sort` is imported):

```go
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
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/issuance/ -run TestAggregateDMZConsumers -v`
Expected: PASS (both cases).

- [ ] **Step 5: Commit**

```bash
git add internal/issuance/issuance.go internal/issuance/issuance_test.go
git commit -m "feat(issuance): aggregate DMZ consumers from the account model"
```

---

### Task 2: Mint DMZ consumer creds + wire into IssueWithRotation

**Files:**
- Modify: `internal/issuance/issuance.go` (`Result`, `IssueWithRotation`, new `mintDMZConsumers`/`mintDMZConsumer`)
- Test: `internal/issuance/issuance_test.go`, `internal/issuance/trustchain_test.go`

**Interfaces:**
- Consumes: `aggregateDMZConsumers` (Task 1); `ensureKeypair`, `jwt.FormatUserConfig`, `wipeBytes`, `requireOwnerOnly`, `userJWTValidity`, `naming.DMZAccountName()`.
- Produces:
  - `type DMZConsumerResult struct { Consumer string; Minted bool; Subjects []string }`
  - `Result.DMZConsumers []DMZConsumerResult`
  - `func mintDMZConsumers(res *Result, model *accounts.Model, dir string, dmzSK nkeys.KeyPair, dmzPub string) error`

- [ ] **Step 1: Write the failing tests**

Add to `internal/issuance/issuance_test.go`. Reuse the existing DMZ fixture pattern (see how other tests build `model, root` from a DMZ topology; `examples/exdot-dmz.json` is a DMZ spec — load it with `topology.Load` then `accounts.Build`, or the file's existing inline helper). The tests:

```go
func TestIssue_DMZConsumerCreds(t *testing.T) {
	dir := credsDir(t)
	m, root := dmzModelRoot(t) // build accounts.Model + *topology.Root from a DMZ spec (reuse existing helper/fixture)
	if _, err := issuance.Issue(m, nil, root, dir); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// pick a known consumer from the fixture (e.g. "research"); adjust to the fixture used
	creds := readBytes(t, filepath.Join(dir, "dmz", "research.creds"))
	tok, err := jwt.ParseDecoratedJWT(creds)
	if err != nil { t.Fatal(err) }
	uc, err := jwt.DecodeUserClaims(tok)
	if err != nil { t.Fatal(err) }
	if len(uc.Pub.Deny) == 0 || uc.Pub.Deny[0] != ">" {
		t.Errorf("consumer must deny all publish, got Pub.Deny=%v", uc.Pub.Deny)
	}
	if len(uc.Pub.Allow) != 0 {
		t.Errorf("consumer must have no publish allow, got %v", uc.Pub.Allow)
	}
	if len(uc.Sub.Allow) == 0 || !strings.HasPrefix(uc.Sub.Allow[0], "vikasa.") {
		t.Errorf("consumer subscribe scope wrong: %v", uc.Sub.Allow)
	}
}

func TestIssue_DMZConsumerIdempotent(t *testing.T) {
	dir := credsDir(t)
	m, root := dmzModelRoot(t)
	r1, err := issuance.Issue(m, nil, root, dir)
	if err != nil { t.Fatal(err) }
	r2, err := issuance.Issue(m, nil, root, dir)
	if err != nil { t.Fatal(err) }
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
	if err := os.MkdirAll(filepath.Join(dir, "dmz"), 0o750); err != nil { t.Fatal(err) }
	m, root := dmzModelRoot(t)
	_, err := issuance.Issue(m, nil, root, dir)
	if err == nil || !strings.Contains(err.Error(), "group/world-accessible") {
		t.Fatalf("expected fail-closed on loose dmz/ dir, got: %v", err)
	}
}
```

Add to `internal/issuance/trustchain_test.go` an assertion that a DMZ consumer JWT's `IssuerAccount` equals the DMZ account pub from `Result.Accounts`, and that decoding it with the DMZ account SK's pubkey validates (mirror the existing cabinet trust-chain checks).

> If a `dmzModelRoot(t)` helper doesn't exist, add a tiny one that does `root := loadInline(t, <dmz spec>)` (or `topology.Load("../../examples/exdot-dmz.json")`) then `m, _ := accounts.Build(root)`. Match the file's existing fixture style; do not duplicate a large fixture.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/issuance/ -run 'TestIssue_DMZConsumer' -v`
Expected: FAIL — no `dmz/` output produced yet (files missing / Result.DMZConsumers empty).

- [ ] **Step 3: Add the Result type + field**

In `internal/issuance/issuance.go`, extend `Result` (after `Revoked int`):
```go
	DMZConsumers []DMZConsumerResult // external-consumer creds minted this run (sorted by Consumer)
```
And add the type near `CabinetResult`:
```go
// DMZConsumerResult reports one minted DMZ external-consumer credential.
type DMZConsumerResult struct {
	Consumer string
	Minted   bool     // true if the user keypair was minted this run (vs reused)
	Subjects []string // subscribe scope (sorted)
}
```

- [ ] **Step 4: Implement the mint functions**

```go
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

func mintDMZConsumer(c dmzConsumer, dmzDir, issuerAccountPub string, acctSK nkeys.KeyPair) (minted bool, err error) {
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
```

- [ ] **Step 5: Wire into `IssueWithRotation`**

In `IssueWithRotation` (`issuance.go`), hoist `skByName` out of the `if inv != nil` block and add the DMZ step after `mintCabinets`:

```go
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
```
(Delete the old inner `skByName` construction that was inside `if inv != nil`.)

- [ ] **Step 6: Run tests + full suite (zero golden diff)**

Run: `go test ./internal/issuance/ -v` then `make test && make lint && make staticcheck`
Expected: PASS; `git status` clean (no `cmd/gen` golden change — this is issuance-only).

- [ ] **Step 7: Commit**

```bash
git add internal/issuance/issuance.go internal/issuance/issuance_test.go internal/issuance/trustchain_test.go
git commit -m "feat(issuance): mint subscribe-only DMZ external-consumer creds (operator mode)"
```

---

### Task 3: Surface DMZ consumers in the `cmd/issue` summary

**Files:**
- Modify: `cmd/issue/main.go` (summary section)
- Test: `cmd/issue/main_test.go`

- [ ] **Step 1: Add to the summary**

In `cmd/issue/main.go`'s `run`, after the existing tallies, include the DMZ consumer count in the printed summary (e.g. append `dmz-consumers=%d` with `len(res.DMZConsumers)` to the summary `Printf` format + args). Match the existing summary style.

- [ ] **Step 2: Test**

Add a `cmd/issue/main_test.go` case (or extend an existing mint test) that runs `run` against a DMZ spec + out dir and asserts the printed summary contains `dmz-consumers=` with the expected count, using the file's existing stdout-capture helper (`captureStdout`).

- [ ] **Step 3: Run + commit**

Run: `make test && make lint && make staticcheck`
```bash
git add cmd/issue/main.go cmd/issue/main_test.go
git commit -m "feat(cmd/issue): report DMZ consumer count in the run summary"
```

---

## Self-review notes

- **Spec coverage:** ADR decision items → aggregation (Task 1), mint+wire+fail-closed+Result+trustchain (Task 2), summary (Task 3). JWT-only ✓ (no cert path). Per-consumer aggregation ✓ (Task 1). Honors deny lists ✓ (perms from template). No `accounts.Build` change ✓ (aggregation in issuance).
- **Out of scope (per ADR):** consumer-cred rotation/revocation, SYSTEM, mTLS — not in any task.
- **Open execution detail:** the `dmzModelRoot(t)` fixture — reuse the existing DMZ test fixture/`examples/exdot-dmz.json`; consumer names in assertions must match whatever spec the fixture uses (adjust `"research"` accordingly).
- **Type consistency:** `dmzConsumer{Name,SubAllow,PubDeny}`, `DMZConsumerResult{Consumer,Minted,Subjects}`, `mintDMZConsumers`/`mintDMZConsumer` used consistently across tasks.
