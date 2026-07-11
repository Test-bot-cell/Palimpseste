package lint

import (
	"testing"

	"golang.org/x/net/html"

	"palimpseste/internal/materialize"
	"palimpseste/internal/render"
	"palimpseste/internal/site"
)

func doc(t *testing.T, body string) *html.Node {
	t.Helper()
	n, err := render.ParseDocument(`<!doctype html><html><body>` + body + `</body></html>`)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func hasRule(issues []Issue, rule string) bool {
	for _, i := range issues {
		if i.Rule == rule {
			return true
		}
	}
	return false
}

var goodPage = site.Page{
	ID:          "p",
	Title:       "A reasonable page title",
	Description: "A clear and reasonable meta description that comfortably sits within the recommended length.",
}

var routes = map[string]bool{"/": true, "/about": true}

func TestCleanPageHasNoIssues(t *testing.T) {
	d := doc(t, `<h1>Title</h1><p>Body</p><a href="/about">go</a><img src="/i.png" alt="pic">`)
	issues := CheckPage(d, &site.Site{}, goodPage, materialize.Report{}, routes)
	if len(issues) != 0 {
		t.Errorf("expected no issues, got %v", issues)
	}
}

func TestMissingAltFlagged(t *testing.T) {
	d := doc(t, `<h1>t</h1><img src="/i.png">`)
	if !hasRule(CheckPage(d, &site.Site{}, goodPage, materialize.Report{}, routes), "img-alt") {
		t.Error("expected img-alt issue")
	}
}

func TestHeadingJumpFlagged(t *testing.T) {
	d := doc(t, `<h1>a</h1><h3>b</h3>`)
	if !hasRule(CheckPage(d, &site.Site{}, goodPage, materialize.Report{}, routes), "heading") {
		t.Error("expected heading issue")
	}
}

func TestBrokenLinkFlagged(t *testing.T) {
	d := doc(t, `<h1>t</h1><a href="/nope">x</a>`)
	if !hasRule(CheckPage(d, &site.Site{}, goodPage, materialize.Report{}, routes), "broken-link") {
		t.Error("expected broken-link issue")
	}
}

func TestExternalAndAssetLinksIgnored(t *testing.T) {
	d := doc(t, `<h1>t</h1><a href="https://x.test">e</a><a href="/assets/x.css">a</a><a href="#s">f</a>`)
	if hasRule(CheckPage(d, &site.Site{}, goodPage, materialize.Report{}, routes), "broken-link") {
		t.Error("external/asset/anchor links must not be flagged")
	}
}

func TestMissingTitleIsError(t *testing.T) {
	d := doc(t, `<h1>t</h1>`)
	issues := CheckPage(d, &site.Site{}, site.Page{ID: "p"}, materialize.Report{}, routes)
	found := false
	for _, i := range issues {
		if i.Rule == "title" && i.Severity == Error {
			found = true
		}
	}
	if !found {
		t.Errorf("expected title error, got %v", issues)
	}
}

func TestMissingSlotReported(t *testing.T) {
	d := doc(t, `<h1>t</h1>`)
	rep := materialize.Report{Missing: []string{"hero"}}
	if !hasRule(CheckPage(d, &site.Site{}, goodPage, rep, routes), "missing-slot") {
		t.Error("expected missing-slot issue")
	}
}
