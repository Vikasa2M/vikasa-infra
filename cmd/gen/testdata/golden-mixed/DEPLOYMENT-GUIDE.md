# Deployment Guide: exdot

> **Generated** by vikasa-infra/cmd/gen — review before applying.

## Topology Summary

**DOT:** `exdot`
**Deployment mode:** mixed (Kubernetes + bare-metal)

### Clusters

| ID | Substrate | JS Domain | Leaf Endpoint |
|----|-----------|-----------|---------------|
| `core` | kubernetes | `core` | `leaf-core.nats.vikasa.exdot:7422` |
| `d7a` | bare-metal | `d7a` | `leaf-d7a.nats.vikasa.exdot:7422` |
| `d7b` | bare-metal | `d7b` | `leaf-d7b.nats.vikasa.exdot:7422` |

## Provisioning Order

1. Ensure the **central** cluster (`core`) is reachable and its operator/accounts exist (prerequisite).
2. Provision the **central** cluster's slice in `clusters/core/` first (k8s: apply the Stream CRs, sync-wave 0; bare-metal: see install notes below).
3. Provision each **regional** cluster's slice in `clusters/<id>/` (k8s: sync-wave 1).
4. Create DNS records (below).
5. Cabinets onboard incrementally (separate runbook).

### Bare-Metal Install Notes

Sub-project B (PKI / accounts) supplies a `security.conf` next to each `nats.conf`
(TLS, accounts, operator/resolver). It is referenced via `include "security.conf"`
and is **not** generated here.
- **`d7a`** — on hosts `exdot-d7a-1, exdot-d7a-2, exdot-d7a-3`: install `nats-server`, drop that host's
  `clusters/d7a/nats-<host>.conf` as `/etc/vikasa/nats.conf`, install
  `clusters/d7a/nats-server.service`, then `systemctl enable --now nats-server`.
  Create the streams with `nats stream add --config clusters/d7a/streams/<NAME>.json`.
- **`d7b`** — on hosts `exdot-d7b-1, exdot-d7b-2, exdot-d7b-3`: install `nats-server`, drop that host's
  `clusters/d7b/nats-<host>.conf` as `/etc/vikasa/nats.conf`, install
  `clusters/d7b/nats-server.service`, then `systemctl enable --now nats-server`.
  Create the streams with `nats stream add --config clusters/d7b/streams/<NAME>.json`.

### Kubernetes Clusters

Applied by Argo / `kubectl apply`.

## Reconciliation Strategy

Each renderer emits per-cluster file slices, so any reconciliation toolchain works. Default: per-cluster Argo CD (pull) for multi-cluster deployments; hub Argo when all clusters are co-administered from one control plane; `kubectl apply` or bare-metal systemd otherwise.

## DNS Records

Create these CNAME/A records in your `vikasa.exdot` zone:

| Name | Target |
|------|--------|
| `leaf-exdot-d7-0.nats.vikasa.exdot` | `leaf-d7a.nats.vikasa.exdot:7422` |
| `leaf-exdot-d7-8.nats.vikasa.exdot` | `leaf-d7b.nats.vikasa.exdot:7422` |

> **Tip:** On Kubernetes with [external-dns](https://github.com/kubernetes-sigs/external-dns), these records can be created automatically from Service/Ingress annotations (optional).

## Verification

For each cluster, verify that the expected streams are present:

**`core`** (`leaf-core.nats.vikasa.exdot:7422`):

```
nats --server leaf-core.nats.vikasa.exdot:7422 stream ls
```
Expected streams:
- `VIKASA_EXDOT_CENTRAL_D7_D7_0`
- `VIKASA_EXDOT_CENTRAL_D7_D7_8`

**`d7a`** (`leaf-d7a.nats.vikasa.exdot:7422`):

```
nats --server leaf-d7a.nats.vikasa.exdot:7422 stream ls
```
Expected streams:
- `VIKASA_EXDOT_D7_D7_0`

**`d7b`** (`leaf-d7b.nats.vikasa.exdot:7422`):

```
nats --server leaf-d7b.nats.vikasa.exdot:7422 stream ls
```
Expected streams:
- `VIKASA_EXDOT_D7_D7_8`

> The central cluster (`core`) should also show each partition stream as a source of its central shard (`VIKASA_<DOT>_CENTRAL_<district>_<partition>`).

## Deployment (GitOps)

Each `clusters/<id>` is a Kustomize overlay (`kustomization.yaml`).

- **Kubernetes:** deploy with Argo CD — apply the generated `argocd/<id>.yaml` (per-cluster pull: each cluster's Argo CD syncs its own `clusters/<id>` from the configured repo) — or directly with `kubectl apply -k clusters/<id>`. `argocd/<id>.yaml` is generated only when `cmd/gen -argo-repo-url` is set.
- **Bare-metal:** no Argo — install the rendered `nats.conf` + systemd unit and apply the streams per the bare-metal steps above.

## Observability

NATS exposes metrics via the prometheus-nats-exporter (enable it in the NATS Helm release; it serves a `metrics` port on the NATS Service). With the Prometheus Operator installed:

- The generated `clusters/<id>/servicemonitor.yaml` scrapes that `metrics` port; `clusters/<id>/prometheusrule.yaml` ships starter NATS/JetStream alerts (`NatsServerDown`, `NatsSlowConsumers`, and `NatsLeafDown` on regional clusters). Both carry `release: kube-prometheus-stack` for operator discovery — tune the alert exprs to your exporter version.
- **Bare-metal:** NATS serves monitoring on `:8222`; scrape it via a separate exporter (not generated here).

## TLS / mTLS

All inter-tier links use mTLS. The CA and certificates are operator-provisioned (not generated here):

- **Kubernetes:** create the `vikasa-ca` ClusterIssuer (your CA — self-signed, Vault, or an intermediate). cert-manager issues each cluster's server cert into secret `<cluster>-nats-server-tls` from the generated `clusters/<id>/certificate.yaml`; mount that secret into the NATS server pods and reference it in the server's TLS listeners.
- **Bare-metal:** provision `/etc/vikasa/tls/{ca,leafnode-server-{cert,key},cluster-{cert,key},client-{cert,key}}.pem` on each host (e.g. via vault-agent). The generated `nats.conf` already references these paths.
- **Cabinet client certs:** `cmd/issue -cabinets` mints `ca/cabinet-ca.crt` (the cabinet client CA) and, per cabinet, `cabinets/<district>/<id>.{crt,key,creds}`. Append `cabinet-ca.crt` to each server's trusted client-CA bundle (the `ca_file` the listeners reference) so cabinet client certs validate; deliver each cabinet its own `.crt`/`.key`/`.creds`. The client and leafnode listeners use `verify` (the cabinet's identity is its user JWT, not the cert).
- **Inventory token (External Secrets):** provision the `vikasa-secrets` ClusterSecretStore (your backend — Vault, AWS/GCP SM, …) and populate `vikasa/exdot/infrahub-token`. The generated `clusters/<id>/externalsecret.yaml` materializes the `infrahub-token` Secret (key `token`); the poller reads it (env/mount) instead of an inline token. Bare-metal/field cabinets pull the same backend value at imaging (sub-project E). No plaintext token is generated.

## Operations — HA, Retention & Scaling

Day-2 operation of the `exdot` topology. Placement is infra-side and mutable; identity (`vikasa.exdot.<district>.>`) is stable — rehosting/rebalancing is transparent to central and DMZ (they subscribe by district token, never by cluster).

### High Availability

- Each cluster runs a **3-node R3 quorum** (JetStream RAFT) — tolerates the loss of **1 node**; RAFT caps JetStream at **3–5 nodes**, so scale by adding clusters, not nodes.
- A district spans **multiple independent clusters** for fault isolation and independent upgrades. Central sources each cluster's partition streams cross-domain (`$JS.<domain>.API`), so losing one cluster degrades **only its partitions**, not the district.
- This deployment's clusters:
  - `core` (kubernetes) — 3-node R3 quorum
  - `d7a` (bare-metal) — 3-node R3 quorum
  - `d7b` (bare-metal) — 3-node R3 quorum
- Central aggregation cluster: `core`.

### Retention

- Every stream is **bounded**: file storage with a `max_age` **and** a `max_bytes` cap (finding C2). The exact values per stream are in each cluster's generated `streams.yaml`.
- Regional streams keep **short retention (hours)** — the recent past, not the archive; **central runs shorter still (minutes)** as an aggregation/routing tier. History lives in **ClickHouse**. Perception volume is governed by the ingest envelope (see `docs/capacity-model.md`), not a fixed multiplier.
- **Node-level backstop (operator-set):** the generator bounds each *stream*, not the NATS *server's* JetStream store. On k8s set `jetstream.max_file_store` in the NATS Helm values; on bare-metal it is in the generated `nats.conf` (`max_file_store`).
- To change retention: edit the spec, rerun `cmd/gen`, and apply (NACK reconciles, or `nats stream update`).

### Scaling

Scale levers, **in order** (reserve identity splits for org changes only):

1. **Vertical** — bigger NVMe nodes first (bounded by the 3–5-node RAFT cap).
2. **Partition within the cluster** — K streams on the same 3 nodes.
3. **Partition across an added cluster** — the district spans clusters.
4. **Split the district identity** — *only* for organizational reorgs, never for capacity.

**Shard, don't split:** assign partitions **explicitly** (by geography, in the spec/inventory), never by hashing — growth moves a *chosen* subset, never a hash-avalanche.

**Expansion = rerun:** edit the spec → rerun `cmd/gen` → review the generated `REBALANCE.md` (emitted with `-previous`) for the ordered move/rehost steps → apply. Humans decide *when* (from the Observability alerts + monitoring); the generator computes the *how*. Exact partition count **K** and node sizing come from a load test (no baseline yet — the capacity advisor is a deferred follow-on).
