# Rebalance Runbook: exdot

> **Generated** by vikasa-infra/cmd/gen from the diff of the previous and new specs. Apply top-to-bottom; each phase is online and idempotent.

## Moved partitions (rehost)

Identity is unchanged; cabinets re-resolve via DNS (durable buffer → no gap; duplicate deliveries during the switch are absorbed by idempotent sinks keyed on `ce-id`). Cabinet config is never touched.

### Phase 1 — Stand up target streams

- Apply `clusters/d7b/streams.yaml` to create `VIKASA_EXDOT_D7_D7_8` on `d7b` (empty).

### Phase 2 — Dual-source at central

- Central now sources `VIKASA_EXDOT_D7_D7_8` from `$JS.d7b.API` (was `$JS.d7a.API`). Dual-source briefly; the overlap yields duplicate deliveries, deduplicated downstream by `ce-id`-keyed idempotent sinks (stream-level dedup is off on sourcing streams — finding C4).

### Phase 3 — Repoint leaf-DNS + cycle connections
Repoint the affected records (see **DNS changes** below), then cycle the affected cabinets' connections at the old cluster so they reconnect and re-resolve.

### Phase 4 — Cabinets re-source (no action)
Cabinets land on the new cluster and re-source telemetry from the current sequence — durable buffer means no gap; any duplicate delivery is reconciled by `ce-id`-keyed sinks.

### Phase 5 — Tear down old streams

- Remove central's source of `VIKASA_EXDOT_D7_D7_8` from `$JS.d7a.API`, then delete `VIKASA_EXDOT_D7_D7_8` on `d7a`.

## DNS changes

Update these records in your `vikasa.exdot` zone (manual unless external-dns automates them):

- repoint `leaf-exdot-d7-8.nats.vikasa.exdot`: `leaf-d7a.nats.vikasa.exdot:7422` → `leaf-d7b.nats.vikasa.exdot:7422`
