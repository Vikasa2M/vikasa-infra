# Stream Bounds + Dedup — Implementation Plan (1 of 4)

**Goal:** Give every generated JetStream stream an explicit size bound (`max_bytes`) and per-tier age bound, and turn on a deduplication window on the DMZ stream — closing findings **C2** (unbounded streams) and **C4** (dedup disabled) at the generator level.

**Architecture:** Add two fields to the substrate-free IR (`plan.Stream`), set conservative per-tier defaults in the three build functions, render them through both the k8s (NACK CR) and bare-metal (stream JSON) renderers, teach `plan.Diff` to track them, and add a Build-time invariant test that fails if any stream is unbounded. This is the first of four sequenced plans from `docs/decisions/2026-07-11-jetstream-scaling-review.md`; it establishes the IR-field-addition pattern the later plans (central sharding, accounts/RePublish, hardening) reuse.

**Tech Stack:** Go 1.x, `text/template` (k8s CR), `encoding/json` (bare-metal), NACK `jetstream.nats.io/v1beta2` Stream CRD, golden-tree byte-comparison tests in `cmd/gen`.

## Global Constraints

- **`internal/naming` is the single source of NATS naming/subject conventions** — never rebuild stream/account names inline (this plan adds no names, only limits).
- **Determinism:** every map iteration that reaches output must be sorted; struct field order in `bareStreamConfig` is fixed for stable JSON. Preserve existing order; insert new fields in the specified position.
- **Golden protocol:** after an intentional output change run `make golden`, then **review the git diff** — the golden diff is the review artifact. Refactors must produce a zero-diff tree; this plan intentionally changes every stream's rendered output.
- **NACK v1beta2 field names:** size bound is `maxBytes` (integer bytes); dedup window is `duplicateWindow` (duration string). Bare-metal (`nats stream add --config`) uses `max_bytes` (integer bytes) and `duplicates` (integer nanoseconds).
- **Limit values are conservative hardcoded defaults** (matching the existing hardcoded `6h` regional `maxAge`), pending the load test. Per-profile/spec-configurable limits are a later plan — do **not** add topology-spec fields here.
- TDD: failing test first; table-driven tests matching each package's existing style (`ptr()` in plan, `strings.Contains` checks in render tests).

---

### Task 1: Add `MaxBytes` + `Duplicates` to the IR and set per-tier bounds

**Files:**
- Modify: `internal/plan/plan.go` (Stream struct ~43-51; `buildRegional` ~220-227; `buildCentral` ~276-284; `buildDMZ` ~320-327)
- Test: `internal/plan/plan_test.go`

**Interfaces:**
- Produces: `plan.Stream.MaxBytes int64` (bytes; 0 = unset) and `plan.Stream.Duplicates string` (duration, e.g. `"5m"`; `""` = unset). Later tasks (render, diff) consume both. Per-tier defaults: regional `MaxAge:"6h"`, `MaxBytes:50 GiB`; central `MaxAge:"15m"`, `MaxBytes:20 GiB`; DMZ `MaxAge:"1h"`, `MaxBytes:10 GiB`, `Duplicates:"5m"`.

- [ ] **Step 1: Write the failing test**

Add to `internal/plan/plan_test.go` (uses the existing `ptr()` helper):

```go
func TestBuild_StreamsAreBounded(t *testing.T) {
	root := &topology.Root{
		Topology: &topology.Topology{
			Dot:     ptr("exdot"),
			Central: &topology.Central{Cluster: ptr("core")},
			Cluster: map[string]*topology.Cluster{
				"core": {JsDomain: ptr("core"), LeafEndpoint: ptr("leaf-core:7422")},
				"dmz":  {JsDomain: ptr("dmz"), LeafEndpoint: ptr("leaf-dmz:7422")},
			},
			District: map[string]*topology.District{
				"d7": {Partition: map[string]*topology.Partition{"d7/0": {Cluster: ptr("core")}}},
			},
			DMZ: &topology.DMZ{
				Cluster: ptr("dmz"),
				Shares: []*topology.Share{
					{Consumer: ptr("r"), From: ptr("vikasa.exdot.d7.>"), As: ptr("vikasa.exdot.share.r.>")},
				},
			},
		},
	}
	p, err := plan.Build(root)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	byTier := map[plan.Tier]plan.Stream{}
	for _, s := range p.Streams {
		byTier[s.Tier] = s
		if s.MaxBytes <= 0 {
			t.Errorf("stream %s (tier %s): MaxBytes must be > 0, got %d", s.Name, s.Tier, s.MaxBytes)
		}
		if s.MaxAge == "" {
			t.Errorf("stream %s (tier %s): MaxAge must be set", s.Name, s.Tier)
		}
	}
	if got := byTier[plan.TierDMZ].Duplicates; got != "5m" {
		t.Errorf("dmz Duplicates window: got %q, want %q", got, "5m")
	}
	if got := byTier[plan.TierRegional].MaxAge; got != "6h" {
		t.Errorf("regional MaxAge: got %q, want %q", got, "6h")
	}
	if got := byTier[plan.TierCentral].MaxAge; got != "15m" {
		t.Errorf("central MaxAge: got %q, want %q", got, "15m")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/plan/ -run TestBuild_StreamsAreBounded -v`
Expected: FAIL — compile error (`s.MaxBytes`/`s.Duplicates` undefined) or, after the struct exists, assertions fail because defaults are unset.

- [ ] **Step 3: Add the struct fields**

In `internal/plan/plan.go`, extend the `Stream` struct (currently ends at `Sources`):

```go
// Stream describes a single NATS JetStream stream to provision.
type Stream struct {
	Name       string // NATS stream name
	Cluster    string // cluster id (placement)
	JSDomain   string // that cluster's js-domain
	Replicas   int
	MaxAge     string
	MaxBytes   int64  // per-stream storage bound in bytes; must be > 0 (finding C2)
	Duplicates string // dedup window (e.g. "5m"); set on the DMZ stream only (finding C4)
	Tier       Tier
	Sources    []Source // cross-domain sources (populated for central stream)
}
```

- [ ] **Step 4: Add the default constants**

In `internal/plan/plan.go`, just below the `Tier` constants block (after line ~28):

```go
// Per-tier stream bounds. Conservative hardcoded defaults pending the load
// test (docs/capacity-model.md §6); spec-configurable limits are a later plan.
const (
	gib = int64(1) << 30

	regionalMaxBytes = 50 * gib
	centralMaxBytes  = 20 * gib
	dmzMaxBytes      = 10 * gib

	centralMaxAge   = "15m" // central is aggregation/routing, not the archive
	dmzMaxAge       = "1h"
	dmzDedupeWindow = "5m" // must be <= dmzMaxAge (NATS: Duplicates <= MaxAge)
)
```

- [ ] **Step 5: Set the bounds in the three build functions**

In `buildRegional` (the `regionalStreams = append(...)` near line 220), add `MaxBytes`:

```go
		regionalStreams = append(regionalStreams, Stream{
			Name:     p.streamName,
			Cluster:  p.clusterID,
			JSDomain: jsDomain,
			Replicas: 3,
			MaxAge:   "6h",
			MaxBytes: regionalMaxBytes,
			Tier:     "regional",
		})
```

In `buildCentral` (the `centralStream := Stream{...}` near line 276), set the short age + bound:

```go
	centralStream := Stream{
		Name:     CentralStreamName(dot),
		Cluster:  centralClusterID,
		JSDomain: *centralCluster.JsDomain,
		Replicas: centralReplicas,
		MaxAge:   centralMaxAge,
		MaxBytes: centralMaxBytes,
		Tier:     "central",
		Sources:  centralSources,
	}
```

In `buildDMZ` (the returned `Stream{...}` near line 320), set age, bound, and dedup:

```go
	return Stream{
		Name:       DMZStreamName(dot),
		Cluster:    *dmz.Cluster,
		JSDomain:   *dmzCluster.JsDomain,
		Replicas:   dmzReplicas,
		MaxAge:     dmzMaxAge,
		MaxBytes:   dmzMaxBytes,
		Duplicates: dmzDedupeWindow,
		Tier:       "dmz",
		Sources:    dmzSources,
	}, true, nil
```

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./internal/plan/ -run TestBuild_StreamsAreBounded -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/plan/plan.go internal/plan/plan_test.go
git commit -m "feat(plan): bound every stream (max_bytes) + DMZ dedup window in the IR"
```

---

### Task 2: Render the bounds in the k8s (NACK) renderer

**Files:**
- Modify: `internal/render/k8s.go` (`streamCRTmpl` ~19-50; `streamCRData` ~53-64; `renderStreamCR` ~92-101)
- Test: `internal/render/k8s_test.go`

**Interfaces:**
- Consumes: `plan.Stream.MaxBytes int64`, `plan.Stream.Duplicates string` (from Task 1).
- Produces: rendered CR lines `maxBytes: <int>` and `duplicateWindow: <dur>` (omitted when zero/empty).

- [ ] **Step 1: Write the failing test**

Add to `internal/render/k8s_test.go`:

```go
func TestRenderStreamCR_Bounds(t *testing.T) {
	s := plan.Stream{
		Name: "VIKASA_EXDOT_DMZ", Tier: plan.TierDMZ, Replicas: 3,
		MaxAge: "1h", MaxBytes: 10 << 30, Duplicates: "5m",
	}
	out, err := renderStreamCR(s, "exdot")
	if err != nil {
		t.Fatalf("renderStreamCR: %v", err)
	}
	got := string(out)
	for _, want := range []string{"maxBytes: 10737418240", "duplicateWindow: 5m", "maxAge: 1h"} {
		if !strings.Contains(got, want) {
			t.Errorf("CR missing %q\n---\n%s", want, got)
		}
	}
}

func TestRenderStreamCR_OmitsDuplicatesWhenUnset(t *testing.T) {
	s := plan.Stream{Name: "VIKASA_EXDOT_D7_D7_0", Tier: plan.TierRegional, Replicas: 3, MaxAge: "6h", MaxBytes: 50 << 30}
	out, err := renderStreamCR(s, "exdot")
	if err != nil {
		t.Fatalf("renderStreamCR: %v", err)
	}
	if strings.Contains(string(out), "duplicateWindow") {
		t.Errorf("regional CR must not emit duplicateWindow:\n%s", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/render/ -run TestRenderStreamCR_ -v`
Expected: FAIL — `maxBytes`/`duplicateWindow` absent from output.

- [ ] **Step 3: Add fields to the template data struct**

In `internal/render/k8s.go`, extend `streamCRData` (add after `MaxAge`):

```go
type streamCRData struct {
	MetaName   string
	DOT        string
	Tier       string
	Wave       int
	Name       string
	Replicas   int
	Storage    string
	Retention  string
	MaxAge     string
	MaxBytes   int64
	Duplicates string
	Sources    []sourceData
}
```

- [ ] **Step 4: Emit the fields in the template**

In `streamCRTmpl`, add the two conditional blocks immediately after the existing `maxAge` block (after line ~35, before `{{- if .Sources }}`):

```
{{- if .MaxBytes }}
  maxBytes: {{ .MaxBytes }}
{{- end }}
{{- if .Duplicates }}
  duplicateWindow: {{ .Duplicates }}
{{- end }}
```

- [ ] **Step 5: Populate the fields in `renderStreamCR`**

In `renderStreamCR`, extend the `data := streamCRData{...}` literal (add after `MaxAge: s.MaxAge,`):

```go
	data := streamCRData{
		MetaName:   toMetaName(s.Name),
		DOT:        dot,
		Tier:       string(s.Tier),
		Wave:       s.Tier.Wave(),
		Name:       s.Name,
		Replicas:   s.Replicas,
		MaxAge:     s.MaxAge,
		MaxBytes:   s.MaxBytes,
		Duplicates: s.Duplicates,
		Sources:    srcs,
	}
```

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./internal/render/ -run TestRenderStreamCR_ -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/render/k8s.go internal/render/k8s_test.go
git commit -m "feat(render/k8s): emit maxBytes + duplicateWindow on Stream CRs"
```

---

### Task 3: Render the bounds in the bare-metal renderer

**Files:**
- Modify: `internal/render/baremetal.go` (`bareStreamConfig` ~137-144; `renderBareStreamConfig` ~207-234)
- Test: `internal/render/baremetal_test.go`

**Interfaces:**
- Consumes: `plan.Stream.MaxBytes int64`, `plan.Stream.Duplicates string`.
- Produces: JSON keys `max_bytes` (int64 bytes) and `duplicates` (int64 nanoseconds), both `omitempty`.

- [ ] **Step 1: Write the failing test**

Add to `internal/render/baremetal_test.go`:

```go
func TestRenderBareStreamConfig_Bounds(t *testing.T) {
	s := plan.Stream{Name: "VIKASA_EXDOT_DMZ", Replicas: 3, MaxAge: "1h", MaxBytes: 10 << 30, Duplicates: "5m"}
	out, err := renderBareStreamConfig(s)
	if err != nil {
		t.Fatalf("renderBareStreamConfig: %v", err)
	}
	got := string(out)
	// max_bytes in bytes; duplicates in nanoseconds (5m = 300000000000).
	for _, want := range []string{`"max_bytes": 10737418240`, `"duplicates": 300000000000`} {
		if !strings.Contains(got, want) {
			t.Errorf("stream JSON missing %q\n---\n%s", want, got)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/render/ -run TestRenderBareStreamConfig_Bounds -v`
Expected: FAIL — keys absent.

- [ ] **Step 3: Add the JSON fields (fixed order)**

In `internal/render/baremetal.go`, extend `bareStreamConfig` — insert `MaxBytes` and `Duplicates` between `MaxAge` and `Sources` (order is load-bearing for deterministic JSON):

```go
type bareStreamConfig struct {
	Name        string             `json:"name"`
	Retention   string             `json:"retention"`
	Storage     string             `json:"storage"`
	NumReplicas int                `json:"num_replicas"`
	MaxAge      int64              `json:"max_age,omitempty"`
	MaxBytes    int64              `json:"max_bytes,omitempty"`
	Duplicates  int64              `json:"duplicates,omitempty"`
	Sources     []bareStreamSource `json:"sources,omitempty"`
}
```

- [ ] **Step 4: Populate them in `renderBareStreamConfig`**

In `renderBareStreamConfig`, after the existing `if s.MaxAge != "" { ... }` block (near line 220) and before the `for _, src := range s.Sources` loop, add:

```go
	cfg.MaxBytes = s.MaxBytes
	if s.Duplicates != "" {
		d, err := time.ParseDuration(s.Duplicates)
		if err != nil {
			return nil, fmt.Errorf("baremetal render: stream %s: bad duplicates window %q: %w", s.Name, s.Duplicates, err)
		}
		cfg.Duplicates = d.Nanoseconds()
	}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/render/ -run TestRenderBareStreamConfig_Bounds -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/render/baremetal.go internal/render/baremetal_test.go
git commit -m "feat(render/baremetal): emit max_bytes + duplicates in stream JSON"
```

---

### Task 4: Track the new fields in `plan.Diff`

**Files:**
- Modify: `internal/plan/diff.go` (`streamConfigChanged` ~92-100)
- Test: `internal/plan/diff_test.go`

**Interfaces:**
- Consumes: `plan.Stream.MaxBytes`, `plan.Stream.Duplicates`.
- Produces: a `MaxBytes`/`Duplicates` change classifies the stream as `Modified` (drives the rebalance runbook).

- [ ] **Step 1: Write the failing test**

Add to `internal/plan/diff_test.go`:

```go
func TestDiff_DetectsMaxBytesChange(t *testing.T) {
	old := &plan.Plan{DOT: "exdot", Streams: []plan.Stream{
		{Name: "VIKASA_EXDOT_CENTRAL", Cluster: "core", JSDomain: "core", Replicas: 5, MaxAge: "15m", MaxBytes: 20 << 30, Tier: plan.TierCentral},
	}}
	newer := &plan.Plan{DOT: "exdot", Streams: []plan.Stream{
		{Name: "VIKASA_EXDOT_CENTRAL", Cluster: "core", JSDomain: "core", Replicas: 5, MaxAge: "15m", MaxBytes: 40 << 30, Tier: plan.TierCentral},
	}}
	d := plan.Diff(old, newer)
	if len(d.Modified) != 1 {
		t.Fatalf("MaxBytes change: want 1 Modified, got %d (%+v)", len(d.Modified), d.Modified)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/plan/ -run TestDiff_DetectsMaxBytesChange -v`
Expected: FAIL — `Modified` is empty (change not detected).

- [ ] **Step 3: Extend the comparison**

In `internal/plan/diff.go`, update the first condition of `streamConfigChanged`:

```go
func streamConfigChanged(a, b Stream) bool {
	if a.Replicas != b.Replicas || a.MaxAge != b.MaxAge ||
		a.MaxBytes != b.MaxBytes || a.Duplicates != b.Duplicates || a.Tier != b.Tier {
		return true
	}
	if len(a.Sources) == 0 && len(b.Sources) == 0 {
		return false
	}
	return sourcesChanged(a.Sources, b.Sources)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/plan/ -run TestDiff_DetectsMaxBytesChange -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/plan/diff.go internal/plan/diff_test.go
git commit -m "feat(plan): diff tracks max_bytes + dedup-window changes"
```

---

### Task 5: Unbounded-stream invariant test (the golden lint)

**Files:**
- Test: `internal/plan/plan_test.go`

**Interfaces:**
- Consumes: `plan.Build` output across every checked-in example spec.
- Produces: a regression guard — any future stream with `MaxBytes == 0` fails CI.

- [ ] **Step 1: Write the failing-until-covered test**

Add to `internal/plan/plan_test.go` (loads every example spec via `topology.Load`; asserts no unbounded stream). This test passes once Tasks 1 land, and guards against regressions forever after:

```go
func TestBuild_NoUnboundedStreams(t *testing.T) {
	specs, err := filepath.Glob("../../examples/*.json")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, spec := range specs {
		spec := spec
		if strings.Contains(spec, "INVALID") {
			continue // fixtures that are expected to fail topology.Load
		}
		t.Run(filepath.Base(spec), func(t *testing.T) {
			root, err := topology.Load(spec)
			if err != nil {
				t.Fatalf("Load %s: %v", spec, err)
			}
			p, err := plan.Build(root)
			if err != nil {
				t.Fatalf("Build %s: %v", spec, err)
			}
			for _, s := range p.Streams {
				if s.MaxBytes <= 0 {
					t.Errorf("%s: stream %s (tier %s) is unbounded (MaxBytes=0)", spec, s.Name, s.Tier)
				}
			}
		})
	}
}
```

Note: confirm `topology.Load`'s signature by reading `internal/topology/topology.go` — if it takes bytes rather than a path, read the file first (`os.ReadFile`) and pass the bytes; adjust the import block accordingly (`path/filepath`, `strings`, and either `os` or nothing extra). The assertion body is unchanged either way.

- [ ] **Step 2: Run test to verify it passes**

Run: `go test ./internal/plan/ -run TestBuild_NoUnboundedStreams -v`
Expected: PASS (every example now builds bounded streams). If it FAILS naming an `examples/*.json` spec, that spec exposes a code path (e.g. a topology with no DMZ) whose stream still lacks a bound — fix the corresponding build function, don't weaken the test.

- [ ] **Step 3: Commit**

```bash
git add internal/plan/plan_test.go
git commit -m "test(plan): assert no example spec produces an unbounded stream"
```

---

### Task 6: Regenerate goldens and review the diff

**Files:**
- Modify (generated): `cmd/gen/testdata/golden*/**/streams.yaml`, `cmd/gen/testdata/golden-dmz-baremetal/clusters/dmz/streams/*.json`, `cmd/gen/testdata/golden-helm/charts/**`, and `internal/render/testdata/golden/**` — all via `make golden`, never by hand.

**Interfaces:**
- Consumes: Tasks 1–3 (the IR + both renderers now emit bounds).
- Produces: golden trees that match the new output; the byte-comparison suite goes green.

- [ ] **Step 1: Run the full suite to confirm goldens are (expectedly) stale**

Run: `make test`
Expected: FAIL in `cmd/gen` golden comparisons — every `streams.yaml` / stream `.json` now differs (new `maxBytes`, `duplicateWindow`/`max_bytes`+`duplicates`, central `maxAge: 15m`). This is the intended, reviewable change.

- [ ] **Step 2: Regenerate the goldens**

Run: `make golden`

- [ ] **Step 3: Review the golden diff (the review artifact)**

Run: `git diff --stat cmd/gen/testdata internal/render/testdata` then `git diff cmd/gen/testdata/golden-dmz`
Expected, and verify by eye:
- Every regional `streams.yaml` gains `maxBytes: 53687091200`.
- `golden-*/clusters/core/streams.yaml` (central) gains `maxAge: 15m` and `maxBytes: 21474836480`.
- `golden-dmz*/clusters/dmz/streams.yaml` gains `maxAge: 1h`, `maxBytes: 10737418240`, `duplicateWindow: 5m`.
- Bare-metal `VIKASA_EXDOT_DMZ.json` gains `"max_bytes": 10737418240` and `"duplicates": 300000000000`.
- **No unexpected files change**; no name/subject/source changes (this plan touches limits only).

- [ ] **Step 4: Run the full suite green**

Run: `make test && make lint`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/gen/testdata internal/render/testdata
git commit -m "test(golden): regenerate for per-tier stream bounds + DMZ dedup window"
```

---

## Self-Review

- **Spec coverage:** C2 (bounded streams) — Tasks 1,2,3,5,6. C4 (dedup) — DMZ `Duplicates` window in Tasks 1,2,3; the *sink-idempotency* half of C4 (ClickHouse ce-id, `Nats-Msg-Id` header) is `vikasa-collector` scope, out of this repo (noted in the ADR). Diff tracking — Task 4. **Not** in this plan (later plans): central sharding (Plan 2), node-level `max_file_store` on the Helm path + account-level limits (fold into Plan 2's central work or a Helm-values task), the false rebalance-dedup prose in `rebalance.tmpl`/`runbook.tmpl` (Plan 4, shipped with `make golden`). These deferrals are intentional and recorded here so nothing is silently dropped.
- **Placeholders:** none — every step has exact code or an exact command. Task 5 Step 1 flags the one signature to confirm (`topology.Load`) with the exact fallback.
- **Type consistency:** `MaxBytes int64` and `Duplicates string` are used identically in plan.go, k8s.go, baremetal.go (parsed to ns), and diff.go. NACK field `duplicateWindow` (string) vs bare-metal `duplicates` (ns int64) is intentional and matches each substrate's schema.
