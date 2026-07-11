package css

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"palimpseste/internal/theme"
)

func loadTheme(t *testing.T, styles ...string) *theme.Theme {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "themes", "x", "theme.json"),
		`{"name":"x","styles":["styles/a.css","styles/b.css"]}`)
	mustWrite(t, filepath.Join(dir, "themes", "x", "styles", "a.css"),
		`:root{--c:oklch(0.5 0.1 250)}`)
	mustWrite(t, filepath.Join(dir, "themes", "x", "styles", "b.css"),
		`body{color:var(--c);margin:0}`)
	tm, err := theme.Load(dir, "x")
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func TestBuildThemeBundles(t *testing.T) {
	tm := loadTheme(t)
	b, err := BuildTheme(tm)
	if err != nil {
		t.Fatal(err)
	}
	if b.Empty() {
		t.Fatal("bundle unexpectedly empty")
	}
	if !strings.HasPrefix(b.Filename, "style.") || !strings.HasSuffix(b.Filename, ".css") {
		t.Errorf("filename = %q", b.Filename)
	}
	body := string(b.Contents)
	if !strings.Contains(body, "oklch") || !strings.Contains(body, "var(--c)") {
		t.Errorf("bundled css missing expected tokens: %s", body)
	}
}

func TestBuildThemeDeterministic(t *testing.T) {
	tm := loadTheme(t)
	b1, err := BuildTheme(tm)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := BuildTheme(tm)
	if err != nil {
		t.Fatal(err)
	}
	if b1.Filename != b2.Filename {
		t.Errorf("nondeterministic filename: %q vs %q", b1.Filename, b2.Filename)
	}
}

func TestBuildThemeNoStyles(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "themes", "y", "theme.json"), `{"name":"y"}`)
	tm, err := theme.Load(dir, "y")
	if err != nil {
		t.Fatal(err)
	}
	b, err := BuildTheme(tm)
	if err != nil {
		t.Fatal(err)
	}
	if !b.Empty() {
		t.Errorf("expected empty bundle, got %q", b.Filename)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
