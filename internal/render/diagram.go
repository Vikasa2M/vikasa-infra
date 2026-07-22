package render

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/Vikasa2M/vikasa-infra/internal/naming"
	"github.com/Vikasa2M/vikasa-infra/internal/plan"
)

// DiagramRenderer emits a single TOPOLOGY.md containing a Mermaid flowchart of
// the plan: the central stream, each cluster's regional streams, and the
// central<-regional JetStream sourcing edges. Cabinet attachments are added by
// the regional-source branch. It is regenerated from the same plan.Plan as every
// other output, so the diagram never drifts from the deployed topology.
type DiagramRenderer struct{}

// sanitizeNodeID returns a Mermaid-safe identifier: prefix + raw with every rune
// outside [A-Za-z0-9_] replaced by '_'. Stream names are already sanitized; this
// covers cluster ids and cabinet ids that may contain '-' or '.'.
func sanitizeNodeID(prefix, raw string) string {
	var b strings.Builder
	b.WriteString(prefix)
	for _, r := range raw {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// idAlloc assigns deterministic, Mermaid-safe, collision-free node ids. The
// first request for a (prefix, raw) pair gets the sanitized base id; a later
// distinct pair whose sanitized id is already taken gets a "_2", "_3", …
// suffix, in first-request order. Repeat requests for the same pair return the
// previously assigned id, so declarations and edges reference the same node.
type idAlloc struct {
	byPair   map[string]string // prefix+"\x00"+raw -> assigned id
	assigned map[string]bool   // every id handed out so far
}

func newIDAlloc() *idAlloc {
	return &idAlloc{byPair: map[string]string{}, assigned: map[string]bool{}}
}

func (a *idAlloc) id(prefix, raw string) string {
	key := prefix + "\x00" + raw
	if got, ok := a.byPair[key]; ok {
		return got
	}
	base := sanitizeNodeID(prefix, raw)
	id := base
	for n := 2; a.assigned[id]; n++ {
		id = fmt.Sprintf("%s_%d", base, n)
	}
	a.assigned[id] = true
	a.byPair[key] = id
	return id
}

// escapeLabel makes text safe inside a Mermaid "..." label by replacing the
// double-quote that would otherwise terminate the label early.
func escapeLabel(s string) string {
	return strings.ReplaceAll(s, `"`, "&quot;")
}

// Render implements the whole-plan renderer contract.
func (DiagramRenderer) Render(p *plan.Plan) (map[string][]byte, error) {
	if p == nil {
		return nil, fmt.Errorf("DiagramRenderer.Render: nil plan")
	}

	// Group streams by cluster: preserve plan order within a cluster, emit
	// clusters in sorted order for determinism.
	type group struct {
		id      string
		streams []plan.Stream
	}
	idx := map[string]int{}
	var groups []group
	for _, s := range p.Streams {
		i, ok := idx[s.Cluster]
		if !ok {
			i = len(groups)
			idx[s.Cluster] = i
			groups = append(groups, group{id: s.Cluster})
		}
		groups[i].streams = append(groups[i].streams, s)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].id < groups[j].id })

	ids := newIDAlloc()

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "# Topology Diagram: %s\n\n", p.DOT)
	buf.WriteString("> **Generated** by vikasa-infra/cmd/gen — regenerated on every run; do not edit.\n\n")
	buf.WriteString("```mermaid\n")
	buf.WriteString("flowchart TD\n")

	for _, g := range groups {
		fmt.Fprintf(&buf, "  subgraph %s[\"cluster: %s\"]\n", ids.id("cl_", g.id), escapeLabel(g.id))
		for _, s := range g.streams {
			label := fmt.Sprintf("%s<br/>js-domain: %s<br/>replicas: %d<br/>tier: %s",
				s.Name, s.JSDomain, s.Replicas, s.Tier)
			if s.MaxAge != "" {
				label += "<br/>maxAge: " + s.MaxAge
			}
			fmt.Fprintf(&buf, "    %s[\"%s\"]\n", ids.id("s_", s.Name), escapeLabel(label))
		}
		buf.WriteString("  end\n")
	}

	// Capture the central stream name once so the DMZ edge loop can reference it.
	centralName := ""
	for _, s := range p.Streams {
		if s.Tier == plan.TierCentral {
			centralName = s.Name
		}
	}

	// Edges: central sourcing (regional --> central), cabinet leaf
	// attachments (cabinet --> regional), and DMZ share (central --> dmz).
	// Iterate p.Streams (sorted) and each stream's Sources (already sorted)
	// for determinism.
	for _, s := range p.Streams {
		switch s.Tier {
		case "central":
			for _, src := range s.Sources {
				fmt.Fprintf(&buf, "  %s -->|source| %s\n",
					ids.id("s_", src.Name), ids.id("s_", s.Name))
			}
		case "regional":
			for _, src := range s.Sources {
				if src.Name != naming.BufferStreamName() {
					continue
				}
				fmt.Fprintf(&buf, "  %s([\"cabinet: %s\"]) -->|leaf| %s\n",
					ids.id("cab_", src.Domain), escapeLabel(src.Domain), ids.id("s_", s.Name))
			}
		case "dmz":
			// Emit one central→dmz share edge per dmz stream. There is at most
			// one dmz stream in a plan, so this fires at most once.
			if centralName != "" {
				fmt.Fprintf(&buf, "  %s -->|share| %s\n",
					ids.id("s_", centralName), ids.id("s_", s.Name))
			}
		}
	}

	buf.WriteString("```\n")

	return map[string][]byte{"TOPOLOGY.md": buf.Bytes()}, nil
}
