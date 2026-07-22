package render

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/Vikasa2M/vikasa-infra/internal/topology"
)

// CatalogRenderer emits the consumer-facing DMZ share catalog (the contract an
// external consumer reads): the public subjects they may subscribe to. Generated
// from the same spec as everything else, so it cannot drift. Subject collapse is
// not anonymization — the message envelope may still carry provenance; envelope
// redaction is a deferred Layer-2 concern.
type CatalogRenderer struct{}

type catalogEntry struct {
	Consumer string `json:"consumer"`
	Public   string `json:"public_subject"`
	Internal string `json:"internal_source"`
}

func (CatalogRenderer) Render(dot string, dmz *topology.DMZ) (map[string][]byte, error) {
	if dmz == nil {
		return map[string][]byte{}, nil
	}
	var entries []catalogEntry
	for _, s := range dmz.Shares {
		if s.Consumer == nil || s.As == nil || s.From == nil {
			continue
		}
		entries = append(entries, catalogEntry{Consumer: *s.Consumer, Public: *s.As, Internal: *s.From})
	}

	var md bytes.Buffer
	fmt.Fprintf(&md, "# DMZ Share Catalog: %s\n\n", dot)
	md.WriteString("> **Generated** by vikasa-infra/cmd/gen — the external-sharing contract; do not edit.\n>\n")
	md.WriteString("> Subscribe-only, subject-scoped. Payload/envelope schema is defined by `openits-models`.\n>\n")
	md.WriteString("> Note: a coarse subject is not anonymization — the CloudEvents envelope may carry provenance.\n\n")
	md.WriteString("| Consumer | Public subject (subscribe) |\n|---|---|\n")
	for _, e := range entries {
		fmt.Fprintf(&md, "| `%s` | `%s` |\n", e.Consumer, e.Public)
	}

	j, err := json.MarshalIndent(map[string]any{"dot": dot, "shares": entries}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("catalog json: %w", err)
	}
	j = append(j, '\n')

	return map[string][]byte{
		"dmz-catalog.md":   md.Bytes(),
		"dmz-catalog.json": j,
	}, nil
}
