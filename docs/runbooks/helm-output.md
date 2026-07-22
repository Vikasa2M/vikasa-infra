# Helm Chart Output Runbook

**Reference for the `-output=helm` packaging mode in `cmd/gen`.** When consumers need
a versioned, installable unit — without committing to Argo CD — this is the what-it-emits,
how-to-consume, and what-is-and-is-not-a-value reference.

> **Scope.** This runbook describes the `helm` packaging mode added to `cmd/gen`. The
> default mode (`-output=kustomize`) is unchanged; this runbook covers only the opt-in
> `-output=helm` path. Substitute your real `-spec`, `-out`, cluster `<id>`, and flag
> values for the placeholders below.

---

## 1. What `-output=helm` does

`cmd/gen` accepts a `-output` flag with two values: `kustomize` (default) and `helm`.
The flag controls **packaging only** — the same topology-derived manifests are generated
either way; what changes is how they are packaged for consumption.

With `-output=helm`, for each **kubernetes-substrate** cluster in the spec, `cmd/gen`
emits a self-contained Helm chart under `charts/<id>/` instead of the Kustomize overlay
under `clusters/<id>/`. Baremetal-substrate clusters are **unaffected** by `-output` in
exactly the same way they are unaffected by `-argo-repo-url`: they always emit
`nats-*.conf` and systemd units under `clusters/<id>/`, regardless of the flag.

Run the generator the same way you always do; just add the flag:

```
cmd/gen -spec <spec> -out <dir> -output=helm [other flags...]
```

---

## 2. Output layout

For each kubernetes cluster, the chart root contains:

```
charts/<id>/
  Chart.yaml            # Helm v2 application chart; name: vikasa-<id>
  values.yaml           # the four overridable deployment knobs; defaults reproduce the kustomize output
  templates/
    streams.yaml
    certificate.yaml
    externalsecret.yaml
    servicemonitor.yaml
    prometheusrule.yaml
    credhealth-prometheusrule.yaml   # central cluster only
```

The `templates/` files are the same NATS Stream, Certificate, ExternalSecret,
ServiceMonitor, and PrometheusRule manifests that the kustomize path emits — with the
four deployment knobs replaced by Helm value references (see §3). `credhealth-prometheusrule.yaml`
is emitted only for the central cluster (the issuer is the source of truth for credential
expiry), exactly as in kustomize mode. No `kustomization.yaml` is written under
`charts/<id>/`; Helm does not use it.

Baremetal clusters continue to appear under `clusters/<id>/` with their `nats-*.conf`
and systemd units as before.

---

## 3. The four overridable values

`values.yaml` exposes exactly four deployment knobs:

| Value | Manifest(s) affected | Default |
|---|---|---|
| `namespace` | all resources (namespace field, cert DNS SANs) | cluster namespace from the spec |
| `tlsIssuer` | `certificate.yaml` (cert-manager issuer ref) | value of `-tls-issuer` flag |
| `secretStore` | `externalsecret.yaml` (External Secrets store ref) | value of `-secret-store` flag |
| `prometheusRelease` | `servicemonitor.yaml`, `prometheusrule.yaml`, `credhealth-prometheusrule.yaml` (release label) | value of `-prometheus-release` flag |

The defaults in `values.yaml` are the concrete flag values passed to `cmd/gen`, so a
bare `helm template charts/<id>` (no value overrides) produces the same manifests as the
kustomize mode (modulo Helm's `---` separators and `# Source:` header comments that Helm
prepends to each resource). Override any knob at install time with `--set` or `-f my-values.yaml`.

Everything else — stream names, replica counts, source/destination subjects, placement
labels, sync-wave annotations — is **topology-derived** and is baked into `templates/`
as literal values. These are **not** Helm values, and they cannot be changed at
install time. To change the topology, update the spec and re-run `cmd/gen`.

---

## 4. Consuming without Argo

The chart is a standard Helm v2 application chart. With Helm installed, consume it
directly:

```
# Render to stdout (no cluster needed):
helm template <release> charts/<id>

# Dry-run with overrides:
helm template <release> charts/<id> --set namespace=vikasa-staging --set tlsIssuer=staging-ca

# Install:
helm install <release> charts/<id>

# Install with value overrides:
helm install <release> charts/<id> --set namespace=vikasa-staging --set tlsIssuer=staging-ca

# Install with a values file:
helm install <release> charts/<id> -f my-values.yaml
```

No Argo CD, no `kubectl apply`, no Kustomize — just Helm.

---

## 5. Consuming with Argo

`-output` and `-argo-repo-url` are orthogonal flags. When both are set, the generator
emits an Argo CD `Application` whose `source.path` points at `charts/<id>` instead of
`clusters/<id>`. Argo auto-detects the Helm chart from `Chart.yaml` and uses its standard
Helm source handling:

```
cmd/gen -spec <spec> -out <dir> -output=helm -argo-repo-url=<url> [other flags...]
```

The emitted `argocd/<id>.yaml` Application contains:

```yaml
source:
  repoURL: <url>
  targetRevision: <rev>
  path: charts/<id>
```

Argo remains fully opt-in. Without `-argo-repo-url` the `argocd/` directory is not
written, and the chart is consumed directly with `helm` as in §4.

---

## 6. Rationale

Helm is a **packaging** format; Argo is a **delivery** engine. They are independent.
This mode exists so consumers who do not use Argo still get a versioned, parameterizable,
installable unit — `helm install` works without any GitOps tooling.
