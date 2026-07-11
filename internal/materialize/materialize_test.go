package materialize

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"palimpseste/internal/content"
	"palimpseste/internal/render"
	"palimpseste/internal/site"
	"palimpseste/internal/theme"
)

// fixture writes a minimal theme + content tree and returns loaders for it.
func fixture(t *testing.T) (*theme.Theme, *content.Loader) {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "themes", "tt", "theme.json"),
		`{"name":"tt","slots":{"body":{"type":"richtext"}}}`)
	writeFile(t, filepath.Join(dir, "themes", "tt", "templates", "p.html"),
		`<!doctype html><html><head></head><body><article data-slot="body"></article></body></html>`)
	writeFile(t, filepath.Join(dir, "content", "pg", "body.html"), `<p>Hello</p>`)

	tm, err := theme.Load(dir, "tt")
	if err != nil {
		t.Fatal(err)
	}
	return tm, content.NewLoader(dir)
}

func TestPageInjectsAndStripsMarker(t *testing.T) {
	tm, ldr := fixture(t)
	doc, rep, err := Page(tm, ldr, site.Page{ID: "pg", Template: "p"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := render.Render(doc)

	if !strings.Contains(out, "<p>Hello</p>") {
		t.Errorf("fragment not injected: %s", out)
	}
	if strings.Contains(out, "data-slot") {
		t.Errorf("data-slot marker not stripped: %s", out)
	}
	if len(rep.Filled) != 1 || rep.Filled[0] != "body" {
		t.Errorf("report.Filled = %v, want [body]", rep.Filled)
	}
}

func TestPageKeepsMarkerInEditMode(t *testing.T) {
	tm, ldr := fixture(t)
	doc, _, err := Page(tm, ldr, site.Page{ID: "pg", Template: "p"}, Options{KeepSlotMarkers: true})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := render.Render(doc)
	if !strings.Contains(out, `data-slot="body"`) {
		t.Errorf("expected data-slot kept in edit mode: %s", out)
	}
}

func TestPageReportsMissingSlot(t *testing.T) {
	tm, ldr := fixture(t)
	_, rep, err := Page(tm, ldr, site.Page{ID: "other", Template: "p"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Missing) != 1 || rep.Missing[0] != "body" {
		t.Errorf("report.Missing = %v, want [body]", rep.Missing)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
