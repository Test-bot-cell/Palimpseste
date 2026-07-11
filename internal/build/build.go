// Package build orchestrates an M0 publish: load site + theme, bundle CSS,
// materialize every page, inject SEO, emit sitemap/robots, write a site.lock
// attestation, and swap the whole tree into place atomically.
//
// Two invariants shape this package:
//
//   - Determinism. Pages iterate in sorted order, assets are content-addressed,
//     and when SOURCE_DATE_EPOCH is set every output file is stamped with it, so
//     the same inputs produce byte-identical output with identical mtimes.
//   - Atomicity. Everything is written to a staging directory and swapped in
//     with rename(2); a reader never sees a half-published site.
package build

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"

	"palimpseste/internal/content"
	"palimpseste/internal/css"
	"palimpseste/internal/lint"
	"palimpseste/internal/materialize"
	"palimpseste/internal/render"
	"palimpseste/internal/seo"
	"palimpseste/internal/site"
	"palimpseste/internal/theme"
)

// assetsDir is the output subdirectory for content-addressed assets.
const assetsDir = "assets"

// Options configure a build.
type Options struct {
	SiteDir string // directory holding site.json, content/, themes/
	// OutDir is an explicit output root, created/replaced atomically. When
	// empty, the build publishes in the site's own §3 layout instead:
	// builds/<hash>/ receives the tree and the public/ symlink swaps onto it
	// atomically — rollback is re-pointing the link (§7).
	OutDir  string
	Check   bool   // run the lint pass and collect issues
	Version string // binary version, recorded in site.lock
	// CacheDir enables content-addressed memoisation (§7). When set, a page
	// whose input tuple is unchanged since a previous build is reused from the
	// cache instead of being re-materialized. Empty disables the cache, and the
	// build always renders every page from scratch. Ignored when Check is set,
	// because the linter needs each page's live document, not just its bytes.
	CacheDir string
}

// Result summarizes a completed build.
type Result struct {
	Pages  int
	Cached int // pages served from the memoisation cache (§7)
	Built  int // pages freshly materialized this run; Built + Cached == Pages
	Assets []string
	Issues []lint.Issue
	Lock   Lock
	OutDir string // where the tree landed (public/ target in publish layout)
}

// Lock is the site.lock attestation: enough to prove what produced this output.
type Lock struct {
	Palimpseste string    `json:"palimpseste"`
	Theme       LockTheme `json:"theme"`
	ContentHash string    `json:"contentHash"`
	DataHash    string    `json:"dataHash"`
	Pages       int       `json:"pages"`
}

// LockTheme identifies the theme by name, declared version and tree hash.
type LockTheme struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Hash    string `json:"hash"`
}

// Run executes a full build and returns its result. On success OutDir holds the
// freshly materialized site; on failure OutDir is left untouched.
func Run(opts Options) (*Result, error) {
	s, err := site.Load(opts.SiteDir)
	if err != nil {
		return nil, err
	}
	t, err := theme.Load(opts.SiteDir, s.Theme)
	if err != nil {
		return nil, err
	}
	ldr := content.NewLoader(opts.SiteDir)

	bundle, err := css.BuildTheme(t)
	if err != nil {
		return nil, err
	}

	epoch, hasEpoch := sourceDateEpoch()
	routes := lint.RouteSet(s)

	// Staging lives on the same filesystem as its destination so the final
	// rename stays atomic: next to OutDir when explicit, under the site's
	// scratch directory in publish layout.
	var staging string
	if opts.OutDir == "" {
		staging = filepath.Join(opts.SiteDir, ".palimpseste", fmt.Sprintf("staging-%d", os.Getpid()))
	} else {
		staging = fmt.Sprintf("%s.staging-%d", filepath.Clean(opts.OutDir), os.Getpid())
	}
	if err := os.RemoveAll(staging); err != nil {
		return nil, err
	}
	// Clean up staging on any early return; a successful swap renames it away
	// first, making this RemoveAll a no-op.
	defer os.RemoveAll(staging)

	res := &Result{}

	styleHref := ""
	if !bundle.Empty() {
		styleHref = "/" + assetsDir + "/" + bundle.Filename
		if err := writeFile(filepath.Join(staging, assetsDir, bundle.Filename), bundle.Contents); err != nil {
			return nil, err
		}
		res.Assets = append(res.Assets, bundle.Filename)
	}

	// Materialize pages in parallel over a bounded pool (§7), each writing its
	// own output file, then merge results in sorted page order so issue
	// ordering and the returned error stay deterministic regardless of which
	// goroutine finished first.
	cache := openCache(opts.CacheDir)
	tables := newTableResolver(opts.SiteDir, t)
	pages := s.SortedPages()
	outs := make([]pageOutcome, len(pages))

	var wg sync.WaitGroup
	sem := make(chan struct{}, workerCount(len(pages)))
	for i := range pages {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			outs[i] = renderPage(t, ldr, s, pages[i], cache, tables, styleHref, staging, opts, routes)
		}(i)
	}
	wg.Wait()

	for i := range outs {
		if outs[i].err != nil {
			return nil, outs[i].err
		}
		res.Pages++
		if outs[i].cached {
			res.Cached++
		} else {
			res.Built++
		}
		res.Issues = append(res.Issues, outs[i].issues...)
	}

	if err := writeFile(filepath.Join(staging, "sitemap.xml"), sitemapXML(s, epoch, hasEpoch)); err != nil {
		return nil, err
	}
	if err := writeFile(filepath.Join(staging, "robots.txt"), robotsTXT(s)); err != nil {
		return nil, err
	}
	// Fragments reference images as media/<path> (§4); the published tree must
	// carry them. Copied verbatim — the derivation pipeline (WebP variants,
	// §10.1) arrives at M3 and will populate media/derived the same way.
	if err := copyMediaDir(filepath.Join(opts.SiteDir, "media"), filepath.Join(staging, "media")); err != nil {
		return nil, err
	}

	contentHash, err := hashTree(ldr.Root())
	if err != nil {
		return nil, err
	}
	themeHash, err := hashTree(t.Dir())
	if err != nil {
		return nil, err
	}
	dataHash, err := hashTree(filepath.Join(opts.SiteDir, "data"))
	if err != nil {
		return nil, err
	}
	res.Lock = Lock{
		Palimpseste: opts.Version,
		Theme:       LockTheme{Name: t.Name, Version: t.Version, Hash: themeHash},
		ContentHash: contentHash,
		DataHash:    dataHash,
		Pages:       res.Pages,
	}
	lockJSON, err := marshalLock(res.Lock)
	if err != nil {
		return nil, err
	}
	if err := writeFile(filepath.Join(staging, "site.lock"), lockJSON); err != nil {
		return nil, err
	}

	if hasEpoch {
		if err := stampMtimes(staging, epoch); err != nil {
			return nil, err
		}
	}
	if opts.OutDir == "" {
		out, err := publish(opts.SiteDir, staging)
		if err != nil {
			return nil, err
		}
		res.OutDir = out
	} else {
		if err := swap(staging, opts.OutDir); err != nil {
			return nil, err
		}
		res.OutDir = opts.OutDir
	}

	lint.Sort(res.Issues)
	return res, nil
}

// publish lands staging in the site's §3 layout: builds/<hash>/ named by the
// output tree's own content hash, then an atomic swap of the public/ symlink
// (§7 — rollback is re-pointing it). The previous build is kept for exactly
// that rollback; older ones are pruned.
func publish(siteDir, staging string) (string, error) {
	outHash, err := hashTree(staging)
	if err != nil {
		return "", err
	}
	short := outHash[:12]
	buildsDir := filepath.Join(siteDir, "builds")
	target := filepath.Join(buildsDir, short)

	prev := ""
	pub := filepath.Join(siteDir, "public")
	if link, err := os.Readlink(pub); err == nil {
		prev = filepath.Base(link)
	}

	if err := os.MkdirAll(buildsDir, 0o755); err != nil {
		return "", err
	}
	if err := os.RemoveAll(target); err != nil {
		return "", err
	}
	if err := os.Rename(staging, target); err != nil {
		return "", err
	}

	// Atomic symlink swap: build the link aside, rename it over public/. A
	// leftover plain directory (a pre-symlink layout) is an artefact by
	// contract ("jamais édité à la main") and is replaced.
	tmp := filepath.Join(siteDir, fmt.Sprintf(".pal-public-%d", os.Getpid()))
	_ = os.Remove(tmp)
	if err := os.Symlink(filepath.Join("builds", short), tmp); err != nil {
		return "", err
	}
	if fi, err := os.Lstat(pub); err == nil && fi.Mode()&os.ModeSymlink == 0 {
		if err := os.RemoveAll(pub); err != nil {
			_ = os.Remove(tmp)
			return "", err
		}
	}
	if err := os.Rename(tmp, pub); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}

	// Prune: keep the live build and its rollback predecessor.
	if entries, err := os.ReadDir(buildsDir); err == nil {
		for _, e := range entries {
			if name := e.Name(); name != short && name != prev {
				_ = os.RemoveAll(filepath.Join(buildsDir, name))
			}
		}
	}
	return target, nil
}

// copyMediaDir copies every regular, non-hidden file under src into dst, preserving
// the relative layout. A missing src is a no-op — sites without media/ stay
// media-free.
func copyMediaDir(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil || !info.IsDir() {
		return nil
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") && p != src {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return writeFile(filepath.Join(dst, rel), b)
	})
}

// --- per-page materialization ------------------------------------------------

// pageOutcome is one page's contribution to a build, collected from a worker
// goroutine and merged deterministically by Run.
type pageOutcome struct {
	issues []lint.Issue
	cached bool
	err    error
}

// renderPage produces one page's output file under staging. When memoisation is
// active (a cache is configured and Check is off) it first resolves the page's
// input tuple and looks it up: a hit writes the cached bytes and skips
// materialization entirely; a miss renders fresh, stores the bytes, then writes
// them. With Check on, or no cache, it always renders fresh and runs the linter.
func renderPage(t *theme.Theme, ldr *content.Loader, s *site.Site, p site.Page, cache *renderCache, tables *tableResolver, styleHref, staging string, opts Options, routes map[string]bool) pageOutcome {
	outPath := filepath.Join(staging, site.OutputPath(p.Route))

	if cache != nil && !opts.Check {
		tmpl, frags, err := materialize.Inputs(t, ldr, p)
		if err != nil {
			return pageOutcome{err: fmt.Errorf("inputs page %q: %w", p.ID, err)}
		}
		key := pageKey(opts.Version, styleHref, tmpl, t, s, p, frags, tables.keyMaterial(frags))
		if b, ok := cache.get(key); ok {
			if err := writeFile(outPath, b); err != nil {
				return pageOutcome{err: err}
			}
			return pageOutcome{cached: true}
		}
		b, _, _, err := materializeRender(t, ldr, s, p, tables, styleHref)
		if err != nil {
			return pageOutcome{err: err}
		}
		cache.put(key, b)
		if err := writeFile(outPath, b); err != nil {
			return pageOutcome{err: err}
		}
		return pageOutcome{}
	}

	b, doc, rep, err := materializeRender(t, ldr, s, p, tables, styleHref)
	if err != nil {
		return pageOutcome{err: err}
	}
	var issues []lint.Issue
	if opts.Check {
		issues = lint.CheckPage(doc, s, p, rep, routes)
	}
	if err := writeFile(outPath, b); err != nil {
		return pageOutcome{err: err}
	}
	return pageOutcome{issues: issues}
}

// materializeRender runs the full pure page pipeline — inject fragments, apply
// SEO, append the stylesheet, ensure the doctype, serialise — and returns the
// rendered bytes alongside the document and slot report (which the linter, but
// not the cache, needs). It is the one function whose output the cache stores,
// so pageKey must hash every input it reads.
func materializeRender(t *theme.Theme, ldr *content.Loader, s *site.Site, p site.Page, tables *tableResolver, styleHref string) ([]byte, *html.Node, materialize.Report, error) {
	doc, rep, err := materialize.Page(t, ldr, p, materialize.Options{
		Tables:   tables.resolve,
		Variants: mediaVariants(tables.siteDir),
	})
	if err != nil {
		return nil, nil, rep, fmt.Errorf("materialize page %q: %w", p.ID, err)
	}
	if err := seo.Apply(doc, s, p); err != nil {
		return nil, nil, rep, err
	}
	if styleHref != "" {
		render.AppendStylesheet(doc, styleHref)
	}
	render.EnsureDoctype(doc)
	out, err := render.Render(doc)
	if err != nil {
		return nil, nil, rep, fmt.Errorf("render page %q: %w", p.ID, err)
	}
	return []byte(out), doc, rep, nil
}

// workerCount bounds the materialization pool at one goroutine per CPU, never
// more than there are pages and never fewer than one.
func workerCount(pages int) int {
	if pages < 1 {
		return 1
	}
	n := runtime.GOMAXPROCS(0)
	if n < 1 {
		n = 1
	}
	if n > pages {
		n = pages
	}
	return n
}

// --- site-wide outputs -------------------------------------------------------

type urlset struct {
	XMLName xml.Name     `xml:"urlset"`
	Xmlns   string       `xml:"xmlns,attr"`
	URLs    []sitemapURL `xml:"url"`
}

type sitemapURL struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

func sitemapXML(s *site.Site, epoch time.Time, hasEpoch bool) []byte {
	pages := s.SortedPages()
	sort.Slice(pages, func(i, j int) bool { return pages[i].Route < pages[j].Route })

	lastmod := ""
	if hasEpoch {
		lastmod = epoch.Format("2006-01-02")
	}
	set := urlset{Xmlns: "http://www.sitemaps.org/schemas/sitemap/0.9"}
	for _, p := range pages {
		set.URLs = append(set.URLs, sitemapURL{Loc: s.CanonicalURL(p.Route), LastMod: lastmod})
	}
	body, _ := xml.MarshalIndent(set, "", "  ")
	return append([]byte(xml.Header), append(body, '\n')...)
}

func robotsTXT(s *site.Site) []byte {
	var b strings.Builder
	b.WriteString("User-agent: *\nAllow: /\n")
	if s.BaseURL != "" {
		fmt.Fprintf(&b, "\nSitemap: %s/sitemap.xml\n", s.BaseURL)
	}
	return []byte(b.String())
}

func marshalLock(l Lock) ([]byte, error) {
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// --- hashing + determinism ---------------------------------------------------

// hashTree returns a sha256 over the sorted (relpath, size, bytes) of every
// file under root. A missing root hashes as the empty tree.
func hashTree(root string) (string, error) {
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)

	h := sha256.New()
	for _, p := range files {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return "", err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\n%d\n", filepath.ToSlash(rel), len(b))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func sourceDateEpoch() (time.Time, bool) {
	v := strings.TrimSpace(os.Getenv("SOURCE_DATE_EPOCH"))
	if v == "" {
		return time.Time{}, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(n, 0).UTC(), true
}

func stampMtimes(root string, epoch time.Time) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, epoch, epoch)
	})
}

// --- filesystem --------------------------------------------------------------

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// swap replaces final with staging using rename(2), keeping a transient backup
// so a failed swap can be rolled back.
func swap(staging, final string) error {
	final = filepath.Clean(final)
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return err
	}
	backup := final + ".bak"
	_ = os.RemoveAll(backup)

	hadFinal := false
	if _, err := os.Stat(final); err == nil {
		hadFinal = true
		if err := os.Rename(final, backup); err != nil {
			return err
		}
	}
	if err := os.Rename(staging, final); err != nil {
		if hadFinal {
			_ = os.Rename(backup, final)
		}
		return err
	}
	_ = os.RemoveAll(backup)
	return nil
}
