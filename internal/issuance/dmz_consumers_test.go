package issuance

// White-box (package issuance) so the test can reach the unexported
// aggregateDMZConsumers helper and dmzConsumer type directly — issuance_test.go
// and trustchain_test.go are black-box (package issuance_test) and cannot see
// unexported identifiers.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nats-io/nkeys"

	"github.com/Vikasa2M/vikasa-infra/internal/accounts"
	"github.com/Vikasa2M/vikasa-infra/internal/naming"
)

func TestAggregateDMZConsumers(t *testing.T) {
	m := &accounts.Model{DOT: "exdot", Accounts: []accounts.Account{
		{Name: "CENTRAL"},
		{Name: naming.DMZAccountName(), Users: []accounts.UserTemplate{
			// consumer "research" appears in two shares → one aggregated cred
			{Label: "research", Subscribe: []string{"vikasa.exdot.share.corridor-b.>"}, PublishDeny: []string{">"}},
			{Label: "research", Subscribe: []string{"vikasa.exdot.share.corridor-a.>"}, PublishDeny: []string{">"}},
			{Label: "peer-neighbor", Subscribe: []string{"vikasa.peer.exdot.>"}, PublishDeny: []string{">"}},
		}},
	}}
	got := aggregateDMZConsumers(m)
	if len(got) != 2 {
		t.Fatalf("want 2 consumers, got %d: %+v", len(got), got)
	}
	// sorted by name: peer-neighbor, research
	if got[0].Name != "peer-neighbor" || got[1].Name != "research" {
		t.Fatalf("consumers not sorted by name: %+v", got)
	}
	// research's two share subjects unioned + sorted
	if len(got[1].SubAllow) != 2 ||
		got[1].SubAllow[0] != "vikasa.exdot.share.corridor-a.>" ||
		got[1].SubAllow[1] != "vikasa.exdot.share.corridor-b.>" {
		t.Errorf("research SubAllow not unioned+sorted: %v", got[1].SubAllow)
	}
	if len(got[1].PubDeny) != 1 || got[1].PubDeny[0] != ">" {
		t.Errorf("research PubDeny: %v", got[1].PubDeny)
	}
}

func TestAggregateDMZConsumers_NoDMZ(t *testing.T) {
	m := &accounts.Model{DOT: "exdot", Accounts: []accounts.Account{{Name: "CENTRAL"}}}
	if got := aggregateDMZConsumers(m); got != nil {
		t.Errorf("want nil for model without DMZ account, got %v", got)
	}
}

// TestMintDMZConsumer_FailsClosedOnEmptyPubDeny proves the issuance layer
// refuses to mint a subscribe-only external credential that lacks an explicit
// publish deny, rather than relying on accounts.Build's current hardcoded
// PublishDeny: [">"]. An absent publish-deny in a NATS user JWT means
// allow-all-publish, which would let the external partner publish
// account-wide.
func TestMintDMZConsumer_FailsClosedOnEmptyPubDeny(t *testing.T) {
	dir := t.TempDir()
	acctKP, err := nkeys.CreateAccount()
	if err != nil {
		t.Fatal(err)
	}
	acctPub, err := acctKP.PublicKey()
	if err != nil {
		t.Fatal(err)
	}
	c := dmzConsumer{Name: "x", SubAllow: []string{"vikasa.exdot.share.x.>"}, PubDeny: nil}
	if _, err := mintDMZConsumer(c, dir, acctPub, acctKP); err == nil || !strings.Contains(err.Error(), "no publish deny") {
		t.Fatalf("mintDMZConsumer error = %v, want error containing %q", err, "no publish deny")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "x.creds")); !os.IsNotExist(statErr) {
		t.Fatalf("expected no creds file written, stat error = %v", statErr)
	}
}
