// Package accounts builds the NATS account topology + ACL model (the identity
// and authorization system of record, per architecture §8) from the topology.
// It produces a substrate-independent Model; rendering to NATS config and
// minting operator-mode JWTs are separate concerns (render.AccountsRenderer and
// sub-project B2 respectively).
package accounts

import (
	"fmt"
	"sort"

	"github.com/Vikasa2M/vikasa-infra/internal/naming"
	"github.com/Vikasa2M/vikasa-infra/internal/topology"
)

// Model is the account topology for one DOT.
type Model struct {
	DOT      string
	Accounts []Account // sorted by Name
}

// Account is one NATS account (an identity/authz lane).
type Account struct {
	Name      string
	JetStream bool
	Exports   []Export       // sorted by Subject
	Imports   []Import       // sorted by (FromAccount, Subject)
	Users     []UserTemplate // permission templates; B2 instantiates real users
}

// Export is a stream export of a subject space to other accounts.
type Export struct{ Subject string }

// Import is a stream import from another account's exported subject.
type Import struct {
	FromAccount string
	Subject     string
}

// UserTemplate is a permission template (not a real credential). B2 mints
// per-entity users from it (e.g. narrowing a cabinet to its own prefix).
// Deny lists exist because NATS config semantics treat an ABSENT publish or
// subscribe key as allow-all: a subscribe-only user must carry an explicit
// publish deny or it can publish account-wide (including $JS.API.>).
type UserTemplate struct {
	Label         string
	Publish       []string
	Subscribe     []string
	PublishDeny   []string
	SubscribeDeny []string
}

// Build computes the data-path account model from the topology: one
// DISTRICT_<d> per district (exports its telemetry, carries a cabinet user
// template), one CENTRAL (imports every district), and SYSTEM.
func Build(root *topology.Root) (*Model, error) {
	if root == nil || root.Topology == nil {
		return nil, fmt.Errorf("accounts.Build: nil topology")
	}
	t := root.Topology
	if t.Dot == nil {
		return nil, fmt.Errorf("accounts.Build: topology.dot is nil")
	}
	m := &Model{DOT: *t.Dot}

	districtIDs := make([]string, 0, len(t.District))
	for id := range t.District {
		districtIDs = append(districtIDs, id)
	}
	sort.Strings(districtIDs)

	// CENTRAL imports each district's telemetry subject space.
	central := Account{Name: naming.CentralAccountName(), JetStream: true}
	for _, d := range districtIDs {
		central.Imports = append(central.Imports, Import{
			FromAccount: naming.DistrictAccountName(d),
			Subject:     subjectSpace(*t.Dot, t.District[d], d),
		})
	}
	m.Accounts = append(m.Accounts, central)

	// One DISTRICT_<d> per district.
	for _, d := range districtIDs {
		subj := subjectSpace(*t.Dot, t.District[d], d)
		m.Accounts = append(m.Accounts, Account{
			Name:      naming.DistrictAccountName(d),
			JetStream: true,
			Exports:   []Export{{Subject: subj}},
			Users: []UserTemplate{{
				Label:     "cabinet",
				Publish:   []string{subj},
				Subscribe: []string{subj},
			}},
		})
	}

	// SYSTEM ($SYS / monitoring).
	m.Accounts = append(m.Accounts, Account{
		Name:  naming.SystemAccountName(),
		Users: []UserTemplate{{Label: "system"}},
	})

	if t.DMZ != nil {
		dmz := Account{Name: naming.DMZAccountName(), JetStream: true}
		seenFrom := map[string]bool{}
		// stable order: iterate shares as declared
		for _, s := range t.DMZ.Shares {
			if s.From == nil || s.As == nil || s.Consumer == nil {
				continue
			}
			if !seenFrom[*s.From] {
				seenFrom[*s.From] = true
				// CENTRAL exports the shared subset.
				for i := range m.Accounts {
					if m.Accounts[i].Name == naming.CentralAccountName() {
						m.Accounts[i].Exports = append(m.Accounts[i].Exports, Export{Subject: *s.From})
					}
				}
				dmz.Imports = append(dmz.Imports, Import{FromAccount: naming.CentralAccountName(), Subject: *s.From})
			}
			// External consumers are subscribe-only (§7); the explicit publish
			// deny keeps them off $JS.API.> and every other account subject.
			dmz.Users = append(dmz.Users, UserTemplate{
				Label:       *s.Consumer,
				Subscribe:   []string{*s.As},
				PublishDeny: []string{">"},
			})
		}
		// determinism: sort exports/imports by subject, users by label.
		sort.Slice(dmz.Imports, func(i, j int) bool { return dmz.Imports[i].Subject < dmz.Imports[j].Subject })
		sort.Slice(dmz.Users, func(i, j int) bool { return dmz.Users[i].Label < dmz.Users[j].Label })
		m.Accounts = append(m.Accounts, dmz)
	}
	// Re-sort CENTRAL's exports for determinism (it may have gained DMZ exports).
	for i := range m.Accounts {
		if m.Accounts[i].Name == naming.CentralAccountName() {
			sort.Slice(m.Accounts[i].Exports, func(a, b int) bool {
				return m.Accounts[i].Exports[a].Subject < m.Accounts[i].Exports[b].Subject
			})
		}
	}

	sort.Slice(m.Accounts, func(i, j int) bool { return m.Accounts[i].Name < m.Accounts[j].Name })
	return m, nil
}

// subjectSpace is a district's NATS subject boundary: its declared prefix, or
// the vikasa.<dot>.<id>.> default when none is declared.
func subjectSpace(dot string, d *topology.District, districtID string) string {
	if d == nil {
		return naming.SubjectSpace(dot, districtID, nil)
	}
	return naming.SubjectSpace(dot, districtID, d.SubjectPrefix)
}
