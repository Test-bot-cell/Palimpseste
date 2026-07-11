package seo

import (
	"strings"
	"testing"

	"palimpseste/internal/render"
	"palimpseste/internal/site"
)

func applied(t *testing.T) string {
	t.Helper()
	doc, err := render.ParseDocument(`<!doctype html><html><head></head><body></body></html>`)
	if err != nil {
		t.Fatal(err)
	}
	s := &site.Site{
		Name:         "Site",
		BaseURL:      "https://x.test",
		Lang:         "en",
		Organization: site.Organization{Name: "Org", URL: "https://org.test", Logo: "/logo.svg"},
	}
	p := site.Page{ID: "p", Route: "/about", Title: "The Title", Description: "A fine description."}
	if err := Apply(doc, s, p); err != nil {
		t.Fatal(err)
	}
	out, err := render.Render(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestApplyInjectsCoreTags(t *testing.T) {
	out := applied(t)
	wants := []string{
		`lang="en"`,
		`<title>The Title</title>`,
		`name="description" content="A fine description."`,
		`rel="canonical" href="https://x.test/about"`,
		`property="og:title" content="The Title"`,
		`property="og:url" content="https://x.test/about"`,
		`property="og:image" content="https://x.test/logo.svg"`,
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("output missing %q\n%s", w, out)
		}
	}
}

func TestApplyEmitsJSONLD(t *testing.T) {
	out := applied(t)
	if !strings.Contains(out, `"@type":"WebSite"`) {
		t.Error("missing WebSite JSON-LD")
	}
	if !strings.Contains(out, `"@type":"Organization"`) {
		t.Error("missing Organization JSON-LD")
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	doc, _ := render.ParseDocument(`<!doctype html><html><head><title>old</title></head><body></body></html>`)
	s := &site.Site{Name: "S", BaseURL: "https://x.test", Lang: "en"}
	p := site.Page{ID: "p", Route: "/", Title: "New"}
	_ = Apply(doc, s, p)
	out, _ := render.Render(doc)
	if strings.Count(out, "<title>") != 1 {
		t.Errorf("expected exactly one <title>, got:\n%s", out)
	}
	if !strings.Contains(out, "<title>New</title>") {
		t.Errorf("title not upserted:\n%s", out)
	}
}
