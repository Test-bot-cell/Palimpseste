package theme

import (
	"os"
	"path/filepath"
	"testing"
)

func tokenTheme(t *testing.T) *Theme {
	t.Helper()
	dir := t.TempDir()
	td := filepath.Join(dir, "themes", "x")
	must := func(rel, body string) {
		p := filepath.Join(td, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("theme.json", `{"name":"x","styles":["styles/tokens.css","styles/base.css"],
	  "tokens":{"--accent":{"type":"color","snap":"open-props"},"--gap":{"type":"length"}}}`)
	must("styles/tokens.css", ":root {\n  --accent: #3584e4;\n  --gap: 1rem;\n}\n")
	must("styles/base.css", "body{color:var(--accent)}")

	th, err := Load(dir, "x")
	if err != nil {
		t.Fatal(err)
	}
	return th
}

func TestReadTokenValues(t *testing.T) {
	th := tokenTheme(t)
	vals, err := th.ReadTokenValues()
	if err != nil {
		t.Fatal(err)
	}
	if vals["--accent"] != "#3584e4" || vals["--gap"] != "1rem" {
		t.Errorf("parsed tokens = %v", vals)
	}
}

// §6: writing tokens rewrites tokens.css only, deterministically, and a
// re-read round-trips.
func TestWriteTokenValuesRoundTrips(t *testing.T) {
	th := tokenTheme(t)
	path, err := th.WriteTokenValues(map[string]string{"--accent": "#e01b24", "--gap": "2rem"})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "tokens.css" {
		t.Errorf("wrote %q, want tokens.css", path)
	}
	// base.css untouched.
	base, _ := os.ReadFile(filepath.Join(th.Dir(), "styles", "base.css"))
	if string(base) != "body{color:var(--accent)}" {
		t.Errorf("base.css was modified: %q", base)
	}
	vals, _ := th.ReadTokenValues()
	if vals["--accent"] != "#e01b24" || vals["--gap"] != "2rem" {
		t.Errorf("round-trip failed: %v", vals)
	}
}

// The panel can only touch declared tokens.
func TestWriteRejectsUndeclaredToken(t *testing.T) {
	th := tokenTheme(t)
	if _, err := th.WriteTokenValues(map[string]string{"--evil": "x"}); err == nil {
		t.Error("writing an undeclared token should fail")
	}
}

func TestParseCustomPropsHandlesFunctionsAndComments(t *testing.T) {
	css := `/* c */ :root { --a: oklch(0.5 0.1 20); --b: color-mix(in oklch, red, blue); }`
	got := parseCustomProps(css)
	if got["--a"] != "oklch(0.5 0.1 20)" {
		t.Errorf("--a = %q", got["--a"])
	}
	if got["--b"] != "color-mix(in oklch, red, blue)" {
		t.Errorf("--b = %q", got["--b"])
	}
}
