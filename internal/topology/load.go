package topology

import (
	"fmt"
	"os"
)

// Load reads the RFC 7951 JSON file at path, unmarshals it into a Root struct,
// validates its structure (Validate), and then performs referential validation
// to ensure every partition.cluster and central.cluster value names a cluster
// that is actually defined in the topology.
func Load(path string) (*Root, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load %s: read file: %w", path, err)
	}

	root := &Root{}
	if err := Unmarshal(data, root); err != nil {
		return nil, fmt.Errorf("load %s: unmarshal: %w", path, err)
	}

	if err := root.Validate(); err != nil {
		return nil, fmt.Errorf("load %s: schema validation: %w", path, err)
	}

	if err := validatePlacement(root); err != nil {
		return nil, fmt.Errorf("load %s: referential validation: %w", path, err)
	}

	return root, nil
}

// validatePlacement checks that every leafref to a cluster id resolves to a
// cluster that actually exists in the topology. Structural validation
// (Validate) does not resolve leafrefs, so we do it here explicitly.
func validatePlacement(root *Root) error {
	if root.Topology == nil {
		return nil
	}
	t := root.Topology

	// Build the set of defined cluster ids.
	defined := make(map[string]struct{}, len(t.Cluster))
	for id := range t.Cluster {
		defined[id] = struct{}{}
	}

	// Check every partition.cluster across all districts.
	for _, distID := range sortedKeys(t.District) {
		district := t.District[distID]
		if district == nil {
			continue
		}
		for _, partID := range sortedKeys(district.Partition) {
			partition := district.Partition[partID]
			if partition == nil || partition.Cluster == nil {
				continue
			}
			clusterRef := *partition.Cluster
			if _, ok := defined[clusterRef]; !ok {
				return fmt.Errorf("district %q partition %q: cluster %q not defined", distID, partID, clusterRef)
			}
		}
	}

	// Check central.cluster.
	if t.Central != nil && t.Central.Cluster != nil {
		clusterRef := *t.Central.Cluster
		if _, ok := defined[clusterRef]; !ok {
			return fmt.Errorf("central: cluster %q not defined", clusterRef)
		}
	}

	// Check dmz.cluster.
	if t.DMZ != nil && t.DMZ.Cluster != nil {
		if _, ok := defined[*t.DMZ.Cluster]; !ok {
			return fmt.Errorf("dmz: cluster %q not defined", *t.DMZ.Cluster)
		}
	}

	return nil
}
