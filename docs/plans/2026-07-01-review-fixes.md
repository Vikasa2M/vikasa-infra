# Review-fix implementation plan (2026-07-01)

> **Status: COMPLETE (2026-07-02).** Phases 0–5 plus the phase-6 hardening
> items (empty-shares/partition-namespace validation, creds-dir permissions
> check, client-cert CN pinning) merged to `main` in PR #4. The only item not
> executed is the pointer-scalar topology model refactor below — track it as a
> GitHub issue.

Source: five-agent deep-dive review (correctness ×2, security, architecture, tests).
Goal: close all findings, ordered so the DMZ/credential trust boundary is fixed
first, consolidation refactors land on top of green tests, and CI pins everything.

**Conventions for every task**

- TDD: write the failing test first, watch it fail, then fix.
- After each task: `gofmt -l .`, `go vet ./...`, `go test -count=1 ./...`,
  `go test -tags integration ./test/integration/`.
- When golden output changes intentionally: regenerate with `UPDATE_GOLDEN=1`,
  then **review the golden diff by hand** before committing — the diff is the
  proof the change did only what it claims.
- One branch per phase off `helm-chart-output` (or its successor default),
  small commits per task.

**Phase 0 — prerequisite:** the working tree has uncommitted changes on
`architecture-docs` (runbook.tmpl, DEPLOYMENT-GUIDE goldens, ARCHITECTURE.md).
Land or stash that branch before starting; Phase 2's runbook task touches the
same files and must not tangle with it.

---

## Phase 1 — DMZ / credential trust boundary (security criticals + majors)

### 1.1 Fix the DMZ deny-by-default `as` guard
Finding: `internal/topology/topology.go:309-311` rejects only the literals `>`
and `vikasa.>`; the realistic dangerous value `vikasa.<dot>.>` passes.

- Examples use exactly two `as` conventions (`examples/exdot-dmz.json:20-21`):
  `vikasa.<dot>.share.<consumer>.>` and `vikasa.peer.<dot>.….>`.
- Rule to implement (allowlist, strictest option consistent with examples):
  `as` must be under `vikasa.<dot>.share.` **or** `vikasa.peer.<dot>.` —
  anything else is a validation error. This subsumes the old literal checks.
- Apply the same check to `from`? No — `from` is *supposed* to reach internal
  subjects; only verify it stays under `vikasa.<dot>.` (it already must).
- Tests first: table-driven negatives in `internal/topology` —
  `as: ">"`, `"vikasa.>"`, `"vikasa.exdot.>"`, `"vikasa.exdot.d1.>"`,
  `"other.prefix.>"`; positives for both blessed conventions.
- Update `docs/ARCHITECTURE.md` §7 if it documents the old rule.
- Size: S.

### 1.2 Bare-metal renderer: emit subject transforms (or refuse)
Finding: `internal/render/baremetal.go:137-154, 213-219` — `bareStreamSource`
has no transform fields; a DMZ share sourced onto a bare-metal cluster renders
an **unfiltered** source (full internal mirror). K8s path is correct
(`internal/render/k8s.go:35-48`).

- Add `SubjectTransforms []bareSubjectTransform` (`src`/`dest` JSON keys, per
  jsm `StreamSource.SubjectTransforms`, NATS ≥ 2.10 — the file is
  `nats stream add --config` input, see baremetal.go:17,135) to
  `bareStreamSource`; populate from `plan.Source.TransformSource/Dest`,
  mutually exclusive with `filter_subject`, mirroring the k8s branch.
- Tests first:
  - Unit test analogous to `TestK8sRenderer_DMZSourceTransform`
    (`internal/render/k8s_test.go:200-271`) for the bare path.
  - Extend the embedded-NATS harness (`test/integration/dmz_flow_test.go`)
    with one case that loads the *bare-metal-rendered* JSON semantics
    (same transform config) to prove the server accepts `subject_transforms`
    in a sourced-stream config. If the server rejects it, fall back to:
    hard-error in `BareMetalRenderer.RenderCluster` when any source carries a
    transform ("DMZ shares require a kubernetes substrate").
- Golden: add a bare-metal-DMZ scenario (topology with `dmz.cluster` on a
  bare-metal cluster) under `cmd/gen/testdata/golden-dmz-baremetal/`.
- Size: M.

### 1.3 DMZ consumer users: explicit publish deny + JetStream-API deny
Finding: `internal/accounts/accounts.go:116` gives DMZ users only `Subscribe`;
rendered config (`internal/render/accounts.tmpl:29-38`) omits the publish
clause → NATS defaults to allow-all publish, including `$JS.API.>`.

- Extend `UserTemplate` with deny fields (e.g. `PublishDeny`, `SubscribeDeny`).
- DMZ consumer users get `publish { deny: [">"] }`. Decide during
  implementation whether peers need *any* publish (JetStream pull consumers
  need `$JS.API.CONSUMER.MSG.NEXT.<stream>.<consumer>` publish); if pull
  consumption is the intended model, allow exactly that subject and deny the
  rest — check how the DMZ consumption story is documented in
  ARCHITECTURE.md §7 / runbook before choosing.
- Template: render `deny:` arrays when present (keep omit-when-empty for
  allow-only users).
- Tests first: unit test on `accounts.Build` for the DMZ user's permission
  sets; golden update for `cmd/gen/testdata/golden-dmz/accounts.conf`.
- Size: S–M (the pull-consumer decision is the only real thinking).

### 1.4 Subject-boundary helpers: fix `underPrefix`, enforce `.>` suffix, seed `internal/naming`
Findings: `internal/plan/cabinets.go:92-98` (`underPrefix` does raw string
prefix match when prefix lacks `.>` — `vikasa.exdot.d7` matches
`vikasa.exdot.d70.internal`); `topology.go:279-284` never requires declared
`subject-prefix` to end in `.>`.

- Create `internal/naming` with just what this phase needs:
  `UnderPrefix(subject, prefix string) bool` (token-boundary-safe regardless
  of trailing `.>`) and `SubjectSpace(dot, districtID string, declared *string) string`
  (declared-or-default resolution, currently duplicated in
  `accounts/accounts.go:138-143` and `plan/cabinets.go:38-41`).
- `topology.Validate`: require declared `SubjectPrefix` to end with `.>`.
- Point `plan.AttachCabinets` and `accounts.Build` at the new helpers.
- Tests first: `UnderPrefix` table test including the `d7`/`d70` boundary case;
  Validate negative for a prefix without `.>`.
- Size: S.

### 1.5 `cmd/issue`: validate cabinet filters before minting
Finding: `internal/issuance/issuance.go:252-297` mints `fleet.Cabinet.Filter`
verbatim into the user JWT's pub+sub allow — `"filter": ">"` in a tampered
inventory yields a district-wide credential incl. `$JS.API.>`. `cmd/gen`
would reject the same inventory; the tools disagree.

- Depends on 1.4. In the issuance cabinet loop: resolve the cabinet's district
  subject space via `naming.SubjectSpace` and reject any filter not
  `naming.UnderPrefix` it — same error wording as `plan.AttachCabinets` so the
  two tools agree byte-for-byte on what a valid inventory is.
- Also deny `$JS.API.>` publish on cabinet users unless the design requires it
  (cabinets publish telemetry; check whether cabinet-side JetStream publish
  acks need `$JS.ACK`/API access before adding the deny — if unclear, ship the
  filter validation alone and file the deny as follow-up).
- Tests first: issuance test with `filter: ">"` and with a filter under a
  *different* district's prefix — both must fail; happy path unchanged.
- Size: S.

**Phase 1 exit gate:** full suite + integration green; `golden-dmz` and new
`golden-dmz-baremetal` diffs reviewed; a topology with `as: "vikasa.exdot.>"`
and an inventory with `filter: ">"` are both rejected by *both* tools.

---

## Phase 2 — Correctness majors

### 2.1 Partition ID validation + stream-name collision detection
Finding: `topology.go:275-285` never token-validates partition IDs;
`plan/subject.go` `sanitize()` is non-injective (`a-b` and `a_b` →
`VIKASA_…_A_B`), and the name-keyed maps in `plan/diff.go:112-118` and
`plan/cabinets.go:52-57` silently drop one colliding stream (under-reported
diffs, cross-wired cabinet telemetry).

- Two layers (both, not either):
  1. `topology.Validate`: apply `tokenRE` to partition IDs (extended to permit
     the in-use `/` separator, e.g. `d7/0`).
  2. `plan.Build`: after building `Streams`, error on duplicate `Name` —
     belt-and-braces against any future non-injective mapping.
- Tests first: Validate negative for a bad partition ID; a `plan.Build` test
  with two IDs that sanitize identically expecting an explicit error.
- Size: S.

### 2.2 Runbook: helm-mode instructions
Finding: `internal/render/runbook.go` never reads `cfg.Output`;
`runbook.tmpl` unconditionally says `kubectl apply -k clusters/<id>` — wrong
and non-existent paths when `-output helm` (files live at
`charts/<id>/templates/`, applied via `helm install`).

- Thread `cfg.Output` into `runbookData` (e.g. `Packaging` + a
  path-prefix helper); branch the template: kustomize narrative vs
  helm narrative (`helm template`/`helm install`, `charts/<id>/`,
  values file pointers, no `kustomization.yaml` claims).
- Coordinate with the uncommitted `architecture-docs` runbook.tmpl edits
  (Phase 0) — rebase on whichever lands first.
- Tests first: assertions on helm-mode runbook content (contains
  `helm install`, does not contain `apply -k`), then regen
  `golden-helm/DEPLOYMENT-GUIDE.md` and review.
- Size: M.

---

## Phase 3 — Consolidation refactors (behavior-preserving; goldens must not change)

### 3.1 Finish `internal/naming`
- Move in: `sanitize()` (3 copies: `plan/subject.go:8`,
  `accounts/accounts.go:146`, `issuance/issuance.go:691`), stream-name
  construction (`PartitionStreamName` etc.), the `DISTRICT_<id>` /
  account-name convention (`accounts.go:71,81`, `issuance.go:260`), and a
  shared `PartitionIndex(root)` (dup: `plan/cabinets.go:32-49`,
  `issuance/issuance.go:229-240`).
- Mechanical migration; the proof of no behavior change is an untouched
  golden tree (`go test ./...` with zero golden diffs).
- Size: M.

### 3.2 Decompose `IssueWithRotation`
Finding: `issuance.go:92-407`, ~320 lines; `userKP.Wipe()` on 9 error paths;
in-loop `defer kp.Wipe()` at :181/:196 accumulates until function return.

- Extract `ensureOperator`, `ensureAccounts`, `mintCabinet` (single
  `defer userKP.Wipe()`), `writeResolverBundle`. Fix the in-loop defers by
  scoping each mint in its own function call. Correct the "wiped right after
  minting" comment or make it true.
- Existing issuance tests are the safety net; add one test asserting seeds are
  wiped after a mid-loop error (can assert via a failing writer injection if
  the seam exists, else skip).
- Size: M.

### 3.3 Type the stringly enums
- `plan.Tier` (`TierRegional/Central/DMZ`) with a `Wave()` method replacing
  the redundant int field; update the 4 consuming packages.
- Reuse `topology.SubstrateType` in `render/substrate.go:14-17` instead of
  re-declared strings; a typed `render.Output` (`kustomize|helm`) validated in
  one place instead of only `cmd/gen`.
- Goldens must not change.
- Size: M.

---

## Phase 4 — Test infrastructure & gaps

### 4.1 `compareGolden` helper
- One `compareGolden(t, gotDir, goldenDir)` in a shared testutil (internal to
  `cmd/gen` is fine), replacing the 6 drifted copies in
  `cmd/gen/main_test.go` (incl. ignored errors at :199,:396,:466,:472,:480);
  fold in the k8s_test variant if convenient. Print diffs, not whole files.
  Add `t.Parallel()` to the six golden tests.
- Size: S.

### 4.2 Validation negative tests
- Table-driven `topology.Validate` rejections (bad tokens incl. unicode/upper,
  missing js-domain/leaf-endpoint, replicas out of range, missing dmz.cluster,
  share field omissions) — much of this lands with 1.1/1.4/2.1; this task
  sweeps the remainder to get Validate well above its current 55%.
- `UnmarshalJSON` duplicate/missing-id errors (`topology.go:176-215`).
- Size: S.

### 4.3 Integration: isolation + fanout rigor
- Negative isolation test: publish a subject outside every share filter and
  assert it never appears in the DMZ stream (expect-timeout or
  `StreamInfo` `SubjectsFilter`); assert internal-form subjects
  (`vikasa.exdot.d1.>`) are absent from the DMZ stream's subject set.
- `TestDMZ_SeparateSourcesFanout` (`dmz_flow_test.go:331-336`): replace the
  log-and-return-green branch with `t.Fatalf` — the design is committed.
- Stretch (file as follow-up if >1 day): derive the integration stream configs
  from the generator's actual output instead of hand-written copies.
- Size: S (M with the stretch).

### 4.4 Edge-case & error-path sweep
- Degenerate topologies: district with zero partitions, central-only (no
  districts), `shares: []`, empty cabinet inventory — pin behavior of
  `plan.Build` + renderers (decide error-vs-empty-output per case, document).
- `cmd/gen run()` bad `-cabinets` / `-previous` paths (main.go:93-95,153-155).
- `pki.Wipe` nil-receiver/nil-key safety test (currently 0%, called via defer
  throughout issuance).
- Size: S–M.

---

## Phase 5 — Tooling, CI, minor cleanups

### 5.1 Makefile targets
- `make golden` → `UPDATE_GOLDEN=1 go test ./cmd/gen/... ./internal/render/...`
- `make integration` → `go test -tags integration ./test/integration/`
- `make lint` → staticcheck (add tool dep); document `UPDATE_GOLDEN` in README.
- Size: S.

### 5.2 GitHub Actions CI
- `.github/workflows/ci.yaml`: build, `gofmt` check, `go vet`, staticcheck,
  `go test -count=1 ./...`, `go test -tags integration ./test/integration/`,
  and the helm-render check (install helm on the runner so
  `TestGenGoldenHelm_RendersUnderHelm` stops silently skipping).
- Size: S.

### 5.3 Minor cleanup batch (one commit each, no coupling)
- `cmd/credhealth/main.go:24,33`: errors → stderr.
- `cmd/gen/main.go:37`: delete zombie `-substrate` flag.
- `internal/topology/load.go:9-11,35`: drop stale ygot/YANG comment claims.
- `internal/render/baremetal.go:233-240`: `centralLeafURL` comment says
  `nats://`, emits `tls://` — fix comment.
- `issuance.go:600,644`: `captureRetired*SK` swallow malformed-JWT errors —
  log a stderr warning so a silent no-retire is auditable.
- Issuance error prefixes say `issuance.Issue:` on rotation paths — parameterize.
- Size: S total.

---

## Phase 6 — Deferred / optional hardening

- **CA CN pinning + name constraints** (`pki.go:88`): pin leaf CN to inventory
  ID; add `PermittedDNSDomains` on the CA. Defense-in-depth (identity is the
  JWT), so scheduled last.
- **Runtime perms check** before writing seeds: verify `creds/` tree isn't
  world-readable.
- **Pointer-scalar topology model** (arch finding 7): a validated value-type
  view post-`Validate` to delete nil-guard boilerplate across plan/render/
  accounts/issuance. Worth doing before the codebase doubles; explicitly out
  of scope for this plan — file an issue.

---

## Sequencing summary

```
Phase 0 (land architecture-docs)
  → Phase 1 (1.1, 1.2, 1.3 independent; 1.4 → 1.5)
  → Phase 2 (2.1, 2.2 independent)
  → Phase 3 (3.1 → 3.2, 3.3)          [goldens frozen: no diffs allowed]
  → Phase 4 (all independent)          [4.1 first — later tests use the helper]
  → Phase 5 → Phase 6 (optional)
```

Phases 1–2 are the "ship even if nothing else lands" cut: they close every
critical/major security and correctness finding.
