# Vikasa Capacity Model — cabinet message rates & JetStream scaling

Status: **analysis / planning** — 2026-07-11
Scope: bottom-up throughput model for the NATS/JetStream data plane. Replaces the
single hand-wave figure in `ARCHITECTURE.md` §6 ("~20–100k msg/s per ~2,140-cabinet
district") with a sensor-grounded model and the scaling math that follows from it.
Operator sizing per deployment profile: `docs/scaling-profiles.md` (uses this as its
derivation).

> **Every number here is a planning estimate, not a measured baseline.** The one
> hard dependency the architecture still lacks is a **load test** (ARCHITECTURE.md
> §6, §12). This doc gives the model to test *against*; the load test replaces the
> anchors (per-leader throughput, per-cabinet rate) with measured values.

---

## 1. TL;DR

- The doc's implicit cabinet (~10–47 msg/s) is a **signal-only (ASC HR) cabinet**.
  It is accurate for that, and low for any perception-equipped cabinet.
- **A fully loaded cabinet's msg/s is not a confident single number** — it swings
  10–100× on the wire-envelope granularity (per-object vs per-frame), which is a
  *design choice*, not physics. What is confident is the **information rate**
  (objects/s × payload); the message rate must be an engineered contract. See §2.5.
- **The sensor vendor already does perception at the edge** (raw points never touch
  NATS). The cabinet rate is set by *which vendor output Vikasa ingests* — **events**
  (~tens/s, ≈T1/T2) or **full object tracks** (~hundreds–thousands/s). This scope
  choice moves the number ~1–2 orders of magnitude (§2.4).
- **If Vikasa ingests full tracks** (perception analytics / HR archive), it must
  ingest **per-frame batched**, not per-object — field uplink and central bandwidth
  make per-object shipping impossible at fleet scale (§2.5, §4).
- Deployment is **per-DOT**; there is no national system. The largest single
  deployment is a big-state DOT (~15–30k cabinets). "National" = ~50 independent
  DOT deployments, not one cluster.
- **Central must shard** to roughly the same leader count as the regional tier
  (finding C1). At sensor-fusion scale a single central stream is impossible.

---

## 2. Per-cabinet message rate (bottom-up)

Each sensor emits at a known physical rate. Message = one CloudEvents-enveloped
record on NATS (raw HR = one msg per event; reduced = one msg per frame/window).

| Source | Basis | Peak rate (msg/s) | Notes |
|---|---|---|---|
| **ASC high-res log** (NTCIP 1202 / ATSPM enumerations) | 0.1s (10 Hz) event log: detector on/off + phase/interval/ped/coord | **10–70** | Dominated by detector actuations at peak. ~25/s daily avg ≈ 2.2M events/day/intersection (matches "millions/day" ATSPM literature) |
| **Camera CV metadata** (1–4 cams, edge object detection) | per-frame detection arrays @ 10–30 fps | **10–120** (per-frame) · 100s–1000s (per-object) | Video is NOT on NATS — only detection output |
| **LiDAR perception** (1–8 sensors) | object tracks @ 10–20 Hz | **10–160** (per-frame) · 200–2000 (per-object) | Raw point cloud NEVER on NATS — only perception output |
| **V2X / BSM** (SAE J2735) | 10 Hz per connected vehicle in range | **50–500** today · 1000+ future | Scales with CV penetration |
| **Misc events** (ped/bike, RWIS, air, acoustic) | sub-Hz to few Hz | **1–10** | Negligible |

### Cabinet tiers (per-cabinet aggregate)

msg/s below assume **per-frame batching** for perception (one message per sensor per
frame, carrying the object array) — the recommended contract. The per-object column
is the naïve worst case (one message per tracked object per frame) and is
granularity-, not physics-, driven.

| Tier | Sensors | msg/s (per-frame) | msg/s (per-object) | Avg msg size |
|---|---|---|---|---|
| **T1 — Signal-only** | ASC HR only | **~25** | ~25 | ~150 B |
| **T2 — Signal + video CV** | ASC + multi-cam CV | **~40–80** | ~200–1000 | ~300 B (frame: ~2–5 KB) |
| **T3 — Full fusion** | ASC + multi-cam CV + LiDAR + radar + V2X | **~100–300** | **~2000–8000+** | ~500 B (frame: ~5–20 KB) |

**The `ARCHITECTURE.md` figure (~9–47 msg/s/cabinet) = T1.** Correct for signal-only.
For T3 the honest statement is a **band, not a point**: ~100–300 msg/s if the wire
envelope batches per frame, or thousands if it emits per object. Grounded anchor: one
LiDAR-instrumented intersection logged ~15.2M vehicle + 170k pedestrian waypoints/24h
= **~175 object-updates/s daily average, ~500–1000+ at peak, from a single sensor**
(Utah study). A fully loaded cabinet stacks several such systems.

## 2.4. The sensor vendor already does perception — the lever is *which output you ingest*

Modern ITS perception (e.g. Ouster BlueCity/Gemini) runs on an **edge appliance**
(Catalyst box): raw ~5.2M points/s is consumed *on the box* and **never touches
NATS**. The appliance exposes two output classes, and *which one Vikasa subscribes
to* — not the sensor's internal rate — sets the cabinet msg/s:

| Vendor output | Content | Rate | Vikasa use case |
|---|---|---|---|
| **Events / Event-Zone** (incl. "ITS Edge" actuation layer) | zone entry/exit, actuation calls, counts, safety triggers | **~1–20/s** | signal ops, counts, alerts |
| **Full object tracks** (Detect API / MQTT) | per-object pos/vel/class/UUID/history @ frame rate | **~175–1000+/s** | trajectory/near-miss analytics, HR archive |

So a **fully-sensored cabinet in event mode is ~tens of msg/s (≈T1/T2)** — the vendor
already reduced tracks→events. The high-rate T3 numbers below are the **full-track**
ingestion case. Vikasa's stated "raw HR + buffer-the-past + ClickHouse-for-history"
ambition, applied to *perception*, implies full-track mode — so the high-rate regime
is a design target if perception analytics is in scope, not a strawman. **Scope
decision to make explicit: does Vikasa ingest perception *events* or *trajectories*?**
It moves the cabinet rate ~1–2 orders of magnitude.

## 2.5. Message rate is a contract; information rate is the invariant

The quantity to size against is the **information rate**, which *is* physically
bounded per intersection:

```
objects/s  ≈ (road users in coverage) × (perception frame rate) × (sensor overlap)
           ≈ ~17 avg → ~50–150 peak objects × 10–20 Hz × 1–3   (fused → un-fused)
           ≈ hundreds to a few thousand object-updates/s at peak
bytes/s    ≈ objects/s × per-object payload (~200–600 B)
```

The **message rate is derived from a framing decision on top of this**:

- **Per-frame batched** (recommended): `msg/s = sensors × frame_rate` → tens–low
  hundreds, *independent of traffic density*. One message carries N objects.
- **Per-object-per-frame** (naïve): `msg/s = objects/s` → hundreds–thousands,
  scales with congestion exactly when you can least afford it.

Same information, same bytes, ~10–100× different message count. Fewer-bigger also
slashes JetStream **per-message** overhead (sequence record, replication round-trip,
dedup bookkeeping) — which dominates the per-leader ceiling — so batching improves
both the bandwidth wall (§4) *and* the C1 shard math (§3).

**Design requirement:** `openits-models` defines the perception envelope as
**per-frame object arrays** (not per-object), making msg/s an engineered constant.
The load test parametrizes on **payload size, batch size, and objects/s** — never a
bare msg/s figure.

---

## 3. Aggregate load & leaders required

Anchor: **~75k msg/s per R3 leader** (conservative planning figure for small
messages on NVMe; R1 benches ~400k @128B, R3 is materially lower due to replication
sync — treat 50k conservative / 150k optimistic as the band, confirm by load test).
A **stream = one RAFT group = one leader**; you scale write throughput by adding
**shards** (more leaders spread across cluster nodes), NOT by adding replicas
(replicas ≤ 5, and replication does not add write capacity).

Operating rates below use the **per-frame batched** envelope (T1=25, T2=60, T3=200).
Per-object emission multiplies T2/T3 by ~10–40× — the reason the envelope contract
(§2.5) is a hard requirement, not a tuning knob.

### Aggregate ingest (msg/s = cabinets × per-frame rate)

| Cabinets | T1 (25) | T2 (60) | T3 (200) | T3 per-object (~4000) |
|---|---|---|---|---|
| 10k | 250k | 600k | 2.0M | 40M |
| 20k | 500k | 1.2M | 4.0M | 80M |
| 50k | 1.25M | 3.0M | 10M | 200M |
| National ~315k | 7.9M | 18.9M | 63M | 1.26B |

### R3 leaders (shards) required @ 75k/leader

| Cabinets | T1 | T2 | T3 (batched) | T3 per-object |
|---|---|---|---|---|
| 10k | 4 | 8 | 27 | 533 |
| 20k | 7 | 16 | 53 | 1067 |
| 50k | 17 | 40 | 133 | 2667 |
| National ~315k | 105 | 252 | 840 | 16800 |

The per-object column is what you get by *not* deciding the envelope — it makes fusion
scale ~20× worse and is included only to show the cost of the wrong contract.

Leaders distribute across the cluster: a stream lives on **R of N** nodes
(`StreamMaxReplicas = 5`), the cluster can be much larger than R, and placement
balances leaders by HA-asset count. Per-node soft budget ≈ **2k HA assets** (streams
+ R>1 consumers both count). So asset count is rarely the limit — **per-leader
throughput is** (hence the load test).

### "5 districts × 9 partitions" = 45 regional leaders

- Ceiling ≈ 45 × 75k = **~3.4M msg/s aggregate**.
- **With the per-frame envelope**: serves **~17,000 T3 fusion cabinets** (3.4M ÷ 200)
  or **~57k T2** or **~135k T1** — 45 leaders is plenty for any realistic single DOT.
- **Without it (per-object T3, ~4000/cab)**: serves only **~850 cabinets** (3.4M ÷ 4000).

**This is the punchline:** the per-frame vs per-object envelope decision is the
difference between 45 partitions serving **~17,000** fusion cabinets or **~850**. The
partition count barely matters until the envelope is fixed.

---

## 4. The bandwidth / field-uplink wall (why edge reduction is mandatory)

Rate alone understates the problem; **rate × size** is the real constraint.

- **T3′ cabinet uplink:** 2000 msg/s × 600 B ≈ 1.2 MB/s ≈ **~10 Mbps sustained,
  per cabinet.** Field cabinets on 4G/LTE/DSL cannot sustain this fleet-wide.
- **Aggregate T3′ @ 50k:** 100M msg/s × 600 B ≈ 60 GB/s ≈ **~480 Gbps into central.**
  Not buildable.
- **T1 @ 50k:** 1.25M × 150 B ≈ 190 MB/s ≈ **~1.5 Gbps** — very tractable.

**Conclusion:** the "buffer-the-past, ship-raw" model holds for the **ASC signal
log** and for **perception events** (both small, event-sparse). It breaks only for
**full perception-track** ingestion. The points→object reduction is already done by
the vendor edge appliance (§2.4); Vikasa's levers are (1) **subscribe to events, not
tracks**, unless a use case needs trajectories, and (2) if it does ingest tracks,
require **per-frame batched** object arrays. Per-object track ingestion at fleet scale
is not viable — that's the design constraint, not "Vikasa must build edge reduction."

---

## 5. Per-DOT framing (there is no national cluster)

The deployment unit is **one DOT** (ARCHITECTURE.md §1); cross-DOT sharing is via
the DMZ, not a federation. So:

- A single deployment = one **operating agency's** scope — what it *operates*, not
  just what it owns. A centralized signal-operations program operates well beyond its
  own state-route inventory (across county/municipal agreements), so operational scope
  is the sizing input, not ownership.
- The largest single system to engineer is a **large statewide / centralized-SigOps
  DOT**, on the order of **mid-tens of thousands of cabinets** (upper bound ~30k).
- **National ≈ ~50 independent DOT deployments**, each sized to its own fleet. The
  315k-cabinet / national rows above are the *sum*, never a single system — do not
  size one cluster for them.
- This is why central sharding + multi-cluster-per-district (not supercluster) is
  sufficient: the worst case one deployment faces is ~30k cabinets, ~T2–T3.

### Design envelope for one deployment

| Deployment | Cabinets | Tier (envelope) | Aggregate | Regional/central leaders | Verdict |
|---|---|---|---|---|---|
| Mid DOT | 10k | T1–T2 batched | 0.25–0.6M | 4–8 | Comfortable |
| Large-state DOT | 30k | T2 batched | 1.8M | ~24 | Fine with sharding |
| Large-state DOT | 30k | T3 batched | 6.0M | ~80 | Multi-cluster; per-frame envelope assumed |
| Large-state DOT | 30k | T3 per-object | 120M | ~1600 | **Not viable** — envelope contract required |

---

## 6. Grounding

- JetStream throughput: benchmarks show R1 ~400k msg/s @128B async file store; R3
  lower due to replica sync. `nats bench` is the tool to reproduce.
  <https://onidel.com/blog/nats-jetstream-rabbitmq-kafka-2025-benchmarks> ·
  <https://docs.nats.io/using-nats/nats-tools/nats_cli/natsbench>
- `StreamMaxReplicas = 5`; stream placed on R of N nodes; HA-asset placement budget
  (`MaxHAAssets`): nats-server `server/stream.go:705`, `server/jetstream_cluster.go:8069,8169`.
- ASC HR: 0.1s-resolution event logging, Indiana/Purdue enumerations (Econolite,
  Siemens, Peek, McCain, Intelight, Trafficware). <https://github.com/udotdevelopment/ATSPM>
- Perception object rate (measured anchor): ~15.2M vehicle + 170k pedestrian
  waypoints/24h at one instrumented intersection ≈ ~175 object-updates/s daily avg,
  ~500–1000+ peak, single sensor. Ouster ITS tracks at 10 Hz.
  <https://www.ncbi.nlm.nih.gov/pmc/articles/PMC11479351/> ·
  <https://ouster.com/insights/blog/using-ouster-lidar-data-to-advance-intersection-safety-research>
- V2X BSM: 10 Hz, SAE J2735.
- US signal count: ~210k analyzed ≈ 2/3 of total → **~315k**.
  <https://inrix.com/blog/suprising-findings-from-the-inrix-signals-scorecard/>

**Load-test targets (replace the anchors above):** measured R3 msg/s and MB/s per
leader at ~200 B and ~600 B payloads on the target node class; per-cabinet rate for
the actual sensor build; cross-domain source-recovery time. These set partition
count K and node sizing (ARCHITECTURE.md §12 open decision).
