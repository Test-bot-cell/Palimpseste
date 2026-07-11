package build

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"palimpseste/internal/data"
	"palimpseste/internal/materialize"
	"palimpseste/internal/theme"
)

// tableResolver serves data/ tables to the materializer's `table` blocks
// (§4.1), loading each declared table at most once per build — page workers
// run in parallel, so access is guarded. Only schema-declared tables resolve:
// the theme's declaration is the gate (§3.3), an undeclared or absent table
// degrades the block gracefully.
type tableResolver struct {
	siteDir string
	theme   *theme.Theme

	mu     sync.Mutex
	loaded map[string]*data.Table // nil entry = known missing/invalid
}

func newTableResolver(siteDir string, t *theme.Theme) *tableResolver {
	return &tableResolver{siteDir: siteDir, theme: t, loaded: map[string]*data.Table{}}
}

func (r *tableResolver) table(name string) *data.Table {
	if r == nil {
		return nil
	}
	schema, declared := r.theme.Data[name]
	if !declared {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, done := r.loaded[name]; done {
		return t
	}
	t, found, err := data.Load(r.siteDir, name)
	if err != nil || !found || data.Validate(t, schema) != nil {
		// A malformed table never breaks a build: the block renders empty and
		// the editor's grid (which validates on write) is where it gets fixed.
		r.loaded[name] = nil
		return nil
	}
	r.loaded[name] = t
	return t
}

// mediaVariants resolves derived renditions for srcset (§10.1) by probing
// media/derived for the §10.1 widths of a stored original. Pure disk lookup —
// the derived tree is part of the build's inputs like any other file.
func mediaVariants(siteDir string) func(string) []materialize.MediaVariant {
	return func(src string) []materialize.MediaVariant {
		const orig = "media/originals/"
		if !strings.HasPrefix(src, orig) {
			return nil
		}
		base := strings.TrimSuffix(path.Base(src), path.Ext(src))
		var out []materialize.MediaVariant
		for _, w := range []int{480, 800, 1200} {
			rel := fmt.Sprintf("media/derived/%s-%d.webp", base, w)
			if _, err := os.Stat(filepath.Join(siteDir, filepath.FromSlash(rel))); err == nil {
				out = append(out, materialize.MediaVariant{Path: rel, Width: w})
			}
		}
		return out
	}
}

// resolve adapts the resolver to materialize.Options.Tables.
func (r *tableResolver) resolve(name string) ([]string, [][]string, bool) {
	t := r.table(name)
	if t == nil {
		return nil, nil, false
	}
	return t.Header, t.Rows, true
}

// keyMaterial returns the §7 table → page dependency edges as hashable bytes:
// for each table the fragments reference, its name and current content. Folded
// into pageKey, a table edit invalidates exactly the pages that show it.
func (r *tableResolver) keyMaterial(frags []materialize.SlotFragment) []string {
	var out []string
	for _, name := range materialize.TablesReferenced(frags) {
		out = append(out, name)
		if t := r.table(name); t != nil {
			row := append([]string(nil), t.Header...)
			for _, cells := range t.Rows {
				row = append(row, cells...)
			}
			out = append(out, row...)
		}
	}
	return out
}
