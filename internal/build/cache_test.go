package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runInto builds the site at siteDir into a fresh output directory and returns
// the result plus the output tree. cacheDir may be empty to disable memoisation.
func runInto(t *testing.T, siteDir, cacheDir string) (*Result, map[string][]byte) {
	t.Helper()
	out := t.TempDir()
	res, err := Run(Options{
		SiteDir:  siteDir,
		OutDir:   out,
		Version:  "test",
		CacheDir: cacheDir,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return res, relFiles(t, out)
}

// assertSameTree fails if two output trees are not byte-identical.
func assertSameTree(t *testing.T, want, got map[string][]byte) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("tree file counts differ: %d vs %d", len(want), len(got))
	}
	for rel, wb := range want {
		gb, ok := got[rel]
		if !ok {
			t.Errorf("missing file %q", rel)
			continue
		}
		if string(gb) != string(wb) {
			t.Errorf("file %q differs between trees", rel)
		}
	}
}

// TestMemoizationIsByteTransparent is the load-bearing guarantee: turning the
// cache on can change build speed but never build output. A no-cache build, a
// cold cached build, and a warm cached build over the same inputs must all
// produce the identical tree, and the counters must show the cold build
// materialized every page while the warm build served every page from cache.
func TestMemoizationIsByteTransparent(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000")
	siteDir := t.TempDir()
	copyTree(t, "../../examples/drake", siteDir)
	cacheDir := t.TempDir()

	_, refTree := runInto(t, siteDir, "") // cache off

	cold, coldTree := runInto(t, siteDir, cacheDir) // populates the cache
	if cold.Pages != 2 || cold.Built != 2 || cold.Cached != 0 {
		t.Errorf("cold build: pages=%d built=%d cached=%d, want 2/2/0", cold.Pages, cold.Built, cold.Cached)
	}

	warm, warmTree := runInto(t, siteDir, cacheDir) // every page a hit
	if warm.Pages != 2 || warm.Built != 0 || warm.Cached != 2 {
		t.Errorf("warm build: pages=%d built=%d cached=%d, want 2/0/2", warm.Pages, warm.Built, warm.Cached)
	}

	assertSameTree(t, refTree, coldTree)
	assertSameTree(t, refTree, warmTree)
}

// TestMemoizationRebuildsOnlyChangedPage proves incrementality at page grain:
// after warming the cache, editing one page's fragment rebuilds that page and
// only that page — the untouched page is served from cache byte-for-byte.
func TestMemoizationRebuildsOnlyChangedPage(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000")
	siteDir := t.TempDir()
	copyTree(t, "../../examples/drake", siteDir)
	cacheDir := t.TempDir()

	_, first := runInto(t, siteDir, cacheDir) // warm the cache
	aboutBefore := first["about/index.html"]

	// Edit only the home page's hero fragment.
	const marker = "MEMO-EDIT-MARKER-42"
	hero := filepath.Join(siteDir, "content", "home", "hero.html")
	if err := os.WriteFile(hero, []byte("<p>"+marker+"</p>"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, second := runInto(t, siteDir, cacheDir)
	if res.Built != 1 || res.Cached != 1 {
		t.Errorf("incremental build: built=%d cached=%d, want 1 built / 1 cached", res.Built, res.Cached)
	}
	if home := string(second["index.html"]); !strings.Contains(home, marker) {
		t.Errorf("edited home page missing new content %q:\n%s", marker, home)
	}
	if string(second["about/index.html"]) != string(aboutBefore) {
		t.Errorf("untouched about page changed across an unrelated edit")
	}
}

// TestMemoizationGlobalStyleInvalidatesEveryPage proves the "_global:* → toutes"
// dependency edge (§7): a change to a theme stylesheet re-content-addresses the
// bundle, so its href changes on every page and no page may be served from the
// pre-change cache.
func TestMemoizationGlobalStyleInvalidatesEveryPage(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000")
	siteDir := t.TempDir()
	copyTree(t, "../../examples/drake", siteDir)
	cacheDir := t.TempDir()

	_, before := runInto(t, siteDir, cacheDir) // warm the cache

	base := filepath.Join(siteDir, "themes", "drake", "styles", "base.css")
	prev, err := os.ReadFile(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base, append(prev, []byte("\n.memo-probe{color:#123456}\n")...), 0o644); err != nil {
		t.Fatal(err)
	}

	res, after := runInto(t, siteDir, cacheDir)
	if res.Cached != 0 || res.Built != 2 {
		t.Errorf("after global style change: built=%d cached=%d, want 2 built / 0 cached", res.Built, res.Cached)
	}
	// Every page's stylesheet href moved, so every page's bytes changed.
	for _, rel := range []string{"index.html", "about/index.html"} {
		if string(after[rel]) == string(before[rel]) {
			t.Errorf("page %q unchanged after a global stylesheet edit", rel)
		}
	}
}

// TestMemoizationGlobalFragmentInvalidatesEveryPage is the doc's literal
// "_global:* → toutes" edge (§7): a shared fragment (here the nav both pages
// pull via "_global:nav") changes, so every page that injects it must rebuild —
// none may come from the pre-change cache.
func TestMemoizationGlobalFragmentInvalidatesEveryPage(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000")
	siteDir := t.TempDir()
	copyTree(t, "../../examples/drake", siteDir)
	cacheDir := t.TempDir()

	runInto(t, siteDir, cacheDir) // warm the cache

	nav := filepath.Join(siteDir, "content", "_global", "nav.html")
	if err := os.WriteFile(nav, []byte("<p>rebuilt nav</p>"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, after := runInto(t, siteDir, cacheDir)
	if res.Cached != 0 || res.Built != 2 {
		t.Errorf("after global fragment change: built=%d cached=%d, want 2 built / 0 cached", res.Built, res.Cached)
	}
	for _, rel := range []string{"index.html", "about/index.html"} {
		if !strings.Contains(string(after[rel]), "rebuilt nav") {
			t.Errorf("page %q did not pick up the edited global fragment", rel)
		}
	}
}
