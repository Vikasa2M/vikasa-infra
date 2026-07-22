package naming_test

import (
	"testing"

	"github.com/Vikasa2M/vikasa-infra/internal/naming"
)

func TestUnderPrefix(t *testing.T) {
	cases := []struct {
		name    string
		subject string
		prefix  string
		want    bool
	}{
		{"inside space", "vikasa.exdot.d7.001.signals", "vikasa.exdot.d7.>", true},
		{"deep inside space", "vikasa.exdot.d7.hwy9.mm42.flow", "vikasa.exdot.d7.>", true},
		{"whole subspace wildcard", "vikasa.exdot.d7.hwy9.>", "vikasa.exdot.d7.>", true},
		{"outside space", "vikasa.exdot.d8.001", "vikasa.exdot.d7.>", false},
		{"sibling token sharing a string prefix", "vikasa.exdot.d70.internal", "vikasa.exdot.d7.>", false},
		{"bare space root not matched by .>", "vikasa.exdot.d7", "vikasa.exdot.d7.>", false},
		{"full wildcard subject", ">", "vikasa.exdot.d7.>", false},
		// A prefix without the .> convention must never widen into a raw
		// string-prefix match (the d7/d70 bypass).
		{"malformed prefix exact match only", "vikasa.exdot.d7", "vikasa.exdot.d7", true},
		{"malformed prefix rejects supersets", "vikasa.exdot.d70.internal", "vikasa.exdot.d7", false},
		{"malformed prefix rejects children", "vikasa.exdot.d7.001", "vikasa.exdot.d7", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := naming.UnderPrefix(tc.subject, tc.prefix); got != tc.want {
				t.Fatalf("UnderPrefix(%q, %q) = %v, want %v", tc.subject, tc.prefix, got, tc.want)
			}
		})
	}
}

func TestSubjectSpace(t *testing.T) {
	if got := naming.SubjectSpace("exdot", "d7", nil); got != "vikasa.exdot.d7.>" {
		t.Fatalf("default space: got %q", got)
	}
	declared := "vikasa.exdot.metro7.>"
	if got := naming.SubjectSpace("exdot", "d7", &declared); got != declared {
		t.Fatalf("declared space: got %q", got)
	}
}

func TestSpaceHelpers(t *testing.T) {
	if got := naming.Anchor("exdot"); got != "vikasa.exdot." {
		t.Errorf("Anchor = %q", got)
	}
	if got := naming.ShareSpace("exdot"); got != "vikasa.exdot.share." {
		t.Errorf("ShareSpace = %q", got)
	}
	if got := naming.PeerSpace("exdot"); got != "vikasa.peer.exdot." {
		t.Errorf("PeerSpace = %q", got)
	}
	if naming.CentralAccountName() != "CENTRAL" || naming.DMZAccountName() != "DMZ" || naming.SystemAccountName() != "SYSTEM" {
		t.Error("fixed account name mismatch")
	}
}

func TestFilterUnderDistrict(t *testing.T) {
	// default space vikasa.exdot.d7.>
	space, ok := naming.FilterUnderDistrict("exdot", "d7", nil, "vikasa.exdot.d7.cab1.>")
	if space != "vikasa.exdot.d7.>" || !ok {
		t.Errorf("in-space: space=%q ok=%v", space, ok)
	}
	if _, ok := naming.FilterUnderDistrict("exdot", "d7", nil, "vikasa.exdot.d70.x.>"); ok {
		t.Error("token-boundary sibling d70 must not be under d7")
	}
	declared := "vikasa.exdot.7.>"
	if _, ok := naming.FilterUnderDistrict("exdot", "d7", &declared, "vikasa.exdot.7.a.>"); !ok {
		t.Error("declared prefix should be honored")
	}
}

func TestNameBuilders(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"Sanitize slash+hyphen", naming.Sanitize("d7/0-a"), "D7_0_A"},
		{"DistrictAccountName", naming.DistrictAccountName("d7"), "DISTRICT_D7"},
		{"DistrictAccountName sanitized", naming.DistrictAccountName("metro-7"), "DISTRICT_METRO_7"},
		{"OperatorName", naming.OperatorName("exdot"), "VIKASA_EXDOT"},
		{"PartitionStreamName", naming.PartitionStreamName("exdot", "d7", "d7/0"), "VIKASA_EXDOT_D7_D7_0"},
		{"CentralStreamName", naming.CentralStreamName("exdot"), "VIKASA_EXDOT_CENTRAL"},
		{"DMZStreamName", naming.DMZStreamName("exdot"), "VIKASA_EXDOT_DMZ"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("got %q, want %q", tc.got, tc.want)
			}
		})
	}
}

func TestCentralShardStreamName(t *testing.T) {
	got := naming.CentralShardStreamName("exdot", "d7", "d7/0")
	want := "VIKASA_EXDOT_CENTRAL_D7_D7_0"
	if got != want {
		t.Errorf("CentralShardStreamName = %q, want %q", got, want)
	}
}

func TestBufferStreamName(t *testing.T) {
	if got := naming.BufferStreamName(); got != "VIKASA_BUFFER" {
		t.Errorf("BufferStreamName() = %q, want VIKASA_BUFFER", got)
	}
}

func TestRootSpace(t *testing.T) {
	if got := naming.RootSpace(); got != "vikasa.>" {
		t.Errorf("RootSpace() = %q, want vikasa.>", got)
	}
}

func TestLeafDNSName(t *testing.T) {
	cases := []struct {
		name        string
		dot         string
		partitionID string
		want        string
	}{
		{"simple", "exdot", "d7/0", "leaf-exdot-d7-0.nats.vikasa.exdot"},
		{"multi-slash", "exdot", "a/b", "leaf-exdot-a-b.nats.vikasa.exdot"},
		{"uppercase is lowered", "EXDOT", "D7/0", "leaf-exdot-d7-0.nats.vikasa.exdot"},
		{"no slash", "exdot", "solo", "leaf-exdot-solo.nats.vikasa.exdot"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := naming.LeafDNSName(tc.dot, tc.partitionID); got != tc.want {
				t.Errorf("LeafDNSName(%q, %q) = %q, want %q", tc.dot, tc.partitionID, got, tc.want)
			}
		})
	}
}
