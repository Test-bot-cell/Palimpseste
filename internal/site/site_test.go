package site

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOutputPath(t *testing.T) {
	cases := map[string]string{
		"/":       "index.html",
		"/about":  filepath.Join("about", "index.html"),
		"/a/b/c":  filepath.Join("a", "b", "c", "index.html"),
		"/about/": filepath.Join("about", "index.html"),
	}
	for route, want := range cases {
		if got := OutputPath(route); got != want {
			t.Errorf("OutputPath(%q) = %q, want %q", route, got, want)
		}
	}
}

func TestCanonicalURL(t *testing.T) {
	s := &Site{BaseURL: "https://example.test"}
	if got := s.CanonicalURL("/"); got != "https://example.test/" {
		t.Errorf("root canonical = %q", got)
	}
	if got := s.CanonicalURL("/about"); got != "https://example.test/about" {
		t.Errorf("about canonical = %q", got)
	}
	empty := &Site{}
	if got := empty.CanonicalURL("/about"); got != "/about" {
		t.Errorf("no-base canonical = %q", got)
	}
}

func TestLoadValid(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "site.json"), `{
		"name": "S", "theme": "t", "baseURL": "https://x.test/",
		"pages": [{"id":"home","route":"/","template":"page"}]
	}`)
	s, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Lang != "en" {
		t.Errorf("default lang = %q, want en", s.Lang)
	}
	if s.BaseURL != "https://x.test" {
		t.Errorf("trailing slash not trimmed: %q", s.BaseURL)
	}
}

func TestLoadRejectsDuplicateRoute(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "site.json"), `{
		"name":"S","theme":"t",
		"pages":[
			{"id":"a","route":"/x","template":"page"},
			{"id":"b","route":"/x","template":"page"}
		]
	}`)
	if _, err := Load(dir); err == nil {
		t.Fatal("expected duplicate-route error")
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "site.json"), `{"name":"S","theme":"t","bogus":1}`)
	if _, err := Load(dir); err == nil {
		t.Fatal("expected unknown-field error")
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
