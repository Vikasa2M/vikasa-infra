package topology_test

import (
	"strings"
	"testing"

	"github.com/Vikasa2M/vikasa-infra/internal/topology"
)

func TestLoadShared(t *testing.T) {
	root, err := topology.Load("../../examples/exdot-shared.json")
	if err != nil {
		t.Fatalf("Load(exdot-shared.json) unexpected error: %v", err)
	}

	topo := root.Topology
	if topo == nil {
		t.Fatal("Topology is nil")
	}

	if got := len(topo.Cluster); got != 3 {
		t.Errorf("expected 3 clusters, got %d", got)
	}
	if got := len(topo.District); got != 1 {
		t.Errorf("expected 1 district, got %d", got)
	}

	d7, ok := topo.District["d7"]
	if !ok {
		t.Fatal("district 'd7' not found")
	}
	if got := len(d7.Partition); got != 2 {
		t.Errorf("expected 2 partitions in district d7, got %d", got)
	}

	if topo.Central == nil {
		t.Fatal("Central is nil")
	}
	if topo.Central.Replicas == nil {
		t.Fatal("Central.Replicas is nil")
	}
	if got := *topo.Central.Replicas; got != 5 {
		t.Errorf("expected central replicas == 5, got %d", got)
	}
	if topo.Central.Cluster == nil {
		t.Fatal("Central.Cluster is nil")
	}
	if got := *topo.Central.Cluster; got != "core" {
		t.Errorf("expected central cluster == 'core', got %q", got)
	}
}

func TestLoadMulticluster(t *testing.T) {
	root, err := topology.Load("../../examples/exdot-multicluster.json")
	if err != nil {
		t.Fatalf("Load(exdot-multicluster.json) unexpected error: %v", err)
	}

	topo := root.Topology
	if topo == nil {
		t.Fatal("Topology is nil")
	}

	if got := len(topo.Cluster); got != 3 {
		t.Errorf("expected 3 clusters, got %d", got)
	}
	if got := len(topo.District); got != 1 {
		t.Errorf("expected 1 district, got %d", got)
	}

	d7, ok := topo.District["d7"]
	if !ok {
		t.Fatal("district 'd7' not found")
	}
	if got := len(d7.Partition); got != 2 {
		t.Errorf("expected 2 partitions in district d7, got %d", got)
	}

	if topo.Central == nil {
		t.Fatal("Central is nil")
	}
	if topo.Central.Replicas == nil {
		t.Fatal("Central.Replicas is nil")
	}
	if got := *topo.Central.Replicas; got != 5 {
		t.Errorf("expected central replicas == 5, got %d", got)
	}
	if topo.Central.Cluster == nil {
		t.Fatal("Central.Cluster is nil")
	}
	if got := *topo.Central.Cluster; got != "core" {
		t.Errorf("expected central cluster == 'core', got %q", got)
	}

	// Verify multicluster mode: each cluster has a distinct context.
	contexts := make(map[string]struct{})
	for id, cl := range topo.Cluster {
		if cl.Substrate == nil || cl.Substrate.Context == nil {
			t.Errorf("cluster %q has nil substrate or context", id)
			continue
		}
		ctx := *cl.Substrate.Context
		if _, dup := contexts[ctx]; dup {
			t.Errorf("cluster %q shares context %q — expected distinct contexts in multicluster mode", id, ctx)
		}
		contexts[ctx] = struct{}{}
	}
}

func TestLoadOrphanFails(t *testing.T) {
	_, err := topology.Load("../../examples/INVALID-orphan.json")
	if err == nil {
		t.Fatal("expected error for orphan partition, got nil")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("expected error message to contain 'nope', got: %v", err)
	}
}
