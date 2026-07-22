package render_test

import (
	"strings"
	"testing"

	"github.com/Vikasa2M/vikasa-infra/internal/fleet"
	"github.com/Vikasa2M/vikasa-infra/internal/plan"
	"github.com/Vikasa2M/vikasa-infra/internal/render"
	"github.com/Vikasa2M/vikasa-infra/internal/topology"
)

func TestDiagramRenderer_ExdotShared(t *testing.T) {
	p := loadPlan(t, "../../examples/exdot-shared.json")

	files, err := render.DiagramRenderer{}.Render(p)
	if err != nil {
		t.Fatalf("DiagramRenderer.Render: %v", err)
	}

	got, ok := files["TOPOLOGY.md"]
	if !ok {
		t.Fatal("TOPOLOGY.md not found in output")
	}
	if len(files) != 1 {
		t.Fatalf("expected exactly 1 file, got %d", len(files))
	}
	s := string(got)

	// Markdown header + generated banner.
	if !strings.HasPrefix(s, "# Topology Diagram: exdot\n") {
		t.Errorf("missing markdown title header\n%s", s)
	}
	if !strings.Contains(s, "> **Generated** by vikasa-infra/cmd/gen") {
		t.Errorf("missing generated banner\n%s", s)
	}

	// Fenced mermaid block.
	if !strings.Contains(s, "```mermaid\nflowchart TD\n") {
		t.Errorf("missing mermaid flowchart opener\n%s", s)
	}

	// Cluster subgraphs (core, d7a, d7b).
	for _, want := range []string{
		`subgraph cl_core["cluster: core"]`,
		`subgraph cl_d7a["cluster: d7a"]`,
		`subgraph cl_d7b["cluster: d7b"]`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing subgraph %q\n%s", want, s)
		}
	}

	// Stream nodes. The exdot-shared.json example has district "d7" with partition
	// IDs "d7/0" and "d7/8", so PartitionStreamName produces VIKASA_EXDOT_D7_D7_0
	// and VIKASA_EXDOT_D7_D7_8 (district token + sanitised partitionID token).
	for _, want := range []string{
		"s_VIKASA_EXDOT_CENTRAL_D7_D7_0[",
		"s_VIKASA_EXDOT_CENTRAL_D7_D7_8[",
		"s_VIKASA_EXDOT_D7_D7_0[",
		"s_VIKASA_EXDOT_D7_D7_8[",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing stream node %q\n%s", want, s)
		}
	}

	// Central is sharded per partition: each regional stream sources its own shard.
	for _, want := range []string{
		"s_VIKASA_EXDOT_D7_D7_0 -->|source| s_VIKASA_EXDOT_CENTRAL_D7_D7_0",
		"s_VIKASA_EXDOT_D7_D7_8 -->|source| s_VIKASA_EXDOT_CENTRAL_D7_D7_8",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing sourcing edge %q\n%s", want, s)
		}
	}

	// No cabinets in this plan.
	if strings.Contains(s, "cabinet:") {
		t.Errorf("unexpected cabinet node in non-cabinet plan\n%s", s)
	}

	// Lock byte-for-byte output.
	checkGolden(t, "TOPOLOGY.md", got)
}

func TestDiagramRenderer_WithCabinets(t *testing.T) {
	root, err := topology.Load("../../examples/exdot-shared.json")
	if err != nil {
		t.Fatalf("topology.Load: %v", err)
	}
	p, err := plan.Build(root)
	if err != nil {
		t.Fatalf("plan.Build: %v", err)
	}
	inv, err := fleet.Load("../../examples/exdot-cabinets.json")
	if err != nil {
		t.Fatalf("fleet.Load: %v", err)
	}
	if err := plan.AttachCabinets(p, inv, root); err != nil {
		t.Fatalf("plan.AttachCabinets: %v", err)
	}

	files, err := render.DiagramRenderer{}.Render(p)
	if err != nil {
		t.Fatalf("DiagramRenderer.Render: %v", err)
	}
	got := files["TOPOLOGY.md"]
	s := string(got)

	// Cabinet nodes and leaf edges to their partition streams.
	for _, want := range []string{
		`cab_exdot_d7a_cab_001(["cabinet: exdot-d7a-cab-001"]) -->|leaf| s_VIKASA_EXDOT_D7_D7_0`,
		`cab_exdot_d7a_cab_002(["cabinet: exdot-d7a-cab-002"]) -->|leaf| s_VIKASA_EXDOT_D7_D7_0`,
		`cab_exdot_d7b_cab_050(["cabinet: exdot-d7b-cab-050"]) -->|leaf| s_VIKASA_EXDOT_D7_D7_8`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing cabinet edge %q\n%s", want, s)
		}
	}

	// The central sourcing edges still present.
	if !strings.Contains(s, "s_VIKASA_EXDOT_D7_D7_0 -->|source| s_VIKASA_EXDOT_CENTRAL") {
		t.Errorf("sourcing edge lost when cabinets attached\n%s", s)
	}

	checkGolden(t, "TOPOLOGY-cabinets.md", got)
}

// TestDiagramShowsDMZ verifies that a plan with a dmz-tier stream produces a
// "cluster: dmz" subgraph and a central-->dmz share edge in the diagram.
func TestDiagramShowsDMZ(t *testing.T) {
	p := &plan.Plan{
		DOT: "exdot",
		Streams: []plan.Stream{
			{
				Name:     "VIKASA_EXDOT_CENTRAL",
				Cluster:  "core",
				JSDomain: "core",
				Replicas: 5,
				Tier:     "central",
				Sources: []plan.Source{
					{Name: "VIKASA_EXDOT_D7_D7_0", Domain: "d7a"},
				},
			},
			{
				Name:     "VIKASA_EXDOT_D7_D7_0",
				Cluster:  "d7a",
				JSDomain: "d7a",
				Replicas: 3,
				Tier:     "regional",
			},
			{
				Name:     "VIKASA_EXDOT_DMZ",
				Cluster:  "dmz",
				JSDomain: "dmz",
				Replicas: 3,
				Tier:     "dmz",

				Sources: []plan.Source{
					{Name: "VIKASA_EXDOT_DMZ_PARTNER_A", Domain: "core", FilterSubject: "vikasa.exdot.>"},
				},
			},
		},
	}

	files, err := render.DiagramRenderer{}.Render(p)
	if err != nil {
		t.Fatalf("DiagramRenderer.Render: %v", err)
	}
	got, ok := files["TOPOLOGY.md"]
	if !ok {
		t.Fatal("TOPOLOGY.md not found in output")
	}
	s := string(got)

	// DMZ cluster subgraph must be present.
	if !strings.Contains(s, `cluster: dmz`) {
		t.Errorf("expected 'cluster: dmz' subgraph in output\n%s", s)
	}

	// Central --> DMZ share edge must be present.
	if !strings.Contains(s, `-->|share|`) {
		t.Errorf("expected -->|share| edge in output\n%s", s)
	}

	// Specifically from central stream node to dmz stream node.
	if !strings.Contains(s, "s_VIKASA_EXDOT_CENTRAL") {
		t.Errorf("expected central stream node id s_VIKASA_EXDOT_CENTRAL in output\n%s", s)
	}
	if !strings.Contains(s, "s_VIKASA_EXDOT_DMZ") {
		t.Errorf("expected dmz stream node id s_VIKASA_EXDOT_DMZ in output\n%s", s)
	}

	// Edge must reference both.
	wantEdge := "s_VIKASA_EXDOT_CENTRAL -->|share| s_VIKASA_EXDOT_DMZ"
	if !strings.Contains(s, wantEdge) {
		t.Errorf("expected edge %q in output\n%s", wantEdge, s)
	}
}

// TestDiagramRenderer_ClusterIDCollision verifies that two cluster ids which
// sanitize to the same string (e.g. "d7.a" and "d7-a" both → "cl_d7_a") are
// assigned distinct subgraph ids ("cl_d7_a" and "cl_d7_a_2") so Mermaid cannot
// silently merge them into a single corrupt node.
func TestDiagramRenderer_ClusterIDCollision(t *testing.T) {
	p := &plan.Plan{
		DOT: "exdot",
		Streams: []plan.Stream{
			{
				Name:     "VIKASA_EXDOT_CENTRAL",
				Cluster:  "core",
				JSDomain: "core",
				Replicas: 3,
				Tier:     "central",
				Sources: []plan.Source{
					{Name: "VIKASA_EXDOT_REG_A", Domain: "d7.a"},
					{Name: "VIKASA_EXDOT_REG_B", Domain: "d7-a"},
				},
			},
			{
				Name:     "VIKASA_EXDOT_REG_A",
				Cluster:  "d7.a",
				JSDomain: "d7.a",
				Replicas: 1,
				Tier:     "regional",
			},
			{
				Name:     "VIKASA_EXDOT_REG_B",
				Cluster:  "d7-a",
				JSDomain: "d7-a",
				Replicas: 1,
				Tier:     "regional",
			},
		},
	}

	files, err := render.DiagramRenderer{}.Render(p)
	if err != nil {
		t.Fatalf("DiagramRenderer.Render: %v", err)
	}
	s := string(files["TOPOLOGY.md"])

	// Both cluster subgraph ids must be present and DISTINCT.
	if !strings.Contains(s, `subgraph cl_d7_a[`) {
		t.Errorf("expected subgraph id cl_d7_a in output\n%s", s)
	}
	if !strings.Contains(s, `subgraph cl_d7_a_2[`) {
		t.Errorf("expected subgraph id cl_d7_a_2 in output (collision suffix)\n%s", s)
	}
}
