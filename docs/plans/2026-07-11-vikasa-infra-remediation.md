# vikasa-infra remediation — Implementation Plan

**Goal:** Close three golden-invisible defects, finish the naming SSOT and the substrate seam, de-god `plan.Build`, and add an advisory fleet-scale fan-in guard — mostly behavior-preserving, gated by the golden-tree suite.

**Architecture:** Four waves, each an independent review checkpoint. Two heavier scale changes (parallel minting, revocation compaction) are deferred to a companion design and are NOT in this plan.

**Tech Stack:** Go 1.x, `text/template`, NATS `nkeys`/`jwt/v2`, NACK CRDs; tests are table-driven (`ptr()` in plan, `loadInline` in topology, `run(options{})` in cmd), golden-tree byte comparison in `cmd/gen`.

## Global Constraints

- **`internal/naming` is the single source of NATS naming/subject strings** — never rebuild stream/account/operator/subject strings inline.
- **Determinism** — every map iteration reaching output must be sorted at the source.
- **Error strings are load-bearing** — tests assert substrings; `cmd/gen`/`cmd/issue` intentionally share boundary-violation wording. Preserve existing substrings when refactoring.
- **TDD** — failing test first, matching each package's existing table-driven style.
- **Golden protocol** — after any intentional output change: `make golden`, then review `git diff`. A refactor must produce a **zero-diff** golden tree. Each task states its expected golden diff.
- **Per-task gate** — run `make test && make lint && make staticcheck` before each commit.
- Branch: `remediation` (already created off `main`).

---

## Wave 1 — P0 defects (zero golden diff for all valid specs)

### Task 1: Reject cross-district leaf-DNS name collisions

The leaf-DNS name (`plan.go:185-186`) is built from `dot` + `partitionID` but omits the district, while `PartitionStreamName` includes it. Two districts with partition ids differing only by separator (`"a/b"` vs `"a-b"`) get distinct stream names (guarded at `plan.go:139-146`) but an **identical** DNS name with different targets → two silent conflicting records in `leaf-dns.yaml`. The leaf-DNS name is operationally load-bearing (cabinets dial it), so we do **not** rename it — we add a Build-time reject guard, exactly as the authors did for stream names.

**Files:**
- Modify: `internal/plan/plan.go` (regional loop, ~148-192)
- Test: `internal/plan/plan_test.go`

**Interfaces:**
- Consumes: `plan.Build(root *topology.Root) (*Plan, error)`, `ptr[T]` test helper.
- Produces: no new exported symbols; `Build` now errors on duplicate leaf-DNS names.

- [ ] **Step 1: Write the failing test**

Add to `internal/plan/plan_test.go`:

```go
func TestBuild_DNSNameCollisionRejected(t *testing.T) {
	// The leaf DNS name omits the district, so two DISTINCT partition ids in
	// two DIFFERENT districts that collapse to the same DNS segment ("a/b" and
	// "a-b" both -> "a-b") produce distinct stream names but an identical DNS
	// name. Build must reject rather than emit two conflicting records.
	root := &topology.Root{
		Topology: &topology.Topology{
			Dot:     ptr("exdot"),
			Central: &topology.Central{Cluster: ptr("core")},
			Cluster: map[string]*topology.Cluster{
				"core": {JsDomain: ptr("core"), LeafEndpoint: ptr("leaf-core:7422")},
				"reg":  {JsDomain: ptr("reg"), LeafEndpoint: ptr("leaf-reg:7422")},
			},
			District: map[string]*topology.District{
				"d1": {Id: ptr("d1"), Partition: map[string]*topology.Partition{
					"a/b": {Id: ptr("a/b"), Cluster: ptr("reg")},
				}},
				"d2": {Id: ptr("d2"), Partition: map[string]*topology.Partition{
					"a-b": {Id: ptr("a-b"), Cluster: ptr("reg")},
				}},
			},
		},
	}
	_, err := plan.Build(root)
	if err == nil {
		t.Fatal("expected leaf-DNS name collision error, got nil")
	}
	for _, want := range []string{"leaf-exdot-a-b.nats.vikasa.exdot", "a/b", "a-b"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("DNS collision error should mention %q: %v", want, err)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/plan/ -run TestBuild_DNSNameCollisionRejected -v`
Expected: FAIL — `expected leaf-DNS name collision error, got nil` (Build currently emits two duplicate records silently).

- [ ] **Step 3: Add the guard in `plan.Build`**

In `internal/plan/plan.go`, before the `for _, p := range parts {` regional loop (near the `var regionalStreams []Stream` / `var dns []DNSRecord` declarations, ~line 148), add:

```go
	// Leaf DNS names omit the district, so distinct partition ids can collide
	// after the '/'->'-' transform (same class as the stream-name guard above).
	// The name is operationally load-bearing (cabinets dial it): reject rather
	// than rename or silently emit conflicting records.
	dnsSeen := map[string]string{} // leaf DNS name -> "<district>/<partition>"
```

Then inside the loop, immediately after `dnsName := ...` (currently line 186) and before the `dns = append(...)`, add:

```go
		key := p.districtID + "/" + p.partitionID
		if prev, dup := dnsSeen[dnsName]; dup {
			return nil, fmt.Errorf("plan.Build: partitions %s and %s collide on leaf DNS name %q (ids differ only by '/' vs '-')", prev, key, dnsName)
		}
		dnsSeen[dnsName] = key
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./internal/plan/ -run 'TestBuild_DNSNameCollisionRejected|TestBuild' -v`
Expected: PASS (new test passes; existing Build tests unaffected).

- [ ] **Step 5: Verify zero golden diff & full suite**

Run: `make test && make lint && make staticcheck`
Expected: PASS, no golden changes (no valid example has colliding DNS names).

- [ ] **Step 6: Commit**

```bash
git add internal/plan/plan.go internal/plan/plan_test.go
git commit -m "fix(plan): reject cross-district leaf-DNS name collisions"
```

---

### Task 2: Wipe the plaintext seed left in the `.creds` buffer

`issuance.go:392` `FormatUserConfig` copies the decorated plaintext seed into a **new** `creds` buffer; `:396` wipes only the source `userSeed`; `creds` is written to disk (`:400`) and falls to GC unwiped. Go's non-moving GC makes in-place `[]byte` wiping effective, so a `defer` closes the gap.

**Files:**
- Modify: `internal/issuance/issuance.go` (`mintCabinet`, ~392-402)
- Test: `internal/issuance/issuance_test.go`

**Interfaces:**
- Consumes: `wipeBytes(b []byte)` (issuance.go:790), `jwt.FormatUserConfig`.
- Produces: no signature change; on-disk `.creds` output unchanged (behavior-preserving).

- [ ] **Step 1: Write the failing test**

The wipe of a function-local buffer is not directly observable, so guard the *behavior* (mint still produces a valid `.creds`) and assert the code path via a focused helper. Add a regression test to `internal/issuance/issuance_test.go` that mints one cabinet and asserts the `.creds` file still contains a valid decorated seed + JWT (unchanged behavior). If an equivalent assertion already exists in `TestIssue_MintsCabinetCreds` (or similar), extend it with an explicit `-U` seed-marker check rather than adding a duplicate:

```go
func TestIssue_CredsFileStillValidAfterSeedWipe(t *testing.T) {
	dir := credsDir(t)
	model, inv, root := smallModelInvRoot(t) // existing helper for a 1-cabinet fixture
	if _, err := issuance.Issue(model, inv, root, dir); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	creds := readCabinetCreds(t, dir) // existing/trivial helper: read the single .creds
	if !strings.Contains(creds, "-----BEGIN NATS USER JWT-----") ||
		!strings.Contains(creds, "-----BEGIN USER NKEY SEED-----") {
		t.Errorf(".creds missing JWT or seed block after wipe change:\n%s", creds)
	}
}
```

> If `smallModelInvRoot`/`readCabinetCreds` don't exist verbatim, reuse the fixture pattern already used by the nearest existing cabinet-mint test in this file (search for `.creds`).

- [ ] **Step 2: Run test to verify it passes first (behavior baseline)**

Run: `go test ./internal/issuance/ -run TestIssue_CredsFileStillValidAfterSeedWipe -v`
Expected: PASS (this is a behavior-lock; the wipe must not change it).

- [ ] **Step 3: Add the deferred wipe**

In `internal/issuance/issuance.go`, in `mintCabinet`, change the block at ~392-402 so `creds` is scrubbed after the write:

```go
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
```

- [ ] **Step 4: Run tests to verify still pass**

Run: `go test ./internal/issuance/ -v`
Expected: PASS (behavior-lock test and all existing mint/rotation tests unchanged).

- [ ] **Step 5: Commit**

```bash
git add internal/issuance/issuance.go internal/issuance/issuance_test.go
git commit -m "fix(issuance): wipe the plaintext seed left in the .creds buffer"
```

---

### Task 3: Extend the fail-closed permission sweep to `ca/` and `cabinets/`

The `requireOwnerOnly` sweep (`issuance.go:121-132`) covers `dir`/`accounts`/`resolver` but not `dir/ca` (home of `cabinet-ca.key`, the longest-lived secret, created later by `pki.LoadOrCreateCA`) or `dir/cabinets`. A pre-existing group-writable `ca/` passes every check.

**Files:**
- Modify: `internal/issuance/issuance.go` (dir sweep, ~119-132)
- Test: `internal/issuance/issuance_test.go`

**Interfaces:**
- Consumes: `requireOwnerOnly(dir string) error` (issuance.go:778).
- Produces: `IssueWithRotation` now also fails closed on a loose `ca/` or `cabinets/`.

- [ ] **Step 1: Write the failing test**

Extend the existing loose-perms coverage. Add to `internal/issuance/issuance_test.go`:

```go
func TestIssue_RejectsLooseCADir(t *testing.T) {
	dir := credsDir(t)
	// Pre-create a group-accessible ca/ so MkdirAll won't tighten it.
	if err := os.MkdirAll(filepath.Join(dir, "ca"), 0o750); err != nil {
		t.Fatal(err)
	}
	model, inv, root := smallModelInvRoot(t)
	_, err := issuance.Issue(model, inv, root, dir)
	if err == nil || !strings.Contains(err.Error(), "group/world-accessible") {
		t.Fatalf("expected fail-closed on loose ca/ dir, got: %v", err)
	}
}
```

Add an analogous `TestIssue_RejectsLooseCabinetsDir` using `filepath.Join(dir, "cabinets")` at `0o750`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/issuance/ -run 'TestIssue_RejectsLooseCADir|TestIssue_RejectsLooseCabinetsDir' -v`
Expected: FAIL — no error today (the loose `ca/`/`cabinets/` dir is not swept).

- [ ] **Step 3: Add the dirs to the sweep**

In `internal/issuance/issuance.go`, change the sweep at ~119-132:

```go
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
```

> Note: this pre-creates `ca/` and `cabinets/` at `0o700`. Verify `pki.LoadOrCreateCA` and the per-district `mintCabinets` mkdir still succeed against an existing owner-only dir (they use `MkdirAll`, which is a no-op on an existing dir) — the golden tree must be unchanged.

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./internal/issuance/ -v`
Expected: PASS (new tests pass; all existing mint/rotation tests unaffected).

- [ ] **Step 5: Verify zero golden diff & full suite**

Run: `make test && make lint && make staticcheck`
Expected: PASS, zero golden changes.

- [ ] **Step 6: Commit**

```bash
git add internal/issuance/issuance.go internal/issuance/issuance_test.go
git commit -m "fix(issuance): fail closed on loose ca/ and cabinets/ dirs"
```

**► WAVE 1 CHECKPOINT — pause for review.**

---

## Wave 2 — finish the naming SSOT (zero golden diff)

### Task 4: Add the missing naming helpers

`naming` covers stream/district-account/operator names and subject-space resolution, but the `vikasa.<dot>.` anchor, the `.share.`/`.peer.` public spaces (the load-bearing DMZ deny-by-default boundary), and the fixed account names `CENTRAL`/`DMZ`/`SYSTEM` are built inline elsewhere. Add helpers so those conventions live in one place.

**Files:**
- Modify: `internal/naming/naming.go`
- Test: `internal/naming/naming_test.go`

**Interfaces:**
- Produces:
  - `Anchor(dot string) string` → `"vikasa." + dot + "."`
  - `ShareSpace(dot string) string` → `"vikasa." + dot + ".share."`
  - `PeerSpace(dot string) string` → `"vikasa.peer." + dot + "."`
  - `CentralAccountName() string` → `"CENTRAL"`
  - `DMZAccountName() string` → `"DMZ"`
  - `SystemAccountName() string` → `"SYSTEM"`
  - `FilterUnderDistrict(dot, districtID string, declared *string, filter string) (space string, ok bool)` — resolves the district subject space and reports whether `filter` is under it (shared boundary check, consumed in Task 6).

- [ ] **Step 1: Write the failing tests**

Add to `internal/naming/naming_test.go`:

```go
func TestSpaceHelpers(t *testing.T) {
	if got := naming.Anchor("exdot"); got != "vikasa.exdot." {
		t.Errorf("Anchor = %q", got)
	}
	if got := naming.ShareSpace("exdot"); got != "vikasa.exdot.share." {
		t.Errorf("ShareSpace = %q", got)
	}
	if got := naming.PeerSpace("exdot"); got != "vikasa.peer.exdot." {
		t.Errorf("PeerSpace = %q", got)
	}
	if naming.CentralAccountName() != "CENTRAL" || naming.DMZAccountName() != "DMZ" || naming.SystemAccountName() != "SYSTEM" {
		t.Error("fixed account name mismatch")
	}
}

func TestFilterUnderDistrict(t *testing.T) {
	// default space vikasa.exdot.d7.>
	space, ok := naming.FilterUnderDistrict("exdot", "d7", nil, "vikasa.exdot.d7.cab1.>")
	if space != "vikasa.exdot.d7.>" || !ok {
		t.Errorf("in-space: space=%q ok=%v", space, ok)
	}
	if _, ok := naming.FilterUnderDistrict("exdot", "d7", nil, "vikasa.exdot.d70.x.>"); ok {
		t.Error("token-boundary sibling d70 must not be under d7")
	}
	declared := "vikasa.exdot.7.>"
	if _, ok := naming.FilterUnderDistrict("exdot", "d7", &declared, "vikasa.exdot.7.a.>"); !ok {
		t.Error("declared prefix should be honored")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/naming/ -run 'TestSpaceHelpers|TestFilterUnderDistrict' -v`
Expected: FAIL — undefined functions.

- [ ] **Step 3: Implement the helpers**

Append to `internal/naming/naming.go`:

```go
// Anchor is the fixed DOT subject anchor "vikasa.<dot>." — the only prefix
// every district subject space starts with.
func Anchor(dot string) string { return "vikasa." + dot + "." }

// ShareSpace is the DMZ public share space prefix "vikasa.<dot>.share." — the
// deny-by-default egress boundary. A well-formed share space is ShareSpace(dot)+"<name>.>".
func ShareSpace(dot string) string { return "vikasa." + dot + ".share." }

// PeerSpace is the quarantined inbound peer-DOT space prefix "vikasa.peer.<dot>.".
func PeerSpace(dot string) string { return "vikasa.peer." + dot + "." }

// CentralAccountName, DMZAccountName, SystemAccountName are the fixed NATS
// account names (not derived from spec ids, so no Sanitize).
func CentralAccountName() string { return "CENTRAL" }
func DMZAccountName() string      { return "DMZ" }
func SystemAccountName() string   { return "SYSTEM" }

// FilterUnderDistrict resolves a district's subject space (declared or default)
// and reports whether filter lies within it. Callers format their own errors so
// their existing (intentionally distinct) wording is preserved.
func FilterUnderDistrict(dot, districtID string, declared *string, filter string) (space string, ok bool) {
	space = SubjectSpace(dot, districtID, declared)
	return space, UnderPrefix(filter, space)
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/naming/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/naming/naming.go internal/naming/naming_test.go
git commit -m "feat(naming): add DOT anchor, share/peer space, fixed-account, and boundary helpers"
```

---

### Task 5: Route inline conventions through the new helpers (zero-diff)

**Files:**
- Modify: `internal/topology/topology.go` (`:82,329,374-375`)
- Modify: `internal/accounts/accounts.go` (`:73,99,104,115,119,136`)

**Interfaces:**
- Consumes: Task 4 helpers.
- Produces: no behavior change; identical output strings and identical error substrings.

- [ ] **Step 1: Route `topology.go`**

`DefaultShareAs` (`:81-83`):
```go
func DefaultShareAs(dot, consumer string) string {
	return naming.ShareSpace(dot) + consumer + ".>"
}
```

`SubjectPrefix` anchor check (`:329`), replace `anchor := "vikasa." + *t.Dot + "."` with:
```go
			anchor := naming.Anchor(*t.Dot)
```

DMZ share validation (`:374-375`), replace the two literals with:
```go
			shareSpace := naming.ShareSpace(*t.Dot)
			peerSpace := naming.PeerSpace(*t.Dot)
```
Leave the surrounding `strings.HasPrefix` check and the error string at `:377` byte-identical (it embeds `shareSpace+">"` / `peerSpace+">"`).

- [ ] **Step 2: Route `accounts.go`**

Replace the fixed-account literals with helper calls:
- `:73` `central := Account{Name: naming.CentralAccountName(), JetStream: true}`
- `:99` `Name: naming.SystemAccountName(),`
- `:104` `dmz := Account{Name: naming.DMZAccountName(), JetStream: true}`
- `:115` `if m.Accounts[i].Name == naming.CentralAccountName() {`
- `:119` `dmz.Imports = append(dmz.Imports, Import{FromAccount: naming.CentralAccountName(), Subject: *s.From})`
- `:136` `if m.Accounts[i].Name == naming.CentralAccountName() {`

(`accounts.go` already imports `internal/naming`.)

- [ ] **Step 3: Run the full suite — must be zero-diff**

Run: `make test && make lint && make staticcheck`
Expected: PASS with **zero golden diff** and all `topology`/`accounts` unit tests green (error substrings unchanged).

- [ ] **Step 4: Confirm no residual inline conventions**

Run:
```bash
grep -rn '"vikasa\.\|"CENTRAL"\|"DMZ"\|"SYSTEM"' internal/topology internal/accounts --include='*.go' | grep -v _test.go
```
Expected: no matches for the routed sites (only comments/docstrings, if any).

- [ ] **Step 5: Commit**

```bash
git add internal/topology/topology.go internal/accounts/accounts.go
git commit -m "refactor: route DMZ share/peer spaces and fixed account names through naming"
```

---

### Task 6: One shared cabinet-boundary check (zero-diff)

`plan.AttachCabinets` (`cabinets.go:54-58`) and `issuance.go:326-329` implement the same rule, kept in sync by a comment. Route both through `naming.FilterUnderDistrict` (Task 4) while preserving each caller's distinct error string.

**Files:**
- Modify: `internal/plan/cabinets.go` (`:54-59`)
- Modify: `internal/issuance/issuance.go` (`:326-329`)

- [ ] **Step 1: Route `cabinets.go`**

Replace `:54-59`:
```go
		if c.Filter != "" {
			pfx, ok := naming.FilterUnderDistrict(p.DOT, distID, root.Topology.District[distID].SubjectPrefix, c.Filter)
			if !ok {
				return fmt.Errorf("plan.AttachCabinets: cabinet %q: filter %q is outside district prefix %q", c.ID, c.Filter, pfx)
			}
		}
```

- [ ] **Step 2: Route `issuance.go`**

At `:326-329`, replace the `naming.SubjectSpace(...)` + `naming.UnderPrefix(...)` pair with the single call, preserving issuance's own error wording:
```go
		space, ok := naming.FilterUnderDistrict(dot, distID, district.SubjectPrefix, c.Filter)
		if !ok {
			return nil, fmt.Errorf(<existing issuance error string using space>, ...)
		}
```
> Read the current `:323-329` error string and keep it byte-identical (tests assert it; `cmd/gen`/`cmd/issue` share boundary wording).

- [ ] **Step 3: Run the full suite — zero-diff**

Run: `make test && make lint && make staticcheck`
Expected: PASS, zero golden diff, `TestIssue_CabinetFilterOutsideDistrictFailsClosed` and the plan cabinet tests green.

- [ ] **Step 4: Commit**

```bash
git add internal/plan/cabinets.go internal/issuance/issuance.go
git commit -m "refactor: share the cabinet subject-boundary check via naming.FilterUnderDistrict"
```

**► WAVE 2 CHECKPOINT — pause for review.**

---

## Wave 3 — de-god + unify the substrate/packaging seam (zero golden diff)

### Task 7: Decompose `plan.Build` + decorate-sort partition names

208-line god function (`plan.go:70-277`). Extract `buildRegional`/`buildCentral`/`buildDMZ`; compute each partition's stream name **once** (decorate-sort) instead of recomputing `PartitionStreamName` in the sort comparator (`:130-134`) and collision loop (`:140-141`); delete the proven no-op `sort.Slice(centralSources,…)` (`:195-197`).

**Files:**
- Modify: `internal/plan/plan.go`
- Test: existing `internal/plan/plan_test.go` (behavior-lock; no new test required beyond Task 1's)

**Interfaces:**
- Produces: unchanged `Build` signature and output; new unexported `buildRegional`/`buildCentral`/`buildDMZ` helpers.

- [ ] **Step 1: Add a `streamName` field to `partEntry` and populate once**

In the collection loop (`:110-127`), set:
```go
		type partEntry struct {
			districtID, partitionID, clusterID, streamName string
		}
		...
			parts = append(parts, partEntry{
				districtID:  distID,
				partitionID: partID,
				clusterID:   *partition.Cluster,
				streamName:  PartitionStreamName(dot, distID, partID),
			})
```

- [ ] **Step 2: Sort and collision-check on the precomputed name**

Replace `:130-134` and `:139-146`:
```go
	sort.Slice(parts, func(i, j int) bool { return parts[i].streamName < parts[j].streamName })
	for i := 1; i < len(parts); i++ {
		if parts[i].streamName == parts[i-1].streamName {
			return nil, fmt.Errorf("plan.Build: partitions %s/%q and %s/%q collide on stream name %q",
				parts[i-1].districtID, parts[i-1].partitionID, parts[i].districtID, parts[i].partitionID, parts[i].streamName)
		}
	}
```
And in the regional loop use `p.streamName` in place of the recomputed `PartitionStreamName(...)` at `:164`.

- [ ] **Step 3: Delete the no-op central-sources sort**

Remove `sort.Slice(centralSources, …)` at `:195-197` — `centralSources` is appended in the already-sorted `parts` order and the collision guard has proven names unique, so it's dead work. Keep the `dns` sort.

> If preferred for safety, replace it with a comment: `// centralSources already in sorted parts order (see collision guard); no re-sort.`

- [ ] **Step 4: Extract the three tier builders**

Lift the regional block into `buildRegional(dot string, parts []partEntry, getCluster func(string) (*topology.Cluster, error)) (streams []Stream, sources []Source, dns []DNSRecord, err error)`; the central block into `buildCentral(...)`; the DMZ block into `buildDMZ(...)`. `Build` becomes an orchestrator that calls the three and assembles + sorts `allStreams`. Keep all logic identical.

- [ ] **Step 5: Run — must be zero-diff**

Run: `make test && make lint && make staticcheck`
Expected: PASS, **zero golden diff**, all plan tests green.

- [ ] **Step 6: Commit**

```bash
git add internal/plan/plan.go
git commit -m "refactor(plan): split Build into tier builders; decorate-sort partition names"
```

---

### Task 8: `render.SliceDir` — one packaging-decision helper

The `charts/` vs `clusters/` rule lives in ~4 sites (`substrate.go:151-158,169-173`, `runbook.go:235-244`, `runbook.tmpl:21`). Centralize it so `runbook.go` stops re-deriving substrate/packaging.

**Files:**
- Modify: `internal/render/substrate.go`
- Modify: `internal/render/runbook.go` (`:235-240`)
- Test: `internal/render/substrate_test.go`

**Interfaces:**
- Produces: `func SliceDir(clusterID string, isK8s bool, out Output) string` → `"charts/<id>/"` when `isK8s && out==OutputHelm`, else `"clusters/<id>/"`.

- [ ] **Step 1: Write the failing test**

Add to `internal/render/substrate_test.go`:
```go
func TestSliceDir(t *testing.T) {
	cases := []struct {
		id    string
		isK8s bool
		out   render.Output
		want  string
	}{
		{"c1", true, render.OutputHelm, "charts/c1/"},
		{"c1", true, render.OutputKustomize, "clusters/c1/"},
		{"c1", false, render.OutputHelm, "clusters/c1/"},   // bare-metal never charts
		{"c1", false, render.OutputKustomize, "clusters/c1/"},
	}
	for _, c := range cases {
		if got := render.SliceDir(c.id, c.isK8s, c.out); got != c.want {
			t.Errorf("SliceDir(%q,%v,%v)=%q want %q", c.id, c.isK8s, c.out, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/render/ -run TestSliceDir -v`
Expected: FAIL — undefined `render.SliceDir`.

- [ ] **Step 3: Implement `SliceDir` and consume it**

Add to `internal/render/substrate.go`:
```go
// SliceDir is the output subdirectory for a cluster's slice: charts/<id>/ for a
// kubernetes cluster in Helm mode, else clusters/<id>/. It is the single owner
// of the packaging-path decision (Dispatch, the Argo path, and the runbook all
// use it).
func SliceDir(clusterID string, isK8s bool, out Output) string {
	if isK8s && out == OutputHelm {
		return "charts/" + clusterID + "/"
	}
	return "clusters/" + clusterID + "/"
}
```
Then in `runbook.go` replace the `sliceDir` closure (`:235-240`) with:
```go
	sliceDir := func(id string) string {
		c := t.Cluster[id]
		isK8s := c != nil && c.Substrate != nil && c.Substrate.Type == topology.SubstrateKubernetes
		return SliceDir(id, isK8s, cfg.Output)
	}
```

> Optional (same commit, zero-diff): in `Dispatch`, derive the output keys from `SliceDir(id, slice.SubstrateType==topology.SubstrateKubernetes.String(), cfg.Output)` instead of the inline `"charts/"+id+"/"` / `"clusters/"+id+"/"` string building, so all four sites share one function. Verify golden is unchanged.

- [ ] **Step 4: Run — zero-diff**

Run: `make test && make lint && make staticcheck`
Expected: PASS, zero golden diff.

- [ ] **Step 5: Commit**

```bash
git add internal/render/substrate.go internal/render/runbook.go internal/render/substrate_test.go
git commit -m "refactor(render): centralize charts/clusters packaging decision in SliceDir"
```

---

### Task 9: `render.WriteTree` (atomic) + collision-checked merge; adopt in `cmd/gen`

`cmd/gen` writes the tree in place (`main.go:176-195`) — non-atomic, no collision detection across 7 `maps.Copy` merges, no prune. Add an atomic writer with a duplicate-key guard and an **off-by-default** `-prune`.

**Files:**
- Create: `internal/render/writetree.go`
- Modify: `cmd/gen/main.go` (merge section `:107-172`, write section `:175-195`, flags)
- Test: `internal/render/writetree_test.go`

**Interfaces:**
- Produces:
  - `func WriteTree(dir string, files map[string][]byte, prune bool) error` — atomic per-file (temp in dest dir → `Chmod(0644)` → rename); when `prune`, removes files previously written by WriteTree that are no longer produced, tracked via a `.vikasa-manifest` file at `dir` root.
  - `func MergeInto(all, part map[string][]byte) error` — copies `part` into `all`, erroring on any duplicate key.

- [ ] **Step 1: Write failing tests**

`internal/render/writetree_test.go`:
```go
func TestMergeInto_RejectsDuplicate(t *testing.T) {
	all := map[string][]byte{"a": []byte("1")}
	if err := render.MergeInto(all, map[string][]byte{"b": []byte("2")}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if err := render.MergeInto(all, map[string][]byte{"a": []byte("x")}); err == nil {
		t.Fatal("expected duplicate-key error")
	}
}

func TestWriteTree_WritesAndPrunes(t *testing.T) {
	dir := t.TempDir()
	if err := render.WriteTree(dir, map[string][]byte{"x/a.yaml": []byte("A"), "b.yaml": []byte("B")}, false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "x/a.yaml")); string(b) != "A" {
		t.Error("a.yaml not written")
	}
	// Second run without a.yaml + prune=true removes it.
	if err := render.WriteTree(dir, map[string][]byte{"b.yaml": []byte("B2")}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "x/a.yaml")); !os.IsNotExist(err) {
		t.Error("a.yaml should have been pruned")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "b.yaml")); string(b) != "B2" {
		t.Error("b.yaml not updated")
	}
}
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/render/ -run 'TestMergeInto_RejectsDuplicate|TestWriteTree_WritesAndPrunes' -v`
Expected: FAIL — undefined `render.MergeInto` / `render.WriteTree`.

- [ ] **Step 3: Implement `writetree.go`**

```go
package render

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const manifestName = ".vikasa-manifest"

// MergeInto copies part into all, erroring on any duplicate key so two
// renderers can never silently clobber each other's output.
func MergeInto(all, part map[string][]byte) error {
	for k, v := range part {
		if _, dup := all[k]; dup {
			return fmt.Errorf("render: two renderers produced %q", k)
		}
		all[k] = v
	}
	return nil
}

// WriteTree writes files (path->bytes, paths relative to dir) atomically per
// file (temp in the destination dir, then rename). When prune, files listed in
// the previous manifest but absent from files are removed. The manifest is the
// sorted list of relative paths written this run.
func WriteTree(dir string, files map[string][]byte, prune bool) error {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	if prune {
		if err := pruneStale(dir, files); err != nil {
			return err
		}
	}
	for _, name := range names {
		dest := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("create dir for %s: %w", dest, err)
		}
		tmp, err := os.CreateTemp(filepath.Dir(dest), ".tmp-*")
		if err != nil {
			return fmt.Errorf("temp for %s: %w", dest, err)
		}
		tmpName := tmp.Name()
		if _, err := tmp.Write(files[name]); err != nil {
			tmp.Close()
			os.Remove(tmpName)
			return fmt.Errorf("write %s: %w", dest, err)
		}
		if err := tmp.Chmod(0o644); err != nil {
			tmp.Close()
			os.Remove(tmpName)
			return err
		}
		if err := tmp.Close(); err != nil {
			os.Remove(tmpName)
			return err
		}
		if err := os.Rename(tmpName, dest); err != nil {
			os.Remove(tmpName)
			return fmt.Errorf("rename %s: %w", dest, err)
		}
	}
	return writeManifest(dir, names)
}

func writeManifest(dir string, names []string) error {
	return os.WriteFile(filepath.Join(dir, manifestName), []byte(strings.Join(names, "\n")+"\n"), 0o644)
}

func pruneStale(dir string, files map[string][]byte) error {
	prev, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return nil // no prior manifest: nothing to prune
	}
	for _, name := range strings.Split(strings.TrimSpace(string(prev)), "\n") {
		if name == "" {
			continue
		}
		if _, keep := files[name]; keep {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name)) // best-effort; empty dirs left in place
	}
	return nil
}
```

- [ ] **Step 4: Adopt in `cmd/gen`**

- Replace each `maps.Copy(all, X)` (`:113,119,125,131,142,148,171`) with `if err := render.MergeInto(all, X); err != nil { return err }`.
- Replace the write section (`:176-195`) with:
```go
	if err := render.WriteTree(opts.out, all, opts.prune); err != nil {
		return fmt.Errorf("write tree: %w", err)
	}
```
- Add a `prune bool` field to `options` and a flag:
```go
	prune := flag.Bool("prune", false, "remove previously-generated files no longer produced by the current spec")
```
wire `opts.prune = *prune`. Drop the now-unused `maps` import if it becomes unused.

> The `.vikasa-manifest` file is new output. Add it to `.gitignore` for the golden dirs OR regenerate goldens. Check `make golden` diff: expect only `.vikasa-manifest` additions per golden scenario. If the golden harness would flag the manifest as an unexpected file, either (a) write the manifest outside the compared tree, or (b) add it to the golden fixtures via `make golden`. **Decide during execution; the manifest location must not break golden byte-comparison.**

- [ ] **Step 5: Run**

Run: `make test && make lint && make staticcheck`
Expected: PASS. Review `git diff` after `make golden`: the only golden change is the manifest handling per Step 4's decision; file **contents** unchanged; `-prune` defaults off so default runs are byte-identical.

- [ ] **Step 6: Commit**

```bash
git add internal/render/writetree.go internal/render/writetree_test.go cmd/gen/main.go .gitignore cmd/gen/testdata
git commit -m "feat(render): atomic WriteTree with collision-checked merge and opt-in prune"
```

**► WAVE 3 CHECKPOINT — pause for review.**

---

## Wave 4 — fleet-scale guard + consistency hardening

### Task 10: Advisory per-partition fan-in guard

`AttachCabinets` (`cabinets.go:60-65`) appends one `Source` per cabinet with no cap. At 10k+ cabinets a mis-assignment can pile thousands onto one regional stream (NACK CR size / single-leader fan-in). Emit an **advisory** warning (not fatal — legitimate partitions hold thousands) and surface the max in the gen summary.

**Files:**
- Modify: `internal/plan/cabinets.go` (add a reporting return or accessor)
- Modify: `cmd/gen/main.go` (flag + summary + stderr warning)
- Test: `internal/plan/cabinets_test.go`, `cmd/gen/main_test.go`

**Interfaces:**
- Produces: `func MaxPartitionFanIn(p *Plan) (stream string, count int)` — the regional stream with the most sources (deterministic: ties broken by stream name).

- [ ] **Step 1: Write the failing test**

Add to `internal/plan/cabinets_test.go`:
```go
func TestMaxPartitionFanIn(t *testing.T) {
	p := &plan.Plan{Streams: []plan.Stream{
		{Name: "A", Tier: plan.TierRegional, Sources: make([]plan.Source, 3)},
		{Name: "B", Tier: plan.TierRegional, Sources: make([]plan.Source, 5)},
		{Name: "C", Tier: plan.TierCentral, Sources: make([]plan.Source, 9)}, // ignored
	}}
	name, n := plan.MaxPartitionFanIn(p)
	if name != "B" || n != 5 {
		t.Errorf("got (%q,%d) want (B,5)", name, n)
	}
}
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/plan/ -run TestMaxPartitionFanIn -v`
Expected: FAIL — undefined `plan.MaxPartitionFanIn`.

- [ ] **Step 3: Implement**

Add to `internal/plan/cabinets.go`:
```go
// MaxPartitionFanIn returns the regional stream carrying the most sources and
// that count (ties broken by name for determinism). Empty plan -> ("", 0).
func MaxPartitionFanIn(p *Plan) (stream string, count int) {
	for _, s := range p.Streams {
		if s.Tier != TierRegional {
			continue
		}
		if len(s.Sources) > count || (len(s.Sources) == count && s.Name < stream) {
			stream, count = s.Name, len(s.Sources)
		}
	}
	return stream, count
}
```

- [ ] **Step 4: Wire the advisory into `cmd/gen`**

Add a flag `maxFanIn := flag.Int("max-partition-sources", 2500, "advisory: warn when a regional partition stream exceeds this many cabinet sources")` (default `2500` — comfortably above the architecture's ~2,140/district example, below NACK-CR/leader stress; document in the flag help). After `plan.AttachCabinets`, add:
```go
	if fanStream, fanN := plan.MaxPartitionFanIn(p); opts.maxFanIn > 0 && fanN > opts.maxFanIn {
		fmt.Fprintf(os.Stderr, "gen: WARNING partition %s has %d cabinet sources (> -max-partition-sources=%d); consider splitting the partition\n", fanStream, fanN, opts.maxFanIn)
	}
```
Append `max-fan-in=%d` (the count) to the summary `Printf` and its args.

Add a `cmd/gen/main_test.go` case asserting the warning fires above threshold and is silent at/below it (capture stderr via the existing pattern, or lower `-max-partition-sources` in a `run(options{})` fixture with a few cabinets).

- [ ] **Step 5: Run**

Run: `make test && make lint && make staticcheck`
Expected: PASS. Golden diff: summary line gains `max-fan-in=…`; if the summary is captured in any golden, regenerate. Manifest/file contents otherwise unchanged.

- [ ] **Step 6: Commit**

```bash
git add internal/plan/cabinets.go internal/plan/cabinets_test.go cmd/gen/main.go cmd/gen/main_test.go
git commit -m "feat(plan): advisory per-partition fan-in guard for large fleets"
```

---

### Task 11: Consistency hardening (independent sub-steps, one commit each)

Each sub-step is independently testable and independently revertible; commit separately.

**11a — typed Tier switch in `cmd/gen`**
- [ ] Modify `cmd/gen/main.go:200-204`: `case plan.TierCentral:` / `case plan.TierRegional:` (import already present). Run `make test`. Commit: `refactor(cmd/gen): switch on typed plan.Tier constants`.

**11b — deterministic error-path map iteration**
- [ ] In `topology.go` `Validate` (`:305`, `:319-323`) and `PartitionIndex` (`:270-284`), and `load.go` `validatePlacement` (`:50-63`), iterate sorted keys (collect ids, `sort.Strings`, range the sorted slice) so the first-reported violation is stable. Add/extend a test that a spec with two violations reports the lexicographically-first deterministically. Run `make test`. Commit: `fix(topology): deterministic error reporting for multi-violation specs`.

**11c — parameterize the three `retired*SK` audit logs**
- [ ] In `issuance.go` (`:551-560,654-662,698-707`), collapse the three copy-pasted audit-log record types/paths into one helper parameterized by log filename + record fields. Zero-diff (golden + existing rotation tests prove it). Run `make test`. Commit: `refactor(issuance): unify retired-SK audit-log handling`.

**11d — shared metric-name constants**
- [ ] Define the `vikasa_*` metric names as exported constants in `internal/credhealth/metrics.go`; consume them in `credhealth.WriteMetrics` and in `internal/render/k8s.go`'s `credhealthRuleTmpl` (inject via template data). `metricdrift_test.go` still passes (now trivially). Run `make test`. Commit: `refactor: single source for credhealth metric names`.

**11e — explicit Sources comparison in diff**
- [ ] Replace `reflect.DeepEqual(a.Sources, b.Sources)` (`diff.go:100`) with a field-by-field slice comparison (length + per-element `Name/Domain/FilterSubject/TransformSource/TransformDest`). Keep `streamConfigChanged` semantics identical (nil == empty). Existing `diff_test.go` proves equivalence. Run `make test`. Commit: `perf(plan): replace reflect.DeepEqual in diff hot path`.

**► WAVE 4 CHECKPOINT — pause for review, then open PR.**

---

## Self-review notes

- **Spec coverage:** W1.1→Task 1, W1.2→Task 2, W1.3→Task 3, W2.1→Tasks 4-5, W2.2→Tasks 4+6, W3.1→Task 7, W3.2→Task 8, W3.3→Task 9, W4.1→Task 10, W4.2→Task 11a-e. Companion-design items (parallel minting, revocation compaction) intentionally excluded. DMZ/SYSTEM operator-mode issuance intentionally excluded (scope debt, noted in spec §2).
- **Refinement vs spec:** W1.1 is a reject guard (not the spec's rename/helper) — preserves operationally load-bearing DNS names and yields zero golden diff. Documented in Task 1.
- **Open execution decisions flagged inline:** Task 9 Step 4 (`.vikasa-manifest` location vs golden harness) and Task 10 Step 4 (default `-max-partition-sources=2500`).
- **Type consistency:** `SliceDir`, `MergeInto`, `WriteTree`, `MaxPartitionFanIn`, `FilterUnderDistrict` signatures are used consistently across the tasks that produce and consume them.
