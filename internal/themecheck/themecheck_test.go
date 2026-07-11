package themecheck

import (
	"os"
	"path/filepath"
	"testing"

	git "github.com/go-git/go-git/v5"
)

// twoThemeSite writes a site with two themes and returns its dir. current is the
// active theme; both start from the same slot contract, and callers mutate the
// candidate to exercise each §5.3 rule.
func twoThemeSite(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	w := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w("site.json", `{"name":"S","theme":"a","pages":[
	  {"id":"home","route":"/","template":"page","title":"Home"},
	  {"id":"about","route":"/about","template":"page","title":"About"}]}`)

	tmpl := `<!doctype html><html><head></head><body>
	  <nav data-slot="nav"></nav><h1 data-slot="hero.title"></h1>
	  <article data-slot="main"></article><footer data-slot="footer"></footer></body></html>`
	for _, name := range []string{"a", "b"} {
		w("themes/"+name+"/theme.json", `{"name":"`+name+`","slots":{
		  "nav":{"type":"nav"},"hero.title":{"type":"plain"},
		  "main":{"type":"richtext","blocks":["gallery","cta"]},"footer":{"type":"richtext"}}}`)
		w("themes/"+name+"/templates/page.html", tmpl)
	}

	w("content/home/main.html", `<p>Home.</p>`)
	w("content/home/hero.title.html", `Bienvenue`)
	w("content/_global/footer.html", `<p>© S</p>`)
	return dir
}

func rules(rep Report) map[string]Severity {
	m := map[string]Severity{}
	for _, f := range rep.Findings {
		m[f.Rule] = f.Severity
	}
	return m
}

func TestCompatibleThemeHasNoBlockingFindings(t *testing.T) {
	dir := twoThemeSite(t)
	rep, err := Check(dir, "b")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Blocking() {
		t.Errorf("identical-contract theme should not block: %+v", rep.Findings)
	}
}

// §5.3: a required slot the candidate drops, while the site holds content for
// it, is a blocking error.
func TestRequiredSlotAbsentBlocks(t *testing.T) {
	dir := twoThemeSite(t)
	// b's template loses the main slot, but home/main.html exists.
	os.WriteFile(filepath.Join(dir, "themes", "b", "templates", "page.html"),
		[]byte(`<!doctype html><html><head></head><body><nav data-slot="nav"></nav><footer data-slot="footer"></footer></body></html>`), 0o644)
	rep, err := Check(dir, "b")
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Blocking() {
		t.Errorf("dropping a slot that carries content must block: %+v", rep.Findings)
	}
	if rules(rep)["slot-required-absent"] != Error {
		t.Errorf("missing slot-required-absent error: %+v", rep.Findings)
	}
}

// §5.3: a slot the candidate offers but no fragment fills → warning, not block.
func TestOfferedEmptySlotWarns(t *testing.T) {
	dir := twoThemeSite(t)
	// b offers a brand-new sidebar slot nothing fills.
	os.WriteFile(filepath.Join(dir, "themes", "b", "templates", "page.html"),
		[]byte(`<!doctype html><html><head></head><body><nav data-slot="nav"></nav><h1 data-slot="hero.title"></h1><article data-slot="main"></article><aside data-slot="sidebar"></aside><footer data-slot="footer"></footer></body></html>`), 0o644)
	os.WriteFile(filepath.Join(dir, "themes", "b", "theme.json"),
		[]byte(`{"name":"b","slots":{"nav":{"type":"nav"},"hero.title":{"type":"plain"},"main":{"type":"richtext"},"sidebar":{"type":"richtext"},"footer":{"type":"richtext"}}}`), 0o644)
	rep, err := Check(dir, "b")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Blocking() {
		t.Errorf("an empty offered slot must not block: %+v", rep.Findings)
	}
	if rules(rep)["slot-empty"] != Warning {
		t.Errorf("missing slot-empty warning: %+v", rep.Findings)
	}
}

// §4.1/§5.3: a block used in content the candidate slot does not declare warns
// (graceful degradation), never blocks.
func TestUndeclaredBlockWarns(t *testing.T) {
	dir := twoThemeSite(t)
	os.WriteFile(filepath.Join(dir, "content", "home", "main.html"),
		[]byte(`<p>Home.</p><aside data-block="cta"><p>go</p></aside>`), 0o644)
	// b's main slot declares only gallery, not cta.
	os.WriteFile(filepath.Join(dir, "themes", "b", "theme.json"),
		[]byte(`{"name":"b","slots":{"nav":{"type":"nav"},"hero.title":{"type":"plain"},"main":{"type":"richtext","blocks":["gallery"]},"footer":{"type":"richtext"}}}`), 0o644)
	rep, err := Check(dir, "b")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Blocking() {
		t.Errorf("undeclared block must not block: %+v", rep.Findings)
	}
	if rules(rep)["block-undeclared"] != Warning {
		t.Errorf("missing block-undeclared warning: %+v", rep.Findings)
	}
}

// §5.3: diverging data schemas produce a migration report.
func TestDataSchemaDivergenceReported(t *testing.T) {
	dir := twoThemeSite(t)
	os.WriteFile(filepath.Join(dir, "themes", "a", "theme.json"),
		[]byte(`{"name":"a","slots":{"nav":{"type":"nav"},"hero.title":{"type":"plain"},"main":{"type":"richtext"},"footer":{"type":"richtext"}},"data":{"equipe":{"format":"csv","columns":{"nom":"string"}}}}`), 0o644)
	os.WriteFile(filepath.Join(dir, "themes", "b", "theme.json"),
		[]byte(`{"name":"b","slots":{"nav":{"type":"nav"},"hero.title":{"type":"plain"},"main":{"type":"richtext"},"footer":{"type":"richtext"}},"data":{"equipe":{"format":"csv","columns":{"nom":"string","role":"string"}}}}`), 0o644)
	rep, err := Check(dir, "b")
	if err != nil {
		t.Fatal(err)
	}
	if rules(rep)["data-column-added"] != Warning {
		t.Errorf("expected data-column-added migration finding: %+v", rep.Findings)
	}
}

// §5.3: renames declared in the candidate's migrate table move fragments (as
// one dedicated set) and are not counted as absent.
func TestApplyPerformsMigrationRenames(t *testing.T) {
	dir := twoThemeSite(t)
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := repo.Config()
	cfg.User.Name, cfg.User.Email = "T", "t@e"
	repo.SetConfig(cfg)

	// b renames hero.title → hero, main → body; templates reference the new names.
	os.WriteFile(filepath.Join(dir, "themes", "b", "templates", "page.html"),
		[]byte(`<!doctype html><html><head></head><body><nav data-slot="nav"></nav><h1 data-slot="hero"></h1><article data-slot="body"></article><footer data-slot="footer"></footer></body></html>`), 0o644)
	os.WriteFile(filepath.Join(dir, "themes", "b", "theme.json"),
		[]byte(`{"name":"b","slots":{"nav":{"type":"nav"},"hero":{"type":"plain"},"body":{"type":"richtext"},"footer":{"type":"richtext"}},"migrate":{"hero.title":"hero","main":"body"}}`), 0o644)

	rep, moved, err := Apply(dir, "b")
	if err != nil {
		t.Fatalf("apply: %v (findings %+v)", err, rep.Findings)
	}
	if len(moved) != 2 {
		t.Errorf("expected 2 migrated fragments, got %d: %+v", len(moved), moved)
	}
	// The fragments now live under their new names.
	if _, err := os.Stat(filepath.Join(dir, "content", "home", "hero.html")); err != nil {
		t.Errorf("hero.title.html not renamed to hero.html: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "content", "home", "body.html")); err != nil {
		t.Errorf("main.html not renamed to body.html: %v", err)
	}
	// site.json now points at b.
	data, _ := os.ReadFile(filepath.Join(dir, "site.json"))
	if !contains(string(data), `"theme": "b"`) {
		t.Errorf("site.json not switched to b:\n%s", data)
	}
}

// A blocking candidate is refused by Apply — no mutation happens.
func TestApplyRefusesBlockingTheme(t *testing.T) {
	dir := twoThemeSite(t)
	os.WriteFile(filepath.Join(dir, "themes", "b", "templates", "page.html"),
		[]byte(`<!doctype html><html><head></head><body><nav data-slot="nav"></nav></body></html>`), 0o644)
	_, moved, err := Apply(dir, "b")
	if err == nil {
		t.Fatal("apply should refuse a blocking theme")
	}
	if len(moved) != 0 {
		t.Errorf("no fragment should move on a refused apply, got %d", len(moved))
	}
	data, _ := os.ReadFile(filepath.Join(dir, "site.json"))
	if !contains(string(data), `"theme":"a"`) {
		t.Errorf("site.json must stay on a after a refused apply:\n%s", data)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
