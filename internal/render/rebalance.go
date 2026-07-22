package render

import (
	"bytes"
	_ "embed"
	"fmt"
	"text/template"

	"github.com/Vikasa2M/vikasa-infra/internal/plan"
)

//go:embed rebalance.tmpl
var rebalanceTmpl string

var rebalanceTemplate = template.Must(template.New("rebalance").Parse(rebalanceTmpl))

// RebalanceRenderer renders REBALANCE.md — the ordered §6 online rebalance steps
// for the transition a plan.Delta describes. It is a whole-delta renderer (not a
// per-cluster SubstrateRenderer), called directly by the CLI like RunbookRenderer.
type RebalanceRenderer struct{}

// Render produces the REBALANCE.md file bytes.
func (RebalanceRenderer) Render(d *plan.Delta) (map[string][]byte, error) {
	data := buildRebalanceData(d)
	var buf bytes.Buffer
	if err := rebalanceTemplate.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("rebalance render: %w", err)
	}
	return map[string][]byte{"REBALANCE.md": buf.Bytes()}, nil
}

type rebalanceData struct {
	DOT        string
	HasChanges bool
	Moved      []moveRow
	Added      []streamRow
	Removed    []streamRow
	Modified   []streamRow
	DNSChanged []dnsChangeRow
	DNSAdded   []dnsChangeRow
	DNSRemoved []dnsChangeRow
}

type moveRow struct {
	Name       string
	OldCluster string
	NewCluster string
	OldDomain  string
	NewDomain  string
}

type streamRow struct {
	Name    string
	Cluster string
}

type dnsChangeRow struct {
	Name      string
	OldTarget string
	NewTarget string
}

func buildRebalanceData(d *plan.Delta) rebalanceData {
	rd := rebalanceData{DOT: d.DOT, HasChanges: d.HasChanges()}
	hasMoves := len(d.Moved) > 0

	for _, m := range d.Moved {
		rd.Moved = append(rd.Moved, moveRow{
			Name:       m.New.Name,
			OldCluster: m.Old.Cluster,
			NewCluster: m.New.Cluster,
			OldDomain:  m.Old.JSDomain,
			NewDomain:  m.New.JSDomain,
		})
	}
	for _, s := range d.Added {
		rd.Added = append(rd.Added, streamRow{Name: s.Name, Cluster: s.Cluster})
	}
	for _, s := range d.Removed {
		rd.Removed = append(rd.Removed, streamRow{Name: s.Name, Cluster: s.Cluster})
	}
	for _, m := range d.Modified {
		// Skip the central stream's in-place change when moves exist — its source
		// changes are already covered by the dual-source (phase 2) / teardown
		// (phase 5) steps, so listing it here would double-report.
		if hasMoves && m.New.Tier == plan.TierCentral {
			continue
		}
		rd.Modified = append(rd.Modified, streamRow{Name: m.New.Name, Cluster: m.New.Cluster})
	}
	for _, c := range d.DNSChanged {
		rd.DNSChanged = append(rd.DNSChanged, dnsChangeRow{Name: c.Name, OldTarget: c.OldTarget, NewTarget: c.NewTarget})
	}
	for _, r := range d.DNSAdded {
		rd.DNSAdded = append(rd.DNSAdded, dnsChangeRow{Name: r.Name, NewTarget: r.Target})
	}
	for _, r := range d.DNSRemoved {
		rd.DNSRemoved = append(rd.DNSRemoved, dnsChangeRow{Name: r.Name, OldTarget: r.Target})
	}
	return rd
}
