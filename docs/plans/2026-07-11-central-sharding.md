# Central Sharding + R3 — Implementation Plan (2 of 4)

**Goal:** Replace the single un-partitioned central stream with **per-partition central aggregation shards** (each sourcing one regional partition), and default them to R3 — closing finding **C1** (the whole-DOT single-leader bottleneck) and **E5** (R5 on the hot tier).

**Architecture:** Central becomes a shard *set*: one JetStream stream per partition, named `VIKASA_<DOT>_CENTRAL_<DISTRICT>_<PART>`, hosted on the central cluster, each sourcing exactly one regional partition stream cross-domain. Shard count ≈ regional leader count, so a large district (already many partitions) arrives pre-split. The DMZ egress — previously sourcing the one central stream — now fans each share across every central shard of that share's district (same subject transform per shard). This reshapes the validated DMZ egress, so the DMZ integration test is the correctness gate. Builds on Plan 1 (streams already carry `MaxBytes`/`MaxAge`).

**Tech Stack:** Go 1.x, `internal/naming` (naming SSOT), `text/template` + `encoding/json` renderers, NACK Stream CRD, golden-tree byte comparison, embedded-NATS integration tests.

## Global Constraints

- **`internal/naming` is the single source of NATS naming** — the central shard name is added there (`CentralShardStreamName`), never inlined. `cmd/gen` and `cmd/issue` must agree byte-for-byte.
- **Determinism:** central shards are emitted in the same sorted `parts` order used for regional streams; DMZ sources within a share preserve shard order. Every map iteration that reaches output is sorted.
- **Golden protocol:** `make golden` then **review the diff** — this plan intentionally splits `VIKASA_<DOT>_CENTRAL` into N shards across every golden and rewrites DMZ `sources`. Verify no *regional* stream or subject/transform *value* changes.
- **DMZ deny-by-default is preserved:** each share still remaps only its declared `from` (a district subject space) onto its public `as` space; fanning across shards does not widen what is shared. `test/integration/dmz_flow_test.go` must still prove both delivery and isolation.
- **Per-partition central shards** (not per-district): a large district is already many partitions; per-district would recreate a single-leader bottleneck for that district and under-shard for full-track (docs/capacity-model.md §3, docs/decisions/2026-07-11-jetstream-scaling-review.md Decision 1).
- **Deferred (Plan 4):** node-level `max_file_store` on the Helm/k8s path and account-level `max_file`/`max_streams`/`max_consumers` — not in this plan.
- TDD; table-driven tests (`ptr()` in plan, `strings.Contains` in render).

---

### Task 1: Add `CentralShardStreamName` to the naming SSOT

**Files:**
- Modify: `internal/naming/naming.go` (after `CentralStreamName` ~52-54)
- Test: `internal/naming/naming_test.go`

**Interfaces:**
- Produces: `naming.CentralShardStreamName(dot, district, partID string) string` → `VIKASA_<DOT>_CENTRAL_<DISTRICT>_<PART>` (each token `Sanitize`d). Consumed by `plan.buildCentralShards` and `plan.buildDMZ`.

- [ ] **Step 1: Write the failing test**

Add to `internal/naming/naming_test.go`:

```go
func TestCentralShardStreamName(t *testing.T) {
	got := naming.CentralShardStreamName("exdot", "d7", "d7/0")
	want := "VIKASA_EXDOT_CENTRAL_D7_D7_0"
	if got != want {
		t.Errorf("CentralShardStreamName = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run — expect fail** — `go test ./internal/naming/ -run TestCentralShardStreamName` → undefined.

- [ ] **Step 3: Implement**

In `internal/naming/naming.go`, after `CentralStreamName`:

```go
// CentralShardStreamName returns the NATS stream name for a per-partition central
// aggregation shard: VIKASA_<DOT>_CENTRAL_<DISTRICT>_<PART>. Central is sharded
// per partition (finding C1) so no single leader holds the whole DOT.
func CentralShardStreamName(dot, district, partID string) string {
	return "VIKASA_" + Sanitize(dot) + "_CENTRAL_" + Sanitize(district) + "_" + Sanitize(partID)
}
```

- [ ] **Step 4: Run — expect pass.**

- [ ] **Step 5: Commit** — `git commit -m "feat(naming): add CentralShardStreamName for per-partition central shards"`

---

### Task 2: Split central into per-partition shards (R3) in the plan

**Files:**
- Modify: `internal/plan/plan.go` — replace `buildCentral` (~261-287) with `buildCentralShards`; update `Build` wiring (~171-188) and the `Source` used for each shard.
- Test: `internal/plan/plan_test.go`

**Interfaces:**
- Consumes: the sorted `parts []partEntry` and the `centralSources []Source` (already one-per-partition, same order) from `buildRegional`.
- Produces: `buildCentralShards(dot string, central *topology.Central, parts []partEntry, centralSources []Source, getCluster func(string) (*topology.Cluster, error)) (shards []Stream, centralCluster *topology.Cluster, byDistrict map[string][]string, err error)` — `shards` is one central Stream per partition (Tier `central`, R3 default, `MaxAge: centralMaxAge`, `MaxBytes: centralMaxBytes`, one Source = that partition); `byDistrict` maps districtID → sorted central shard names, for the DMZ.

- [ ] **Step 1: Write the failing test**

Add to `internal/plan/plan_test.go` (a 2-partition, 2-district topology so we see one shard per partition, all on the core cluster):

```go
func TestBuild_CentralIsShardedPerPartition(t *testing.T) {
	root := &topology.Root{Topology: &topology.Topology{
		Dot:     ptr("exdot"),
		Central: &topology.Central{Cluster: ptr("core")},
		Cluster: map[string]*topology.Cluster{
			"d7a":  {JsDomain: ptr("d7a"), LeafEndpoint: ptr("leaf-d7a:7422")},
			"core": {JsDomain: ptr("core"), LeafEndpoint: ptr("leaf-core:7422")},
		},
		District: map[string]*topology.District{
			"d7": {Partition: map[string]*topology.Partition{
				"d7/0": {Cluster: ptr("d7a")},
				"d7/8": {Cluster: ptr("d7a")},
			}},
		},
	}}
	p, err := plan.Build(root)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var central []plan.Stream
	for _, s := range p.Streams {
		if s.Tier == plan.TierCentral {
			central = append(central, s)
		}
		if s.Name == "VIKASA_EXDOT_CENTRAL" {
			t.Errorf("the un-sharded central stream must no longer exist: %+v", s)
		}
	}
	if len(central) != 2 {
		t.Fatalf("want 2 central shards (one per partition), got %d: %+v", len(central), central)
	}
	byName := map[string]plan.Stream{}
	for _, s := range central {
		byName[s.Name] = s
	}
	sh, ok := byName["VIKASA_EXDOT_CENTRAL_D7_D7_0"]
	if !ok {
		t.Fatalf("missing central shard for d7/0; got %v", byName)
	}
	if sh.Replicas != 3 {
		t.Errorf("central shard replicas: got %d, want 3 (R3 default)", sh.Replicas)
	}
	if sh.Cluster != "core" {
		t.Errorf("central shard cluster: got %q, want core", sh.Cluster)
	}
	if len(sh.Sources) != 1 || sh.Sources[0].Name != "VIKASA_EXDOT_D7_D7_0" || sh.Sources[0].Domain != "d7a" {
		t.Errorf("central shard d7/0 must source the regional partition from d7a: %+v", sh.Sources)
	}
	if sh.MaxAge == "" || sh.MaxBytes <= 0 {
		t.Errorf("central shard must be bounded: %+v", sh)
	}
}
```

- [ ] **Step 2: Run — expect fail** — currently one `VIKASA_EXDOT_CENTRAL` stream exists; the test wants two shards and none named `VIKASA_EXDOT_CENTRAL`.

- [ ] **Step 3: Replace `buildCentral` with `buildCentralShards`**

In `internal/plan/plan.go`, replace the whole `buildCentral` function with:

```go
// buildCentralShards constructs one central aggregation shard per partition, each
// sourcing exactly that partition's regional stream (finding C1: central is
// sharded so no single leader holds the whole DOT). parts and centralSources are
// in the same sorted order (see buildRegional), so shard[i] sources centralSources[i].
// Returns the shards, the resolved central cluster (reused by buildDMZ), and a
// districtID -> sorted shard names map for DMZ share fan-out.
func buildCentralShards(dot string, central *topology.Central, parts []partEntry, centralSources []Source, getCluster func(string) (*topology.Cluster, error)) ([]Stream, *topology.Cluster, map[string][]string, error) {
	centralClusterID := *central.Cluster
	centralCluster, err := getCluster(centralClusterID)
	if err != nil {
		return nil, nil, nil, err
	}
	centralReplicas := 3
	if central.Replicas != nil {
		centralReplicas = int(*central.Replicas)
	}
	centralJSDomain := *centralCluster.JsDomain

	shards := make([]Stream, 0, len(parts))
	byDistrict := map[string][]string{}
	for i, part := range parts {
		name := CentralShardStreamName(dot, part.districtID, part.partitionID)
		shards = append(shards, Stream{
			Name:     name,
			Cluster:  centralClusterID,
			JSDomain: centralJSDomain,
			Replicas: centralReplicas,
			MaxAge:   centralMaxAge,
			MaxBytes: centralMaxBytes,
			Tier:     "central",
			Sources:  []Source{centralSources[i]},
		})
		byDistrict[part.districtID] = append(byDistrict[part.districtID], name)
	}
	// byDistrict lists are already in sorted parts order (names are monotonic
	// within a district because parts is sorted by stream name); no re-sort needed.
	return shards, centralCluster, byDistrict, nil
}
```

Note: `CentralShardStreamName` needs importing via the existing `naming` usage — the plan package already calls `PartitionStreamName`/`CentralStreamName` through package-local wrappers or direct import. Confirm how `CentralStreamName` is currently referenced in `plan.go` (it is called bare as `CentralStreamName(dot)`, so there is a package-local alias or dot-import of `naming`); add a matching `CentralShardStreamName` reference the same way. If `plan.go` re-exports naming helpers (e.g. `var CentralStreamName = naming.CentralStreamName`), add `var CentralShardStreamName = naming.CentralShardStreamName` beside it.

- [ ] **Step 4: Update the `Build` wiring**

In `Build`, replace the central + DMZ assembly (~171-188) with:

```go
	// --- Central shards (one per partition) ---

	centralShards, centralCluster, centralByDistrict, err := buildCentralShards(dot, t.Central, parts, centralSources, getCluster)
	if err != nil {
		return nil, err
	}

	// --- DMZ egress stream (Wave 2) ---

	allStreams := append(centralShards, regionalStreams...)

	dmzStream, hasDMZ, err := buildDMZ(dot, t.DMZ, centralCluster, centralByDistrict, getCluster)
	if err != nil {
		return nil, err
	}
	if hasDMZ {
		allStreams = append(allStreams, dmzStream)
	}
```

(Task 3 changes `buildDMZ`'s signature to take `centralByDistrict`; until then this will not compile — implement Task 3 in the same working session before running the suite.)

- [ ] **Step 5: Run the new test — expect pass** (after Task 3 lands, since `buildDMZ` signature changes). Run: `go test ./internal/plan/ -run TestBuild_CentralIsShardedPerPartition -v`.

- [ ] **Step 6: Commit (with Task 3)** — central + DMZ compile together; commit after Task 3.

---

### Task 3: Fan each DMZ share across its district's central shards

**Files:**
- Modify: `internal/plan/plan.go` — `buildDMZ` (~289-328).
- Test: `internal/plan/plan_test.go` (update `TestBuildEmitsDMZStream`; add a fan test).

**Interfaces:**
- Consumes: `centralByDistrict map[string][]string` (from Task 2), and per-share `from`/`as`.
- Produces: `buildDMZ(dot string, dmz *topology.DMZ, centralCluster *topology.Cluster, centralByDistrict map[string][]string, getCluster func(string) (*topology.Cluster, error)) (stream Stream, ok bool, err error)`. Each share emits one `Source` **per central shard of the share's district**, `Name` = shard name, `Domain` = central js-domain, with the share's `TransformSource`/`TransformDest`.

- [ ] **Step 1: Write the failing test**

Replace the source assertions in `TestBuildEmitsDMZStream` (the single-source expectation) and add a fan test. New test — a district with 2 partitions and one share must yield 2 DMZ sources (one per shard), both carrying the same transform:

```go
func TestBuildDMZ_FansShareAcrossCentralShards(t *testing.T) {
	root := &topology.Root{Topology: &topology.Topology{
		Dot:     ptr("exdot"),
		Central: &topology.Central{Cluster: ptr("core")},
		Cluster: map[string]*topology.Cluster{
			"d7a":  {JsDomain: ptr("d7a"), LeafEndpoint: ptr("leaf-d7a:7422")},
			"core": {JsDomain: ptr("core"), LeafEndpoint: ptr("leaf-core:7422")},
			"dmz":  {JsDomain: ptr("dmz"), LeafEndpoint: ptr("leaf-dmz:7422")},
		},
		District: map[string]*topology.District{
			"d7": {Partition: map[string]*topology.Partition{
				"d7/0": {Cluster: ptr("d7a")},
				"d7/8": {Cluster: ptr("d7a")},
			}},
		},
		DMZ: &topology.DMZ{Cluster: ptr("dmz"), Shares: []*topology.Share{
			{Consumer: ptr("r"), From: ptr("vikasa.exdot.d7.>"), As: ptr("vikasa.exdot.share.r.>")},
		}},
	}}
	p, err := plan.Build(root)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var dmz *plan.Stream
	for i := range p.Streams {
		if p.Streams[i].Tier == plan.TierDMZ {
			dmz = &p.Streams[i]
		}
	}
	if dmz == nil {
		t.Fatal("no dmz stream")
	}
	if len(dmz.Sources) != 2 {
		t.Fatalf("share must fan across 2 central shards, got %d sources: %+v", len(dmz.Sources), dmz.Sources)
	}
	wantNames := map[string]bool{"VIKASA_EXDOT_CENTRAL_D7_D7_0": false, "VIKASA_EXDOT_CENTRAL_D7_D7_8": false}
	for _, src := range dmz.Sources {
		if _, ok := wantNames[src.Name]; !ok {
			t.Errorf("unexpected DMZ source name %q", src.Name)
		}
		wantNames[src.Name] = true
		if src.Domain != "core" {
			t.Errorf("DMZ source %s domain: got %q, want core", src.Name, src.Domain)
		}
		if src.TransformSource != "vikasa.exdot.d7.>" || src.TransformDest != "vikasa.exdot.share.r.>" {
			t.Errorf("DMZ source %s transform: got %q->%q", src.Name, src.TransformSource, src.TransformDest)
		}
	}
	for n, seen := range wantNames {
		if !seen {
			t.Errorf("DMZ missing source for shard %s", n)
		}
	}
}
```

Also update `TestBuildEmitsDMZStream` (single-partition d7/0): its share now fans across exactly one shard, so change the expected source `Name` from `plan.CentralStreamName("exdot")` to `"VIKASA_EXDOT_CENTRAL_D7_D7_0"` and keep `len(dmz.Sources) == 1`.

- [ ] **Step 2: Run — expect fail** (compile: `buildDMZ` signature mismatch, then assertion failures).

- [ ] **Step 3: Rewrite `buildDMZ`**

Replace `buildDMZ` in `internal/plan/plan.go` with:

```go
// buildDMZ constructs the DMZ egress stream. Each share remaps its declared
// district subject space (from) onto a public share space (as) via a per-source
// subject transform. Because central is sharded per partition (finding C1), a
// share fans across every central shard of the share's district: one Source per
// shard, each carrying the same transform. ok is false when there is no DMZ block.
func buildDMZ(dot string, dmz *topology.DMZ, centralCluster *topology.Cluster, centralByDistrict map[string][]string, getCluster func(string) (*topology.Cluster, error)) (stream Stream, ok bool, err error) {
	if dmz == nil || dmz.Cluster == nil {
		return Stream{}, false, nil
	}
	dmzCluster, err := getCluster(*dmz.Cluster)
	if err != nil {
		return Stream{}, false, err
	}
	dmzReplicas := 3
	if dmz.Replicas != nil {
		dmzReplicas = int(*dmz.Replicas)
	}
	centralJSDomain := *centralCluster.JsDomain

	// Resolve the district a share draws from by matching its `from` subject
	// against each district's subject space, so we fan across that district's shards.
	districtOf := func(from string) (string, bool) {
		for distID := range centralByDistrict {
			if naming.UnderPrefix(from, naming.DefaultSubjectPrefix(dot, distID)) {
				return distID, true
			}
		}
		return "", false
	}

	var dmzSources []Source
	for _, s := range dmz.Shares {
		if s.From == nil || s.As == nil {
			continue
		}
		distID, matched := districtOf(*s.From)
		if !matched {
			return Stream{}, false, fmt.Errorf("plan.Build: DMZ share from %q is not under any district subject space", *s.From)
		}
		for _, shardName := range centralByDistrict[distID] {
			dmzSources = append(dmzSources, Source{
				Name:            shardName,
				Domain:          centralJSDomain,
				TransformSource: *s.From,
				TransformDest:   *s.As,
			})
		}
	}
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
}
```

Note: this uses `naming.UnderPrefix` / `naming.DefaultSubjectPrefix`. If the DMZ share `from` may use a *declared* (custom) district prefix, resolve the space via the district's declared prefix instead of `DefaultSubjectPrefix` — check whether `topology.District` carries a prefix field and, if so, thread `SubjectSpace(dot, distID, declared)` into `districtOf`. The example specs all use default prefixes, so `DefaultSubjectPrefix` matches the goldens; confirm against `internal/topology` before finalizing.

- [ ] **Step 4: Run both DMZ tests + the Task 2 central test — expect pass.**

Run: `go test ./internal/plan/ -run 'TestBuild_CentralIsShardedPerPartition|TestBuildDMZ_FansShareAcrossCentralShards|TestBuildEmitsDMZStream' -v`

- [ ] **Step 5: Commit**

```bash
git add internal/plan/plan.go internal/plan/plan_test.go internal/naming/naming.go internal/naming/naming_test.go
git commit -m "feat(plan): shard central per partition (R3) and fan DMZ shares across shards"
```

---

### Task 4: Remove now-dead `CentralStreamName` usage / keep the helper

**Files:**
- Modify: `internal/plan/plan.go` (any residual `CentralStreamName` reference), `internal/naming/naming.go` (keep or drop).
- Test: existing suite.

**Interfaces:** none new.

- [ ] **Step 1:** Grep for remaining references:

Run: `grep -rn "CentralStreamName" internal/ cmd/`
Expected: after Task 3, the only references should be the naming definition + its test. If `plan.go` still calls it, remove the dead call.

- [ ] **Step 2:** If `CentralStreamName` is now unused outside its own test, leave the function (it is a stable naming helper and harmless) but ensure `staticcheck`/`go vet` are clean:

Run: `make lint && make staticcheck`
Expected: PASS (no "unused" complaints — exported funcs are not flagged).

- [ ] **Step 3: Commit if anything changed** — `git commit -m "refactor(plan): drop dead single-central reference"` (skip if no change).

---

### Task 5: Regenerate goldens and review the (large) diff

**Files:**
- Modify (generated): all `cmd/gen/testdata/golden*/**` and `internal/render/testdata/golden/**` central + DMZ artifacts, via `make golden`.

**Interfaces:** consumes Tasks 1–3.

- [ ] **Step 1: Confirm goldens are stale** — `make test` → FAIL in `cmd/gen` (central stream renamed/split; DMZ sources changed).

- [ ] **Step 2: Regenerate** — `make golden`.

- [ ] **Step 3: Review the diff — the review artifact**

Run: `git diff cmd/gen/testdata/golden internal/render/testdata/golden` and `git diff cmd/gen/testdata/golden-dmz`
Verify:
- `clusters/core/streams.yaml`: the single `VIKASA_EXDOT_CENTRAL` is replaced by N `VIKASA_EXDOT_CENTRAL_<D>_<P>` streams, each with `replicas: 3`, `maxAge: 15m`, `maxBytes:`, and exactly one source (its partition, `externalApiPrefix: $JS.<partition-domain>.API`).
- `golden-dmz*/clusters/dmz/streams.yaml`: DMZ `sources` now name the central **shards** (not `VIKASA_EXDOT_CENTRAL`), each with the same `subjectTransforms` as before; the overlapping-corridor share still fans correctly.
- `TOPOLOGY.md` / `DEPLOYMENT-GUIDE.md`: reflect the shard set; **no regional stream, subject, or transform *value* changed**.
- `dmz-catalog.md`/`.json`: the public subject ↔ consumer mapping is **unchanged** (sharding is internal; the catalog is the external contract).

- [ ] **Step 4: Full suite green** — `make test && make lint && make integration`. The **DMZ integration test is the key gate** — it re-derives the config against embedded servers and proves delivery + isolation still hold with sharded central.

- [ ] **Step 5: Commit** — `git commit -m "test(golden): regenerate for per-partition central sharding + DMZ fan"`

---

## Self-Review

- **Spec coverage:** C1 (central sharding) — Tasks 1,2,3,5. E5 (R5→R3) — Task 2 (`centralReplicas := 3`). DMZ correctness under sharding — Task 3 + the integration gate in Task 5. **Not** in this plan: node-level `max_file_store` / account limits (Plan 4), RePublish + accounts service-imports (Plan 3), fan-in guardrail + alerts (Plan 4) — all recorded as deferred.
- **Placeholders:** two flagged confirmations (how `plan.go` references naming helpers in Task 2 Step 3; whether districts carry a declared prefix in Task 3 Step 3) — both with exact fallbacks. No other gaps.
- **Type consistency:** `buildCentralShards` returns `(…, map[string][]string, error)` and `buildDMZ` consumes that same `centralByDistrict map[string][]string`; `Source` fields (`Name`, `Domain`, `TransformSource`, `TransformDest`) match Plan 1 usage; central shard `Stream` carries the Plan-1 `MaxBytes`/`Duplicates` fields (Duplicates empty for central).
- **Risk:** this reshapes the validated DMZ egress. The `dmz-catalog` (external contract) must be byte-unchanged and the DMZ integration test must stay green — both are explicit gates in Task 5.
