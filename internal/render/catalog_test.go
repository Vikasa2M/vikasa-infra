package render

import (
	"strings"
	"testing"

	"github.com/Vikasa2M/vikasa-infra/internal/topology"
)

func TestCatalogRender(t *testing.T) {
	cons, from, as := "research", "vikasa.exdot.d7.>", "vikasa.exdot.share.research.>"
	dmz := &topology.DMZ{Cluster: sp("dmz"), Shares: []*topology.Share{{Consumer: &cons, From: &from, As: &as}}}
	files, err := CatalogRenderer{}.Render("exdot", dmz)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	md := string(files["dmz-catalog.md"])
	if !strings.Contains(md, "research") || !strings.Contains(md, as) {
		t.Fatalf("catalog md missing consumer/subject:\n%s", md)
	}
	if _, ok := files["dmz-catalog.json"]; !ok {
		t.Fatalf("missing dmz-catalog.json")
	}
	if got, _ := (CatalogRenderer{}).Render("exdot", nil); len(got) != 0 {
		t.Fatalf("nil dmz should yield no files, got %d", len(got))
	}
}

func sp(s string) *string { return &s }
