package render_test

import (
	"strings"
	"testing"

	"github.com/Vikasa2M/vikasa-infra/internal/render"
	"github.com/Vikasa2M/vikasa-infra/internal/topology"
)

func dispatchFor(t *testing.T, path string) map[string][]byte {
	t.Helper()
	root, err := topology.Load(path)
	if err != nil {
		t.Fatalf("topology.Load(%q): %v", path, err)
	}
	p := loadPlan(t, path)
	files, err := render.Dispatch(p, root, render.Config{TLSIssuer: "vikasa-ca", SecretStore: "vikasa-secrets", PrometheusRelease: "kube-prometheus-stack"})
	if err != nil {
		t.Fatalf("Dispatch(%q): %v", path, err)
	}
	return files
}

func TestDispatch_ExdotShared(t *testing.T) {
	files := dispatchFor(t, "../../examples/exdot-shared.json")

	want := []string{
		"clusters/core/certificate.yaml",
		"clusters/core/credhealth-prometheusrule.yaml",
		"clusters/core/externalsecret.yaml",
		"clusters/core/kustomization.yaml",
		"clusters/core/prometheusrule.yaml",
		"clusters/core/servicemonitor.yaml",
		"clusters/core/streams.yaml",
		"clusters/d7a/certificate.yaml",
		"clusters/d7a/externalsecret.yaml",
		"clusters/d7a/kustomization.yaml",
		"clusters/d7a/prometheusrule.yaml",
		"clusters/d7a/servicemonitor.yaml",
		"clusters/d7a/streams.yaml",
		"clusters/d7b/certificate.yaml",
		"clusters/d7b/externalsecret.yaml",
		"clusters/d7b/kustomization.yaml",
		"clusters/d7b/prometheusrule.yaml",
		"clusters/d7b/servicemonitor.yaml",
		"clusters/d7b/streams.yaml",
	}
	for _, name := range want {
		if _, ok := files[name]; !ok {
			t.Errorf("Dispatch: expected file %q in output; got keys: %v", name, keysOf(files))
		}
	}
	if len(files) != len(want) {
		t.Errorf("Dispatch: expected %d files, got %d: %v", len(want), len(files), keysOf(files))
	}
	if !strings.Contains(string(files["clusters/core/streams.yaml"]), "sources:") {
		t.Errorf("core slice should contain sources")
	}
	for _, name := range want {
		checkGolden(t, name, files[name])
	}
}

func TestDispatch_Baremetal(t *testing.T) {
	files := dispatchFor(t, "../../examples/exdot-baremetal.json")

	want := []string{
		"clusters/d7a/nats-exdot-d7a-1.conf",
		"clusters/d7a/nats-server.service",
		"clusters/d7a/streams/VIKASA_EXDOT_D7_D7_0.json",
		"clusters/core/nats-exdot-core-1.conf",
		"clusters/core/streams/VIKASA_EXDOT_CENTRAL_D7_D7_0.json",
		"clusters/core/streams/VIKASA_EXDOT_CENTRAL_D7_D7_8.json",
	}
	for _, name := range want {
		if _, ok := files[name]; !ok {
			t.Errorf("expected %q in output; keys=%v", name, keysOf(files))
		}
	}
	for k := range files {
		if strings.HasSuffix(k, "streams.yaml") {
			t.Errorf("baremetal spec should not emit a k8s streams.yaml: %s", k)
		}
	}
	if !strings.Contains(string(files["clusters/d7a/nats-exdot-d7a-1.conf"]), "remotes") {
		t.Errorf("d7a conf should have a leaf remote to central")
	}
	if strings.Contains(string(files["clusters/core/nats-exdot-core-1.conf"]), "remotes") {
		t.Errorf("core (central) conf must not have a leaf remote")
	}
	for _, name := range want {
		checkGolden(t, name, files[name])
	}
}

func TestDispatch_Mixed(t *testing.T) {
	files := dispatchFor(t, "../../examples/exdot-mixed.json")

	if _, ok := files["clusters/d7a/nats-exdot-d7a-1.conf"]; !ok {
		t.Errorf("d7a should be bare-metal (nats.conf expected)")
	}
	if _, ok := files["clusters/core/streams.yaml"]; !ok {
		t.Errorf("core should be k8s (streams.yaml expected)")
	}
	checkGolden(t, "clusters/core/streams.yaml", files["clusters/core/streams.yaml"])
	checkGolden(t, "clusters/d7a/nats-exdot-d7a-1.conf", files["clusters/d7a/nats-exdot-d7a-1.conf"])
}

func keysOf(m map[string][]byte) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func TestDispatch_ArgoApplication(t *testing.T) {
	root, err := topology.Load("../../examples/exdot-shared.json")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	p := loadPlan(t, "../../examples/exdot-shared.json")
	out, err := render.Dispatch(p, root, render.Config{
		TLSIssuer: "vikasa-ca", SecretStore: "vikasa-secrets",
		ArgoRepoURL: "https://git.example/vikasa-deploy", ArgoTargetRevision: "main",
		PrometheusRelease: "kube-prometheus-stack",
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	app, ok := out["argocd/d7a.yaml"]
	if !ok {
		t.Fatal("argocd/d7a.yaml not emitted")
	}
	s := string(app)
	for _, want := range []string{
		"kind: Application", "name: vikasa-d7a",
		"repoURL: https://git.example/vikasa-deploy", "targetRevision: main",
		"path: clusters/d7a", "server: https://kubernetes.default.svc",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("argocd/d7a.yaml missing %q:\n%s", want, s)
		}
	}
}

func TestDispatch_ArgoApplication_HelmMode(t *testing.T) {
	root, err := topology.Load("../../examples/exdot-shared.json")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	p := loadPlan(t, "../../examples/exdot-shared.json")
	out, err := render.Dispatch(p, root, render.Config{
		TLSIssuer: "vikasa-ca", SecretStore: "vikasa-secrets",
		ArgoRepoURL: "https://git.example/vikasa-deploy", ArgoTargetRevision: "main",
		PrometheusRelease: "kube-prometheus-stack", Output: "helm",
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	app, ok := out["argocd/d7a.yaml"]
	if !ok {
		t.Fatal("argocd/d7a.yaml not emitted")
	}
	s := string(app)
	for _, want := range []string{
		"kind: Application", "name: vikasa-d7a",
		"repoURL: https://git.example/vikasa-deploy", "targetRevision: main",
		"path: charts/d7a", "server: https://kubernetes.default.svc",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("argocd/d7a.yaml missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "path: clusters/d7a") {
		t.Errorf("argocd/d7a.yaml should not contain 'path: clusters/d7a' in helm mode:\n%s", s)
	}
}

func TestDispatch_NoArgoWithoutRepoURL(t *testing.T) {
	root, err := topology.Load("../../examples/exdot-shared.json")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	p := loadPlan(t, "../../examples/exdot-shared.json")
	out, err := render.Dispatch(p, root, render.Config{TLSIssuer: "vikasa-ca", SecretStore: "vikasa-secrets", PrometheusRelease: "kube-prometheus-stack"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	for name := range out {
		if strings.HasPrefix(name, "argocd/") {
			t.Errorf("no argocd/ file expected when ArgoRepoURL is empty, got %s", name)
		}
	}
}

func TestDispatch_HelmMode(t *testing.T) {
	root, err := topology.Load("../../examples/exdot-shared.json")
	if err != nil {
		t.Fatalf("topology.Load: %v", err)
	}
	p := loadPlan(t, "../../examples/exdot-shared.json")
	files, err := render.Dispatch(p, root, render.Config{
		TLSIssuer: "vikasa-ca", SecretStore: "vikasa-secrets",
		PrometheusRelease: "kube-prometheus-stack", Output: "helm",
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	var sawChart, sawClustersK8s bool
	for name := range files {
		if strings.HasPrefix(name, "charts/") && strings.HasSuffix(name, "/Chart.yaml") {
			sawChart = true
		}
		if strings.HasPrefix(name, "clusters/") && strings.HasSuffix(name, "/kustomization.yaml") {
			sawClustersK8s = true
		}
	}
	if !sawChart {
		t.Error("helm mode should emit at least one charts/<id>/Chart.yaml")
	}
	if sawClustersK8s {
		t.Error("helm mode should not emit clusters/<id>/kustomization.yaml for k8s clusters")
	}
}

func TestSliceDir(t *testing.T) {
	cases := []struct {
		id    string
		isK8s bool
		out   render.Output
		want  string
	}{
		{"c1", true, render.OutputHelm, "charts/c1/"},
		{"c1", true, render.OutputKustomize, "clusters/c1/"},
		{"c1", false, render.OutputHelm, "clusters/c1/"}, // bare-metal never charts
		{"c1", false, render.OutputKustomize, "clusters/c1/"},
	}
	for _, c := range cases {
		if got := render.SliceDir(c.id, c.isK8s, c.out); got != c.want {
			t.Errorf("SliceDir(%q,%v,%v)=%q want %q", c.id, c.isK8s, c.out, got, c.want)
		}
	}
}
