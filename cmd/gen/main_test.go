package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Vikasa2M/vikasa-infra/internal/render"
)

const (
	goldenDir             = "testdata/golden"
	goldenDirMixed        = "testdata/golden-mixed"
	goldenDirCabinets     = "testdata/golden-cabinets"
	goldenDirHelm         = "testdata/golden-helm"
	goldenDirRebalance    = "testdata/golden-rebalance"
	goldenDirDMZ          = "testdata/golden-dmz"
	goldenDirDMZBaremetal = "testdata/golden-dmz-baremetal"
)

// baseCfg is the render.Config every golden scenario uses (helm mode adds Output).
func baseCfg() render.Config {
	return render.Config{TLSIssuer: "vikasa-ca", SecretStore: "vikasa-secrets", ArgoTargetRevision: "main", PrometheusRelease: "kube-prometheus-stack"}
}

// compareGolden byte-compares the produced tree against the golden tree in
// both directions: every produced file must match its golden, every golden
// must have been produced. With UPDATE_GOLDEN=1 it creates or rewrites the
// goldens instead of failing (review the git diff afterwards).
func compareGolden(t *testing.T, gotDir, goldenDir string) {
	t.Helper()
	update := os.Getenv("UPDATE_GOLDEN") != ""
	produced := relFiles(t, gotDir)
	golden := relFiles(t, goldenDir)

	for _, name := range produced {
		got, err := os.ReadFile(filepath.Join(gotDir, name))
		if err != nil {
			t.Fatalf("read produced %s: %v", name, err)
		}
		want, err := os.ReadFile(filepath.Join(goldenDir, name))
		if err != nil {
			if update {
				dst := filepath.Join(goldenDir, name)
				if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
					t.Fatalf("mkdir golden %s: %v", name, err)
				}
				if err := os.WriteFile(dst, got, 0o644); err != nil {
					t.Fatalf("write golden %s: %v", name, err)
				}
				t.Logf("wrote golden %s (UPDATE_GOLDEN set)", name)
				continue
			}
			t.Errorf("produced %s has no golden (run UPDATE_GOLDEN=1): %v", name, err)
			continue
		}
		if !bytes.Equal(want, got) {
			if update {
				if err := os.WriteFile(filepath.Join(goldenDir, name), got, 0o644); err != nil {
					t.Fatalf("update golden %s: %v", name, err)
				}
				t.Logf("updated golden %s (UPDATE_GOLDEN set)", name)
				continue
			}
			t.Errorf("file %s differs from golden (UPDATE_GOLDEN=1 to accept):\n%s", name, diffSummary(want, got))
		}
	}

	pset := map[string]struct{}{}
	for _, p := range produced {
		pset[p] = struct{}{}
	}
	for _, g := range golden {
		if _, ok := pset[g]; !ok {
			t.Errorf("golden %s not produced by run()", g)
		}
	}
}

// diffSummary reports the first differing line with three lines of context
// from each side — enough to locate the change without dumping whole files.
func diffSummary(want, got []byte) string {
	wl := strings.Split(string(want), "\n")
	gl := strings.Split(string(got), "\n")
	i := 0
	for i < len(wl) && i < len(gl) && wl[i] == gl[i] {
		i++
	}
	window := func(lines []string) string {
		lo := max(0, i-3)
		hi := min(len(lines), i+4)
		var b strings.Builder
		for n := lo; n < hi; n++ {
			marker := "  "
			if n == i {
				marker = "> "
			}
			fmt.Fprintf(&b, "  %s%4d| %s\n", marker, n+1, lines[n])
		}
		return b.String()
	}
	return fmt.Sprintf("first difference at line %d\n--- golden ---\n%s--- got ---\n%s", i+1, window(wl), window(gl))
}

// relFiles returns all file paths under dir, relative to dir, slash-separated, sorted.
func relFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	sort.Strings(out)
	return out
}

// TestGenGolden runs the core run() logic against the default spec into a
// fresh temp directory and byte-compares every produced file against the
// committed golden files in testdata/golden/.
func TestGenGolden(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	if err := run(options{spec: "../../examples/exdot-shared.json", out: tmp, cfg: baseCfg()}); err != nil {
		t.Fatalf("run: %v", err)
	}
	compareGolden(t, tmp, goldenDir)
}

func TestGenGoldenMixed(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	if err := run(options{spec: "../../examples/exdot-mixed.json", out: tmp, cfg: baseCfg()}); err != nil {
		t.Fatalf("run: %v", err)
	}
	compareGolden(t, tmp, goldenDirMixed)
}

func TestGenGoldenCabinets(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	if err := run(options{
		spec: "../../examples/exdot-shared.json", out: tmp, cabinets: "../../examples/exdot-cabinets.json",
		cfg: baseCfg(),
	}); err != nil {
		t.Fatalf("run: %v", err)
	}

	// Semantic backstop, independent of the goldens: cabinet sources landed.
	d7a, err := os.ReadFile(filepath.Join(tmp, "clusters/d7a/streams.yaml"))
	if err != nil {
		t.Fatalf("read d7a streams.yaml: %v", err)
	}
	for _, want := range []string{
		"externalApiPrefix: $JS.exdot-d7a-cab-001.API",
		"externalApiPrefix: $JS.exdot-d7a-cab-002.API",
	} {
		if !strings.Contains(string(d7a), want) {
			t.Errorf("d7a streams.yaml missing %q\n%s", want, d7a)
		}
	}

	compareGolden(t, tmp, goldenDirCabinets)
}

// captureStderr redirects the process-wide os.Stderr for the duration of fn,
// returning what was written. Not safe to run in t.Parallel() alongside other
// tests that write to os.Stderr (none currently do outside main()'s own
// top-level error path, which run() never exercises).
func captureStderr(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	runErr := fn()
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	os.Stderr = orig
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	return string(out), runErr
}

// TestFanInWarning pins the advisory (non-fatal) per-partition fan-in guard:
// examples/exdot-cabinets.json attaches 2 cabinets to partition d7/0 and 1 to
// d7/8, so MaxPartitionFanIn reports (VIKASA_EXDOT_D7_D7_0, 2). The guard
// must warn to stderr when that count exceeds -max-partition-sources, stay
// silent at or below it, and never fail run() either way — it is advisory.
func TestFanInWarning(t *testing.T) {
	runWithFanIn := func(t *testing.T, maxFanIn int) (stderr string, err error) {
		t.Helper()
		tmp := t.TempDir()
		return captureStderr(t, func() error {
			return run(options{
				spec: "../../examples/exdot-shared.json", out: tmp, cabinets: "../../examples/exdot-cabinets.json",
				maxFanIn: maxFanIn,
				cfg:      baseCfg(),
			})
		})
	}

	t.Run("above threshold warns", func(t *testing.T) {
		stderr, err := runWithFanIn(t, 1)
		if err != nil {
			t.Fatalf("run() must succeed even when the guard fires (advisory only), got: %v", err)
		}
		if !strings.Contains(stderr, "WARNING") || !strings.Contains(stderr, "VIKASA_EXDOT_D7_D7_0") || !strings.Contains(stderr, "2 cabinet sources") {
			t.Errorf("expected fan-in WARNING naming VIKASA_EXDOT_D7_D7_0 and its count on stderr, got: %q", stderr)
		}
	})

	t.Run("at threshold silent", func(t *testing.T) {
		stderr, err := runWithFanIn(t, 2)
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if strings.Contains(stderr, "WARNING") {
			t.Errorf("expected no warning at threshold, got: %q", stderr)
		}
	})

	t.Run("below default disabled", func(t *testing.T) {
		stderr, err := runWithFanIn(t, 0)
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if strings.Contains(stderr, "WARNING") {
			t.Errorf("maxFanIn=0 must disable the guard entirely, got: %q", stderr)
		}
	})
}

func TestGenGoldenHelm(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	cfg := baseCfg()
	cfg.Output = render.OutputHelm
	if err := run(options{spec: "../../examples/exdot-shared.json", out: tmp, cfg: cfg}); err != nil {
		t.Fatalf("run: %v", err)
	}
	compareGolden(t, tmp, goldenDirHelm)
}

func TestGenGoldenHelm_RendersUnderHelm(t *testing.T) {
	t.Parallel()
	helmBin, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not installed; skipping render smoke test")
	}
	// Render every generated chart with `helm template` to prove the templates
	// parse and execute (guards against literal {{ }} in manifests breaking Helm).
	chartsRoot := filepath.Join(goldenDirHelm, "charts")
	entries, err := os.ReadDir(chartsRoot)
	if err != nil {
		t.Fatalf("read charts dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no charts found to render")
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		chart := filepath.Join(chartsRoot, e.Name())
		out, err := exec.Command(helmBin, "template", "testrel", chart).CombinedOutput()
		if err != nil {
			t.Errorf("helm template %s failed: %v\n%s", chart, err, out)
		}
	}
}

func TestGenInvalidOutput(t *testing.T) {
	t.Parallel()
	err := run(options{
		spec: "../../examples/exdot-shared.json", out: t.TempDir(),
		cfg: render.Config{Output: "tarball"},
	})
	if err == nil {
		t.Fatal("expected run() to reject unknown -output value, got nil")
	}
	if !strings.Contains(err.Error(), "output") {
		t.Errorf("error should mention output, got: %v", err)
	}
}

// TestGenInvalidExits asserts that run() returns a non-nil error when given
// the invalid orphan spec, and that the error message mentions the bad cluster
// name ("nope").
func TestGenInvalidExits(t *testing.T) {
	t.Parallel()
	err := run(options{spec: "../../examples/INVALID-orphan.json", out: t.TempDir(), cfg: baseCfg()})
	if err == nil {
		t.Fatal("expected run() to return error for invalid spec, got nil")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("expected error to mention cluster 'nope', got: %v", err)
	}
}

func TestGenGoldenRebalance(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	if err := run(options{
		spec: "../../examples/exdot-shared.json", out: tmp, previous: "../../examples/exdot-1cluster.json",
		cfg: baseCfg(),
	}); err != nil {
		t.Fatalf("run: %v", err)
	}

	// Semantic backstop, independent of the goldens.
	reb, err := os.ReadFile(filepath.Join(tmp, "REBALANCE.md"))
	if err != nil {
		t.Fatalf("read REBALANCE.md: %v", err)
	}
	for _, want := range []string{
		"VIKASA_EXDOT_D7_D7_8",
		"Phase 1 — Stand up target streams",
		"$JS.d7b.API",
		"leaf-d7a.nats.vikasa.exdot:7422` → `leaf-d7b.nats.vikasa.exdot:7422",
	} {
		if !strings.Contains(string(reb), want) {
			t.Errorf("REBALANCE.md missing %q\n%s", want, reb)
		}
	}

	compareGolden(t, tmp, goldenDirRebalance)
}

func TestGenGoldenDMZ(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	if err := run(options{
		spec: "../../examples/exdot-dmz.json", out: tmp, cabinets: "../../examples/exdot-dmz-cabinets.json",
		cfg: baseCfg(),
	}); err != nil {
		t.Fatalf("run: %v", err)
	}

	// Semantic backstops, independent of the goldens.
	dmzStreams, err := os.ReadFile(filepath.Join(tmp, "clusters/dmz/streams.yaml"))
	if err != nil {
		t.Fatalf("read dmz streams: %v", err)
	}
	if !strings.Contains(string(dmzStreams), "VIKASA_EXDOT_DMZ") {
		t.Errorf("dmz streams.yaml missing DMZ stream:\n%s", dmzStreams)
	}
	cat, err := os.ReadFile(filepath.Join(tmp, "dmz-catalog.md"))
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	for _, want := range []string{"research-aggregate", "peer-neighbor-corridor", "vikasa.exdot.share.research-aggregate.>", "vikasa.peer.exdot.hwy9.>"} {
		if !strings.Contains(string(cat), want) {
			t.Errorf("catalog missing %q", want)
		}
	}

	compareGolden(t, tmp, goldenDirDMZ)
}

// TestGenMissingInputPaths pins run()'s error paths for the optional file
// inputs: a nonexistent -cabinets or -previous path must return a non-nil
// error that names the missing file.
func TestGenMissingInputPaths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		missing string
		set     func(o *options, path string)
	}{
		{"nonexistent cabinets path", "no-such-cabinets.json", func(o *options, p string) { o.cabinets = p }},
		{"nonexistent previous path", "no-such-previous.json", func(o *options, p string) { o.previous = p }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tmp := t.TempDir()
			missing := filepath.Join(tmp, tc.missing)
			opts := options{spec: "../../examples/exdot-shared.json", out: filepath.Join(tmp, "out"), cfg: baseCfg()}
			tc.set(&opts, missing)
			err := run(opts)
			if err == nil {
				t.Fatalf("expected error for missing %s, got nil", tc.missing)
			}
			if !strings.Contains(err.Error(), missing) {
				t.Errorf("error should mention the missing file %q, got: %v", missing, err)
			}
		})
	}
}

// centralOnlySpec is a minimal valid spec: one central cluster, no districts,
// no dmz. Degenerate but accepted by topology.Load.
const centralOnlySpec = `{
  "vikasa-infra-topology:topology": {
    "dot": "exdot",
    "central": {"cluster": "core"},
    "cluster": [
      {"id": "core", "js-domain": "core", "leaf-endpoint": "leaf-core.nats.vikasa.exdot:7422",
       "substrate": {"type": "kubernetes", "context": "core-ctx", "namespace": "vikasa-nats"}}
    ]
  }
}`

// dmzEmptySharesSpec adds a dmz block with no shares to the central-only spec.
const dmzEmptySharesSpec = `{
  "vikasa-infra-topology:topology": {
    "dot": "exdot",
    "central": {"cluster": "core"},
    "cluster": [
      {"id": "core", "js-domain": "core", "leaf-endpoint": "leaf-core.nats.vikasa.exdot:7422",
       "substrate": {"type": "kubernetes", "context": "core-ctx", "namespace": "vikasa-nats"}},
      {"id": "dmz", "js-domain": "dmz", "leaf-endpoint": "leaf-dmz.nats.vikasa.exdot:7422",
       "substrate": {"type": "kubernetes", "context": "dmz-ctx", "namespace": "vikasa-nats"}}
    ],
    "dmz": {"cluster": "dmz"}
  }
}`

// writeSpec writes an inline spec into dir and returns its path.
func writeSpec(t *testing.T, dir, name, spec string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(spec), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	return path
}

// TestGenCentralOnly pins the degenerate central-only path end-to-end.
// Pinned contract: a central-only spec (no districts, hence no partitions)
// renders successfully with an empty DNS record list and no regional cluster
// directories. Central is sharded per partition (finding C1), so with zero
// partitions there is nothing to aggregate — no central stream is emitted.
func TestGenCentralOnly(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	spec := writeSpec(t, tmp, "central-only.json", centralOnlySpec)
	out := filepath.Join(tmp, "out")
	if err := run(options{spec: spec, out: out, cfg: baseCfg()}); err != nil {
		t.Fatalf("run: %v", err)
	}

	// No partitions ⇒ no central shards ⇒ no streams.yaml.
	if _, err := os.Stat(filepath.Join(out, "clusters/core/streams.yaml")); !os.IsNotExist(err) {
		t.Errorf("central-only spec must not emit a central streams.yaml (nothing to aggregate); stat err=%v", err)
	}

	dns, err := os.ReadFile(filepath.Join(out, "leaf-dns.yaml"))
	if err != nil {
		t.Fatalf("read leaf-dns.yaml: %v", err)
	}
	if !strings.Contains(string(dns), "records:") || strings.Contains(string(dns), "- name:") {
		t.Errorf("leaf-dns.yaml should have an empty records list:\n%s", dns)
	}

	for _, f := range relFiles(t, out) {
		if strings.HasPrefix(f, "clusters/") && !strings.HasPrefix(f, "clusters/core/") {
			t.Errorf("unexpected non-central cluster file %s", f)
		}
	}
}

// TestGenDMZEmptyShares: a dmz block with no shares is rejected at
// validation — it would provision an inert, sourceless egress stream.
func TestGenDMZEmptyShares(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	spec := writeSpec(t, tmp, "dmz-empty-shares.json", dmzEmptySharesSpec)
	err := run(options{spec: spec, out: filepath.Join(tmp, "out"), cfg: baseCfg()})
	if err == nil {
		t.Fatal("expected run() to reject a dmz block with no shares, got nil")
	}
	if !strings.Contains(err.Error(), "at least one share") {
		t.Errorf("error should say at least one share is required, got: %v", err)
	}
}

// TestGenGoldenDMZBaremetal pins the DMZ-on-bare-metal scenario: the DMZ
// egress stream's per-share subject transforms must survive into the
// bare-metal stream JSON (they are the deny-by-default boundary).
func TestGenGoldenDMZBaremetal(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	if err := run(options{spec: "../../examples/exdot-dmz-baremetal.json", out: tmp, cfg: baseCfg()}); err != nil {
		t.Fatalf("run: %v", err)
	}

	// Semantic backstop, independent of the goldens: the rendered stream JSON
	// carries one transform per share and never a bare (unfiltered) source.
	raw, err := os.ReadFile(filepath.Join(tmp, "clusters/dmz/streams/VIKASA_EXDOT_DMZ.json"))
	if err != nil {
		t.Fatalf("read dmz stream json: %v", err)
	}
	var cfg struct {
		Sources []struct {
			Name              string `json:"name"`
			FilterSubject     string `json:"filter_subject"`
			SubjectTransforms []struct {
				Src  string `json:"src"`
				Dest string `json:"dest"`
			} `json:"subject_transforms"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("dmz stream json does not parse: %v\n%s", err, raw)
	}
	// Central is sharded per partition (C1): each share fans across every central
	// shard of its district. This spec has 2 d1 partitions and 2 shares ⇒ 4 sources.
	if len(cfg.Sources) != 4 {
		t.Fatalf("want 4 DMZ sources (2 shares × 2 d1 central shards), got %d:\n%s", len(cfg.Sources), raw)
	}
	wantDest := map[string]bool{"vikasa.exdot.share.research-aggregate.>": false, "vikasa.peer.exdot.hwy9.>": false}
	for i, src := range cfg.Sources {
		if len(src.SubjectTransforms) != 1 || src.FilterSubject != "" {
			t.Errorf("source %d: every DMZ source must carry exactly one transform and no filter_subject: %+v", i, src)
			continue
		}
		wantDest[src.SubjectTransforms[0].Dest] = true
	}
	for dest, seen := range wantDest {
		if !seen {
			t.Errorf("no DMZ source transforms to %q", dest)
		}
	}

	compareGolden(t, tmp, goldenDirDMZBaremetal)
}
