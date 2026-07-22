package render

import (
	"strings"
	"testing"

	"github.com/Vikasa2M/vikasa-infra/internal/plan"
	"github.com/Vikasa2M/vikasa-infra/internal/topology"
)

func helmTestSlice() ClusterSlice {
	return ClusterSlice{
		ID:                "core",
		SubstrateType:     topology.SubstrateKubernetes.String(),
		Namespace:         "vikasa",
		IssuerName:        "vikasa-ca",
		SecretStore:       "vikasa-secrets",
		PrometheusRelease: "kube-prometheus-stack",
		LeafEndpoint:      "nats.core.example:7422",
		DOT:               "exdot",
		IsCentral:         true,
		Streams:           []plan.Stream{{Name: "VIKASA_EXDOT_CENTRAL", Tier: "central", Replicas: 5}},
	}
}

func TestRenderHelmChart_FilesAndPlaceholders(t *testing.T) {
	files, err := renderHelmChart(helmTestSlice())
	if err != nil {
		t.Fatalf("renderHelmChart: %v", err)
	}

	if _, ok := files["kustomization.yaml"]; ok {
		t.Error("chart must not contain kustomization.yaml")
	}
	for _, want := range []string{
		"Chart.yaml",
		"values.yaml",
		"templates/streams.yaml",
		"templates/certificate.yaml",
		"templates/servicemonitor.yaml",
		"templates/externalsecret.yaml",
		"templates/credhealth-prometheusrule.yaml",
	} {
		if _, ok := files[want]; !ok {
			t.Errorf("missing chart file %q", want)
		}
	}
	sm := string(files["templates/servicemonitor.yaml"])
	if !strings.Contains(sm, "{{ .Values.namespace }}") {
		t.Errorf("servicemonitor should reference {{ .Values.namespace }}, got:\n%s", sm)
	}
	if !strings.Contains(sm, "{{ .Values.prometheusRelease }}") {
		t.Errorf("servicemonitor should reference {{ .Values.prometheusRelease }}, got:\n%s", sm)
	}
	if strings.Contains(sm, "kube-prometheus-stack") {
		t.Errorf("servicemonitor should not bake the release value, got:\n%s", sm)
	}
	cert := string(files["templates/certificate.yaml"])
	if !strings.Contains(cert, "{{ .Values.tlsIssuer }}") {
		t.Errorf("certificate should reference {{ .Values.tlsIssuer }}, got:\n%s", cert)
	}
	es := string(files["templates/externalsecret.yaml"])
	if !strings.Contains(es, "{{ .Values.secretStore }}") {
		t.Errorf("externalsecret should reference {{ .Values.secretStore }}, got:\n%s", es)
	}
	vals := string(files["values.yaml"])
	for _, want := range []string{"namespace: vikasa", "tlsIssuer: vikasa-ca", "secretStore: vikasa-secrets", "prometheusRelease: kube-prometheus-stack"} {
		if !strings.Contains(vals, want) {
			t.Errorf("values.yaml missing default %q, got:\n%s", want, vals)
		}
	}
	if !strings.Contains(string(files["Chart.yaml"]), "name: vikasa-core") {
		t.Errorf("Chart.yaml should name chart vikasa-core, got:\n%s", files["Chart.yaml"])
	}
	if strings.Contains(string(files["templates/streams.yaml"]), "{{ .Values") {
		t.Errorf("streams.yaml should carry no .Values placeholders, got:\n%s", files["templates/streams.yaml"])
	}
}

func TestRenderHelmChart_EscapesLiteralBraces(t *testing.T) {
	files, err := renderHelmChart(helmTestSlice())
	if err != nil {
		t.Fatalf("renderHelmChart: %v", err)
	}
	ch, ok := files["templates/credhealth-prometheusrule.yaml"]
	if !ok {
		t.Fatal("central chart should emit templates/credhealth-prometheusrule.yaml")
	}
	s := string(ch)
	// The raw Prometheus expression must NOT survive as a live Helm action.
	if strings.Contains(s, "{{ $labels") {
		t.Errorf("credhealth template contains raw {{ $labels ... }} which Helm would try to evaluate:\n%s", s)
	}
	// It must be escaped to a Helm string-literal action that emits the braces literally.
	if !strings.Contains(s, `{{ "{{" }}`) {
		t.Errorf("credhealth template should escape literal braces via {{ \"{{\" }}, got:\n%s", s)
	}
	// Our knob placeholders must still be live Helm actions.
	if !strings.Contains(s, "{{ .Values.namespace }}") || !strings.Contains(s, "{{ .Values.prometheusRelease }}") {
		t.Errorf("credhealth template lost its knob placeholders:\n%s", s)
	}
}
