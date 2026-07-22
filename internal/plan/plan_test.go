package plan_test

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Vikasa2M/vikasa-infra/internal/plan"
	"github.com/Vikasa2M/vikasa-infra/internal/topology"
)

// loadPlan is a test helper that loads a topology file and builds a Plan.
func loadPlan(t *testing.T, path string) *plan.Plan {
	t.Helper()
	root, err := topology.Load(path)
	if err != nil {
		t.Fatalf("topology.Load(%q): %v", path, err)
	}
	p, err := plan.Build(root)
	if err != nil {
		t.Fatalf("plan.Build: %v", err)
	}
	return p
}

func TestSubjectNames(t *testing.T) {
	tests := []struct {
		name string
		fn   func() string
		want string
	}{
		{
			name: "PartitionStreamName basic",
			fn:   func() string { return plan.PartitionStreamName("exdot", "d7", "d7/0") },
			want: "VIKASA_EXDOT_D7_D7_0",
		},
		{
			name: "PartitionStreamName d7/8",
			fn:   func() string { return plan.PartitionStreamName("exdot", "d7", "d7/8") },
			want: "VIKASA_EXDOT_D7_D7_8",
		},
		{
			name: "CentralStreamName",
			fn:   func() string { return plan.CentralStreamName("exdot") },
			want: "VIKASA_EXDOT_CENTRAL",
		},
		{
			name: "PartitionStreamName with dashes",
			fn:   func() string { return plan.PartitionStreamName("my-dot", "my-district", "part-1/0") },
			want: "VIKASA_MY_DOT_MY_DISTRICT_PART_1_0",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.fn()
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuild_ExdotShared(t *testing.T) {
	p := loadPlan(t, "../../examples/exdot-shared.json")

	// --- DOT ---
	if p.DOT != "exdot" {
		t.Errorf("DOT: got %q, want %q", p.DOT, "exdot")
	}

	// --- Streams: 4 total (2 central shards + 2 regional) ---
	if len(p.Streams) != 4 {
		t.Fatalf("len(Streams): got %d, want 4", len(p.Streams))
	}

	// Streams[0..1] are the per-partition central shards (Wave 0), sorted by name.
	// Each sources exactly its own partition; replicas honor the spec override (5).
	wantShards := map[string]struct{ srcName, srcDomain string }{
		plan.CentralShardStreamName("exdot", "d7", "d7/0"): {plan.PartitionStreamName("exdot", "d7", "d7/0"), "d7a"},
		plan.CentralShardStreamName("exdot", "d7", "d7/8"): {plan.PartitionStreamName("exdot", "d7", "d7/8"), "d7b"},
	}
	for i := 0; i <= 1; i++ {
		s := p.Streams[i]
		if s.Tier != "central" || s.Tier.Wave() != 0 {
			t.Errorf("Streams[%d]: got tier %q wave %d, want central/0", i, s.Tier, s.Tier.Wave())
		}
		if s.Cluster != "core" {
			t.Errorf("Streams[%d].Cluster: got %q, want core", i, s.Cluster)
		}
		if s.Replicas != 5 {
			t.Errorf("Streams[%d].Replicas: got %d, want 5 (spec override)", i, s.Replicas)
		}
		want, ok := wantShards[s.Name]
		if !ok {
			t.Errorf("Streams[%d]: unexpected central shard name %q", i, s.Name)
			continue
		}
		if len(s.Sources) != 1 || s.Sources[0].Name != want.srcName || s.Sources[0].Domain != want.srcDomain {
			t.Errorf("Streams[%d] %q: sources got %+v, want one source %q@%q", i, s.Name, s.Sources, want.srcName, want.srcDomain)
		}
	}

	// Streams[2..3] are the regional partition streams (Wave 1, no sources).
	wantRegional := map[string]string{
		plan.PartitionStreamName("exdot", "d7", "d7/0"): "d7a",
		plan.PartitionStreamName("exdot", "d7", "d7/8"): "d7b",
	}
	for i := 2; i <= 3; i++ {
		s := p.Streams[i]
		if s.Tier != "regional" || s.Tier.Wave() != 1 {
			t.Errorf("Streams[%d]: got tier %q wave %d, want regional/1", i, s.Tier, s.Tier.Wave())
		}
		if s.Replicas != 3 {
			t.Errorf("Streams[%d].Replicas: got %d, want 3", i, s.Replicas)
		}
		if s.MaxAge != "6h" {
			t.Errorf("Streams[%d].MaxAge: got %q, want %q", i, s.MaxAge, "6h")
		}
		if len(s.Sources) != 0 {
			t.Errorf("Streams[%d].Sources: got %d, want 0 (regional shell has no sources)", i, len(s.Sources))
		}
		wantCluster, ok := wantRegional[s.Name]
		if !ok {
			t.Errorf("Streams[%d]: unexpected stream name %q", i, s.Name)
			continue
		}
		if s.Cluster != wantCluster {
			t.Errorf("Streams[%d] %q: Cluster got %q, want %q", i, s.Name, s.Cluster, wantCluster)
		}
	}

	// --- DNS: 2 records ---
	if len(p.DNS) != 2 {
		t.Fatalf("len(DNS): got %d, want 2", len(p.DNS))
	}

	wantDNS := map[string]string{
		"leaf-exdot-d7-0.nats.vikasa.exdot": "leaf-d7a.nats.vikasa.exdot:7422",
		"leaf-exdot-d7-8.nats.vikasa.exdot": "leaf-d7b.nats.vikasa.exdot:7422",
	}
	for _, rec := range p.DNS {
		wantTarget, ok := wantDNS[rec.Name]
		if !ok {
			t.Errorf("unexpected DNS record name %q", rec.Name)
			continue
		}
		if rec.Target != wantTarget {
			t.Errorf("DNS %q: Target got %q, want %q", rec.Name, rec.Target, wantTarget)
		}
	}
}

// TestBuild_SubstrateIndependence asserts that exdot-shared.json and
// exdot-multicluster.json produce DeepEqual Plans. The two specs differ only in
// their substrate context fields, which must not influence the Plan.
func TestBuild_SubstrateIndependence(t *testing.T) {
	shared := loadPlan(t, "../../examples/exdot-shared.json")
	multicluster := loadPlan(t, "../../examples/exdot-multicluster.json")

	if !reflect.DeepEqual(shared, multicluster) {
		t.Errorf("plans differ between exdot-shared and exdot-multicluster:\nshared=%+v\nmulticluster=%+v",
			shared, multicluster)
	}
}

// ptr returns a pointer to a string literal, for use in struct literals.
func ptr(s string) *string { return &s }

func TestBuildEmitsDMZStream(t *testing.T) {
	root := &topology.Root{
		Topology: &topology.Topology{
			Dot: ptr("exdot"),
			Central: &topology.Central{
				Cluster: ptr("core"),
			},
			Cluster: map[string]*topology.Cluster{
				"core": {JsDomain: ptr("core"), LeafEndpoint: ptr("leaf-core:7422")},
				"dmz":  {JsDomain: ptr("dmz"), LeafEndpoint: ptr("leaf-dmz:7422")},
			},
			District: map[string]*topology.District{
				"d7": {
					Partition: map[string]*topology.Partition{
						"d7/0": {Cluster: ptr("core")},
					},
				},
			},
			DMZ: &topology.DMZ{
				Cluster: ptr("dmz"),
				Shares: []*topology.Share{
					{
						Consumer: ptr("r"),
						From:     ptr("vikasa.exdot.d7.>"),
						As:       ptr("vikasa.exdot.share.r.>"),
					},
				},
			},
		},
	}
	p, err := plan.Build(root)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var dmz *plan.Stream
	for i := range p.Streams {
		if p.Streams[i].Tier == "dmz" {
			dmz = &p.Streams[i]
		}
	}
	if dmz == nil {
		t.Fatalf("no dmz stream; streams=%+v", p.Streams)
	}
	if dmz.Tier.Wave() != 2 || dmz.Cluster != "dmz" || dmz.Name != "VIKASA_EXDOT_DMZ" {
		t.Fatalf("dmz stream fields: %+v", dmz)
	}
	if len(dmz.Sources) != 1 {
		t.Fatalf("dmz sources len: got %d, want 1; sources=%+v", len(dmz.Sources), dmz.Sources)
	}
	src := dmz.Sources[0]
	// Central is sharded per partition; the single-partition d7 share fans across
	// exactly one central shard.
	if src.Name != "VIKASA_EXDOT_CENTRAL_D7_D7_0" {
		t.Errorf("dmz source name: got %q, want %q", src.Name, "VIKASA_EXDOT_CENTRAL_D7_D7_0")
	}
	if src.Domain != "core" {
		t.Errorf("dmz source domain: got %q, want %q", src.Domain, "core")
	}
	if src.FilterSubject != "" {
		t.Errorf("dmz source: FilterSubject must be empty (transforms used instead), got %q", src.FilterSubject)
	}
	if src.TransformSource != "vikasa.exdot.d7.>" {
		t.Errorf("dmz source TransformSource: got %q, want %q", src.TransformSource, "vikasa.exdot.d7.>")
	}
	if src.TransformDest != "vikasa.exdot.share.r.>" {
		t.Errorf("dmz source TransformDest: got %q, want %q", src.TransformDest, "vikasa.exdot.share.r.>")
	}
}

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

// TestBuild_NoUnboundedStreams guards finding C2: no checked-in example spec may
// produce a stream without a size bound. This is the generator-level "lint".
func TestBuild_NoUnboundedStreams(t *testing.T) {
	specs, err := filepath.Glob("../../examples/*.json")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(specs) == 0 {
		t.Fatal("no example specs found under ../../examples")
	}
	for _, spec := range specs {
		spec := spec
		// INVALID fixtures are expected to fail topology.Load; *cabinets* files
		// are cabinet-inventory fixtures (consumed by AttachCabinets), not
		// topology specs, so they are out of scope for this stream-bound check.
		if strings.Contains(spec, "INVALID") || strings.Contains(spec, "cabinets") {
			continue
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

// TestBuild_DegenerateTopologies pins Build's behavior on structurally valid
// but empty-ish specs: Build succeeds (no error) rather than rejecting them.
func TestBuild_DegenerateTopologies(t *testing.T) {
	// centralOnly returns a minimal valid topology: one central cluster, no
	// districts, no dmz. Each case mutates a fresh copy.
	centralOnly := func() *topology.Topology {
		return &topology.Topology{
			Dot:     ptr("exdot"),
			Central: &topology.Central{Cluster: ptr("core")},
			Cluster: map[string]*topology.Cluster{
				"core": {JsDomain: ptr("core"), LeafEndpoint: ptr("leaf-core:7422")},
			},
		}
	}

	tests := []struct {
		name      string
		mutate    func(*topology.Topology)
		wantTiers []plan.Tier // expected Streams tiers in (Wave, Name) order
	}{
		{
			// Pinned contract: a district with zero partitions contributes
			// nothing. Central is sharded per partition (C1), so zero partitions
			// means zero central shards — an empty plan, no sources, no DNS.
			name: "district with zero partitions",
			mutate: func(tp *topology.Topology) {
				tp.District = map[string]*topology.District{
					"d7": {Id: ptr("d7"), Partition: map[string]*topology.Partition{}},
				}
			},
			wantTiers: nil,
		},
		{
			// Pinned contract: a central-only spec (no districts, so no
			// partitions) has nothing to aggregate — no central shards, no
			// streams, no DNS.
			name:      "no districts (central-only)",
			mutate:    func(*topology.Topology) {},
			wantTiers: nil,
		},
		{
			// Pinned contract: dmz with an empty shares list still emits the
			// DMZ egress stream (wave 2) — with zero sources, i.e. an inert
			// stream that ingests nothing. With no partitions there are no
			// central shards, so the DMZ stream is the only one.
			name: "dmz with empty shares",
			mutate: func(tp *topology.Topology) {
				tp.Cluster["dmz"] = &topology.Cluster{JsDomain: ptr("dmz"), LeafEndpoint: ptr("leaf-dmz:7422")}
				tp.DMZ = &topology.DMZ{Cluster: ptr("dmz"), Shares: []*topology.Share{}}
			},
			wantTiers: []plan.Tier{plan.TierDMZ},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			topo := centralOnly()
			tc.mutate(topo)
			p, err := plan.Build(&topology.Root{Topology: topo})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if len(p.Streams) != len(tc.wantTiers) {
				t.Fatalf("len(Streams): got %d, want %d; streams=%+v", len(p.Streams), len(tc.wantTiers), p.Streams)
			}
			for i, want := range tc.wantTiers {
				if p.Streams[i].Tier != want {
					t.Errorf("Streams[%d].Tier: got %q, want %q", i, p.Streams[i].Tier, want)
				}
				if len(p.Streams[i].Sources) != 0 {
					t.Errorf("Streams[%d] (%s): got %d sources, want 0", i, p.Streams[i].Name, len(p.Streams[i].Sources))
				}
			}
			if len(p.DNS) != 0 {
				t.Errorf("DNS: got %d records, want 0 (no partitions ⇒ no leaf DNS): %+v", len(p.DNS), p.DNS)
			}
		})
	}
}

// TestBuild_ErrorCases checks that Build returns errors for malformed inputs.
func TestBuild_ErrorCases(t *testing.T) {
	t.Run("nil root", func(t *testing.T) {
		_, err := plan.Build(nil)
		if err == nil {
			t.Error("expected error for nil root, got nil")
		}
	})

	t.Run("nil topology", func(t *testing.T) {
		_, err := plan.Build(&topology.Root{})
		if err == nil {
			t.Error("expected error for nil topology, got nil")
		}
	})

	t.Run("nil dot", func(t *testing.T) {
		root := &topology.Root{
			Topology: &topology.Topology{
				// Dot deliberately left nil
			},
		}
		_, err := plan.Build(root)
		if err == nil {
			t.Error("expected error for nil dot, got nil")
		}
	})

	t.Run("nil central", func(t *testing.T) {
		root := &topology.Root{
			Topology: &topology.Topology{
				Dot: ptr("testdot"),
				// Central deliberately left nil
			},
		}
		_, err := plan.Build(root)
		if err == nil {
			t.Error("expected error for nil central, got nil")
		}
	})

	t.Run("partition references unknown cluster", func(t *testing.T) {
		const missingClusterID = "does-not-exist"
		root := &topology.Root{
			Topology: &topology.Topology{
				Dot: ptr("testdot"),
				Central: &topology.Central{
					Cluster: ptr("core"),
				},
				Cluster: map[string]*topology.Cluster{
					"core": {JsDomain: ptr("core-domain")},
				},
				District: map[string]*topology.District{
					"d1": {
						Partition: map[string]*topology.Partition{
							"d1/0": {Cluster: ptr(missingClusterID)},
						},
					},
				},
			},
		}
		_, err := plan.Build(root)
		if err == nil {
			t.Fatal("expected error for unknown cluster reference, got nil")
		}
		if !strings.Contains(err.Error(), missingClusterID) {
			t.Errorf("error message %q does not mention missing cluster id %q", err.Error(), missingClusterID)
		}
	})
}

func TestBuild_StreamNameCollisionRejected(t *testing.T) {
	// sanitize() is not injective ('/' and '-' both map to '_'), so two
	// distinct partition ids can produce the same stream name. Silently
	// carrying duplicates cross-wires cabinet attachment and diffing —
	// Build must fail instead.
	root := &topology.Root{
		Topology: &topology.Topology{
			Dot:     ptr("exdot"),
			Central: &topology.Central{Cluster: ptr("core")},
			Cluster: map[string]*topology.Cluster{
				"core": {JsDomain: ptr("core"), LeafEndpoint: ptr("leaf-core:7422")},
				"d7a":  {JsDomain: ptr("d7a"), LeafEndpoint: ptr("leaf-d7a:7422")},
			},
			District: map[string]*topology.District{
				"d7": {Id: ptr("d7"), Partition: map[string]*topology.Partition{
					"d7/0": {Id: ptr("d7/0"), Cluster: ptr("d7a")},
					"d7-0": {Id: ptr("d7-0"), Cluster: ptr("d7a")},
				}},
			},
		},
	}
	_, err := plan.Build(root)
	if err == nil {
		t.Fatal("expected stream-name collision error, got nil")
	}
	for _, want := range []string{"VIKASA_EXDOT_D7_D7_0", "d7/0", "d7-0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("collision error should mention %q: %v", want, err)
		}
	}
}

func TestBuild_DNSNameCollisionRejected(t *testing.T) {
	// The leaf DNS name omits the district, so two DISTINCT partition ids in
	// two DIFFERENT districts that collapse to the same DNS segment ("a/b" and
	// "a-b" both -> "a-b") produce distinct stream names but an identical DNS
	// name. Build must reject rather than emit two conflicting records.
	root := &topology.Root{
		Topology: &topology.Topology{
			Dot:     ptr("exdot"),
			Central: &topology.Central{Cluster: ptr("core")},
			Cluster: map[string]*topology.Cluster{
				"core": {JsDomain: ptr("core"), LeafEndpoint: ptr("leaf-core:7422")},
				"reg":  {JsDomain: ptr("reg"), LeafEndpoint: ptr("leaf-reg:7422")},
			},
			District: map[string]*topology.District{
				"d1": {Id: ptr("d1"), Partition: map[string]*topology.Partition{
					"a/b": {Id: ptr("a/b"), Cluster: ptr("reg")},
				}},
				"d2": {Id: ptr("d2"), Partition: map[string]*topology.Partition{
					"a-b": {Id: ptr("a-b"), Cluster: ptr("reg")},
				}},
			},
		},
	}
	_, err := plan.Build(root)
	if err == nil {
		t.Fatal("expected leaf-DNS name collision error, got nil")
	}
	for _, want := range []string{"leaf-exdot-a-b.nats.vikasa.exdot", "a/b", "a-b"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("DNS collision error should mention %q: %v", want, err)
		}
	}
}
