# DMZ RePublish — Implementation Plan (3a of 4)

**Goal:** Give the DMZ egress stream a `RePublish` directive so committed share/peer messages are echoed onto the core-NATS bus — enabling the **core-NATS fan-out** tier for third-party consumers (Decision D3), the scalable default alongside durable JetStream consumers for named peers.

**Architecture:** RePublish is a stream config that re-emits each committed message onto a core subject (verified in nats-server `server/stream.go:7054-7072`: after store, `outq.send(newJSPubMsg(tsubj,…))`). The DMZ stream is source-only (no subject listeners), so an identity republish `vikasa.> → vikasa.>` is loop-safe — the echo cannot be re-ingested. External DMZ users already hold `subscribe` on their share subject, so the same ACL now covers both a durable JS consumer and a live core subscription. This adds two IR fields threaded through both renderers, exactly like Plan 1's bounds. Splits from Plan 3b (account collapse + `$JS.API` service imports), which is a separate cycle.

**Tech Stack:** Go, `text/template` (k8s NACK CR: `republish: { source, destination }`), `encoding/json` (bare-metal: `"republish": { "src", "dest" }`), embedded-NATS integration test.

## Global Constraints

- **NACK v1beta2 field:** `spec.republish.source` / `spec.republish.destination`. Bare-metal (`nats stream add --config`) uses `republish.src` / `republish.dest` (the server's `RePublish` json tags).
- **Loop-safety invariant:** RePublish is only ever set on a **source-only** stream (the DMZ). Never set it on a stream that also declares `subjects` — the echo would feed back. This plan sets it solely in `buildDMZ`.
- **Determinism / golden protocol:** `make golden` then review — only DMZ `streams.yaml` / stream JSON gain a `republish` block; nothing else changes. `dmz-catalog` (external contract) stays byte-identical.
- TDD.

---

### Task 1: Add RePublish to the IR and set it on the DMZ stream

**Files:** Modify `internal/plan/plan.go` (Stream struct; `buildDMZ`); Test `internal/plan/plan_test.go`.

**Interfaces:** Produces `plan.Stream.RePublishSource string` / `plan.Stream.RePublishDest string` (empty = no republish). Set on the DMZ stream to `"vikasa.>"` / `"vikasa.>"`.

- [ ] **Step 1: Failing test** — add to `plan_test.go`:

```go
func TestBuildDMZ_HasRePublish(t *testing.T) {
	root := &topology.Root{Topology: &topology.Topology{
		Dot:     ptr("exdot"),
		Central: &topology.Central{Cluster: ptr("core")},
		Cluster: map[string]*topology.Cluster{
			"core": {JsDomain: ptr("core"), LeafEndpoint: ptr("leaf-core:7422")},
			"dmz":  {JsDomain: ptr("dmz"), LeafEndpoint: ptr("leaf-dmz:7422")},
		},
		District: map[string]*topology.District{
			"d7": {Partition: map[string]*topology.Partition{"d7/0": {Cluster: ptr("core")}}},
		},
		DMZ: &topology.DMZ{Cluster: ptr("dmz"), Shares: []*topology.Share{
			{Consumer: ptr("r"), From: ptr("vikasa.exdot.d7.>"), As: ptr("vikasa.exdot.share.r.>")},
		}},
	}}
	p, err := plan.Build(root)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, s := range p.Streams {
		if s.Tier == plan.TierDMZ {
			if s.RePublishSource != "vikasa.>" || s.RePublishDest != "vikasa.>" {
				t.Errorf("dmz republish: got %q->%q, want vikasa.>->vikasa.>", s.RePublishSource, s.RePublishDest)
			}
		} else if s.RePublishSource != "" {
			t.Errorf("%s (tier %s) must NOT republish (loop-safety): %q", s.Name, s.Tier, s.RePublishSource)
		}
	}
}
```

- [ ] **Step 2: Run — expect fail** (`s.RePublishSource` undefined).

- [ ] **Step 3: Add struct fields** — in `Stream` (after `Duplicates`):

```go
	RePublishSource string // core-NATS fan-out echo (DMZ only, loop-safe); "" = off (Decision D3)
	RePublishDest   string
```

- [ ] **Step 4: Set in `buildDMZ`** — add to the returned DMZ `Stream{…}` literal:

```go
		RePublishSource: "vikasa.>",
		RePublishDest:   "vikasa.>",
```

- [ ] **Step 5: Run — expect pass.**

- [ ] **Step 6: Commit** — `feat(plan): DMZ RePublish for core-NATS fan-out (D3)`.

---

### Task 2: Render `republish` in the k8s (NACK) renderer

**Files:** Modify `internal/render/k8s.go` (`streamCRTmpl`, `streamCRData`, `renderStreamCR`); Test `internal/render/k8s_test.go`.

- [ ] **Step 1: Failing test** — append to `k8s_test.go`:

```go
func TestK8sRenderer_RePublish(t *testing.T) {
	dmz := plan.Stream{
		Name: "VIKASA_EXDOT_DMZ", Cluster: "dmz", JSDomain: "dmz", Replicas: 3, Tier: "dmz",
		MaxAge: "1h", MaxBytes: 10 << 30, Duplicates: "5m",
		RePublishSource: "vikasa.>", RePublishDest: "vikasa.>",
	}
	files, err := render.K8sRenderer{}.RenderCluster(render.ClusterSlice{
		ID: "dmz", SubstrateType: "kubernetes", DOT: "exdot", JSDomain: "dmz",
		LeafEndpoint: "leaf-dmz.nats.vikasa.exdot:7422", IssuerName: "vikasa-ca", SecretStore: "vikasa-secrets",
		Streams: []plan.Stream{dmz},
	})
	if err != nil {
		t.Fatalf("RenderCluster: %v", err)
	}
	s := string(files["streams.yaml"])
	for _, want := range []string{"republish:", "source: vikasa.>", "destination: vikasa.>"} {
		if !strings.Contains(s, want) {
			t.Errorf("CR missing %q:\n%s", want, s)
		}
	}
}
```

- [ ] **Step 2: Run — expect fail.**

- [ ] **Step 3: Template data** — add to `streamCRData`: `RePublishSource string` and `RePublishDest string` (after `Duplicates`).

- [ ] **Step 4: Template** — in `streamCRTmpl`, after the `duplicateWindow` block and before `{{- if .Sources }}`:

```
{{- if .RePublishSource }}
  republish:
    source: {{ .RePublishSource }}
    destination: {{ .RePublishDest }}
{{- end }}
```

- [ ] **Step 5: Populate** — in `renderStreamCR`'s `data := streamCRData{…}`: `RePublishSource: s.RePublishSource, RePublishDest: s.RePublishDest,`.

- [ ] **Step 6: Run — expect pass. Commit** — `feat(render/k8s): emit republish on the DMZ Stream CR`.

---

### Task 3: Render `republish` in the bare-metal renderer

**Files:** Modify `internal/render/baremetal.go` (`bareStreamConfig`, `renderBareStreamConfig`); Test `internal/render/baremetal_test.go`.

- [ ] **Step 1: Failing test** — append to `baremetal_test.go`:

```go
func TestBareMetalRenderer_RePublish(t *testing.T) {
	dmz := plan.Stream{
		Name: "VIKASA_EXDOT_DMZ", Replicas: 3, MaxAge: "1h", MaxBytes: 10 << 30, Duplicates: "5m",
		RePublishSource: "vikasa.>", RePublishDest: "vikasa.>",
	}
	files, err := render.BareMetalRenderer{}.RenderCluster(render.ClusterSlice{
		ID: "dmz", SubstrateType: "bare-metal", Hosts: []string{"exdot-dmz-1"},
		DOT: "exdot", JSDomain: "dmz",
		LeafEndpoint: "leaf-dmz.nats.vikasa.exdot:7422", CentralLeafEndpoint: "leaf-core.nats.vikasa.exdot:7422",
		Streams: []plan.Stream{dmz},
	})
	if err != nil {
		t.Fatalf("RenderCluster: %v", err)
	}
	var cfg struct {
		RePublish *struct {
			Src  string `json:"src"`
			Dest string `json:"dest"`
		} `json:"republish"`
	}
	if err := json.Unmarshal(files["streams/VIKASA_EXDOT_DMZ.json"], &cfg); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.RePublish == nil || cfg.RePublish.Src != "vikasa.>" || cfg.RePublish.Dest != "vikasa.>" {
		t.Errorf("republish = %+v, want src/dest vikasa.>", cfg.RePublish)
	}
}
```

- [ ] **Step 2: Run — expect fail.**

- [ ] **Step 3: Add JSON type + field** — in `internal/render/baremetal.go`, add a type and a field on `bareStreamConfig` (after `Duplicates`, before `Sources`):

```go
type bareRePublish struct {
	Src  string `json:"src"`
	Dest string `json:"dest"`
}
```

and in `bareStreamConfig`: `RePublish *bareRePublish `json:"republish,omitempty"``.

- [ ] **Step 4: Populate** — in `renderBareStreamConfig`, after the `Duplicates` block:

```go
	if s.RePublishSource != "" {
		cfg.RePublish = &bareRePublish{Src: s.RePublishSource, Dest: s.RePublishDest}
	}
```

- [ ] **Step 5: Run — expect pass. Commit** — `feat(render/baremetal): emit republish in DMZ stream JSON`.

---

### Task 4: Diff-track RePublish

**Files:** Modify `internal/plan/diff.go` (`streamConfigChanged`); Test `internal/plan/diff_test.go`.

- [ ] **Step 1: Failing test** — append to `diff_test.go`:

```go
func TestDiff_DetectsRePublishChange(t *testing.T) {
	base := plan.Stream{Name: "VIKASA_EXDOT_DMZ", Cluster: "dmz", JSDomain: "dmz", Replicas: 3, Tier: plan.TierDMZ}
	old := &plan.Plan{DOT: "exdot", Streams: []plan.Stream{base}}
	n := base
	n.RePublishSource, n.RePublishDest = "vikasa.>", "vikasa.>"
	newer := &plan.Plan{DOT: "exdot", Streams: []plan.Stream{n}}
	if d := plan.Diff(old, newer); len(d.Modified) != 1 {
		t.Fatalf("republish change: want 1 Modified, got %d", len(d.Modified))
	}
}
```

- [ ] **Step 2: Run — expect fail.**

- [ ] **Step 3: Extend `streamConfigChanged`** — add to the first `if`:

```go
		a.RePublishSource != b.RePublishSource || a.RePublishDest != b.RePublishDest ||
```

- [ ] **Step 4: Run — expect pass. Commit** — `feat(plan): diff tracks republish changes`.

---

### Task 5: Prove D3 — core-NATS subscriber receives the republished feed

**Files:** Modify `test/integration/dmz_flow_test.go` (add one test using the existing embedded-server helpers).

**Interfaces:** Consumes the existing `startServer`/`connectJS`/`hubConf`/`leafConf` helpers.

- [ ] **Step 1: Write the test** — mirror the existing DMZ delivery test, but instead of a JS pull consumer, set `RePublish` on the DMZ stream and assert a **core** subscription receives the message. Sketch (adapt to the file's exact helpers):

```go
func TestDMZ_RePublishToCoreSubscriber(t *testing.T) {
	core := startServer(t, "core", hubConf(t, "core", "core", -1))
	dmz := startServer(t, "dmz", leafConf(t, "dmz", "dmz", core))
	checkLeafConnected(t, core, dmz)

	ncCore, coreJS := connectJS(t, core, "core")
	if _, err := coreJS.AddStream(&nats.StreamConfig{
		Name: "VIKASA_EXDOT_CENTRAL_D1_D1_0", Storage: nats.FileStorage,
		Subjects: []string{"vikasa.exdot.d1.>"},
	}); err != nil {
		t.Fatalf("central: %v", err)
	}
	ncDMZ, dmzJS := connectJS(t, dmz, "dmz")
	if _, err := dmzJS.AddStream(&nats.StreamConfig{
		Name: "VIKASA_EXDOT_DMZ", Storage: nats.FileStorage,
		Sources: []*nats.StreamSource{{
			Name: "VIKASA_EXDOT_CENTRAL_D1_D1_0",
			External: &nats.ExternalStream{APIPrefix: "$JS.core.API"},
			SubjectTransforms: []nats.SubjectTransformConfig{{Source: "vikasa.exdot.d1.>", Destination: "vikasa.exdot.share.r.>"}},
		}},
		RePublish: &nats.RePublish{Source: "vikasa.>", Destination: "vikasa.>"},
	}); err != nil {
		t.Fatalf("dmz: %v", err)
	}
	// Core subscription on the DMZ server (no JetStream consumer).
	sub, err := ncDMZ.SubscribeSync("vikasa.exdot.share.r.>")
	if err != nil {
		t.Fatalf("core sub: %v", err)
	}
	defer sub.Unsubscribe()
	if _, err := coreJS.Publish("vikasa.exdot.d1.001.signals", []byte("sig")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	msg, err := sub.NextMsg(5 * time.Second)
	if err != nil {
		t.Fatalf("core subscriber did not receive republished message: %v", err)
	}
	if msg.Subject != "vikasa.exdot.share.r.signals" && !strings.HasPrefix(msg.Subject, "vikasa.exdot.share.r.") {
		t.Errorf("republished subject: got %q", msg.Subject)
	}
	_ = ncCore
}
```

Note: confirm the file's exact helper signatures (`connectJS` return arity, `checkLeafConnected`, whether `nats.RePublish` is the vendored client's type) before finalizing; the assertion is the load-bearing part — a core subscriber must receive the message with no JS consumer.

- [ ] **Step 2: Run** — `go test -tags integration ./test/integration/ -run TestDMZ_RePublishToCoreSubscriber -v` → PASS. If the transform yields a different republished subject shape, adjust the prefix assertion to match observed output (do not weaken to "any subject").

- [ ] **Step 3: Commit** — `test(integration): prove DMZ RePublish reaches core-NATS subscribers (D3)`.

---

### Task 6: Regenerate goldens and review

- [ ] **Step 1:** `make test` → FAIL (DMZ streams gain `republish`).
- [ ] **Step 2:** `make golden`.
- [ ] **Step 3: Review** — `git diff cmd/gen/testdata/golden-dmz`: only the DMZ `streams.yaml` gains a `republish:` block (`source: vikasa.>`, `destination: vikasa.>`); bare-metal DMZ JSON gains `"republish": {"src":"vikasa.>","dest":"vikasa.>"}`. **`dmz-catalog` unchanged**; no non-DMZ churn.
- [ ] **Step 4:** `make test && make lint && make integration` → all green.
- [ ] **Step 5: Commit** — `test(golden): regenerate for DMZ republish`.

---

## Self-Review

- **Spec coverage:** D3 (core-NATS fan-out) — Tasks 1-6; the delivery proof is Task 5. **Not** here: durable-consumer tier for peers (already the existing model), account collapse + service imports (Plan 3b).
- **Placeholders:** Task 5 flags the integration-helper signatures to confirm.
- **Loop-safety:** RePublish is set only in `buildDMZ` (a source-only stream) — asserted negatively in Task 1 (no non-DMZ stream republishes).
- **Type consistency:** `RePublishSource`/`RePublishDest` used identically across plan, both renderers, and diff; NACK `source`/`destination` vs bare-metal `src`/`dest` matches each schema.
