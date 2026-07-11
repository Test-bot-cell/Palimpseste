// Package content resolves slot names to portable HTML fragments on disk.
//
// Fragments are the source of truth in Palimpseste's inverted storage: pages
// are materialized views, but the prose, navigation and media each page shows
// live here as standalone, theme-agnostic HTML under <siteDir>/content.
//
// Resolution is by convention with explicit overrides:
//
//	content/<pageID>/<slot>.html      the default location for a page's slot
//	content/_global/<name>.html       a shared fragment (header, footer, nav)
//
// A page may redirect a slot through site.json (Page.Slots). The redirect is a
// path relative to content/ (without the .html suffix); the "_global:" prefix
// is sugar for the _global directory, so "_global:mainnav" resolves to
// content/_global/mainnav.html.
package content

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Loader reads fragments rooted at a single content directory.
type Loader struct {
	root string
}

// NewLoader roots a loader at <siteDir>/content.
func NewLoader(siteDir string) *Loader {
	return &Loader{root: filepath.Join(siteDir, "content")}
}

// Root is the content directory the loader reads from.
func (l *Loader) Root() string { return l.root }

// Fragment returns the HTML for a slot on a page. A missing fragment is not an
// error: found is false and the caller (materializer, linter) decides what an
// empty slot means. overrides is the page's Slots map from site.json.
func (l *Loader) Fragment(pageID, slot string, overrides map[string]string) (html string, found bool, err error) {
	rel := ref(pageID, slot, overrides)
	path := filepath.Join(l.root, rel+".html")
	if !within(l.root, path) {
		return "", false, fmt.Errorf("fragment %q escapes content root", rel)
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read fragment %q: %w", rel, err)
	}
	return string(raw), true, nil
}

// WriteFragment stores safeHTML as the fragment backing a page's slot. It
// resolves the path through the same ref/within rules as Fragment (one source
// of truth for where a slot lives), creates parent directories, and writes
// atomically so a concurrent reader never sees a half-written fragment.
//
// safeHTML must already satisfy the content contract — WriteFragment is the
// storage half of the round-trip, not the sanitiser. Callers pass the output of
// internal/sanitize.Fragment; the bytes are stored verbatim so a subsequent
// Fragment read returns the identical string.
func (l *Loader) WriteFragment(pageID, slot string, overrides map[string]string, safeHTML string) (string, error) {
	rel := ref(pageID, slot, overrides)
	path := filepath.Join(l.root, rel+".html")
	if !within(l.root, path) {
		return "", fmt.Errorf("fragment %q escapes content root", rel)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create fragment dir for %q: %w", rel, err)
	}
	if err := atomicWrite(path, []byte(safeHTML)); err != nil {
		return "", fmt.Errorf("write fragment %q: %w", rel, err)
	}
	return path, nil
}

// atomicWrite writes data to a sibling temp file and renames it over path.
// rename(2) within a directory is atomic, so readers see either the old or the
// new fragment, never a partial one.
func atomicWrite(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pal-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // harmless no-op once the rename succeeds
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// ref computes the content-relative path (without .html) for a slot, applying
// an explicit override when present and expanding the "_global:" prefix.
func ref(pageID, slot string, overrides map[string]string) string {
	if r, ok := overrides[slot]; ok && r != "" {
		if rest, isGlobal := strings.CutPrefix(r, "_global:"); isGlobal {
			return filepath.Join("_global", filepath.FromSlash(rest))
		}
		return filepath.FromSlash(r)
	}
	return filepath.Join(pageID, slot)
}

// within reports whether path stays inside root once cleaned, guarding against
// "../" traversal in a page id, slot name or override.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
