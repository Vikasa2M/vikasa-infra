// Package fleet loads the cabinet inventory — the operational list of cabinets
// and the partition each sources into. It is plain JSON (not the logical
// topology spec, not YANG): cabinets are physical and onboarded incrementally.
package fleet

import (
	"encoding/json"
	"fmt"
	"os"
)

// Cabinet is one field cabinet. ID is its JetStream domain (becomes the source
// Domain). Partition is the topology partition id it sources into. Filter, when
// non-empty, is emitted verbatim as the source FilterSubject.
type Cabinet struct {
	ID        string `json:"id"`
	Partition string `json:"partition"`
	Filter    string `json:"filter,omitempty"`
}

// Inventory is a DOT's cabinet fleet.
type Inventory struct {
	DOT      string    `json:"dot"`
	Cabinets []Cabinet `json:"cabinets"`
}

// Load reads and structurally validates a cabinet inventory JSON file.
// Referential validation (partition existence) is the caller's job (it needs
// the topology) — see plan.AttachCabinets.
func Load(path string) (*Inventory, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("fleet.Load %s: read file: %w", path, err)
	}
	var inv Inventory
	if err := json.Unmarshal(data, &inv); err != nil {
		return nil, fmt.Errorf("fleet.Load %s: parse json: %w", path, err)
	}
	if inv.DOT == "" {
		return nil, fmt.Errorf("fleet.Load %s: dot is empty", path)
	}
	seen := make(map[string]struct{}, len(inv.Cabinets))
	for i, c := range inv.Cabinets {
		if c.ID == "" {
			return nil, fmt.Errorf("fleet.Load %s: cabinet[%d]: id is empty", path, i)
		}
		if c.Partition == "" {
			return nil, fmt.Errorf("fleet.Load %s: cabinet %q: partition is empty", path, c.ID)
		}
		if _, dup := seen[c.ID]; dup {
			return nil, fmt.Errorf("fleet.Load %s: duplicate cabinet id %q", path, c.ID)
		}
		seen[c.ID] = struct{}{}
	}
	return &inv, nil
}
