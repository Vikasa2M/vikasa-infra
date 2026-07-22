# Vikasa-infra documentation

Platform/deployment layer for Vikasa — a generator that renders GitOps artifacts
from a declarative topology spec. Start here to find the right doc.

## Start here

| If you want… | Read |
|---|---|
| The 5-minute, diagram-led overview | [`CONCEPTS.md`](CONCEPTS.md) |
| The full north-star design + load-bearing decisions | [`ARCHITECTURE.md`](ARCHITECTURE.md) |

## Sizing & scaling

| | |
|---|---|
| [`capacity-model.md`](capacity-model.md) | Bottom-up throughput model: sensor rates → per-tier msg/s and the JetStream scaling math (replaces the old hand-wave figure). |
| [`scaling-profiles.md`](scaling-profiles.md) | Operator sizing guide: **Normal** vs **Full-Track / Digital-Twin** vs **Mixed** profiles — partitions, shards, nodes, storage, retention. |

## Decisions (ADRs)

`decisions/` records the load-bearing choices, one file per decision cluster
(Context → Decision → Alternatives → Consequences).

| ADR | Scope |
|---|---|
| [`decisions/2026-07-11-jetstream-scaling-review.md`](decisions/2026-07-11-jetstream-scaling-review.md) | JetStream scaling review — findings **C1–C5 / E1–E5** and Decisions 1–7 (central sharding, bounded streams, dedup strategy, DMZ tiering, account model, perception envelope). |
| [`decisions/2026-07-11-control-plane-issuance-decisions.md`](decisions/2026-07-11-control-plane-issuance-decisions.md) | Control-plane issuance — DMZ external-consumer credentials. |

## Runbooks (day-2 ops)

| | |
|---|---|
| [`runbooks/credential-compromise.md`](runbooks/credential-compromise.md) | Break-glass for the NATS trust chain + mTLS PKI. |
| [`runbooks/helm-output.md`](runbooks/helm-output.md) | The `-output=helm` packaging mode. |
| generated `REBALANCE.md` / `DEPLOYMENT-GUIDE.md` / `TOPOLOGY.md` | Per-`gen`-run, rendered from the same spec — cannot drift. |

## Implementation plans

`plans/` holds spec→plan→build records (mostly historical / delivered). The
JetStream-scaling remediation plans (stream bounds, central sharding, DMZ
republish, hardening) live under `plans/2026-07-11-*`.

---

**Reading order for a newcomer:** `CONCEPTS.md` → `ARCHITECTURE.md` → (to size a
real deployment) `scaling-profiles.md`. The repo root [`CLAUDE.md`](../CLAUDE.md)
is the contributor guide (invariants, commands, golden-test protocol).
