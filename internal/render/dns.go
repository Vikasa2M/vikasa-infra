package render

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/Vikasa2M/vikasa-infra/internal/plan"
)

// DNSRenderer emits a single leaf-dns.yaml file listing all DNS records
// in the plan, sorted by name for determinism.
type DNSRenderer struct{}

// Render implements Renderer.
func (DNSRenderer) Render(p *plan.Plan) (map[string][]byte, error) {
	// Defensive copy + sort (plan already sorts, but we own our determinism).
	records := make([]plan.DNSRecord, len(p.DNS))
	copy(records, p.DNS)
	sort.Slice(records, func(i, j int) bool {
		return records[i].Name < records[j].Name
	})

	var buf bytes.Buffer
	buf.WriteString(banner)
	buf.WriteString("records:\n")
	for _, r := range records {
		line := fmt.Sprintf("  - name: %s\n    target: %s\n", r.Name, r.Target)
		buf.WriteString(line)
	}

	return map[string][]byte{
		"leaf-dns.yaml": buf.Bytes(),
	}, nil
}
