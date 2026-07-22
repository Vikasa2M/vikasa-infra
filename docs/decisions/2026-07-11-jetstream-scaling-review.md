# ADR: JetStream configuration review — scaling, dedup, DMZ, cross-account authz

Status: **Findings accepted; decisions partly pending confirmation** — 2026-07-11
Scope: the generated NATS/JetStream data plane (`internal/plan`, `internal/render`,
`internal/accounts`) and the topology it renders for 10k+ cabinet DOTs.
Companion: `docs/capacity-model.md` (throughput math), `docs/ARCHITECTURE.md` (north star).

Format: Context (findings) → Decisions → Alternatives → Consequences. All findings
are grounded in the generated goldens + the nats-server source (`2.15.0-dev`,
commit `b7aeab5`); file:line cites are to that tree.

---

## Context — review findings

Ground truth: every stream the generator emits (regional, central, DMZ) is a
**sourcing** stream, and the generator sets only `name`, `replicas`,
`retention: limits`, `storage: file`, and `maxAge: 6h` on regional. Nothing else.

### C1 — Central is a single un-partitioned R5 stream (P0)
Golden `core/streams.yaml`: one `VIKASA_EXDOT_CENTRAL` sources **every** partition.
Regional shards into K streams; central re-collapses the whole DOT into one RAFT
group / one leader. Replicas do not add write throughput (`StreamMaxReplicas = 5`,
`server/stream.go:705`). At raw HR a 5-district DOT is 100–500k+ msg/s through one
leader — the hard scale ceiling. Contradicts the doc's own "partition heavily."

### C2 — No stream has a size bound; central & DMZ have no bound at all (P0)
Only regional sets `maxAge: 6h`. Central/DMZ are `retention: limits` with **zero
limits on any axis**. `max_bytes`/`max_msgs`/`discard`/`duplicates` set nowhere
(IR `Stream` struct has 7 fields, none of them). k8s path sets no node store cap;
only bare-metal `nats.conf` caps `max_file_store: 100GB`. NATS default `discard =
old`; a `limits` stream with no limits is bounded only by the server store — when
that fills, publishers error and one stream starves every other on the node.

### C3 — Cross-account JetStream sourcing authz is unmodeled; only single-account is proven (P0)
The source's internal consumer is a **push consumer with flow control**, created on
the *origin* via its JS API (`server/stream.go:3948-3957`: `DeliverSubject`,
`AckPolicy: AckNone`, `FlowControl: true`; API subject `$JS.<domain>.API` via
`ExternalStream.ApiPrefix`). Cross-**account** sourcing therefore needs the origin
account to export `$JS.<domain>.API` + a deliver subject (`ExternalStream.DeliverPrefix`,
`server/stream.go:428`) + `$JS.FC.>`, and the consumer account to import all three.
`internal/accounts` models **only** data-subject `{ stream: ... }` import/export —
no service crossing. The integration test proves delivery only within a **single
`APP` account spanning domains** (`dmz_flow_test.go` `leafConf`); the multi-account
production path is unmodeled and untested.

### C4 — Dedup is DISABLED on every stream, not defaulted (P0)
`server/stream.go:1714`: the 2-minute default duplicate window is applied only
`if cfg.Duplicates == 0 && cfg.Mirror == nil && len(cfg.Sources) == 0`. Every
Vikasa stream has `sources:`, so it keeps `Duplicates = 0`, and
`storeMsgIdLocked` (`server/stream.go:5308-5311`) treats `Duplicates <= 0` as
**disabled** — no msg-id is ever tracked. The rebalance runbook's "ce-id dedup makes
overlap harmless" (ARCHITECTURE.md §6) is **false as generated**: during a dual-source
drain the same ce-id arrives at central via two different origin streams (distinct
sequences → source tracking can't dedup) with msg-id dedup off. Note: sourced
messages *are* dedup-eligible (`!isMirror`, `server/stream.go:6610`) — the gate is
purely the missing window. Also `Duplicates ≤ MaxAge` (`server/stream.go:1735`), so
a real window needs a matching `MaxAge`.

### C5 — Regional 6h retention is the only slack to central (P1)
Regional ages out at 6h; central sources it. A central-side stall > 6h ages messages
out of regional before central pulls them; the cabinet buffer is the true backstop.
Confirm a recovered central re-derives from current sequence without a gap.

### E-series (P2)
- **E1** fan-in has no cap: `MaxPartitionFanIn` reports but never errors
  (`internal/plan/cabinets.go:77-94`). 10k cabinets = 10k sources; no guardrail.
- **E2** overlapping subject transforms double-store corridors (intended fan-out;
  unbounded without C2 fix).
- **E3** no `MaxMsgsPerSubject` — a stuck cabinet can balloon one subject.
- **E4** monitoring watches server/leaf/slow-consumer but **nothing JetStream**
  (no store-usage, consumer-pending, or RAFT-health alerts) — blind to C1/C2/E1.
- **E5** R5 default on the hottest tier; R3 is the documented sweet spot.

### Consumption model (ground truth)
External DMZ consumers today bind **JetStream pull consumers** (`dmz_flow_test.go:145`).
A consumer binds to exactly one stream (`server/consumer.go:422`) — so a sharded
central is consumed by N consumers, and JetStream stores do not reach core
subscribers unless `RePublish` (`server/stream.go:180`) echoes them to the core bus.

---

## Decisions

> Cross-references elsewhere use the shorthand **D1–D7** for Decisions 1–7 below.

1. **Central = sharded JetStream, per-partition-group, short retention** *(confirmed)*.
   Central hosts multiple JetStream aggregation streams keyed by the existing placement
   map (partition = scaling atom), so a large district arrives pre-split and small
   districts co-locate. Fixes C1 for districts of any size; shard count ≈ regional
   leader count (capacity-model §3). **Central retention is short (minutes)** — it is
   an aggregation/routing tier, not the archive (ClickHouse = history, regional JS =
   deep replay buffer), so re-persisting the DOT-wide aggregate costs little storage.
   Sinks (ClickHouse, DMZ) get **one consumption point** (central shards, with
   discovery) rather than reaching into every regional cluster.

   **Escape hatch (documented, not built): pure core-NATS central.** If a deployment
   blows past the JetStream per-leader ceiling — e.g. full-track/twin fleet-wide at the
   upper cabinet range, where all-JetStream central roughly doubles the JS node count —
   central can become a pure core-NATS fan-out fabric (regional `RePublish` + leaf
   propagation; durability stays at cabinet+regional; archive pulls from regional JS
   directly; DMZ stays JS). This removes the C1 ceiling and the central cross-account
   `$JS.API` crossing, at the cost of tier-aware sinks. Not built now — the per-frame
   envelope (Decision 7) keeps the realistic max within JetStream's comfortable range,
   and a single central consumption point is simpler to operate.

2. **Dedup strategy = idempotent sink + NATS-at-the-boundary** *(confirmed)*.
   ClickHouse is the idempotency authority on `ce-id` (ReplacingMergeTree) — JetStream
   consumers are at-least-once regardless, so the sink had to be idempotent anyway.
   Enable an explicit `Duplicates` window (with matching `MaxAge`) **on the DMZ
   stream** — the last controlled hop before data leaves the trust boundary, where
   peers can't dedup themselves. Internal streams rely on the sink.

3. **DMZ consumption = tiered** *(confirmed)*. Keep the DMZ JetStream stream as the
   ingest/transform/dedup buffer. Expose it two ways: **core-NATS fan-out via
   `RePublish`** onto `share.`/`peer.` subjects for the many loss-tolerant third
   parties (ephemeral, partition-agnostic, keeps strangers off the JS API, no HA-asset
   cost); **durable JetStream consumers** only for the few named peers needing replay.

4. **Sink consumption = shard-aware with discovery** *(confirmed)*. Durable sinks
   (ClickHouse, peers) run one consumer/worker per central shard and **discover shards**
   via `$JS.API.STREAM.NAMES` glob (`VIKASA_<DOT>_CENTRAL_*`) so scale-out needs no
   sink reconfiguration. Loss-tolerant consumers use the core-NATS subject (Decision 3).

5. **Stream limits = bounded per tier** *(confirmed)*. Add `MaxBytes`/`MaxAge`
   (+`Duplicates` where dedup is on) to the IR; set account-level `max_file`/`max_streams`/
   `max_consumers`; render a node store cap on the k8s/Helm path; add a golden lint that
   fails on any unbounded stream. Final values come from the load test.

6. **Account model = internal-shared, DMZ/peer isolated** *(confirmed)*.
   Collapse DISTRICT+CENTRAL sourcing into one internal account (hot regional→central
   path becomes intra-account, no service imports); keep DMZ and PEER as separate
   accounts with generated `$JS.<domain>.API` + `DeliverPrefix` + `$JS.FC` service
   import/export pairs. Isolation exactly at the real trust boundary. Alternatives:
   full multi-account (every crossing needs service imports — most surface), or single
   account (ACL-only isolation — too weak for the sharing boundary).

7. **Perception ingest = events by default; per-frame tracks where analytics needs them**
   *(from capacity-model)*. A fully loaded cabinet's msg/s is **not a confident single
   number** — it's set by *which vendor output Vikasa ingests*, not the sensor's
   internal rate. The vendor edge appliance already does perception (raw points never
   touch NATS) and exposes **events** (~tens/s, ≈T1/T2) vs **full object tracks**
   (~hundreds–thousands/s). Two requirements follow: (a) **scope decision** — subscribe
   to perception *events* unless a use case (near-miss, trajectory, HR archive) needs
   tracks; (b) if ingesting tracks, `openits-models` defines the envelope as **per-frame
   object arrays** (not per-object), pinning msg/s to `sensors × frame_rate`. The
   envelope decision is the difference between 45 partitions serving ~17,000 fusion
   cabinets or ~850 (capacity-model §2.4, §2.5, §3). This is an
   `openits-models`/`vikasa-collector` scope requirement surfaced by the infra model.

---

## Consequences

- IR gains stream fields (`MaxBytes`, `Duplicates`, per-tier `MaxAge`) and a central
  shard model; both renderers + goldens change; `plan.Diff` must compare the new fields.
- `internal/accounts` gains a **service** export/import type (`$JS.API`/deliver/FC) —
  the first non-stream crossing it models.
- `dmz_flow_test.go` must be extended to exercise the **multi-account** topology and
  the `RePublish` core-NATS path, so the proof matches production (closes C3's test gap).
- Sinks (ClickHouse writer, peer consumers) become shard-aware — a `vikasa-collector`
  change, out of this repo, but driven by this repo's placement map.
- The capacity model (not a single figure) becomes the sizing input; the load test
  (ARCHITECTURE.md §12) replaces its anchors.

Priority: **C1, C2, C4 are P0** (C4 upgraded from "short window" to "disabled" after
source inspection). C3 P0 for multi-account correctness. C5/E-series follow.
