// Package main is the vikasa-infra code generator CLI.
// It loads a topology spec, builds the substrate-independent IR, and renders
// all output files (K8s stream CRs, DNS records, deployment runbook) into the
// specified output directory.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Vikasa2M/vikasa-infra/internal/accounts"
	"github.com/Vikasa2M/vikasa-infra/internal/fleet"
	"github.com/Vikasa2M/vikasa-infra/internal/plan"
	"github.com/Vikasa2M/vikasa-infra/internal/render"
	"github.com/Vikasa2M/vikasa-infra/internal/topology"
)

// options bundles the inputs to run(): the CLI-level IO paths plus the
// render-layer knobs (render.Config). It exists so run() takes one parameter
// instead of a long positional list.
type options struct {
	spec     string
	out      string
	cabinets string
	previous string
	prune    bool
	maxFanIn int
	cfg      render.Config
}

func main() {
	spec := flag.String("spec", "examples/exdot-shared.json", "path to topology spec (RFC 7951 JSON)")
	out := flag.String("out", "rendered", "output directory")
	// Registered for CLI back-compat; value intentionally unused (substrate is declared per-cluster in the spec).
	flag.String("substrate", "k8s", "(ignored; substrate is declared per-cluster in the spec)")
	cabinets := flag.String("cabinets", "", "optional cabinet inventory JSON (attaches cabinet sources to partition streams)")
	previous := flag.String("previous", "", "optional previous topology spec; emits REBALANCE.md diffing previous→spec")
	tlsIssuer := flag.String("tls-issuer", "vikasa-ca", "cert-manager (Cluster)Issuer name referenced by generated Certificate CRs")
	secretStore := flag.String("secret-store", "vikasa-secrets", "External Secrets (Cluster)SecretStore name referenced by generated ExternalSecret CRs")
	argoRepoURL := flag.String("argo-repo-url", "", "git repo URL for generated Argo CD Application source; empty disables Application emission")
	argoTargetRevision := flag.String("argo-target-revision", "main", "targetRevision for generated Argo CD Applications")
	prometheusRelease := flag.String("prometheus-release", "kube-prometheus-stack", "Prometheus Operator release label on generated ServiceMonitor/PrometheusRule CRs")
	output := flag.String("output", "kustomize", "packaging format: kustomize (default) or helm")
	prune := flag.Bool("prune", false, "remove previously-generated files no longer produced by the current spec")
	maxFanIn := flag.Int("max-partition-sources", 2500, "advisory: warn (stderr, non-fatal) when a regional partition stream would carry more than this many cabinet sources; default sits comfortably above the architecture doc's ~2,140/district example and below NACK-CR-size/single-leader fan-in stress")
	flag.Parse()

	opts := options{
		spec:     *spec,
		out:      *out,
		cabinets: *cabinets,
		previous: *previous,
		prune:    *prune,
		maxFanIn: *maxFanIn,
		cfg: render.Config{
			TLSIssuer:          *tlsIssuer,
			SecretStore:        *secretStore,
			ArgoRepoURL:        *argoRepoURL,
			ArgoTargetRevision: *argoTargetRevision,
			PrometheusRelease:  *prometheusRelease,
			Output:             render.Output(*output),
		},
	}
	if err := run(opts); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

// run is the core logic, separated from main so it can be tested directly.
func run(opts options) error {
	switch opts.cfg.Output {
	case "", render.OutputKustomize, render.OutputHelm:
		// ok ("" is treated as the kustomize default)
	default:
		return fmt.Errorf("invalid -output %q (want kustomize or helm)", opts.cfg.Output)
	}

	// Load and validate the topology spec.
	root, err := topology.Load(opts.spec)
	if err != nil {
		return fmt.Errorf("load spec: %w", err)
	}

	// Build the substrate-independent IR.
	p, err := plan.Build(root)
	if err != nil {
		return fmt.Errorf("build plan: %w", err)
	}

	// Optional cabinet inventory (attached to the new plan, and to the previous
	// plan below so the diff reflects topology placement, not cabinet churn).
	var inv *fleet.Inventory
	if opts.cabinets != "" {
		inv, err = fleet.Load(opts.cabinets)
		if err != nil {
			return fmt.Errorf("load cabinets: %w", err)
		}
		if err := plan.AttachCabinets(p, inv, root); err != nil {
			return fmt.Errorf("attach cabinets: %w", err)
		}
	}
	cabinetCount := 0
	if inv != nil {
		cabinetCount = len(inv.Cabinets)
	}

	// Advisory (non-fatal) fan-in guard: a mis-assigned fleet can pile
	// thousands of cabinet sources onto one partition stream (NACK CR size /
	// single-leader fan-in). Legitimate partitions can also legitimately hold
	// thousands, so this only warns — it never fails the run.
	fanStream, fanN := plan.MaxPartitionFanIn(p)
	if opts.maxFanIn > 0 && fanN > opts.maxFanIn {
		fmt.Fprintf(os.Stderr, "gen: WARNING partition %s has %d cabinet sources (> -max-partition-sources=%d); consider splitting the partition\n", fanStream, fanN, opts.maxFanIn)
	}

	// Run all renderers and merge their output maps.
	all := make(map[string][]byte)

	clusterFiles, err := render.Dispatch(p, root, opts.cfg)
	if err != nil {
		return fmt.Errorf("dispatch: %w", err)
	}
	if err := render.MergeInto(all, clusterFiles); err != nil {
		return fmt.Errorf("merge cluster output: %w", err)
	}

	dnsFiles, err := render.DNSRenderer{}.Render(p)
	if err != nil {
		return fmt.Errorf("dns renderer: %w", err)
	}
	if err := render.MergeInto(all, dnsFiles); err != nil {
		return fmt.Errorf("merge dns output: %w", err)
	}

	diagramFiles, err := render.DiagramRenderer{}.Render(p)
	if err != nil {
		return fmt.Errorf("diagram renderer: %w", err)
	}
	if err := render.MergeInto(all, diagramFiles); err != nil {
		return fmt.Errorf("merge diagram output: %w", err)
	}

	runbookFiles, err := render.RunbookRenderer{}.Render(p, root, opts.cfg)
	if err != nil {
		return fmt.Errorf("runbook renderer: %w", err)
	}
	if err := render.MergeInto(all, runbookFiles); err != nil {
		return fmt.Errorf("merge runbook output: %w", err)
	}

	// Account & ACL model (always emitted — core security config).
	acctModel, err := accounts.Build(root)
	if err != nil {
		return fmt.Errorf("build accounts: %w", err)
	}
	acctFiles, err := render.AccountsRenderer{}.Render(acctModel)
	if err != nil {
		return fmt.Errorf("accounts renderer: %w", err)
	}
	if err := render.MergeInto(all, acctFiles); err != nil {
		return fmt.Errorf("merge accounts output: %w", err)
	}

	catFiles, err := render.CatalogRenderer{}.Render(*root.Topology.Dot, root.Topology.DMZ)
	if err != nil {
		return fmt.Errorf("catalog renderer: %w", err)
	}
	if err := render.MergeInto(all, catFiles); err != nil {
		return fmt.Errorf("merge catalog output: %w", err)
	}

	// Optional rebalance runbook from diffing a previous spec against this one.
	rebalanceChanges := 0
	if opts.previous != "" {
		prevRoot, err := topology.Load(opts.previous)
		if err != nil {
			return fmt.Errorf("load previous spec: %w", err)
		}
		prevPlan, err := plan.Build(prevRoot)
		if err != nil {
			return fmt.Errorf("build previous plan: %w", err)
		}
		if inv != nil {
			if err := plan.AttachCabinets(prevPlan, inv, prevRoot); err != nil {
				return fmt.Errorf("attach cabinets to previous: %w", err)
			}
		}
		delta := plan.Diff(prevPlan, p)
		rebalanceFiles, err := render.RebalanceRenderer{}.Render(delta)
		if err != nil {
			return fmt.Errorf("rebalance renderer: %w", err)
		}
		if err := render.MergeInto(all, rebalanceFiles); err != nil {
			return fmt.Errorf("merge rebalance output: %w", err)
		}
		rebalanceChanges = delta.ChangeCount()
	}

	// Write the merged tree atomically (opts.prune off by default; when on,
	// files a prior prune=true run produced but this run does not are removed).
	if err := render.WriteTree(opts.out, all, opts.prune); err != nil {
		return fmt.Errorf("write tree: %w", err)
	}

	// Count streams and DNS records for the summary.
	var centralCount, partitionCount int
	for _, s := range p.Streams {
		switch s.Tier {
		case plan.TierCentral:
			centralCount++
		case plan.TierRegional:
			partitionCount++
		}
	}
	dnsCount := len(p.DNS)

	argoOrNone := opts.cfg.ArgoRepoURL
	if argoOrNone == "" {
		argoOrNone = "none"
	}
	fmt.Printf("gen: DOT=%s  partition-streams=%d  central-streams=%d  accounts=%d  cabinets=%d  rebalance=%d  dns-records=%d  files=%d  tls-issuer=%s  secret-store=%s  argo=%s  prom-release=%s  out=%s  max-fan-in=%d\n",
		p.DOT, partitionCount, centralCount, len(acctModel.Accounts), cabinetCount, rebalanceChanges, dnsCount, len(all), opts.cfg.TLSIssuer, opts.cfg.SecretStore, argoOrNone, opts.cfg.PrometheusRelease, opts.out, fanN)

	return nil
}
