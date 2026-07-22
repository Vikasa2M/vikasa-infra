package render

import (
	"bytes"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Vikasa2M/vikasa-infra/internal/credhealth"
	"github.com/Vikasa2M/vikasa-infra/internal/topology"
)

// TestCredhealthMetricNamesNoDrift guards the contract that every vikasa_* metric
// referenced by the credhealth PrometheusRule's expr: strings (rendered from
// credhealthRuleTmpl in k8s.go) is a metric the credhealth exporter actually emits
// (credhealth.WriteMetrics). The two are independent string literals in two packages;
// rename one without the other and the alert silently references a non-existent series
// and never fires. The check is subset-only (referenced ⊆ emitted): a metric may be
// emitted without being alerted on (e.g. vikasa_cred_artifacts_total), but a rule must
// never reference a metric that is not emitted.
func TestCredhealthMetricNamesNoDrift(t *testing.T) {
	emitted := emittedMetricNames(t)
	referenced := referencedMetricNames(t)

	if len(referenced) == 0 {
		t.Fatal("no vikasa_* metrics found in the credhealth PrometheusRule — extraction broke")
	}
	for name := range referenced {
		if !emitted[name] {
			t.Errorf("credhealth PrometheusRule references %q, which the exporter does not emit (metric-name drift)\n  emitted:    %v\n  referenced: %v",
				name, sortedKeys(emitted), sortedKeys(referenced))
		}
	}
}

// emittedMetricNames runs the real exporter on an empty report and collects the metric
// names from its `# TYPE vikasa_<name> ...` lines (written unconditionally).
func emittedMetricNames(t *testing.T) map[string]bool {
	t.Helper()
	var buf bytes.Buffer
	if err := credhealth.WriteMetrics(&buf, &credhealth.Report{}, time.Unix(0, 0)); err != nil {
		t.Fatalf("WriteMetrics: %v", err)
	}
	out := map[string]bool{}
	for line := range strings.SplitSeq(buf.String(), "\n") {
		if rest, ok := strings.CutPrefix(line, "# TYPE "); ok {
			out[strings.Fields(rest)[0]] = true
		}
	}
	return out
}

var vikasaMetricRe = regexp.MustCompile(`vikasa_[a-z0-9_]+`)

// referencedMetricNames renders the central cluster's credhealth PrometheusRule and
// extracts every vikasa_* token from its expr: lines.
func referencedMetricNames(t *testing.T) map[string]bool {
	t.Helper()
	out, err := K8sRenderer{}.RenderCluster(ClusterSlice{
		ID: "core", SubstrateType: topology.SubstrateKubernetes.String(), Namespace: "vikasa",
		DOT: "exdot", IsCentral: true, PrometheusRelease: "kube-prometheus-stack",
	})
	if err != nil {
		t.Fatalf("RenderCluster central: %v", err)
	}
	rule, ok := out["credhealth-prometheusrule.yaml"]
	if !ok {
		t.Fatal("central cluster did not emit credhealth-prometheusrule.yaml")
	}
	names := map[string]bool{}
	for line := range strings.SplitSeq(string(rule), "\n") {
		if !strings.Contains(line, "expr:") {
			continue
		}
		for _, m := range vikasaMetricRe.FindAllString(line, -1) {
			names[m] = true
		}
	}
	return names
}

func sortedKeys(m map[string]bool) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
