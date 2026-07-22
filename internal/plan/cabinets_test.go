package plan_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Vikasa2M/vikasa-infra/internal/fleet"
	"github.com/Vikasa2M/vikasa-infra/internal/plan"
	"github.com/Vikasa2M/vikasa-infra/internal/topology"
)

func sharedPlan(t *testing.T) (*plan.Plan, *topology.Root) {
	t.Helper()
	root, err := topology.Load("../../examples/exdot-shared.json")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	p, err := plan.Build(root)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return p, root
}

func streamByName(p *plan.Plan, name string) (plan.Stream, bool) {
	for _, s := range p.Streams {
		if s.Name == name {
			return s, true
		}
	}
	return plan.Stream{}, false
}

func TestAttachCabinets_AttachesSortedSources(t *testing.T) {
	p, root := sharedPlan(t)
	inv := &fleet.Inventory{DOT: "exdot", Cabinets: []fleet.Cabinet{
		{ID: "cab-b", Partition: "d7/0"},
		{ID: "cab-a", Partition: "d7/0", Filter: "vikasa.exdot.d7.>"},
		{ID: "cab-c", Partition: "d7/8"},
	}}
	if err := plan.AttachCabinets(p, inv, root); err != nil {
		t.Fatalf("AttachCabinets: %v", err)
	}

	s0, ok := streamByName(p, "VIKASA_EXDOT_D7_D7_0")
	if !ok {
		t.Fatal("VIKASA_EXDOT_D7_D7_0 not found")
	}
	if len(s0.Sources) != 2 {
		t.Fatalf("d7/0: expected 2 sources, got %d", len(s0.Sources))
	}
	if s0.Sources[0].Domain != "cab-a" || s0.Sources[1].Domain != "cab-b" {
		t.Errorf("sources not sorted by domain: %+v", s0.Sources)
	}
	if s0.Sources[0].Name != "VIKASA_BUFFER" {
		t.Errorf("source name = %q, want VIKASA_BUFFER", s0.Sources[0].Name)
	}
	if s0.Sources[0].FilterSubject != "vikasa.exdot.d7.>" {
		t.Errorf("filter not carried: %q", s0.Sources[0].FilterSubject)
	}

	s8, _ := streamByName(p, "VIKASA_EXDOT_D7_D7_8")
	if len(s8.Sources) != 1 || s8.Sources[0].Domain != "cab-c" {
		t.Errorf("d7/8: expected 1 source cab-c, got %+v", s8.Sources)
	}
}

func TestAttachCabinets_OrphanPartition(t *testing.T) {
	p, root := sharedPlan(t)
	inv := &fleet.Inventory{DOT: "exdot", Cabinets: []fleet.Cabinet{
		{ID: "cab-x", Partition: "d9/9"},
	}}
	err := plan.AttachCabinets(p, inv, root)
	if err == nil || !strings.Contains(err.Error(), "d9/9") {
		t.Fatalf("expected orphan-partition error mentioning d9/9, got %v", err)
	}
}

func TestAttachCabinets_DotMismatch(t *testing.T) {
	p, root := sharedPlan(t)
	inv := &fleet.Inventory{DOT: "scdot"}
	if err := plan.AttachCabinets(p, inv, root); err == nil {
		t.Fatal("expected dot-mismatch error, got nil")
	}
}

func TestAttachCabinetsRejectsFilterOutsidePrefix(t *testing.T) {
	p, root := sharedPlan(t)
	inv := &fleet.Inventory{DOT: "exdot", Cabinets: []fleet.Cabinet{
		{ID: "cab-x", Partition: "d7/0", Filter: "vikasa.exdot.atl.999.>"},
	}}
	err := plan.AttachCabinets(p, inv, root)
	if err == nil {
		t.Fatalf("expected error for filter outside district prefix")
	}
	if !strings.Contains(err.Error(), "vikasa.exdot.d7.") {
		t.Fatalf("error should name the district prefix, got: %v", err)
	}
}

// Pinned contract: an inventory with an empty cabinets list (but matching DOT)
// is a complete no-op — nil error, plan byte-for-byte unchanged.
func TestAttachCabinets_EmptyCabinetsNoop(t *testing.T) {
	p, root := sharedPlan(t)
	pristine, _ := sharedPlan(t) // second build for comparison
	inv := &fleet.Inventory{DOT: "exdot", Cabinets: []fleet.Cabinet{}}
	if err := plan.AttachCabinets(p, inv, root); err != nil {
		t.Fatalf("empty cabinets list should be a no-op, got %v", err)
	}
	if !reflect.DeepEqual(p, pristine) {
		t.Errorf("plan changed by empty-inventory attach:\nafter=%+v\npristine=%+v", p, pristine)
	}
}

func TestAttachCabinets_NilInventoryNoop(t *testing.T) {
	p, root := sharedPlan(t)
	if err := plan.AttachCabinets(p, nil, root); err != nil {
		t.Fatalf("nil inventory should be a no-op, got %v", err)
	}
	for _, s := range p.Streams {
		if s.Tier == "regional" && len(s.Sources) != 0 {
			t.Errorf("regional stream %s should have no sources after nil attach", s.Name)
		}
	}
}

func TestMaxPartitionFanIn(t *testing.T) {
	p := &plan.Plan{Streams: []plan.Stream{
		{Name: "A", Tier: plan.TierRegional, Sources: make([]plan.Source, 3)},
		{Name: "B", Tier: plan.TierRegional, Sources: make([]plan.Source, 5)},
		{Name: "C", Tier: plan.TierCentral, Sources: make([]plan.Source, 9)}, // ignored
	}}
	name, n := plan.MaxPartitionFanIn(p)
	if name != "B" || n != 5 {
		t.Errorf("got (%q,%d) want (B,5)", name, n)
	}
}

func TestMaxPartitionFanIn_Empty(t *testing.T) {
	name, n := plan.MaxPartitionFanIn(&plan.Plan{})
	if name != "" || n != 0 {
		t.Errorf("empty plan: got (%q,%d) want (\"\",0)", name, n)
	}
}

func TestMaxPartitionFanIn_TieBreaksByName(t *testing.T) {
	p := &plan.Plan{Streams: []plan.Stream{
		{Name: "Z", Tier: plan.TierRegional, Sources: make([]plan.Source, 4)},
		{Name: "A", Tier: plan.TierRegional, Sources: make([]plan.Source, 4)},
	}}
	name, n := plan.MaxPartitionFanIn(p)
	if name != "A" || n != 4 {
		t.Errorf("tie: got (%q,%d) want (A,4)", name, n)
	}
}
