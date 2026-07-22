# Vikasa Scaling Profiles — Normal, Full-Track / Digital-Twin, Mixed

Status: **planning / sizing guide** — 2026-07-11
Scope: how to size a DOT deployment (partitions, shards, nodes, storage, retention)
for its data profile. Derivation and per-sensor grounding: `docs/capacity-model.md`.
Architecture: `docs/ARCHITECTURE.md`. Findings: `docs/decisions/2026-07-11-jetstream-scaling-review.md`.

> **Same architecture, two data profiles.** Both profiles run the *identical*
> topology — per-partition regional streams, per-partition-group central shards, DMZ
> tiering, sink-idempotency + DMZ dedup. They differ only in **cabinet data rate →
> partition/shard count, storage, and retention**. This is the "one design, many
> profiles" principle (ARCHITECTURE.md §6) extended to data volume.
>
> **All numbers are planning anchors, not measured.** They are replaced by the load
> test (capacity-model §6). Per-R3-leader anchors used here:
> **~75k msg/s** and **~100 MB/s** (whichever binds first). Confirm both on the
> target node class before committing K and node sizing.

---

## 1. The profiles

| | **Normal (operational)** | **Full-Track / Digital-Twin** |
|---|---|---|
| **What flows** | ASC HR signal events; perception **events** (zone occupancy, actuation, counts, safety triggers); SPaT/status | ASC HR; **full fused object tracks** (all road users, per-frame); high-res SPaT; V2X BSM ingest; rich per-object attributes |
| **Vendor output ingested** | Event / Event-Zone / ITS-Edge actuation | Full object-track stream (Detect API / MQTT) |
| **Use cases** | Signal ops, ATSPM, adaptive control, counts, incident/safety alerts | Digital twin, near-miss/conflict analysis, trajectory analytics, simulation calibration, safety research |
| **Bound by** | **message rate** | **bandwidth + storage** |
| **Typical scope** | **fleet-wide** (every cabinet) | **subset** — high-value corridors/hotspots/pilot zones (rarely fleet-wide) |
| **Envelope requirement** | n/a (already events) | **per-frame object arrays** (never per-object) — capacity-model §2.5 |
| **Extra requirements** | — | low-latency **live** path + durable replay (dual path); short JetStream retention + archive |

**Rule of thumb:** you do not digital-twin a rural signal. Twin mode lands on the
1–20% of intersections where trajectory-grade data has value; the rest run normal.
Size for a **mixed fleet** (§4), not all-or-nothing.

---

## 2. Per-cabinet budget

| Profile | msg/s | Bytes/s | Uplink | Notes |
|---|---|---|---|---|
| **Normal** | **~40** | ~15 KB/s | ~0.12 Mbps | small payloads (~150–300 B); message-rate dominated |
| **Full-Track (per-frame batched)** | **~80** | **~200 KB/s** | **~1.6 Mbps** | modest msg/s, **large frames** (~5–20 KB fused object arrays); byte dominated |
| Full-Track (per-object — **avoid**) | ~2000–8000 | ~250 KB/s | ~2 Mbps | same *bytes*, ~30× the message overhead → wrecks the per-leader ceiling |

> These are *profile* rates (a whole-cabinet blend); they map onto the *per-sensor
> tiers* of `capacity-model.md` §2 — **Normal** ≈ T1 (signal-only) plus light
> event-mode perception ≈ T1–T2; **Full-Track** ≈ T3 batched. The two tables are the
> same physics at different granularity, not different estimates.

The two full-track rows carry the **same information**; per-object just multiplies
JetStream per-message cost (sequence, replication, dedup). **Mandate per-frame
batching** and twin mode is byte-bound and manageable; leave it per-object and it is
message-bound and not viable at scale.

---

## 3. Single-profile sizing (per DOT)

Leaders = max(aggregate msg/s ÷ 75k, aggregate MB/s ÷ 100). Central shards ≈ regional
leaders (central ingests the whole DOT — finding C1). "Leaders" below is the binding
of the two.

### Normal mode (40 msg/s, 0.015 MB/s per cabinet) — **message-bound**

| Cabinets | msg/s | MB/s | Leaders (msg / byte) | Verdict |
|---|---|---|---|---|
| 10k | 400k | 150 | 6 / 2 → **6** | trivial |
| 20k | 800k | 300 | 11 / 3 → **11** | easy |
| 50k | 2.0M | 750 | 27 / 8 → **27** | comfortable with sharding |

### Full-Track / Digital-Twin, **fleet-wide** (80 msg/s, 0.2 MB/s per cabinet) — **byte-bound**

| Cabinets | msg/s | GB/s | Leaders (msg / byte) | Verdict |
|---|---|---|---|---|
| 10k | 800k | 2.0 | 11 / 20 → **20** | one strong cluster |
| 20k | 1.6M | 4.0 | 22 / 40 → **40** | multi-cluster |
| 50k | 4.0M | 10.0 | 54 / 100 → **100** | multi-cluster; ~80 Gbps into central — big but buildable, rarely warranted fleet-wide |

Fleet-wide twin is byte-bound: you add shards to spread **MB/s**, not msg/s.

---

## 4. Mixed fleet (the realistic deployment)

Most DOTs run **normal fleet-wide + twin on a subset**. Size the two and add.

Example — **50k cabinets, 10% (5k) in twin mode**:

| Segment | Cabinets | msg/s | MB/s | Leaders (binding) |
|---|---|---|---|---|
| Normal | 45k | 1.8M | 675 | 24 (msg) |
| Twin | 5k | 400k | 1000 | 10 (byte) |
| **Total** | 50k | 2.2M | 1.68 GB/s | **~34** |

A 50k-cabinet DOT with a 5k-intersection twin program needs **~34 R3 leaders** across
regional + a matching central shard set — well within a modest multi-cluster core.
**Sizing formula:**

```
leaders ≈ Σ_segment max( cabinets·rate_msg / 75_000 , cabinets·rate_MB / 100 )
central_shards ≈ leaders           # central ingests the whole DOT
nodes_core ≈ ceil(central_shards / leaders_per_node)   # leaders_per_node from load test, HA-asset budget ~2k
```

---

## 5. Storage & retention (where the profiles diverge most)

Region/central are the *recent* buffer, not the archive (ClickHouse/object store for
history — ARCHITECTURE.md §6). Retention must be sized so the JetStream tier does not
become the archive.

| Profile | Bytes/s @ 50k | 1h retention | Practical JetStream retention | Archive |
|---|---|---|---|---|
| **Normal** | ~0.75 GB/s | ~2.7 TB | **hours** (6h default ≈ 16 TB, NVMe-fine) | ClickHouse |
| **Full-Track twin @ 5k** | ~1.0 GB/s | ~3.6 TB | **minutes** at region; twin state reconstructed live | ClickHouse (downsampled) + object store |
| Full-Track twin fleet-wide @ 50k | ~10 GB/s | ~36 TB | **minutes only** — 1h is 36 TB | mandatory downsample before archive |

**Twin retention must be short** (minutes, not hours) at the JetStream tier — a live
twin reads the current stream, and history is the archive's job. This is a
`MaxAge`/`MaxBytes` setting per shard (finding C2): normal shards get hours, twin
shards get minutes, both explicitly bounded.

---

## 6. Latency & durability (twin needs the dual path)

Normal mode is throughput-only. **Digital-twin adds a latency + completeness
requirement** a live twin can't meet from replay alone:

- **Live path** — core-NATS fan-out (`RePublish`, Decision 3) delivers current world
  state to the twin at sub-second latency, partition-agnostic, no per-consumer JS
  state. This is how the twin stays "live."
- **Durable path** — JetStream durable consumers per shard (with discovery, Decision
  4) give the twin gap-fill/replay after a disconnect and feed the analytics archive.

Both already exist in the design — twin mode just *uses both at once*. No new
mechanism; the DMZ/sink tiering decisions cover it.

---

## 7. Constraints & guardrails (apply to both profiles)

- **Per-leader ceiling** (~75k msg/s **or** ~100 MB/s) is the sharding trigger — load-test both axes; twin hits the byte axis first, normal the message axis.
- **HA-asset budget** ~2k replicated assets/node (streams + R>1 consumers) — rarely the limit vs throughput, but caps total shards+consumers per node (capacity-model §3).
- **Field uplink wall** — twin per-object shipping is ~impossible on cellular field links; per-frame batching keeps twin uplink ~1.6 Mbps/cabinet (capacity-model §4).
- **Per-DOT ceiling** — no national cluster; largest single deployment ~15–30k cabinets; national = ~50 DOTs (capacity-model §5).
- **Central must shard** to ≈ regional leader count — a single central stream serves only the smallest normal deployments (finding C1).
- **Every shard bounded** — `MaxAge`/`MaxBytes` per tier and per profile (normal=hours, twin=minutes); no unbounded `limits` stream (finding C2).

---

## 8. How to use this doc

1. Classify the deployment: normal fleet size, twin subset size (§1 rule of thumb).
2. Look up per-cabinet budget (§2) and compute per-segment aggregate.
3. Apply the §4 formula → regional leaders, central shards, core node count.
4. Set per-profile retention (§5) and confirm the live+durable paths for twin (§6).
5. **Run the load test** to replace the 75k msg/s / 100 MB/s anchors with measured
   values on your node class and payload sizes — then re-run §3–§4.
