package plan_test

import (
	"testing"

	"github.com/Vikasa2M/vikasa-infra/internal/plan"
)

func TestDiff_Move(t *testing.T) {
	oldP := &plan.Plan{DOT: "exdot",
		Streams: []plan.Stream{
			{Name: "VIKASA_EXDOT_D7_D7_0", Cluster: "d7a", JSDomain: "d7a", Replicas: 3, MaxAge: "6h", Tier: "regional"},
			{Name: "VIKASA_EXDOT_D7_D7_8", Cluster: "d7a", JSDomain: "d7a", Replicas: 3, MaxAge: "6h", Tier: "regional"},
		},
		DNS: []plan.DNSRecord{{Name: "leaf-exdot-d7-8.nats.vikasa.exdot", Target: "leaf-d7a.nats.vikasa.exdot:7422"}},
	}
	newP := &plan.Plan{DOT: "exdot",
		Streams: []plan.Stream{
			{Name: "VIKASA_EXDOT_D7_D7_0", Cluster: "d7a", JSDomain: "d7a", Replicas: 3, MaxAge: "6h", Tier: "regional"},
			{Name: "VIKASA_EXDOT_D7_D7_8", Cluster: "d7b", JSDomain: "d7b", Replicas: 3, MaxAge: "6h", Tier: "regional"},
		},
		DNS: []plan.DNSRecord{{Name: "leaf-exdot-d7-8.nats.vikasa.exdot", Target: "leaf-d7b.nats.vikasa.exdot:7422"}},
	}
	d := plan.Diff(oldP, newP)
	if len(d.Moved) != 1 {
		t.Fatalf("expected 1 moved, got %d (%+v)", len(d.Moved), d.Moved)
	}
	m := d.Moved[0]
	if m.New.Name != "VIKASA_EXDOT_D7_D7_8" || m.Old.Cluster != "d7a" || m.New.Cluster != "d7b" {
		t.Errorf("unexpected move: %+v", m)
	}
	if len(d.Added)+len(d.Removed)+len(d.Modified) != 0 {
		t.Errorf("expected only a move; added=%d removed=%d modified=%d", len(d.Added), len(d.Removed), len(d.Modified))
	}
	if len(d.DNSChanged) != 1 || d.DNSChanged[0].OldTarget != "leaf-d7a.nats.vikasa.exdot:7422" || d.DNSChanged[0].NewTarget != "leaf-d7b.nats.vikasa.exdot:7422" {
		t.Errorf("expected 1 dns change d7a->d7b, got %+v", d.DNSChanged)
	}
}

func TestDiff_AddRemove(t *testing.T) {
	oldP := &plan.Plan{DOT: "exdot", Streams: []plan.Stream{{Name: "A", Cluster: "x", JSDomain: "x", Tier: "regional"}}}
	newP := &plan.Plan{DOT: "exdot", Streams: []plan.Stream{{Name: "B", Cluster: "y", JSDomain: "y", Tier: "regional"}}}
	d := plan.Diff(oldP, newP)
	if len(d.Added) != 1 || d.Added[0].Name != "B" {
		t.Errorf("expected added B, got %+v", d.Added)
	}
	if len(d.Removed) != 1 || d.Removed[0].Name != "A" {
		t.Errorf("expected removed A, got %+v", d.Removed)
	}
}

func TestDiff_ModifiedInPlace(t *testing.T) {
	oldP := &plan.Plan{DOT: "exdot", Streams: []plan.Stream{{Name: "A", Cluster: "x", JSDomain: "x", Replicas: 3, Tier: "regional"}}}
	newP := &plan.Plan{DOT: "exdot", Streams: []plan.Stream{{Name: "A", Cluster: "x", JSDomain: "x", Replicas: 5, Tier: "regional"}}}
	d := plan.Diff(oldP, newP)
	if len(d.Modified) != 1 || len(d.Moved) != 0 {
		t.Fatalf("expected 1 modified, 0 moved; got modified=%d moved=%d", len(d.Modified), len(d.Moved))
	}
	if d.Modified[0].Old.Replicas != 3 || d.Modified[0].New.Replicas != 5 {
		t.Errorf("unexpected modified: %+v", d.Modified[0])
	}
}

func TestDiff_Identical(t *testing.T) {
	p := &plan.Plan{DOT: "exdot", Streams: []plan.Stream{{Name: "A", Cluster: "x", JSDomain: "x", Tier: "regional"}}}
	d := plan.Diff(p, p)
	if d.HasChanges() {
		t.Errorf("identical plans should produce no changes, got %+v", d)
	}
	if d.ChangeCount() != 0 {
		t.Errorf("expected ChangeCount 0, got %d", d.ChangeCount())
	}
}

func TestDiff_DNSAddedRemoved(t *testing.T) {
	oldP := &plan.Plan{DOT: "exdot", DNS: []plan.DNSRecord{{Name: "old.nats.vikasa.exdot", Target: "1.2.3.4"}}}
	newP := &plan.Plan{DOT: "exdot", DNS: []plan.DNSRecord{{Name: "new.nats.vikasa.exdot", Target: "5.6.7.8"}}}
	d := plan.Diff(oldP, newP)
	if len(d.DNSAdded) != 1 || d.DNSAdded[0].Name != "new.nats.vikasa.exdot" {
		t.Errorf("DNSAdded: %+v", d.DNSAdded)
	}
	if len(d.DNSRemoved) != 1 || d.DNSRemoved[0].Name != "old.nats.vikasa.exdot" {
		t.Errorf("DNSRemoved: %+v", d.DNSRemoved)
	}
}

func TestDiff_TierChangeIsModified(t *testing.T) {
	oldP := &plan.Plan{DOT: "exdot", Streams: []plan.Stream{{Name: "A", Cluster: "x", JSDomain: "x", Tier: "regional"}}}
	newP := &plan.Plan{DOT: "exdot", Streams: []plan.Stream{{Name: "A", Cluster: "x", JSDomain: "x", Tier: "central"}}}
	d := plan.Diff(oldP, newP)
	if len(d.Modified) != 1 || len(d.Moved) != 0 {
		t.Fatalf("expected Tier/Wave-only change → 1 Modified, 0 Moved; got modified=%d moved=%d", len(d.Modified), len(d.Moved))
	}
}

// TestDiff_SourcesChanged exercises streamConfigChanged's explicit
// field-by-field Sources comparison (which replaced reflect.DeepEqual on
// this hot path): additions/removals, a changed field within one element,
// nil-vs-empty equivalence, and identical Sources producing no change.
func TestDiff_SourcesChanged(t *testing.T) {
	base := func(srcs []plan.Source) *plan.Plan {
		return &plan.Plan{DOT: "exdot", Streams: []plan.Stream{
			{Name: "A", Cluster: "x", JSDomain: "x", Tier: "regional", Sources: srcs},
		}}
	}
	srcA := plan.Source{Name: "VIKASA_BUFFER", Domain: "cab-a", FilterSubject: "vikasa.exdot.d7.a.>"}
	srcB := plan.Source{Name: "VIKASA_BUFFER", Domain: "cab-b", FilterSubject: "vikasa.exdot.d7.b.>"}
	srcATransform := plan.Source{Name: "VIKASA_BUFFER", Domain: "cab-a", TransformSource: "vikasa.exdot.d7.a.>", TransformDest: "vikasa.exdot.d7.>"}

	cases := []struct {
		name         string
		old, new     []plan.Source
		wantModified bool
	}{
		{"nil vs nil", nil, nil, false},
		{"nil vs empty", nil, []plan.Source{}, false},
		{"empty vs empty", []plan.Source{}, []plan.Source{}, false},
		{"identical single source", []plan.Source{srcA}, []plan.Source{srcA}, false},
		{"identical multi source", []plan.Source{srcA, srcB}, []plan.Source{srcA, srcB}, false},
		{"source added", []plan.Source{srcA}, []plan.Source{srcA, srcB}, true},
		{"source removed", []plan.Source{srcA, srcB}, []plan.Source{srcA}, true},
		{"nil vs one source", nil, []plan.Source{srcA}, true},
		{"domain changed", []plan.Source{srcA}, []plan.Source{{Name: srcA.Name, Domain: "cab-c", FilterSubject: srcA.FilterSubject}}, true},
		{"filter subject changed", []plan.Source{srcA}, []plan.Source{{Name: srcA.Name, Domain: srcA.Domain, FilterSubject: "vikasa.exdot.d7.z.>"}}, true},
		{"transform fields changed", []plan.Source{srcATransform}, []plan.Source{{Name: srcATransform.Name, Domain: srcATransform.Domain, TransformSource: srcATransform.TransformSource, TransformDest: "vikasa.exdot.d8.>"}}, true},
		{"order swapped (position-sensitive)", []plan.Source{srcA, srcB}, []plan.Source{srcB, srcA}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := plan.Diff(base(tc.old), base(tc.new))
			gotModified := len(d.Modified) == 1
			if gotModified != tc.wantModified {
				t.Errorf("old=%+v new=%+v: Modified=%d, want modified=%v", tc.old, tc.new, len(d.Modified), tc.wantModified)
			}
			if len(d.Moved) != 0 {
				t.Errorf("Sources-only change must never be classified as Moved: %+v", d.Moved)
			}
		})
	}
}

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
