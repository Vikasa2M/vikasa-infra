package render

import (
	"bytes"
	_ "embed"
	"fmt"
	"sort"
	"strings"
	"text/template"

	"github.com/Vikasa2M/vikasa-infra/internal/plan"
	"github.com/Vikasa2M/vikasa-infra/internal/topology"
)

//go:embed runbook.tmpl
var runbookTmpl string

var runbookTemplate = template.Must(template.New("runbook").Parse(runbookTmpl))

// RunbookRenderer produces a human-readable Markdown deployment guide
// (DEPLOYMENT-GUIDE.md) from both the plan and the topology (which carries
// substrate detail the plan does not retain).
type RunbookRenderer struct{}

// Render produces the DEPLOYMENT-GUIDE.md file bytes.
func (RunbookRenderer) Render(p *plan.Plan, root *topology.Root, cfg Config) (map[string][]byte, error) {
	data, err := buildRunbookData(p, root, cfg)
	if err != nil {
		return nil, fmt.Errorf("runbook render: %w", err)
	}

	var buf bytes.Buffer
	if err := runbookTemplate.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("runbook render: execute template: %w", err)
	}

	return map[string][]byte{
		"DEPLOYMENT-GUIDE.md": buf.Bytes(),
	}, nil
}

// --- template data types ---

type runbookData struct {
	DOT               string
	DeploymentMode    string
	CentralCluster    string
	Clusters          []clusterRow
	BareMetalClusters []bareMetalNote
	K8sClusters       bool
	DNS               []dnsRow
	HasCabinets       bool
	CabinetCounts     []cabinetCount
	TLSIssuer         string
	SecretStore       string
	PrometheusRelease string
	HasDMZ            bool
	DMZStream         *dmzStreamData

	// Packaging mode (cfg.Output). In helm mode kubernetes slices land under
	// charts/<id>/ (templates/ for resources, no kustomization.yaml) while
	// bare-metal slices stay under clusters/<id>/ — the guide's paths and
	// deploy commands must match what was actually generated.
	Helm           bool
	CentralPath    string // slice dir of the central cluster
	DMZPath        string // slice dir of the DMZ cluster ("" without DMZ)
	K8sSliceDir    string // generic k8s slice dir pattern, e.g. "charts/<id>/"
	K8sResourceDir string // generic k8s resource dir pattern, e.g. "charts/<id>/templates/"
}

// dmzStreamData carries the minimal DMZ stream info the template needs.
type dmzStreamData struct {
	Name    string
	Cluster string
}

type cabinetCount struct {
	Stream string
	Count  int
}

type clusterRow struct {
	ID             string
	SubstrateLabel string
	JsDomain       string
	LeafEndpoint   string
	Streams        []string // stream names expected on this cluster
}

type bareMetalNote struct {
	ID    string
	Hosts string // comma-joined host list
}

type dnsRow struct {
	Name   string
	Target string
}

// buildRunbookData assembles the template data from a Plan and topology Root.
func buildRunbookData(p *plan.Plan, root *topology.Root, cfg Config) (*runbookData, error) {
	if root == nil || root.Topology == nil {
		return nil, fmt.Errorf("nil topology")
	}
	t := root.Topology

	// --- Sort cluster IDs for determinism (map iteration is random). ---
	clusterIDs := make([]string, 0, len(t.Cluster))
	for id := range t.Cluster {
		clusterIDs = append(clusterIDs, id)
	}
	sort.Strings(clusterIDs)

	// --- Determine deployment mode. ---
	// Inspect all clusters for substrate type and context.
	hasBare := false
	hasK8s := false
	k8sContexts := map[string]struct{}{}
	for _, id := range clusterIDs {
		c := t.Cluster[id]
		if c == nil || c.Substrate == nil {
			continue
		}
		switch c.Substrate.Type {
		case topology.SubstrateBareMetal:
			hasBare = true
		case topology.SubstrateKubernetes:
			hasK8s = true
			if c.Substrate.Context != nil {
				k8sContexts[*c.Substrate.Context] = struct{}{}
			}
		}
	}

	var deploymentMode string
	switch {
	case hasBare && hasK8s:
		deploymentMode = "mixed (Kubernetes + bare-metal)"
	case hasBare:
		deploymentMode = "bare-metal"
	case hasK8s && len(k8sContexts) <= 1:
		deploymentMode = "shared cluster (Kubernetes)"
	case hasK8s:
		deploymentMode = "per-cluster (Kubernetes)"
	default:
		deploymentMode = "unknown"
	}

	// --- Build per-cluster stream index (which streams live on which cluster). ---
	clusterStreams := map[string][]string{}
	for _, s := range p.Streams {
		clusterStreams[s.Cluster] = append(clusterStreams[s.Cluster], s.Name)
	}
	// The plan already sorts streams, but sort per-cluster lists defensively.
	for id := range clusterStreams {
		sort.Strings(clusterStreams[id])
	}

	// --- Build sorted cluster rows and bare-metal notes. ---
	var rows []clusterRow
	var bareMetalNotes []bareMetalNote
	k8sPresent := false

	for _, id := range clusterIDs {
		c := t.Cluster[id]
		if c == nil {
			continue
		}

		subLabel := "unknown"
		if c.Substrate != nil {
			switch c.Substrate.Type {
			case topology.SubstrateKubernetes:
				subLabel = "kubernetes"
				k8sPresent = true
			case topology.SubstrateBareMetal:
				subLabel = "bare-metal"
			}
		}

		jsDomain := ""
		if c.JsDomain != nil {
			jsDomain = *c.JsDomain
		}
		leafEP := ""
		if c.LeafEndpoint != nil {
			leafEP = *c.LeafEndpoint
		}

		rows = append(rows, clusterRow{
			ID:             id,
			SubstrateLabel: subLabel,
			JsDomain:       jsDomain,
			LeafEndpoint:   leafEP,
			Streams:        clusterStreams[id],
		})

		if c.Substrate != nil && c.Substrate.Type == topology.SubstrateBareMetal {
			hosts := strings.Join(c.Substrate.Hosts, ", ")
			bareMetalNotes = append(bareMetalNotes, bareMetalNote{
				ID:    id,
				Hosts: hosts,
			})
		}
	}

	// Bare-metal notes are already in sorted-cluster-id order (clusterIDs is sorted).

	// --- DNS rows (plan already sorts these). ---
	dnsRows := make([]dnsRow, len(p.DNS))
	for i, r := range p.DNS {
		dnsRows[i] = dnsRow{Name: r.Name, Target: r.Target}
	}

	// --- Central cluster ID. ---
	centralCluster := ""
	if t.Central != nil && t.Central.Cluster != nil {
		centralCluster = *t.Central.Cluster
	}

	var cabinetCounts []cabinetCount
	var dmzStream *dmzStreamData
	for _, s := range p.Streams {
		if s.Tier == plan.TierRegional && len(s.Sources) > 0 {
			cabinetCounts = append(cabinetCounts, cabinetCount{Stream: s.Name, Count: len(s.Sources)})
		}
		if s.Tier == plan.TierDMZ && dmzStream == nil {
			dmzStream = &dmzStreamData{Name: s.Name, Cluster: s.Cluster}
		}
	}

	helm := cfg.Output == OutputHelm
	// sliceDir delegates to SliceDir, the single owner of the packaging-path
	// decision shared with Dispatch — this keeps the runbook's paths and deploy
	// commands in sync with what was actually generated.
	sliceDir := func(id string) string {
		c := t.Cluster[id]
		isK8s := c != nil && c.Substrate != nil && c.Substrate.Type == topology.SubstrateKubernetes
		return SliceDir(id, isK8s, cfg.Output)
	}
	k8sSliceDir, k8sResourceDir := "clusters/<id>/", "clusters/<id>/"
	if helm {
		k8sSliceDir, k8sResourceDir = "charts/<id>/", "charts/<id>/templates/"
	}
	dmzPath := ""
	if dmzStream != nil {
		dmzPath = sliceDir(dmzStream.Cluster)
	}

	return &runbookData{
		DOT:               p.DOT,
		DeploymentMode:    deploymentMode,
		CentralCluster:    centralCluster,
		Clusters:          rows,
		BareMetalClusters: bareMetalNotes,
		K8sClusters:       k8sPresent,
		DNS:               dnsRows,
		HasCabinets:       len(cabinetCounts) > 0,
		CabinetCounts:     cabinetCounts,
		TLSIssuer:         cfg.TLSIssuer,
		SecretStore:       cfg.SecretStore,
		PrometheusRelease: cfg.PrometheusRelease,
		HasDMZ:            dmzStream != nil,
		DMZStream:         dmzStream,
		Helm:              helm,
		CentralPath:       sliceDir(centralCluster),
		DMZPath:           dmzPath,
		K8sSliceDir:       k8sSliceDir,
		K8sResourceDir:    k8sResourceDir,
	}, nil
}
