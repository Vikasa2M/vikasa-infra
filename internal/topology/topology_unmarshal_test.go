package topology

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestSubjectPrefixDefaultAndOverride(t *testing.T) {
	if got := DefaultSubjectPrefix("exdot", "d7"); got != "vikasa.exdot.d7.>" {
		t.Fatalf("default prefix: got %q", got)
	}
}

func TestUnmarshalSharedSpec(t *testing.T) {
	root, err := Load("../../examples/exdot-shared.json")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tp := root.Topology
	if tp == nil || tp.Dot == nil || *tp.Dot != "exdot" {
		t.Fatalf("dot not parsed: %+v", tp)
	}
	if len(tp.Cluster) != 3 {
		t.Fatalf("want 3 clusters, got %d", len(tp.Cluster))
	}
	core := tp.Cluster["core"]
	if core == nil || core.JsDomain == nil || *core.JsDomain != "core" {
		t.Fatalf("core cluster not indexed by id: %+v", core)
	}
	if core.Substrate == nil || core.Substrate.Type != SubstrateKubernetes {
		t.Fatalf("substrate type not parsed: %+v", core.Substrate)
	}
	d7 := tp.District["d7"]
	if d7 == nil || d7.Partition["d7/0"] == nil || *d7.Partition["d7/0"].Cluster != "d7a" {
		t.Fatalf("partition not indexed: %+v", d7)
	}
	if tp.Central == nil || tp.Central.Cluster == nil || *tp.Central.Cluster != "core" {
		t.Fatalf("central not parsed: %+v", tp.Central)
	}
	if tp.Central.Replicas == nil || *tp.Central.Replicas != 5 {
		t.Fatalf("replicas not parsed: %+v", tp.Central.Replicas)
	}
}

func TestDMZParseAndValidate(t *testing.T) {
	root, err := loadInline(`{"vikasa-infra-topology:topology":{
	  "dot":"exdot",
	  "cluster":[{"id":"core","js-domain":"core","leaf-endpoint":"x:7422","substrate":{"type":"kubernetes","namespace":"n"}},
	             {"id":"dmz","js-domain":"dmz","leaf-endpoint":"y:7422","substrate":{"type":"kubernetes","namespace":"d"}}],
	  "central":{"cluster":"core","replicas":3},
	  "dmz":{"cluster":"dmz","replicas":3,"shares":[{"consumer":"research","from":"vikasa.exdot.>","as":"vikasa.exdot.share.research.>"}]}
	}}`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	d := root.Topology.DMZ
	if d == nil || d.Cluster == nil || *d.Cluster != "dmz" || len(d.Shares) != 1 {
		t.Fatalf("dmz not parsed: %+v", d)
	}
	if *d.Shares[0].From != "vikasa.exdot.>" || *d.Shares[0].As != "vikasa.exdot.share.research.>" {
		t.Fatalf("share fields: %+v", d.Shares[0])
	}
	if got := DefaultShareAs("exdot", "peer-neighbor"); got != "vikasa.exdot.share.peer-neighbor.>" {
		t.Fatalf("default as: %q", got)
	}
}

// TestUnmarshalRejections covers the id-indexing errors UnmarshalJSON raises
// while converting the wire arrays into id-keyed maps. These fire before
// Validate, so the specs only need enough shape to reach the indexing code.
func TestUnmarshalRejections(t *testing.T) {
	cases := []struct {
		name        string
		spec        string
		errContains string
	}{
		{"duplicate cluster id",
			`{"vikasa-infra-topology:topology":{"dot":"exdot","cluster":[{"id":"core"},{"id":"core"}]}}`,
			`duplicate cluster id "core"`},
		{"duplicate district id",
			`{"vikasa-infra-topology:topology":{"dot":"exdot","district":[{"id":"d7"},{"id":"d7"}]}}`,
			`duplicate district id "d7"`},
		{"duplicate partition id within district",
			`{"vikasa-infra-topology:topology":{"dot":"exdot","district":[{"id":"d7","partition":[{"id":"d7/0"},{"id":"d7/0"}]}]}}`,
			`district "d7": duplicate partition id "d7/0"`},
		{"cluster missing id",
			`{"vikasa-infra-topology:topology":{"dot":"exdot","cluster":[{"js-domain":"core"}]}}`,
			"cluster[0]: missing id"},
		{"district missing id",
			`{"vikasa-infra-topology:topology":{"dot":"exdot","district":[{"subject-prefix":"vikasa.exdot.d7.>"}]}}`,
			"district[0]: missing id"},
		{"partition missing id",
			`{"vikasa-infra-topology:topology":{"dot":"exdot","district":[{"id":"d7","partition":[{"cluster":"core"}]}]}}`,
			`district "d7" partition[0]: missing id`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadInline(tc.spec)
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.errContains)
			}
			if !strings.Contains(err.Error(), tc.errContains) {
				t.Fatalf("want error containing %q, got: %v", tc.errContains, err)
			}
		})
	}
}

func TestUnmarshalShareAsDefault(t *testing.T) {
	root, err := loadInline(`{"vikasa-infra-topology:topology":{
	  "dot":"exdot",
	  "cluster":[{"id":"core","js-domain":"core","leaf-endpoint":"x:7422","substrate":{"type":"kubernetes","namespace":"n"}}],
	  "central":{"cluster":"core"},
	  "dmz":{"cluster":"core","shares":[{"consumer":"research","from":"vikasa.exdot.d1.>"}]}
	}}`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	s := root.Topology.DMZ.Shares[0]
	if s.As == nil || *s.As != "vikasa.exdot.share.research.>" {
		t.Fatalf("omitted as should default to the consumer's share space, got %+v", s.As)
	}
}

// TestPartitionIndex covers the flat-namespace mapping and its
// duplicate-across-districts rejection. Validate now calls PartitionIndex,
// so a cross-district duplicate fails Load itself; the direct-index error is
// still asserted via Unmarshal (which only de-dupes within a district).
func TestPartitionIndex(t *testing.T) {
	base := `{"vikasa-infra-topology:topology":{
	  "dot":"exdot",
	  "cluster":[{"id":"core","js-domain":"core","leaf-endpoint":"x:7422","substrate":{"type":"kubernetes","namespace":"n"}}],
	  "district":[{"id":"d7","partition":[{"id":%q,"cluster":"core"}]},
	              {"id":"d8","partition":[{"id":%q,"cluster":"core"}]}],
	  "central":{"cluster":"core"}
	}}`

	root, err := loadInline(fmt.Sprintf(base, "d7/0", "d8/0"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	idx, err := root.Topology.PartitionIndex()
	if err != nil {
		t.Fatalf("PartitionIndex: %v", err)
	}
	if idx["d7/0"] != "d7" || idx["d8/0"] != "d8" || len(idx) != 2 {
		t.Fatalf("index mapping wrong: %v", idx)
	}

	if _, err = loadInline(fmt.Sprintf(base, "p0", "p0")); err == nil ||
		!strings.Contains(err.Error(), `duplicate partition id "p0" across districts`) {
		t.Fatalf("cross-district duplicate must fail Load, got: %v", err)
	}
	dup := &Root{}
	if err := Unmarshal([]byte(fmt.Sprintf(base, "p0", "p0")), dup); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, err := dup.Topology.PartitionIndex(); err == nil ||
		!strings.Contains(err.Error(), `duplicate partition id "p0" across districts`) {
		t.Fatalf("want duplicate-across-districts error from the index itself, got: %v", err)
	}
}

func loadInline(jsonStr string) (*Root, error) {
	f, err := os.CreateTemp("", "topo-*.json")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(jsonStr); err != nil {
		return nil, err
	}
	f.Close()
	return Load(f.Name())
}
