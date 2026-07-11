package theme

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTheme(t *testing.T, name, themeJSON string) string {
	t.Helper()
	dir := t.TempDir()
	base := filepath.Join(dir, "themes", name)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "theme.json"), []byte(themeJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadValid(t *testing.T) {
	dir := writeTheme(t, "d", `{"name":"d","version":"1.0","slots":{"body":{"type":"richtext"}},"styles":["styles/a.css"]}`)
	tm, err := Load(dir, "d")
	if err != nil {
		t.Fatal(err)
	}
	if tm.Name != "d" || tm.Version != "1.0" {
		t.Errorf("unexpected theme: %+v", tm)
	}
	if tm.Slots["body"].Type != SlotRichtext {
		t.Errorf("slot type = %q", tm.Slots["body"].Type)
	}
	if got := tm.TemplatesDir(); got != filepath.Join(dir, "themes", "d", "templates") {
		t.Errorf("TemplatesDir = %q", got)
	}
}

func TestLoadRejectsMissingName(t *testing.T) {
	dir := writeTheme(t, "d", `{"slots":{}}`)
	if _, err := Load(dir, "d"); err == nil {
		t.Fatal("expected missing-name error")
	}
}

func TestLoadRejectsUnknownSlotType(t *testing.T) {
	dir := writeTheme(t, "d", `{"name":"d","slots":{"x":{"type":"bogus"}}}`)
	if _, err := Load(dir, "d"); err == nil {
		t.Fatal("expected unknown-slot-type error")
	}
}

func TestTemplateReads(t *testing.T) {
	dir := writeTheme(t, "d", `{"name":"d"}`)
	tmplPath := filepath.Join(dir, "themes", "d", "templates", "p.html")
	if err := os.MkdirAll(filepath.Dir(tmplPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmplPath, []byte("<main></main>"), 0o644); err != nil {
		t.Fatal(err)
	}
	tm, err := Load(dir, "d")
	if err != nil {
		t.Fatal(err)
	}
	body, err := tm.Template("p")
	if err != nil {
		t.Fatal(err)
	}
	if body != "<main></main>" {
		t.Errorf("Template body = %q", body)
	}
}
