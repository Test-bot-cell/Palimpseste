package content

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRefConvention(t *testing.T) {
	if got := ref("home", "hero", nil); got != filepath.Join("home", "hero") {
		t.Errorf("convention ref = %q", got)
	}
}

func TestRefGlobalPrefix(t *testing.T) {
	got := ref("home", "nav", map[string]string{"nav": "_global:nav"})
	if got != filepath.Join("_global", "nav") {
		t.Errorf("global ref = %q", got)
	}
}

func TestRefPlainOverride(t *testing.T) {
	got := ref("home", "x", map[string]string{"x": "shared/block"})
	if got != filepath.FromSlash("shared/block") {
		t.Errorf("override ref = %q", got)
	}
}

func TestFragmentRoundTrip(t *testing.T) {
	dir := t.TempDir()
	frag := filepath.Join(dir, "content", "home", "hero.html")
	if err := os.MkdirAll(filepath.Dir(frag), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(frag, []byte("<h1>Hi</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}

	l := NewLoader(dir)
	html, found, err := l.Fragment("home", "hero", nil)
	if err != nil || !found {
		t.Fatalf("Fragment: found=%v err=%v", found, err)
	}
	if html != "<h1>Hi</h1>" {
		t.Errorf("fragment body = %q", html)
	}
}

func TestFragmentMissingIsNotError(t *testing.T) {
	l := NewLoader(t.TempDir())
	_, found, err := l.Fragment("home", "nope", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Error("expected found=false for missing fragment")
	}
}

func TestFragmentTraversalGuard(t *testing.T) {
	l := NewLoader(t.TempDir())
	_, _, err := l.Fragment("home", "slot", map[string]string{"slot": "../../etc/passwd"})
	if err == nil {
		t.Fatal("expected traversal to be rejected")
	}
}

func TestWriteFragmentRoundTrip(t *testing.T) {
	dir := t.TempDir()
	l := NewLoader(dir)

	path, err := l.WriteFragment("home", "hero", nil, "<p>hi</p>")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "content", "home", "hero.html")
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}

	// The stored bytes are exactly what a read returns — the round-trip closes.
	got, found, err := l.Fragment("home", "hero", nil)
	if err != nil || !found {
		t.Fatalf("read back: found=%v err=%v", found, err)
	}
	if got != "<p>hi</p>" {
		t.Errorf("round-tripped fragment = %q", got)
	}
}

func TestWriteFragmentCreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	l := NewLoader(dir)
	if _, err := l.WriteFragment("deep", "block", nil, "<p>x</p>"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "content", "deep", "block.html")); err != nil {
		t.Errorf("expected file created: %v", err)
	}
}

func TestWriteFragmentHonoursOverride(t *testing.T) {
	dir := t.TempDir()
	l := NewLoader(dir)
	overrides := map[string]string{"nav": "_global:nav"}
	if _, err := l.WriteFragment("home", "nav", overrides, "<nav>x</nav>"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "content", "_global", "nav.html")); err != nil {
		t.Errorf("override target not written: %v", err)
	}
}

func TestWriteFragmentOverwrites(t *testing.T) {
	dir := t.TempDir()
	l := NewLoader(dir)
	if _, err := l.WriteFragment("home", "hero", nil, "<p>one</p>"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.WriteFragment("home", "hero", nil, "<p>two</p>"); err != nil {
		t.Fatal(err)
	}
	got, _, _ := l.Fragment("home", "hero", nil)
	if got != "<p>two</p>" {
		t.Errorf("overwrite failed, got %q", got)
	}
}

func TestWriteFragmentTraversalRejected(t *testing.T) {
	dir := t.TempDir()
	l := NewLoader(dir)
	_, err := l.WriteFragment("home", "x", map[string]string{"x": "../../escape"}, "<p>x</p>")
	if err == nil {
		t.Fatal("expected traversal write to be rejected")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "escape.html")); statErr == nil {
		t.Fatal("traversal wrote a file outside the content root")
	}
}
