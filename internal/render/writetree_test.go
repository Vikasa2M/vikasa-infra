package render_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Vikasa2M/vikasa-infra/internal/render"
)

func TestMergeInto_RejectsDuplicate(t *testing.T) {
	all := map[string][]byte{"a": []byte("1")}
	if err := render.MergeInto(all, map[string][]byte{"b": []byte("2")}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if err := render.MergeInto(all, map[string][]byte{"a": []byte("x")}); err == nil {
		t.Fatal("expected duplicate-key error")
	}
}

// TestWriteTree_WritesAndPrunes pins two behaviors: (1) with prune=false
// (cmd/gen's default, and what every golden test uses) WriteTree writes
// exactly the requested files and never touches a manifest, so default runs
// stay byte-identical to the pre-WriteTree writer; (2) with prune=true a
// later run removes files that a prior WriteTree run produced but the
// current one does not, tracked via the on-disk manifest.
func TestWriteTree_WritesAndPrunes(t *testing.T) {
	dir := t.TempDir()

	// First run: prune=false. Writes both files, no manifest.
	if err := render.WriteTree(dir, map[string][]byte{"x/a.yaml": []byte("A"), "b.yaml": []byte("B")}, false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "x/a.yaml")); string(b) != "A" {
		t.Error("a.yaml not written")
	}
	if _, err := os.Stat(filepath.Join(dir, ".vikasa-manifest")); !os.IsNotExist(err) {
		t.Error("prune=false must not write a manifest")
	}

	// Second run: prune=true, still includes both files. Since no manifest
	// exists yet (the prune=false run above wrote none), this run has
	// nothing to prune against — it just establishes the first manifest,
	// which now tracks both x/a.yaml and b.yaml.
	if err := render.WriteTree(dir, map[string][]byte{"x/a.yaml": []byte("A"), "b.yaml": []byte("B1")}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "x/a.yaml")); err != nil {
		t.Error("a.yaml should still be present: no manifest existed to prune against")
	}
	if _, err := os.Stat(filepath.Join(dir, ".vikasa-manifest")); err != nil {
		t.Error("prune=true must write a manifest")
	}

	// Third run: prune=true again, this time omitting x/a.yaml. Now a
	// manifest from the previous run exists and lists x/a.yaml, so it gets
	// removed.
	if err := render.WriteTree(dir, map[string][]byte{"b.yaml": []byte("B2")}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "x/a.yaml")); !os.IsNotExist(err) {
		t.Error("a.yaml should have been pruned")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "b.yaml")); string(b) != "B2" {
		t.Error("b.yaml not updated")
	}
}
