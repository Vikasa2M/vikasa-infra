# Hardening: JetStream alerts + generated-doc truth — Implementation Plan (4 of 4)

**Goal:** Add JetStream-layer Prometheus alerts (the "wall gauges" — store fill, consumer lag, HA-asset budget) closing **E4**, and correct the two generated docs that still assert things the review disproved: the rebalance runbook's "`ce-id` dedup makes overlap harmless" (**C4** disproved it) and the deployment guide's retention prose (now bounded streams + short central retention, **C2/C1**).

**Architecture:** Two template surfaces. `prometheusRuleTmpl` (`internal/render/k8s.go`) gains JetStream alerts in the existing "starter exprs operators tune" style, using the prometheus-nats-exporter `gnatsd_jsz_*` (JSZ) metric family. `rebalance.tmpl` and `runbook.tmpl` get prose corrections. All changes flow through `make golden`. **E1 is intentionally NOT changed** — `cmd/gen` already warns (non-fatal, `-max-partition-sources=2500`, `main.go:46`), a deliberate design because legitimate partitions can be large; a hard error would contradict it.

**Tech Stack:** Go `text/template`, Prometheus/Alertmanager PrometheusRule CRD, golden-tree byte comparison.

## Global Constraints

- **Exporter dependency:** JetStream alerts require the exporter's **JSZ** collector (`-jsz`); metric names are `gnatsd_jsz_*`. Ship them with the same "starter exprs, tune to your exporter version" disclaimer the existing alerts carry — do not claim they are turnkey.
- **Golden protocol:** `make golden` then review — every cluster's `prometheusrule.yaml` gains the JetStream alerts; `REBALANCE.md` and `DEPLOYMENT-GUIDE.md` prose changes. No stream config changes.
- Deferred to Plan 3b: **account-level** limits (`max_file`/`max_streams`/`max_consumers`) live in the accounts model. Deferred to operator (documented here, not generated): the k8s NATS **server** `max_file_store` (Helm values, not owned by this generator).
- TDD where there's logic (render test for the alerts); prose fixes are golden-verified.

---

### Task 1: Add JetStream alerts (E4)

**Files:** Modify `internal/render/k8s.go` (`prometheusRuleTmpl` ~229-269, its doc comment); Test `internal/render/k8s_test.go`.

**Interfaces:** No data-struct change — alerts use the existing `prometheusRuleData` (`Cluster`, `Namespace`). Cluster-wide, emitted on every cluster.

- [ ] **Step 1: Write the failing test** — append to `k8s_test.go`:

```go
func TestK8sRenderer_JetStreamAlerts(t *testing.T) {
	files, err := render.K8sRenderer{}.RenderCluster(render.ClusterSlice{
		ID: "d7a", SubstrateType: "kubernetes", DOT: "exdot", JSDomain: "d7a", Namespace: "vikasa-d7a",
		LeafEndpoint: "leaf-d7a.nats.vikasa.exdot:7422", IssuerName: "vikasa-ca", SecretStore: "vikasa-secrets",
		PromRelease: "kube-prometheus-stack",
	})
	if err != nil {
		t.Fatalf("RenderCluster: %v", err)
	}
	pr := string(files["prometheusrule.yaml"])
	for _, want := range []string{"JetStreamStorageHigh", "JetStreamConsumerLagging", "JetStreamAssetsHigh", "gnatsd_jsz_"} {
		if !strings.Contains(pr, want) {
			t.Errorf("prometheusrule.yaml missing %q:\n%s", want, pr)
		}
	}
}
```

Note: confirm the `ClusterSlice` field for the Prometheus release label (grep an existing render test — it is `PromRelease` or similar) and the exact key `prometheusrule.yaml`; adjust if the test helper differs.

- [ ] **Step 2: Run — expect fail.**

- [ ] **Step 3: Add the alerts** — in `prometheusRuleTmpl`, immediately after the `NatsSlowConsumers` alert block (before `{{- if not .IsCentral }}`):

```
        - alert: JetStreamStorageHigh
          expr: gnatsd_jsz_max_storage{namespace="{{ .Namespace }}"} > 0 and gnatsd_jsz_storage{namespace="{{ .Namespace }}"} / gnatsd_jsz_max_storage{namespace="{{ .Namespace }}"} > 0.8
          for: 10m
          labels:
            severity: critical
            vikasa_cluster: {{ .Cluster }}
          annotations:
            summary: "JetStream store >80% of max in cluster {{ .Cluster }} — publishers fail when full (finding C2)"
        - alert: JetStreamConsumerLagging
          expr: gnatsd_jsz_consumer_num_pending{namespace="{{ .Namespace }}"} > 100000
          for: 15m
          labels:
            severity: warning
            vikasa_cluster: {{ .Cluster }}
          annotations:
            summary: "JetStream consumer backlog >100k in cluster {{ .Cluster }} — a sink is falling behind"
        - alert: JetStreamAssetsHigh
          expr: gnatsd_jsz_total_streams{namespace="{{ .Namespace }}"} + gnatsd_jsz_total_consumers{namespace="{{ .Namespace }}"} > 1500
          for: 30m
          labels:
            severity: warning
            vikasa_cluster: {{ .Cluster }}
          annotations:
            summary: "JetStream HA-assets approaching the ~2k/node budget in cluster {{ .Cluster }} — add nodes or partitions"
```

- [ ] **Step 4: Update the template's doc comment** — extend the `prometheusRuleTmpl` comment to note: "JetStream alerts (JetStream*) use the exporter's JSZ collector (`gnatsd_jsz_*`, requires `-jsz`); like the varz exprs they are starters operators tune to their exporter version."

- [ ] **Step 5: Run — expect pass. Commit** — `feat(render): JetStream Prometheus alerts — store/consumer-lag/assets (E4)`.

---

### Task 2: Correct the rebalance runbook's false dedup claim (C4)

**Files:** Modify `internal/render/rebalance.tmpl` (lines 12, 21, 28).

**Interfaces:** none (prose).

- [ ] **Step 1: Replace the three claims.** The review proved stream-level `ce-id` dedup is OFF on sourcing streams; duplicates during dual-source are absorbed by **idempotent sinks** (ClickHouse `ce-id`) internally and the DMZ dedup window at the boundary — not by stream dedup.

Line 12 — change:
`Identity is unchanged; cabinets re-resolve via DNS (durable buffer → no gap, `ce-id` dedup → no dup). Cabinet config is never touched.`
to:
`Identity is unchanged; cabinets re-resolve via DNS (durable buffer → no gap; duplicate deliveries during the switch are absorbed by idempotent sinks keyed on `ce-id`). Cabinet config is never touched.`

Line 21 — change:
`- Central now sources `{{ .Name }}` from `$JS.{{ .NewDomain }}.API` (was `$JS.{{ .OldDomain }}.API`). Dual-source briefly; `ce-id` dedup makes overlap harmless.`
to:
`- Central now sources `{{ .Name }}` from `$JS.{{ .NewDomain }}.API` (was `$JS.{{ .OldDomain }}.API`). Dual-source briefly; the overlap yields duplicate deliveries, deduplicated downstream by `ce-id`-keyed idempotent sinks (stream-level dedup is off on sourcing streams — finding C4).`

Line 28 — change:
`Cabinets land on the new cluster and re-source telemetry from the current sequence — durable buffer means no gap, dedup means no dup.`
to:
`Cabinets land on the new cluster and re-source telemetry from the current sequence — durable buffer means no gap; any duplicate delivery is reconciled by `ce-id`-keyed sinks.`

- [ ] **Step 2: Commit** — `docs(rebalance): overlap is absorbed by idempotent sinks, not stream dedup (C4)`.

---

### Task 3: Correct the deployment-guide retention prose (C2/C1)

**Files:** Modify `internal/render/runbook.tmpl` (Retention section ~137-141).

- [ ] **Step 1: Update the Retention bullets** — reflect bounded-everywhere + central short retention + the node-store-cap operator note.

Change line 139 to note both bounds:
`- Every stream is **bounded**: file storage with a `max_age` **and** a `max_bytes` cap (finding C2). The exact values per stream are in each cluster's generated `streams.yaml`.`

Change line 140 to reflect the two tiers + the reframe away from the bare "50×" figure:
`- Regional streams keep **short retention (hours)** — the recent past, not the archive; **central runs shorter still (minutes)** as an aggregation/routing tier. History lives in **ClickHouse**. Perception volume is governed by the ingest envelope (see `docs/capacity-model.md`), not a fixed multiplier.`

Add a new bullet after line 141 (the node-store-cap operator note):
`- **Node-level backstop (operator-set):** the generator bounds each *stream*, but not the NATS *server's* JetStream store. On k8s set `jetstream.max_file_store` in the NATS Helm values; on bare-metal it is in the generated `nats.conf` (`max_file_store`).`

- [ ] **Step 2: Commit** — `docs(runbook): bounded-streams + central short retention + node store-cap note (C2/C1)`.

---

### Task 4: Regenerate goldens and verify

- [ ] **Step 1:** `make test` → FAIL (prometheusrule + REBALANCE.md + DEPLOYMENT-GUIDE.md differ).
- [ ] **Step 2:** `make golden`.
- [ ] **Step 3: Review** — `git diff cmd/gen/testdata internal/render/testdata`:
  - Every `prometheusrule.yaml` gains the 3 JetStream alerts (`gnatsd_jsz_*`).
  - `REBALANCE.md` no longer says "dedup makes overlap harmless"; says idempotent sinks.
  - `DEPLOYMENT-GUIDE.md` retention section reflects bounds + central-minutes + node store-cap note.
  - **No stream-config (`streams.yaml`/stream JSON) changes.**
- [ ] **Step 4:** `make test && make lint && make integration` → all green.
- [ ] **Step 5: Commit** — `test(golden): regenerate for JetStream alerts + corrected runbook prose`.

---

## Self-Review

- **Coverage:** E4 — Task 1. C4 generated-doc lie — Task 2. C2/C1 runbook prose — Task 3. E1 — intentionally unchanged (existing warn is the deliberate design; recorded). Account-level limits + k8s server store-cap — deferred/operator-noted.
- **Placeholders:** Task 1 flags the `ClusterSlice` Prometheus-release field name to confirm. JetStream alert exprs are deliberately "starter" (exporter-tuned) — consistent with the existing template contract, not a placeholder defect.
- **Type consistency:** no struct changes; alerts reuse `prometheusRuleData`.
