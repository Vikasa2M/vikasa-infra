# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this repo is

Platform/deployment layer for traffic-infrastructure telemetry (speaking the
`openits-models` vocabulary) on NATS JetStream. It **generates** GitOps
artifacts from a declarative topology spec — it does not run services. One
deployment unit = one DOT: N districts (each with partition streams on
regional clusters), a central aggregation tier, and a DMZ (the
external-sharing trust boundary). Cabinets are Raspberry Pi field devices
outside k8s. `docs/README.md` indexes all docs; `docs/capacity-model.md` +
`docs/scaling-profiles.md` are the sizing SSOT (partitions/nodes/retention per
profile). `docs/ARCHITECTURE.md` is the north-star design; `docs/CONCEPTS.md`
is the short overview.

## Commands

```sh
make test          # unit suite, includes golden-tree byte comparisons
make integration   # embedded-NATS end-to-end tests (build tag `integration`)
make lint          # go vet + gofmt check
make golden        # regenerate goldens after an INTENTIONAL output change
make staticcheck   # staticcheck ./... (install: go install honnef.co/go/tools/cmd/staticcheck@latest)

go test ./internal/plan/ -run TestBuild_ExdotShared     # single test
go test -tags integration ./test/integration/ -run TestDMZ -v
go run ./cmd/gen -spec examples/exdot-dmz.json -out /tmp/out   # run the generator
```

CI (`.github/workflows/ci.yaml`) runs all of the above on every push/PR.

## Golden-test protocol

`cmd/gen` tests byte-compare the full output tree against
`cmd/gen/testdata/golden-*` (one dir per scenario), both directions
(missing golden AND unexpected produced file fail). After an intentional
output change: `make golden`, then **review the git diff** — the golden diff is
the review artifact proving the change did only what it claims. Refactors are
expected to produce a zero-diff golden tree.

## Architecture: three layers, one seam

```
topology spec (RFC 7951 JSON, examples/*.json)
  → internal/topology  (hand-written model; Load = Unmarshal + Validate + placement checks)
  → internal/plan      (substrate-free IR: streams/tiers/sources/DNS; Build, AttachCabinets, Diff)
  → internal/render    (SubstrateRenderer per substrate; render.Dispatch is the ONLY
                        place topology substrate detail crosses into rendering)
```

- `render.Dispatch` routes each cluster slice to `K8sRenderer` (NACK Stream CRs,
  kustomize overlays under `clusters/<id>/`) or `BareMetalRenderer` (nats.conf,
  systemd unit, `nats stream add --config` JSON). `-output helm` wraps kubernetes
  slices as charts under `charts/<id>/` by re-rendering K8sRenderer output through
  sentinel substitution (`internal/render/helm.go`) — bare-metal slices are
  unaffected. The runbook (`DEPLOYMENT-GUIDE.md`) is packaging-mode-aware.
- Control plane: `internal/accounts` (NATS account/ACL model) → `internal/issuance`
  (nkey/JWT trust chain + per-cabinet creds; `cmd/issue`) → `internal/credhealth`
  (expiry monitoring; `cmd/credhealth`). `internal/pki` signs cabinet client certs
  behind the `Signer` seam (a Vault backend can replace it).

## Load-bearing invariants

- **`internal/naming` is the single source of NATS naming/subject conventions**
  (Sanitize, stream/account/operator names, `UnderPrefix`, `SubjectSpace`).
  Never rebuild these strings inline — `cmd/gen` and `cmd/issue` must agree
  byte-for-byte on names and boundaries (issuance signs against the account
  names the model produces).
- **Determinism**: every map iteration that reaches output is sorted. Golden
  tests will catch violations, but sort at the source.
- **DMZ deny-by-default**: shares remap internal subjects onto public spaces
  (`vikasa.<dot>.share.` / `vikasa.peer.<dot>.`) via per-source subject
  transforms — one source per share (NATS rejects overlapping transforms in one
  source; duplicate-upstream-name sources fan out). DMZ consumer users are
  subscribe-only with an explicit `publish { deny: [">"] }` (an absent publish
  key in NATS config means allow-all). `test/integration/dmz_flow_test.go`
  proves both delivery and isolation against embedded servers.
- **Fail closed on credentials**: issuance rejects filters outside the cabinet's
  district subject space, refuses group/world-accessible creds dirs (tests use
  the `credsDir(t)` helper because `t.TempDir()` subdirs are 0755), and pins
  client-cert CNs to the inventory id. Seeds are 0600 and wiped after use —
  preserve the key-lifetime comments in `internal/issuance` when refactoring.
- Topology model fields are pointer scalars with id-keyed maps (preserved from
  its ygot-generated ancestor) — nil-check before dereferencing; validation
  belongs in `topology.Validate`, not scattered downstream.

## Conventions

- TDD: failing test first; table-driven tests matching each package's existing
  style (`loadInline` in topology, `ptr()` in plan, `run(options{...})` in cmd/gen).
- Error strings are load-bearing (tests assert substrings; `cmd/gen` and
  `cmd/issue` intentionally share boundary-violation wording).
- Never attribute commits or PRs to Claude/Anthropic.
