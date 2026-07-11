package materialize

import (
	"path/filepath"
	"strings"
	"testing"

	"palimpseste/internal/content"
	"palimpseste/internal/render"
	"palimpseste/internal/site"
	"palimpseste/internal/theme"
)

// tocFixture is a page whose main slot carries headings and a toc block.
func tocFixture(t *testing.T, mainHTML string) (*theme.Theme, *content.Loader) {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "themes", "tt", "theme.json"),
		`{"name":"tt","slots":{"body":{"type":"richtext"}}}`)
	writeFile(t, filepath.Join(dir, "themes", "tt", "templates", "p.html"),
		`<!doctype html><html><head></head><body><article data-slot="body"></article></body></html>`)
	writeFile(t, filepath.Join(dir, "content", "pg", "body.html"), mainHTML)

	tm, err := theme.Load(dir, "tt")
	if err != nil {
		t.Fatal(err)
	}
	return tm, content.NewLoader(dir)
}

// §4.1/§7: the toc computed block renders at materialization — nested list,
// deterministic slug anchors, headings given matching ids.
func TestTocBlockRenders(t *testing.T) {
	tm, ldr := tocFixture(t,
		`<aside data-block="toc"></aside>`+
			`<h2>Première partie</h2><p>a</p>`+
			`<h3>Détail</h3><p>b</p>`+
			`<h2>Deuxième partie</h2><p>c</p>`)
	doc, _, err := Page(tm, ldr, site.Page{ID: "pg", Template: "p", Route: "/"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := render.Render(doc)

	for _, want := range []string{
		`<h2 id="première-partie">`,
		`<h3 id="détail">`,
		`<a href="#première-partie">Première partie</a>`,
		`<a href="#détail">Détail</a>`,
		`<a href="#deuxième-partie">Deuxième partie</a>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("toc output missing %q:\n%s", want, out)
		}
	}
	// The h3 nests one list deeper than its h2 siblings.
	if !strings.Contains(out, `<li><a href="#première-partie">Première partie</a><ul><li><a href="#détail">`) {
		t.Errorf("toc nesting wrong:\n%s", out)
	}
}

func TestTocRespectsDepthParam(t *testing.T) {
	tm, ldr := tocFixture(t,
		`<aside data-block="toc" data-depth="2"></aside>`+
			`<h2>Un</h2><h3>Profond</h3>`)
	doc, _, err := Page(tm, ldr, site.Page{ID: "pg", Template: "p", Route: "/"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := render.Render(doc)
	if !strings.Contains(out, `href="#un"`) {
		t.Errorf("depth-2 toc lost its h2:\n%s", out)
	}
	if strings.Contains(out, `href="#profond"`) {
		t.Errorf("depth-2 toc included an h3:\n%s", out)
	}
}

func TestDuplicateHeadingsGetUniqueAnchors(t *testing.T) {
	tm, ldr := tocFixture(t,
		`<div data-block="toc"></div><h2>Intro</h2><h2>Intro</h2>`)
	doc, _, err := Page(tm, ldr, site.Page{ID: "pg", Template: "p", Route: "/"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := render.Render(doc)
	if !strings.Contains(out, `id="intro"`) || !strings.Contains(out, `id="intro-2"`) {
		t.Errorf("duplicate headings not disambiguated:\n%s", out)
	}
}

// Pages without a toc block are byte-identical to a build without the computed
// pass: no ids sprout on their headings.
func TestNoTocLeavesHeadingsUntouched(t *testing.T) {
	tm, ldr := tocFixture(t, `<h2>Libre</h2>`)
	doc, _, err := Page(tm, ldr, site.Page{ID: "pg", Template: "p", Route: "/"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := render.Render(doc)
	if strings.Contains(out, `id=`) {
		t.Errorf("headings gained ids without a toc block:\n%s", out)
	}
}

// §4: stored fragments say media/<path>; the materializer resolves that to the
// URL that works from the page's depth, host-agnostic.
func TestMediaSrcResolvedByRouteDepth(t *testing.T) {
	cases := []struct{ route, want string }{
		{"/", `src="media/logo.webp"`},
		{"/about", `src="../media/logo.webp"`},
		{"/docs/guide", `src="../../media/logo.webp"`},
	}
	for _, c := range cases {
		t.Run(c.route, func(t *testing.T) {
			tm, ldr := tocFixture(t, `<figure><img src="media/logo.webp" alt="logo"/></figure>`)
			doc, _, err := Page(tm, ldr, site.Page{ID: "pg", Template: "p", Route: c.route}, Options{})
			if err != nil {
				t.Fatal(err)
			}
			out, _ := render.Render(doc)
			if !strings.Contains(out, c.want) {
				t.Errorf("route %s: missing %s in:\n%s", c.route, c.want, out)
			}
		})
	}
}

// §4.1/§7/§14: the table computed block renders its data/ source with
// systematic escaping; unresolved sources degrade to an empty container.
func TestTableBlockRendersEscaped(t *testing.T) {
	tm, ldr := tocFixture(t, `<div data-block="table" data-source="equipe">stale</div>`)
	resolve := func(name string) ([]string, [][]string, bool) {
		if name != "equipe" {
			return nil, nil, false
		}
		return []string{"nom", "rôle"}, [][]string{{"Ada", "<script>alert(1)</script>"}}, true
	}
	doc, _, err := Page(tm, ldr, site.Page{ID: "pg", Template: "p", Route: "/"}, Options{Tables: resolve})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := render.Render(doc)
	for _, want := range []string{"<thead><tr><th>nom</th><th>rôle</th></tr></thead>", "<td>Ada</td>", "&lt;script&gt;"} {
		if !strings.Contains(out, want) {
			t.Errorf("table render missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "<script>") || strings.Contains(out, "stale") {
		t.Errorf("unescaped cell or stale content survived:\n%s", out)
	}
}

func TestTableBlockDegradesWithoutSource(t *testing.T) {
	tm, ldr := tocFixture(t, `<div data-block="table" data-source="absente">stale</div>`)
	doc, _, err := Page(tm, ldr, site.Page{ID: "pg", Template: "p", Route: "/"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := render.Render(doc)
	if !strings.Contains(out, `<div data-block="table" data-source="absente"></div>`) {
		t.Errorf("unresolved table block should render as its empty container:\n%s", out)
	}
}

// §10.2: an inline image slot's SVG is embedded (fill="currentColor" kept), the
// <img> replaced — the logo mechanism.
func TestInlineSVGLogo(t *testing.T) {
	tm, ldr := tocFixture(t, `<figure><img src="media/logo.svg" alt="logo"/></figure>`)
	resolve := func(slot, src string) (string, bool) {
		if src == "media/logo.svg" {
			return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><path d="M0 0h10v10H0z" fill="currentColor"/></svg>`, true
		}
		return "", false
	}
	doc, _, err := Page(tm, ldr, site.Page{ID: "pg", Template: "p", Route: "/"}, Options{InlineSVG: resolve})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := render.Render(doc)
	if strings.Contains(out, "<img") {
		t.Errorf("img not replaced by inline svg:\n%s", out)
	}
	if !strings.Contains(out, `fill="currentColor"`) || !strings.Contains(out, "<svg") {
		t.Errorf("inline svg not embedded:\n%s", out)
	}
}
