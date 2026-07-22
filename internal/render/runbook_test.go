package render_test

import (
	"strings"
	"testing"

	"github.com/Vikasa2M/vikasa-infra/internal/plan"
	"github.com/Vikasa2M/vikasa-infra/internal/render"
	"github.com/Vikasa2M/vikasa-infra/internal/topology"
)

// ptrStr returns a pointer to the given string (test helper).
func ptrStr(s string) *string { return &s }

func TestRunbookRenderer_WithDMZ(t *testing.T) {
	// Build a synthetic plan with a dmz-tier stream.
	p := &plan.Plan{
		DOT: "exdot",
		Streams: []plan.Stream{
			{
				Name:     "VIKASA_EXDOT_CENTRAL",
				Cluster:  "core",
				JSDomain: "core",
				Replicas: 5,
				Tier:     "central",
			},
			{
				Name:     "VIKASA_EXDOT_D7_D7_0",
				Cluster:  "d7a",
				JSDomain: "d7a",
				Replicas: 3,
				Tier:     "regional",
			},
			{
				Name:     "VIKASA_EXDOT_DMZ",
				Cluster:  "dmz",
				JSDomain: "dmz",
				Replicas: 3,
				Tier:     "dmz",

				Sources: []plan.Source{
					{Name: "VIKASA_EXDOT_DMZ_PARTNER_A", Domain: "core", FilterSubject: "vikasa.exdot.>"},
				},
			},
		},
		DNS: []plan.DNSRecord{
			{Name: "leaf-exdot-d7-0.nats.vikasa.exdot", Target: "leaf-d7a.nats.vikasa.exdot:7422"},
		},
	}

	// Build a synthetic topology root that includes the dmz cluster.
	root := &topology.Root{
		Topology: &topology.Topology{
			Dot: ptrStr("exdot"),
			Central: &topology.Central{
				Cluster: ptrStr("core"),
			},
			Cluster: map[string]*topology.Cluster{
				"core": {
					JsDomain:     ptrStr("core"),
					LeafEndpoint: ptrStr("leaf-core.nats.vikasa.exdot:7422"),
					Substrate: &topology.Substrate{
						Type:    topology.SubstrateKubernetes,
						Context: ptrStr("exdot"),
					},
				},
				"d7a": {
					JsDomain:     ptrStr("d7a"),
					LeafEndpoint: ptrStr("leaf-d7a.nats.vikasa.exdot:7422"),
					Substrate: &topology.Substrate{
						Type:    topology.SubstrateKubernetes,
						Context: ptrStr("exdot"),
					},
				},
				"dmz": {
					JsDomain:     ptrStr("dmz"),
					LeafEndpoint: ptrStr("leaf-dmz.nats.vikasa.exdot:7422"),
					Substrate: &topology.Substrate{
						Type:    topology.SubstrateKubernetes,
						Context: ptrStr("exdot"),
					},
				},
			},
			District: map[string]*topology.District{},
			DMZ: &topology.DMZ{
				Cluster: ptrStr("dmz"),
				Shares: []*topology.Share{
					{
						Consumer: ptrStr("partner-a"),
						From:     ptrStr("vikasa.exdot.>"),
						As:       ptrStr("vikasa.exdot.share.partner-a.>"),
					},
				},
			},
		},
	}

	files, err := render.RunbookRenderer{}.Render(p, root, render.Config{
		TLSIssuer: "vikasa-ca", SecretStore: "vikasa-secrets", PrometheusRelease: "kube-prometheus-stack",
	})
	if err != nil {
		t.Fatalf("RunbookRenderer.Render: %v", err)
	}

	got, ok := files["DEPLOYMENT-GUIDE.md"]
	if !ok {
		t.Fatal("DEPLOYMENT-GUIDE.md not found in output")
	}
	md := string(got)

	// The DMZ cluster must appear in the cluster table.
	if !strings.Contains(md, "`dmz`") {
		t.Errorf("expected DMZ cluster 'dmz' in output\n%s", md)
	}

	// The guide must have an External Sharing section.
	if !strings.Contains(md, "External Sharing") {
		t.Errorf("expected 'External Sharing' section in output\n%s", md)
	}

	// The guide must reference the DMZ catalog.
	if !strings.Contains(md, "dmz-catalog.md") {
		t.Errorf("expected 'dmz-catalog.md' reference in output\n%s", md)
	}
}

func TestRunbookRenderer_ExdotShared(t *testing.T) {
	root, err := topology.Load("../../examples/exdot-shared.json")
	if err != nil {
		t.Fatalf("topology.Load: %v", err)
	}
	p, err := plan.Build(root)
	if err != nil {
		t.Fatalf("plan.Build: %v", err)
	}

	files, err := render.RunbookRenderer{}.Render(p, root, render.Config{TLSIssuer: "vikasa-ca", SecretStore: "vikasa-secrets", PrometheusRelease: "kube-prometheus-stack"})
	if err != nil {
		t.Fatalf("RunbookRenderer.Render: %v", err)
	}

	got, ok := files["DEPLOYMENT-GUIDE.md"]
	if !ok {
		t.Fatal("DEPLOYMENT-GUIDE.md not found in output")
	}
	md := string(got)

	// 1. DOT is present.
	if !strings.Contains(md, "exdot") {
		t.Errorf("expected DOT 'exdot' in output\n%s", md)
	}

	// 2. Deployment mode is "shared cluster" (all three clusters share context "exdot").
	if !strings.Contains(md, "shared cluster") {
		t.Errorf("expected 'shared cluster' deployment mode\n%s", md)
	}

	// 3. Partition stream names are present.
	for _, streamName := range []string{"VIKASA_EXDOT_D7_D7_0", "VIKASA_EXDOT_D7_D7_8"} {
		if !strings.Contains(md, streamName) {
			t.Errorf("expected stream %q in output\n%s", streamName, md)
		}
	}

	// 4. DNS record names and targets are present.
	wantDNS := []struct{ name, target string }{
		{"leaf-exdot-d7-0.nats.vikasa.exdot", "leaf-d7a.nats.vikasa.exdot:7422"},
		{"leaf-exdot-d7-8.nats.vikasa.exdot", "leaf-d7b.nats.vikasa.exdot:7422"},
	}
	for _, want := range wantDNS {
		if !strings.Contains(md, want.name) {
			t.Errorf("expected DNS name %q in output\n%s", want.name, md)
		}
		if !strings.Contains(md, want.target) {
			t.Errorf("expected DNS target %q in output\n%s", want.target, md)
		}
	}

	// 5. Central-before-regional ordering: the central provisioning step appears
	//    before the regional provisioning step.
	idxCentral := strings.Index(md, "Provision the **central** cluster's slice")
	idxRegional := strings.Index(md, "Provision each **regional** cluster's slice")
	if idxCentral < 0 {
		t.Errorf("expected central provisioning step in output\n%s", md)
	}
	if idxRegional < 0 {
		t.Errorf("expected regional provisioning step in output\n%s", md)
	}
	if idxCentral >= 0 && idxRegional >= 0 && idxCentral >= idxRegional {
		t.Errorf("central provisioning step must appear before regional step")
	}

	// 6. TLS/mTLS section is present and references the issuer.
	if !strings.Contains(md, "## TLS / mTLS") || !strings.Contains(md, "vikasa-ca") {
		t.Errorf("expected TLS/mTLS section referencing the issuer\n%s", md)
	}
	if !strings.Contains(md, "vikasa-secrets") {
		t.Errorf("expected SecretStore 'vikasa-secrets' in the rendered runbook\n%s", md)
	}

	// C3: Operations — HA / Retention / Scaling section.
	for _, want := range []string{
		"## Operations — HA, Retention & Scaling",
		"### High Availability",
		"### Retention",
		"### Scaling",
		"3-node R3 quorum",
		"Shard, don't split",
		"REBALANCE.md",
		"**Vertical**",
		"**Partition within the cluster**",
		"**Partition across an added cluster**",
		"**Split the district identity**",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("expected Operations section content %q in output\n%s", want, md)
		}
	}
	for _, id := range []string{"d7a", "d7b", "core"} {
		if !strings.Contains(md, "`"+id+"`") {
			t.Errorf("expected cluster %q listed in the Operations/HA section\n%s", id, md)
		}
	}

	// 7. Golden comparison (writes on first run, compares thereafter).
	checkGolden(t, "DEPLOYMENT-GUIDE.md", got)
}

func TestRunbookRenderer_HelmMode(t *testing.T) {
	// In -output helm, kubernetes slices are Helm charts under charts/<id>/
	// (no kustomization.yaml exists) while bare-metal slices stay under
	// clusters/<id>/. The guide's instructions must match what was generated.
	p := &plan.Plan{
		DOT: "exdot",
		Streams: []plan.Stream{
			{Name: "VIKASA_EXDOT_CENTRAL", Cluster: "core", JSDomain: "core", Replicas: 5, Tier: "central"},
			{Name: "VIKASA_EXDOT_D7_D7_0", Cluster: "d7a", JSDomain: "d7a", Replicas: 3, Tier: "regional"},
		},
	}
	root := &topology.Root{
		Topology: &topology.Topology{
			Dot:     ptrStr("exdot"),
			Central: &topology.Central{Cluster: ptrStr("core")},
			Cluster: map[string]*topology.Cluster{
				"core": {
					JsDomain:     ptrStr("core"),
					LeafEndpoint: ptrStr("leaf-core.nats.vikasa.exdot:7422"),
					Substrate:    &topology.Substrate{Type: topology.SubstrateKubernetes, Context: ptrStr("exdot")},
				},
				"d7a": {
					JsDomain:     ptrStr("d7a"),
					LeafEndpoint: ptrStr("leaf-d7a.nats.vikasa.exdot:7422"),
					Substrate:    &topology.Substrate{Type: topology.SubstrateBareMetal, Hosts: []string{"exdot-d7a-1"}},
				},
			},
			District: map[string]*topology.District{},
		},
	}

	files, err := render.RunbookRenderer{}.Render(p, root, render.Config{
		Output: "helm", TLSIssuer: "vikasa-ca", SecretStore: "vikasa-secrets", PrometheusRelease: "kube-prometheus-stack",
	})
	if err != nil {
		t.Fatalf("RunbookRenderer.Render: %v", err)
	}
	md := string(files["DEPLOYMENT-GUIDE.md"])

	for _, want := range []string{
		"charts/core/",           // the central slice is a chart
		"helm install",           // helm deployment narrative
		"charts/<id>/templates/", // k8s resource paths live under templates/
		"clusters/d7a/",          // bare-metal slice keeps its path
		"nats stream add --config clusters/d7a/streams/", // bare-metal install unchanged
	} {
		if !strings.Contains(md, want) {
			t.Errorf("helm-mode guide missing %q", want)
		}
	}
	for _, reject := range []string{
		"kubectl apply -k",  // kustomize-only instruction
		"Kustomize overlay", // helm mode emits no kustomization.yaml
		"clusters/core/",    // the k8s slice does not live there in helm mode
	} {
		if strings.Contains(md, reject) {
			t.Errorf("helm-mode guide must not contain %q", reject)
		}
	}
	if t.Failed() {
		t.Logf("guide:\n%s", md)
	}

	// Kustomize mode keeps the kustomize narrative.
	files, err = render.RunbookRenderer{}.Render(p, root, render.Config{
		Output: "kustomize", TLSIssuer: "vikasa-ca", SecretStore: "vikasa-secrets", PrometheusRelease: "kube-prometheus-stack",
	})
	if err != nil {
		t.Fatalf("RunbookRenderer.Render(kustomize): %v", err)
	}
	md = string(files["DEPLOYMENT-GUIDE.md"])
	for _, want := range []string{"kubectl apply -k", "clusters/core/"} {
		if !strings.Contains(md, want) {
			t.Errorf("kustomize-mode guide missing %q", want)
		}
	}
	if strings.Contains(md, "charts/") {
		t.Errorf("kustomize-mode guide must not reference charts/")
	}
}
