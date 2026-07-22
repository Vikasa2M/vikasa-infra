package accounts_test

import (
	"testing"

	"github.com/Vikasa2M/vikasa-infra/internal/accounts"
	"github.com/Vikasa2M/vikasa-infra/internal/topology"
)

func buildExdot(t *testing.T) *accounts.Model {
	t.Helper()
	root, err := topology.Load("../../examples/exdot-shared.json")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	m, err := accounts.Build(root)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return m
}

func find(m *accounts.Model, name string) (accounts.Account, bool) {
	for _, a := range m.Accounts {
		if a.Name == name {
			return a, true
		}
	}
	return accounts.Account{}, false
}

func TestBuild_AccountSet(t *testing.T) {
	m := buildExdot(t)
	if m.DOT != "exdot" {
		t.Errorf("DOT = %q, want exdot", m.DOT)
	}
	var names []string
	for _, a := range m.Accounts {
		names = append(names, a.Name)
	}
	want := []string{"CENTRAL", "DISTRICT_D7", "SYSTEM"}
	if len(names) != 3 || names[0] != want[0] || names[1] != want[1] || names[2] != want[2] {
		t.Fatalf("accounts = %v, want %v", names, want)
	}
}

func TestBuild_DistrictExportsAndCabinetTemplate(t *testing.T) {
	m := buildExdot(t)
	d, ok := find(m, "DISTRICT_D7")
	if !ok {
		t.Fatal("DISTRICT_D7 not found")
	}
	if !d.JetStream {
		t.Error("DISTRICT_D7 should have JetStream enabled")
	}
	if len(d.Exports) != 1 || d.Exports[0].Subject != "vikasa.exdot.d7.>" {
		t.Errorf("DISTRICT_D7 exports = %+v, want [vikasa.exdot.d7.>]", d.Exports)
	}
	if len(d.Users) != 1 || d.Users[0].Label != "cabinet" {
		t.Fatalf("DISTRICT_D7 users = %+v, want one 'cabinet' template", d.Users)
	}
	u := d.Users[0]
	if len(u.Publish) != 1 || u.Publish[0] != "vikasa.exdot.d7.>" || len(u.Subscribe) != 1 || u.Subscribe[0] != "vikasa.exdot.d7.>" {
		t.Errorf("cabinet template perms = pub %v sub %v, want [vikasa.exdot.d7.>] both", u.Publish, u.Subscribe)
	}
}

func TestBuild_CentralImportsDistrict(t *testing.T) {
	m := buildExdot(t)
	c, ok := find(m, "CENTRAL")
	if !ok {
		t.Fatal("CENTRAL not found")
	}
	if !c.JetStream {
		t.Error("CENTRAL should have JetStream enabled")
	}
	if len(c.Imports) != 1 || c.Imports[0].FromAccount != "DISTRICT_D7" || c.Imports[0].Subject != "vikasa.exdot.d7.>" {
		t.Errorf("CENTRAL imports = %+v, want [{DISTRICT_D7 vikasa.exdot.d7.>}]", c.Imports)
	}
}

func TestBuild_NilTopology(t *testing.T) {
	if _, err := accounts.Build(nil); err == nil {
		t.Error("expected error for nil root, got nil")
	}
	if _, err := accounts.Build(&topology.Root{}); err == nil {
		t.Error("expected error for nil topology, got nil")
	}
}

func TestBuildUsesDeclaredPrefix(t *testing.T) {
	dot := "exdot"
	pfx := "vikasa.exdot.d7.custom.>"
	root := &topology.Root{Topology: &topology.Topology{
		Dot: &dot,
		District: map[string]*topology.District{
			"d7": {Id: strptr("d7"), SubjectPrefix: &pfx},
		},
	}}
	m, err := accounts.Build(root)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var found bool
	for _, a := range m.Accounts {
		if a.Name == "DISTRICT_D7" {
			for _, e := range a.Exports {
				if e.Subject == pfx {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatalf("DISTRICT_D7 did not export declared prefix %q; model=%+v", pfx, m)
	}
}

func strptr(s string) *string { return &s }

func TestBuildEmitsDMZAccount(t *testing.T) {
	dot := "exdot"
	from := "vikasa.exdot.d7.>"
	as := "vikasa.exdot.share.research.>"
	cons := "research"
	root := &topology.Root{Topology: &topology.Topology{
		Dot:      &dot,
		District: map[string]*topology.District{"d7": {Id: strptr("d7")}},
		DMZ:      &topology.DMZ{Cluster: strptr("dmz"), Shares: []*topology.Share{{Consumer: &cons, From: &from, As: &as}}},
	}}
	m, err := accounts.Build(root)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var dmz, central *accounts.Account
	for i := range m.Accounts {
		switch m.Accounts[i].Name {
		case "DMZ":
			dmz = &m.Accounts[i]
		case "CENTRAL":
			central = &m.Accounts[i]
		}
	}
	if dmz == nil {
		t.Fatalf("no DMZ account; got %+v", m.Accounts)
	}
	if !dmz.JetStream {
		t.Error("DMZ account must have JetStream enabled (it hosts VIKASA_EXDOT_DMZ stream)")
	}
	if len(dmz.Imports) != 1 || dmz.Imports[0].FromAccount != "CENTRAL" || dmz.Imports[0].Subject != from {
		t.Fatalf("DMZ imports wrong: %+v", dmz.Imports)
	}
	if len(dmz.Users) != 1 || len(dmz.Users[0].Subscribe) != 1 || dmz.Users[0].Subscribe[0] != as {
		t.Fatalf("DMZ user template wrong: %+v", dmz.Users)
	}
	// External consumers are subscribe-only (§7): without an explicit deny,
	// NATS treats an absent publish key as allow-all — including $JS.API.>.
	if len(dmz.Users[0].Publish) != 0 {
		t.Fatalf("DMZ user must have no publish allow: %+v", dmz.Users[0])
	}
	if len(dmz.Users[0].PublishDeny) != 1 || dmz.Users[0].PublishDeny[0] != ">" {
		t.Fatalf("DMZ user must carry publish deny [>]: %+v", dmz.Users[0])
	}
	var exported bool
	for _, e := range central.Exports {
		if e.Subject == from {
			exported = true
		}
	}
	if !exported {
		t.Fatalf("CENTRAL did not export %q: %+v", from, central.Exports)
	}
}
