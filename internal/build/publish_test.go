package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// publishSite writes a minimal site for publish-layout tests (the golden site
// under testdata stays reserved for byte-exact assertions).
func publishSite(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("site.json", `{"name":"P","theme":"tt","pages":[{"id":"home","route":"/","template":"p","title":"Home"}]}`)
	mustWrite("themes/tt/theme.json", `{"name":"tt","slots":{"body":{"type":"richtext"}}}`)
	mustWrite("themes/tt/templates/p.html",
		`<!doctype html><html><head></head><body><article data-slot="body"></article></body></html>`)
	mustWrite("content/home/body.html", `<p>v1</p><figure><img src="media/pix.webp" alt="p"/></figure>`)
	mustWrite("media/pix.webp", "RIFFfakewebp")
	return dir
}

// §3/§7: no explicit OutDir → the tree lands in builds/<hash>/ and public/ is
// an atomically swapped symlink onto it; media/ ships with the build.
func TestPublishLayoutSymlinkAndMedia(t *testing.T) {
	dir := publishSite(t)
	res, err := Run(Options{SiteDir: dir, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}

	pub := filepath.Join(dir, "public")
	link, err := os.Readlink(pub)
	if err != nil {
		t.Fatalf("public is not a symlink: %v", err)
	}
	if !strings.HasPrefix(link, "builds"+string(filepath.Separator)) {
		t.Errorf("public -> %q, want a builds/<hash> target", link)
	}
	if got := filepath.Join(dir, link); got != res.OutDir {
		t.Errorf("Result.OutDir = %q, symlink resolves to %q", res.OutDir, got)
	}

	page, err := os.ReadFile(filepath.Join(pub, "index.html"))
	if err != nil {
		t.Fatalf("page unreadable through the symlink: %v", err)
	}
	if !strings.Contains(string(page), "<p>v1</p>") {
		t.Errorf("published page missing content")
	}
	if _, err := os.Stat(filepath.Join(pub, "media", "pix.webp")); err != nil {
		t.Errorf("media/ not shipped with the build: %v", err)
	}
	if _, err := os.Stat(filepath.Join(pub, "site.lock")); err != nil {
		t.Errorf("site.lock missing: %v", err)
	}
}

// Rollback story (§7): a content change re-points the symlink to a new hash dir
// and keeps the previous build; a third build prunes the oldest.
func TestPublishKeepsRollbackBuildAndPrunes(t *testing.T) {
	dir := publishSite(t)
	link := func() string {
		l, err := os.Readlink(filepath.Join(dir, "public"))
		if err != nil {
			t.Fatal(err)
		}
		return filepath.Base(l)
	}
	buildsCount := func() int {
		entries, err := os.ReadDir(filepath.Join(dir, "builds"))
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	rewrite := func(body string) {
		if err := os.WriteFile(filepath.Join(dir, "content", "home", "body.html"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := Run(Options{SiteDir: dir, Version: "test"}); err != nil {
		t.Fatal(err)
	}
	first := link()

	rewrite(`<p>v2</p>`)
	if _, err := Run(Options{SiteDir: dir, Version: "test"}); err != nil {
		t.Fatal(err)
	}
	second := link()
	if second == first {
		t.Fatal("content change did not produce a new build hash")
	}
	if buildsCount() != 2 {
		t.Errorf("builds/ holds %d entries after 2 builds, want 2 (live + rollback)", buildsCount())
	}
	if _, err := os.Stat(filepath.Join(dir, "builds", first)); err != nil {
		t.Errorf("previous build pruned too early: %v", err)
	}

	rewrite(`<p>v3</p>`)
	if _, err := Run(Options{SiteDir: dir, Version: "test"}); err != nil {
		t.Fatal(err)
	}
	if buildsCount() != 2 {
		t.Errorf("builds/ holds %d entries after 3 builds, want 2", buildsCount())
	}
	if _, err := os.Stat(filepath.Join(dir, "builds", first)); !os.IsNotExist(err) {
		t.Errorf("oldest build not pruned")
	}
}

// Same inputs → same hash dir → the symlink target is stable (idempotent
// republish, no churn).
func TestPublishIsIdempotent(t *testing.T) {
	dir := publishSite(t)
	t.Setenv("SOURCE_DATE_EPOCH", "1750000000")
	if _, err := Run(Options{SiteDir: dir, Version: "test"}); err != nil {
		t.Fatal(err)
	}
	first, _ := os.Readlink(filepath.Join(dir, "public"))
	if _, err := Run(Options{SiteDir: dir, Version: "test"}); err != nil {
		t.Fatal(err)
	}
	second, _ := os.Readlink(filepath.Join(dir, "public"))
	if first != second {
		t.Errorf("republish moved the symlink: %q -> %q", first, second)
	}
}
