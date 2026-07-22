package plan

import (
	"fmt"
	"sort"

	"github.com/Vikasa2M/vikasa-infra/internal/fleet"
	"github.com/Vikasa2M/vikasa-infra/internal/naming"
	"github.com/Vikasa2M/vikasa-infra/internal/topology"
)

// AttachCabinets enriches the plan's regional partition streams with one
// VIKASA_BUFFER source per cabinet in the inventory (sourcing the cabinet's
// JetStream domain). It is additive: a plan built without this step — or with a
// nil inventory — is unchanged. Cabinet creds/JWTs are out of scope (sub-project B).
func AttachCabinets(p *Plan, inv *fleet.Inventory, root *topology.Root) error {
	if p == nil {
		return fmt.Errorf("plan.AttachCabinets: nil plan")
	}
	if inv == nil {
		return nil
	}
	if inv.DOT != p.DOT {
		return fmt.Errorf("plan.AttachCabinets: inventory dot %q does not match topology dot %q", inv.DOT, p.DOT)
	}
	if root == nil || root.Topology == nil {
		return fmt.Errorf("plan.AttachCabinets: nil topology")
	}

	partToDistrict, err := root.Topology.PartitionIndex()
	if err != nil {
		return fmt.Errorf("plan.AttachCabinets: %w", err)
	}

	// Index regional streams by name (pointers so we can mutate in place).
	streamByName := map[string]*Stream{}
	for i := range p.Streams {
		if p.Streams[i].Tier == TierRegional {
			streamByName[p.Streams[i].Name] = &p.Streams[i]
		}
	}

	touched := map[string]*Stream{}
	for _, c := range inv.Cabinets {
		distID, ok := partToDistrict[c.Partition]
		if !ok {
			return fmt.Errorf("plan.AttachCabinets: cabinet %q: partition %q not defined", c.ID, c.Partition)
		}
		name := PartitionStreamName(p.DOT, distID, c.Partition)
		s, ok := streamByName[name]
		if !ok {
			return fmt.Errorf("plan.AttachCabinets: cabinet %q: no stream %q for partition %q", c.ID, name, c.Partition)
		}
		if c.Filter != "" {
			pfx, ok := naming.FilterUnderDistrict(p.DOT, distID, root.Topology.District[distID].SubjectPrefix, c.Filter)
			if !ok {
				return fmt.Errorf("plan.AttachCabinets: cabinet %q: filter %q is outside district prefix %q", c.ID, c.Filter, pfx)
			}
		}
		s.Sources = append(s.Sources, Source{
			Name:          naming.BufferStreamName(),
			Domain:        c.ID,
			FilterSubject: c.Filter,
		})
		touched[name] = s
	}

	// Determinism: sort each touched stream's sources by Domain.
	for _, s := range touched {
		sort.Slice(s.Sources, func(i, j int) bool {
			return s.Sources[i].Domain < s.Sources[j].Domain
		})
	}
	return nil
}

// MaxPartitionFanIn returns the regional (TierRegional) stream carrying the
// most sources and that count. It is advisory tooling for cmd/gen: a
// mis-assigned fleet can pile thousands of cabinet sources onto one
// partition stream (NACK CR size / single-leader fan-in), and legitimate
// partitions can also legitimately hold thousands — this only reports the
// max, it never errors. Ties are broken by stream name for determinism.
// An empty plan (or one with no regional streams) returns ("", 0).
func MaxPartitionFanIn(p *Plan) (stream string, count int) {
	for _, s := range p.Streams {
		if s.Tier != TierRegional {
			continue
		}
		if len(s.Sources) > count || (len(s.Sources) == count && s.Name < stream) {
			stream, count = s.Name, len(s.Sources)
		}
	}
	return stream, count
}
