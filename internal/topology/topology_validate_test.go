package topology

import (
	"fmt"
	"strings"
	"testing"
)

// dmzSpec builds a minimal valid two-cluster topology with a single DMZ
// share whose "as" subject is the value under test.
func dmzSpec(as string) string {
	return fmt.Sprintf(`{"vikasa-infra-topology:topology":{
	  "dot":"exdot",
	  "cluster":[{"id":"core","js-domain":"core","leaf-endpoint":"x:7422","substrate":{"type":"kubernetes","namespace":"n"}},
	             {"id":"dmz","js-domain":"dmz","leaf-endpoint":"y:7422","substrate":{"type":"kubernetes","namespace":"d"}}],
	  "central":{"cluster":"core","replicas":3},
	  "dmz":{"cluster":"dmz","replicas":3,"shares":[{"consumer":"research","from":"vikasa.exdot.d1.>","as":%q}]}
	}}`, as)
}

// Shared fragments for composing minimal specs in rejection tables.
const (
	okCluster = `"cluster":[{"id":"core","js-domain":"core","leaf-endpoint":"x:7422","substrate":{"type":"kubernetes","namespace":"n"}}]`
	okCentral = `"central":{"cluster":"core"}`
)

// topoSpec wraps a topology body in the RFC 7951 module-qualified envelope.
func topoSpec(body string) string {
	return `{"vikasa-infra-topology:topology":{` + body + `}}`
}

func TestValidateMinimalSpec(t *testing.T) {
	root, err := loadInline(topoSpec(`"dot":"exdot",` + okCluster + `,` + okCentral))
	if err != nil {
		t.Fatalf("minimal valid spec should load: %v", err)
	}
	if root.Topology == nil || *root.Topology.Dot != "exdot" {
		t.Fatalf("topology not parsed: %+v", root.Topology)
	}
}

func TestValidateRejections(t *testing.T) {
	cases := []struct {
		name        string
		spec        string
		errContains string // empty means the spec must pass
	}{
		{"empty document", `{}`, "topology: missing"},
		{"missing dot", topoSpec(okCluster + `,` + okCentral), "topology.dot"},
		{"dot uppercase", topoSpec(`"dot":"EXDOT",` + okCluster + `,` + okCentral), "topology.dot"},
		{"dot unicode", topoSpec(`"dot":"gdöt",` + okCluster + `,` + okCentral), "topology.dot"},
		{"dot empty", topoSpec(`"dot":"",` + okCluster + `,` + okCentral), "topology.dot"},
		{"cluster id uppercase",
			topoSpec(`"dot":"exdot","cluster":[{"id":"Core","js-domain":"core","leaf-endpoint":"x:7422","substrate":{"type":"kubernetes"}}],"central":{"cluster":"Core"}`),
			"id is not a token"},
		{"cluster missing js-domain",
			topoSpec(`"dot":"exdot","cluster":[{"id":"core","leaf-endpoint":"x:7422","substrate":{"type":"kubernetes"}}],` + okCentral),
			"js-domain"},
		{"cluster missing leaf-endpoint",
			topoSpec(`"dot":"exdot","cluster":[{"id":"core","js-domain":"core","substrate":{"type":"kubernetes"}}],` + okCentral),
			"leaf-endpoint is required"},
		{"cluster empty leaf-endpoint",
			topoSpec(`"dot":"exdot","cluster":[{"id":"core","js-domain":"core","leaf-endpoint":"","substrate":{"type":"kubernetes"}}],` + okCentral),
			"leaf-endpoint is required"},
		{"cluster missing substrate",
			topoSpec(`"dot":"exdot","cluster":[{"id":"core","js-domain":"core","leaf-endpoint":"x:7422"}],` + okCentral),
			"substrate.type"},
		{"cluster unknown substrate type",
			topoSpec(`"dot":"exdot","cluster":[{"id":"core","js-domain":"core","leaf-endpoint":"x:7422","substrate":{"type":"vm"}}],` + okCentral),
			"substrate.type"},
		{"district id uppercase",
			topoSpec(`"dot":"exdot",` + okCluster + `,"district":[{"id":"D7"}],` + okCentral),
			"id is not a token"},
		{"missing central", topoSpec(`"dot":"exdot",` + okCluster), "central.cluster is required"},
		{"central missing cluster",
			topoSpec(`"dot":"exdot",` + okCluster + `,"central":{"replicas":3}`),
			"central.cluster is required"},
		{"central replicas 0",
			topoSpec(`"dot":"exdot",` + okCluster + `,"central":{"cluster":"core","replicas":0}`),
			"out of range"},
		{"central replicas 6",
			topoSpec(`"dot":"exdot",` + okCluster + `,"central":{"cluster":"core","replicas":6}`),
			"out of range"},
		{"central replicas at lower bound",
			topoSpec(`"dot":"exdot",` + okCluster + `,"central":{"cluster":"core","replicas":1}`),
			""},
		{"central replicas at upper bound",
			topoSpec(`"dot":"exdot",` + okCluster + `,"central":{"cluster":"core","replicas":5}`),
			""},
		{"dmz missing cluster",
			topoSpec(`"dot":"exdot",` + okCluster + `,` + okCentral + `,"dmz":{"replicas":3}`),
			"dmz.cluster is required"},
		{"dmz replicas 0",
			topoSpec(`"dot":"exdot",` + okCluster + `,` + okCentral + `,"dmz":{"cluster":"core","replicas":0}`),
			"out of range"},
		{"dmz replicas 6",
			topoSpec(`"dot":"exdot",` + okCluster + `,` + okCentral + `,"dmz":{"cluster":"core","replicas":6}`),
			"out of range"},
		{"share missing consumer",
			topoSpec(`"dot":"exdot",` + okCluster + `,` + okCentral + `,"dmz":{"cluster":"core","shares":[{"from":"vikasa.exdot.d1.>","as":"vikasa.exdot.share.x.>"}]}`),
			"consumer is required"},
		{"share empty consumer",
			topoSpec(`"dot":"exdot",` + okCluster + `,` + okCentral + `,"dmz":{"cluster":"core","shares":[{"consumer":"","from":"vikasa.exdot.d1.>","as":"vikasa.exdot.share.x.>"}]}`),
			"consumer is required"},
		{"share missing from",
			topoSpec(`"dot":"exdot",` + okCluster + `,` + okCentral + `,"dmz":{"cluster":"core","shares":[{"consumer":"research","as":"vikasa.exdot.share.research.>"}]}`),
			"from is required"},
		// An omitted "as" is auto-defaulted at unmarshal time; only an
		// explicitly empty "as" reaches Validate's required-check.
		{"share empty as",
			topoSpec(`"dot":"exdot",` + okCluster + `,` + okCentral + `,"dmz":{"cluster":"core","shares":[{"consumer":"research","from":"vikasa.exdot.d1.>","as":""}]}`),
			"as is required"},
		{"dmz with empty shares list",
			topoSpec(`"dot":"exdot",` + okCluster + `,` + okCentral + `,"dmz":{"cluster":"core","shares":[]}`),
			"at least one share"},
		{"dmz with omitted shares",
			topoSpec(`"dot":"exdot",` + okCluster + `,` + okCentral + `,"dmz":{"cluster":"core"}`),
			"at least one share"},
		{"partition id duplicated across districts",
			topoSpec(`"dot":"exdot",` + okCluster + `,"district":[{"id":"d7","partition":[{"id":"p0","cluster":"core"}]},{"id":"d8","partition":[{"id":"p0","cluster":"core"}]}],` + okCentral),
			"duplicate partition id"},
		{"share omitted as defaults and passes",
			topoSpec(`"dot":"exdot",` + okCluster + `,` + okCentral + `,"dmz":{"cluster":"core","shares":[{"consumer":"research","from":"vikasa.exdot.d1.>"}]}`),
			""},
		// Finding 1: a `from` that satisfies the district-prefix match but
		// carries an accounts.conf quote-breakout payload must be rejected.
		{"share from with injection payload",
			topoSpec(`"dot":"exdot",` + okCluster + `,` + okCentral + `,"dmz":{"cluster":"core","shares":[{"consumer":"research","from":"vikasa.exdot.d1.\" } ] } ATTACKER { jetstream: enabled","as":"vikasa.exdot.share.research.>"}]}`),
			"from"},
		// Finding 1: a non-token consumer would flow unescaped into the DMZ
		// user label and the defaulted `as` subject.
		{"share consumer not a token",
			topoSpec(`"dot":"exdot",` + okCluster + `,` + okCentral + `,"dmz":{"cluster":"core","shares":[{"consumer":"bad consumer\"","from":"vikasa.exdot.d1.>","as":"vikasa.exdot.share.x.>"}]}`),
			"consumer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadInline(tc.spec)
			if tc.errContains == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.errContains)
			}
			if !strings.Contains(err.Error(), tc.errContains) {
				t.Fatalf("want error containing %q, got: %v", tc.errContains, err)
			}
		})
	}
}

func TestValidateDeclaredSubjectPrefix(t *testing.T) {
	spec := func(prefix string) string {
		return fmt.Sprintf(`{"vikasa-infra-topology:topology":{
		  "dot":"exdot",
		  "cluster":[{"id":"core","js-domain":"core","leaf-endpoint":"x:7422","substrate":{"type":"kubernetes","namespace":"n"}}],
		  "district":[{"id":"d7","subject-prefix":%q}],
		  "central":{"cluster":"core","replicas":3}
		}}`, prefix)
	}
	cases := []struct {
		name    string
		prefix  string
		wantErr bool
	}{
		{"well-formed space", "vikasa.exdot.metro7.>", false},
		// Without the trailing .>, boundary checks degrade to raw string
		// prefix matching (d7 would admit d70 subjects).
		{"missing .> suffix", "vikasa.exdot.d7", true},
		{"bare wildcard suffix without dot", "vikasa.exdot.d7>", true},
		{"unanchored", "other.d7.>", true},
		// Finding 1: anchored + .>-suffixed but with an injection payload in
		// the middle must still be rejected (quote-breakout into accounts.conf).
		{"anchored but injection in middle", "vikasa.exdot.d7.\" } bad.>", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadInline(spec(tc.prefix))
			if tc.wantErr && err == nil {
				t.Fatalf("prefix %q: want validation error, got nil", tc.prefix)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("prefix %q: unexpected error: %v", tc.prefix, err)
			}
		})
	}
}

func TestValidatePartitionID(t *testing.T) {
	spec := func(partID string) string {
		return fmt.Sprintf(`{"vikasa-infra-topology:topology":{
		  "dot":"exdot",
		  "cluster":[{"id":"core","js-domain":"core","leaf-endpoint":"x:7422","substrate":{"type":"kubernetes","namespace":"n"}}],
		  "district":[{"id":"d7","partition":[{"id":%q,"cluster":"core"}]}],
		  "central":{"cluster":"core","replicas":3}
		}}`, partID)
	}
	cases := []struct {
		name    string
		partID  string
		wantErr bool
	}{
		{"conventional slash form", "d7/0", false},
		{"plain token", "west", false},
		{"hyphenated", "d7-west", false},
		{"uppercase", "D7/0", true},
		// '_' must stay illegal: sanitize() maps '/' and '-' to '_', so a
		// literal underscore forges another partition's stream name.
		{"underscore", "d7_0", true},
		{"unicode", "pärt", true},
		{"leading slash", "/d7", true},
		{"space", "d7 0", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadInline(spec(tc.partID))
			if tc.wantErr && err == nil {
				t.Fatalf("partition id %q: want validation error, got nil", tc.partID)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("partition id %q: unexpected error: %v", tc.partID, err)
			}
		})
	}
}

// TestValidateDeterministicMultiViolation proves that when a spec carries more
// than one structural or referential violation, Validate/PartitionIndex/
// validatePlacement always report the lexicographically-first offending id —
// i.e. the violation-scanning loops range sorted keys rather than a raw map
// (whose Go iteration order is randomized per range call, not just per
// process). Each case runs several times so unsorted iteration would flake
// rather than silently pass by chance.
func TestValidateDeterministicMultiViolation(t *testing.T) {
	const runs = 25

	t.Run("cluster id token violation picks lexicographically-first id", func(t *testing.T) {
		spec := topoSpec(`"dot":"exdot","cluster":[
		  {"id":"Zclu","js-domain":"z","leaf-endpoint":"x:1","substrate":{"type":"kubernetes","namespace":"n"}},
		  {"id":"Aclu","js-domain":"a","leaf-endpoint":"y:1","substrate":{"type":"kubernetes","namespace":"n"}}
		],"central":{"cluster":"Zclu"}`)
		for i := 0; i < runs; i++ {
			_, err := loadInline(spec)
			if err == nil || !strings.Contains(err.Error(), `cluster "Aclu": id is not a token`) {
				t.Fatalf("run %d: want deterministic first-violation on %q, got: %v", i, "Aclu", err)
			}
		}
	})

	t.Run("district id token violation picks lexicographically-first id", func(t *testing.T) {
		spec := topoSpec(`"dot":"exdot",` + okCluster + `,"district":[{"id":"Zdist"},{"id":"Adist"}],` + okCentral)
		for i := 0; i < runs; i++ {
			_, err := loadInline(spec)
			if err == nil || !strings.Contains(err.Error(), `district "Adist": id is not a token`) {
				t.Fatalf("run %d: want deterministic first-violation on %q, got: %v", i, "Adist", err)
			}
		}
	})

	t.Run("partition id token violation within a district picks lexicographically-first id", func(t *testing.T) {
		spec := topoSpec(`"dot":"exdot",` + okCluster + `,"district":[{"id":"d7","partition":[{"id":"Zpart","cluster":"core"},{"id":"Apart","cluster":"core"}]}],` + okCentral)
		for i := 0; i < runs; i++ {
			_, err := loadInline(spec)
			if err == nil || !strings.Contains(err.Error(), `partition id "Apart"`) {
				t.Fatalf("run %d: want deterministic first-violation on %q, got: %v", i, "Apart", err)
			}
		}
	})

	t.Run("referential violation across districts picks lexicographically-first district id", func(t *testing.T) {
		spec := topoSpec(`"dot":"exdot",` + okCluster + `,"district":[{"id":"d9","partition":[{"id":"p0","cluster":"ghost"}]},{"id":"d1","partition":[{"id":"p1","cluster":"ghost"}]}],` + okCentral)
		for i := 0; i < runs; i++ {
			_, err := loadInline(spec)
			if err == nil || !strings.Contains(err.Error(), `district "d1" partition "p1": cluster "ghost" not defined`) {
				t.Fatalf("run %d: want deterministic first-violation on district %q, got: %v", i, "d1", err)
			}
		}
	})
}

func TestValidateDMZShareAs(t *testing.T) {
	cases := []struct {
		name    string
		as      string
		wantErr bool
	}{
		{"bare wildcard", ">", true},
		{"whole namespace", "vikasa.>", true},
		{"whole dot space", "vikasa.exdot.>", true},
		{"internal district space", "vikasa.exdot.d1.>", true},
		{"single internal subject", "vikasa.exdot.d1.hwy9.x", true},
		{"foreign prefix", "other.prefix.>", true},
		{"another dot's share space", "vikasa.otherdot.share.x.>", true},
		{"peer space of another dot", "vikasa.peer.otherdot.hwy9.>", true},
		{"share space", "vikasa.exdot.share.research.>", false},
		{"share space single subject", "vikasa.exdot.share.research.summary", false},
		{"peer space", "vikasa.peer.exdot.hwy9.>", false},
		// Finding 2: an `as` under the share space but NOT scoped to this
		// consumer ("research") leaks every other consumer's shares.
		{"share space root not consumer-scoped", "vikasa.exdot.share.>", true},
		{"other consumer's share space", "vikasa.exdot.share.other.>", true},
		// Finding 1: injection characters in `as` must be rejected even when
		// the mandatory share-space prefix is present.
		{"share space with quote breakout", `vikasa.exdot.share.research." } ] } ATTACKER { x`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadInline(dmzSpec(tc.as))
			if tc.wantErr && err == nil {
				t.Fatalf("as %q: want validation error, got nil", tc.as)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("as %q: unexpected error: %v", tc.as, err)
			}
			if tc.wantErr && err != nil && !strings.Contains(err.Error(), "as") {
				t.Fatalf("as %q: error should mention the as subject: %v", tc.as, err)
			}
		})
	}
}
