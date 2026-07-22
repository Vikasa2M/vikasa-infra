package render_test

import (
	"strings"
	"testing"

	"github.com/Vikasa2M/vikasa-infra/internal/plan"
	"github.com/Vikasa2M/vikasa-infra/internal/render"
)

func TestRebalanceRenderer_MoveCase(t *testing.T) {
	d := &plan.Delta{
		DOT: "exdot",
		Moved: []plan.StreamMove{{
			Old: plan.Stream{Name: "VIKASA_EXDOT_D7_D7_8", Cluster: "d7a", JSDomain: "d7a", Tier: "regional"},
			New: plan.Stream{Name: "VIKASA_EXDOT_D7_D7_8", Cluster: "d7b", JSDomain: "d7b", Tier: "regional"},
		}},
		Modified: []plan.StreamMove{{
			Old: plan.Stream{Name: "VIKASA_EXDOT_CENTRAL", Cluster: "core", JSDomain: "core", Tier: "central"},
			New: plan.Stream{Name: "VIKASA_EXDOT_CENTRAL", Cluster: "core", JSDomain: "core", Tier: "central"},
		}},
		DNSChanged: []plan.DNSChange{{
			Name:      "leaf-exdot-d7-8.nats.vikasa.exdot",
			OldTarget: "leaf-d7a.nats.vikasa.exdot:7422", NewTarget: "leaf-d7b.nats.vikasa.exdot:7422",
		}},
	}

	files, err := render.RebalanceRenderer{}.Render(d)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	md := string(files["REBALANCE.md"])

	for _, want := range []string{
		"Phase 1 — Stand up target streams",
		"Phase 2 — Dual-source at central",
		"Phase 3 — Repoint leaf-DNS",
		"Phase 4 — Cabinets re-source",
		"Phase 5 — Tear down old streams",
		"VIKASA_EXDOT_D7_D7_8",
		"$JS.d7b.API",
		"$JS.d7a.API",
		"leaf-d7a.nats.vikasa.exdot:7422` → `leaf-d7b.nats.vikasa.exdot:7422",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("REBALANCE.md missing %q\n%s", want, md)
		}
	}
	// Central stream modification is folded into phases 2/5, not the in-place section.
	if strings.Contains(md, "In-place updates") {
		t.Errorf("central in-place section should be suppressed when moves exist\n%s", md)
	}

	checkGolden(t, "rebalance/REBALANCE-move.md", files["REBALANCE.md"])
}

func TestRebalanceRenderer_DNSOnlyChange(t *testing.T) {
	d := &plan.Delta{
		DOT: "exdot",
		DNSChanged: []plan.DNSChange{{
			Name:      "leaf-exdot-d7-0.nats.vikasa.exdot",
			OldTarget: "leaf-d7a.nats.vikasa.exdot:7422", NewTarget: "leaf-d7a-new.nats.vikasa.exdot:7422",
		}},
	}
	files, err := render.RebalanceRenderer{}.Render(d)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	md := string(files["REBALANCE.md"])
	if strings.Contains(md, "Nothing to rebalance") {
		t.Errorf("a DNS-only change must not render 'Nothing to rebalance'\n%s", md)
	}
	if !strings.Contains(md, "## DNS changes") {
		t.Errorf("expected a DNS changes section\n%s", md)
	}
	if !strings.Contains(md, "leaf-d7a.nats.vikasa.exdot:7422` → `leaf-d7a-new.nats.vikasa.exdot:7422") {
		t.Errorf("expected the DNS repoint to be rendered\n%s", md)
	}
}

func TestRebalanceRenderer_NoChanges(t *testing.T) {
	files, err := render.RebalanceRenderer{}.Render(&plan.Delta{DOT: "exdot"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	md := string(files["REBALANCE.md"])
	if !strings.Contains(md, "Nothing to rebalance") {
		t.Errorf("expected no-changes runbook, got\n%s", md)
	}
	checkGolden(t, "rebalance/REBALANCE-nochanges.md", files["REBALANCE.md"])
}
