package render

import (
	"strings"
	"testing"

	"github.com/Vikasa2M/vikasa-infra/internal/topology"
)

func TestK8sRenderer_Observability(t *testing.T) {
	regional, err := K8sRenderer{}.RenderCluster(ClusterSlice{
		ID: "d7a", SubstrateType: topology.SubstrateKubernetes.String(), Namespace: "vikasa",
		JSDomain: "VIKASA_EXDOT_D7_D7A", LeafEndpoint: "nats-d7a.exdot.example:7422",
		DOT: "exdot", IsCentral: false, PrometheusRelease: "kube-prometheus-stack",
	})
	if err != nil {
		t.Fatalf("RenderCluster: %v", err)
	}
	sm := string(regional["servicemonitor.yaml"])
	for _, want := range []string{
		"kind: ServiceMonitor", "name: vikasa-nats-d7a", "namespace: vikasa",
		"release: kube-prometheus-stack", "app.kubernetes.io/name: nats", "port: metrics",
	} {
		if !strings.Contains(sm, want) {
			t.Errorf("servicemonitor.yaml missing %q:\n%s", want, sm)
		}
	}
	pr := string(regional["prometheusrule.yaml"])
	for _, want := range []string{"kind: PrometheusRule", "alert: NatsServerDown", `namespace="vikasa"`, "alert: NatsLeafDown"} {
		if !strings.Contains(pr, want) {
			t.Errorf("prometheusrule.yaml (regional) missing %q:\n%s", want, pr)
		}
	}
	central, err := K8sRenderer{}.RenderCluster(ClusterSlice{
		ID: "core", SubstrateType: topology.SubstrateKubernetes.String(), Namespace: "vikasa",
		DOT: "exdot", IsCentral: true, PrometheusRelease: "kube-prometheus-stack",
	})
	if err != nil {
		t.Fatalf("RenderCluster central: %v", err)
	}
	if pc := string(central["prometheusrule.yaml"]); strings.Contains(pc, "NatsLeafDown") {
		t.Errorf("central prometheusrule.yaml must NOT contain NatsLeafDown:\n%s", pc)
	}

	// Central cluster also emits the credhealth PrometheusRule, listed in kustomization.
	chRule, ok := central["credhealth-prometheusrule.yaml"]
	if !ok {
		t.Fatal("central must emit credhealth-prometheusrule.yaml")
	}
	chr := string(chRule)
	for _, want := range []string{
		"kind: PrometheusRule",
		"name: vikasa-credhealth-core",
		"alert: CredExpiringSoon",
		"alert: CredExpiryCritical",
		"alert: CredExpired",
		"alert: CredHealthStale",
		"vikasa_cred_expiry_seconds < 432000",
		"vikasa_cred_expiry_seconds < 3456000",
		"time() - vikasa_credhealth_last_scan_timestamp_seconds > 86400",
		"{{ $labels.identity }}",
		"release: kube-prometheus-stack",
	} {
		if !strings.Contains(chr, want) {
			t.Errorf("credhealth-prometheusrule.yaml missing %q:\n%s", want, chr)
		}
	}
	if kc := string(central["kustomization.yaml"]); !strings.Contains(kc, "- credhealth-prometheusrule.yaml") {
		t.Errorf("central kustomization.yaml must list credhealth-prometheusrule.yaml:\n%s", kc)
	}

	// Regional clusters get neither the rule file nor the kustomization entry.
	if _, ok := regional["credhealth-prometheusrule.yaml"]; ok {
		t.Error("regional cluster must NOT emit credhealth-prometheusrule.yaml")
	}
	if kr := string(regional["kustomization.yaml"]); strings.Contains(kr, "credhealth-prometheusrule.yaml") {
		t.Errorf("regional kustomization.yaml must NOT list credhealth-prometheusrule.yaml:\n%s", kr)
	}
}
