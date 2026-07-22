package fleet_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Vikasa2M/vikasa-infra/internal/fleet"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cabinets.json")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	return p
}

func TestLoad_OK(t *testing.T) {
	p := writeTemp(t, `{"dot":"exdot","cabinets":[
		{"id":"c1","partition":"d7/0","filter":"vikasa.exdot.>"},
		{"id":"c2","partition":"d7/8"}
	]}`)
	inv, err := fleet.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if inv.DOT != "exdot" {
		t.Errorf("dot = %q, want exdot", inv.DOT)
	}
	if len(inv.Cabinets) != 2 {
		t.Fatalf("cabinets = %d, want 2", len(inv.Cabinets))
	}
	if inv.Cabinets[0].Filter != "vikasa.exdot.>" {
		t.Errorf("filter not parsed: %q", inv.Cabinets[0].Filter)
	}
	if inv.Cabinets[1].Filter != "" {
		t.Errorf("missing filter should be empty, got %q", inv.Cabinets[1].Filter)
	}
}

func TestLoad_DuplicateID(t *testing.T) {
	p := writeTemp(t, `{"dot":"exdot","cabinets":[
		{"id":"c1","partition":"d7/0"},
		{"id":"c1","partition":"d7/8"}
	]}`)
	_, err := fleet.Load(p)
	if err == nil || !strings.Contains(err.Error(), "c1") {
		t.Fatalf("expected duplicate-id error mentioning c1, got %v", err)
	}
}

func TestLoad_MissingFields(t *testing.T) {
	cases := map[string]string{
		"empty dot":       `{"dot":"","cabinets":[{"id":"c1","partition":"d7/0"}]}`,
		"empty id":        `{"dot":"exdot","cabinets":[{"id":"","partition":"d7/0"}]}`,
		"empty partition": `{"dot":"exdot","cabinets":[{"id":"c1","partition":""}]}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := fleet.Load(writeTemp(t, content)); err == nil {
				t.Errorf("expected error for %s, got nil", name)
			}
		})
	}
}
