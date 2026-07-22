package render

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const manifestName = ".vikasa-manifest"

// MergeInto copies part into all, erroring on any duplicate key so two
// renderers can never silently clobber each other's output.
func MergeInto(all, part map[string][]byte) error {
	for k, v := range part {
		if _, dup := all[k]; dup {
			return fmt.Errorf("render: two renderers produced %q", k)
		}
		all[k] = v
	}
	return nil
}

// WriteTree writes files (path->bytes, paths relative to dir) atomically per
// file: a temp file is created in the destination directory, written,
// chmod'd to 0644, closed, then renamed into place — a reader never observes
// a partially-written file.
//
// The manifest (.vikasa-manifest, listing every path WriteTree wrote) is
// only read or written when prune is true. This keeps a default (prune=false)
// run byte-identical to a plain write loop with no extra output file — the
// golden tests all run with prune=false and must see no manifest. When prune
// is true, any path from a *prior* prune=true run's manifest that is absent
// from the current files map is removed (best-effort) before writing, and a
// fresh manifest reflecting this run is written afterward. A prune=true run
// with no prior manifest (e.g. the first one) has nothing to prune against.
func WriteTree(dir string, files map[string][]byte, prune bool) error {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	if prune {
		if err := pruneStale(dir, files); err != nil {
			return err
		}
	}

	for _, name := range names {
		dest := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("create dir for %s: %w", dest, err)
		}
		if err := writeFileAtomic(dest, files[name]); err != nil {
			return err
		}
	}

	if prune {
		return writeManifest(dir, names)
	}
	return nil
}

// writeFileAtomic writes data to dest via a temp file in dest's directory
// followed by a rename, so a reader never sees a partial write.
func writeFileAtomic(dest string, data []byte) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".tmp-*")
	if err != nil {
		return fmt.Errorf("temp for %s: %w", dest, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			os.Remove(tmpName)
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", dest, err)
	}
	if err = tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod %s: %w", dest, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", dest, err)
	}
	if err = os.Rename(tmpName, dest); err != nil {
		return fmt.Errorf("rename %s: %w", dest, err)
	}
	return nil
}

func writeManifest(dir string, names []string) error {
	return os.WriteFile(filepath.Join(dir, manifestName), []byte(strings.Join(names, "\n")+"\n"), 0o644)
}

// pruneStale removes files listed in dir's prior manifest (written by an
// earlier prune=true WriteTree run) that are absent from the current files
// map. If no manifest is present — including every run following a
// prune=false WriteTree, which never writes one — there is nothing tracked
// to prune against, so this is a no-op.
func pruneStale(dir string, files map[string][]byte) error {
	prev, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return nil // no prior manifest: nothing to prune
	}
	for _, name := range strings.Split(strings.TrimSpace(string(prev)), "\n") {
		if name == "" {
			continue
		}
		if _, keep := files[name]; keep {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name)) // best-effort; empty dirs left in place
	}
	return nil
}
