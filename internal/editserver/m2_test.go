package editserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// m2Site writes a site with declared tokens, a stack slot and a second theme,
// so the M2 endpoints have something real to act on.
func m2Site(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	w := func(rel, body string) { write(t, filepath.Join(dir, rel), body) }

	w("site.json", `{
	  "name":"M2","theme":"one","lang":"en",
	  "pages":[{"id":"home","route":"/","template":"page","title":"Home",
	    "slots":{"nav":"_global:nav","footer":"_global:footer","side":"_global:side"}}]
	}`)
	tmpl := `<!doctype html><html><head><title>x</title></head><body>
	  <nav data-slot="nav"></nav>
	  <p data-slot="tagline"></p>
	  <article data-slot="main"></article>
	  <aside data-slot="side"></aside>
	  <footer data-slot="footer"></footer></body></html>`
	for _, name := range []string{"one", "two"} {
		w("themes/"+name+"/theme.json", `{
		  "name":"`+name+`","version":"1.0.0",
		  "slots":{"nav":{"type":"nav"},"tagline":{"type":"plain"},
		    "main":{"type":"richtext","blocks":["cta","columns"]},
		    "side":{"type":"stack","blocks":["cta","columns"]},
		    "footer":{"type":"richtext"}},
		  "styles":["styles/tokens.css","styles/base.css"],
		  "tokens":{"--accent":{"type":"color"},"--gap":{"type":"length"}}}`)
		w("themes/"+name+"/templates/page.html", tmpl)
		w("themes/"+name+"/styles/tokens.css", ":root {\n  --accent: #3584e4;\n  --gap: 1rem;\n}\n")
		w("themes/"+name+"/styles/base.css", "body{color:var(--accent)}")
	}
	w("content/home/main.html", `<p>Body.</p>`)
	w("content/_global/nav.html", `<a href="/">Home</a>`)
	w("content/_global/footer.html", `<p>© M2</p>`)
	w("content/_global/side.html", `<aside data-block="cta" data-variant="primary"><p>go</p></aside>`)
	return dir
}

func m2Server(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	srv, err := New(Options{SiteDir: m2Site(t)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return srv, ts
}

func getJSON(t *testing.T, url string, v any) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("GET %s = %d: %s", url, res.StatusCode, b)
	}
	if err := json.NewDecoder(res.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

func putJSON(t *testing.T, srv *Server, url string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPut, url, strings.NewReader(string(b)))
	req.Header.Set("X-Pal-CSRF", srv.csrf)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// --- GET /api/theme, /api/themes ------------------------------------------------

func TestThemeInfoExposesTokensWithValues(t *testing.T) {
	_, ts := m2Server(t)
	var info themeInfo
	getJSON(t, ts.URL+"/api/theme", &info)
	if info.Name != "one" || !info.Editable {
		t.Errorf("theme info = %+v", info)
	}
	if info.Tokens["--accent"].Value != "#3584e4" || info.Tokens["--accent"].Type != "color" {
		t.Errorf("accent token = %+v", info.Tokens["--accent"])
	}
}

func TestThemesListMarksActive(t *testing.T) {
	_, ts := m2Server(t)
	var list []themeEntry
	getJSON(t, ts.URL+"/api/themes", &list)
	active, seen := "", map[string]bool{}
	for _, e := range list {
		seen[e.Name] = true
		if e.Active {
			active = e.Name
		}
	}
	if active != "one" || !seen["two"] {
		t.Errorf("themes list = %+v", list)
	}
}

// --- PUT /api/theme/tokens (§6) --------------------------------------------------

func TestTokensPutRewritesOnlyTokensCSS(t *testing.T) {
	srv, ts := m2Server(t)
	res := putJSON(t, srv, ts.URL+"/api/theme/tokens", map[string]string{"--accent": "#e01b24", "--gap": "2rem"})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("tokens PUT = %d: %s", res.StatusCode, b)
	}
	tokens, _ := os.ReadFile(filepath.Join(srv.opts.SiteDir, "themes", "one", "styles", "tokens.css"))
	if !strings.Contains(string(tokens), "--accent: #e01b24") {
		t.Errorf("tokens.css not rewritten: %s", tokens)
	}
	base, _ := os.ReadFile(filepath.Join(srv.opts.SiteDir, "themes", "one", "styles", "base.css"))
	if string(base) != "body{color:var(--accent)}" {
		t.Errorf("base.css was touched: %s", base)
	}
}

func TestTokensPutRejectsStructureInValue(t *testing.T) {
	srv, ts := m2Server(t)
	res := putJSON(t, srv, ts.URL+"/api/theme/tokens", map[string]string{"--accent": "red; } body{ } /*"})
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("hostile token value = %d, want 400", res.StatusCode)
	}
}

// --- GET /api/theme/check + POST /api/theme/apply (§5.3) -------------------------

func TestThemeCheckAndApplyRoundTrip(t *testing.T) {
	srv, ts := m2Server(t)

	// Dry-run check: compatible (same slot contract), no blocking.
	var rep struct {
		Findings []struct{ Severity string } `json:"findings"`
	}
	getJSON(t, ts.URL+"/api/theme/check?theme=two", &rep)
	for _, f := range rep.Findings {
		if f.Severity == "error" {
			t.Errorf("compatible theme reported a blocking finding: %+v", rep.Findings)
		}
	}

	// Apply: site.json switches to two, page rebuilds.
	b, _ := json.Marshal(map[string]string{"theme": "two"})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/theme/apply", strings.NewReader(string(b)))
	req.Header.Set("X-Pal-CSRF", srv.csrf)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("apply = %d", res.StatusCode)
	}
	if got := activeTheme(t, srv.opts.SiteDir); got != "two" {
		t.Errorf("active theme = %q, want two after apply", got)
	}
}

func TestThemeApplyRefusesBlockingWith409(t *testing.T) {
	srv, ts := m2Server(t)
	// Break theme two so its template drops the main slot (which carries content).
	os.WriteFile(filepath.Join(srv.opts.SiteDir, "themes", "two", "templates", "page.html"),
		[]byte(`<!doctype html><html><head></head><body><nav data-slot="nav"></nav><footer data-slot="footer"></footer></body></html>`), 0o644)
	b, _ := json.Marshal(map[string]string{"theme": "two"})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/theme/apply", strings.NewReader(string(b)))
	req.Header.Set("X-Pal-CSRF", srv.csrf)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Errorf("blocking apply = %d, want 409", res.StatusCode)
	}
	if got := activeTheme(t, srv.opts.SiteDir); got != "one" {
		t.Errorf("active theme = %q, must stay one after a refused apply", got)
	}
}

func activeTheme(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "site.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Theme string `json:"theme"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	return s.Theme
}

// --- PUT /api/pages/{page}/meta (§9/§11) -----------------------------------------

func TestMetaPutUpdatesSiteAndPublishes(t *testing.T) {
	srv, ts := m2Server(t)
	res := putJSON(t, srv, ts.URL+"/api/pages/home/meta", map[string]string{
		"title":       "New Title",
		"description": "A fresh description for the home page, long enough to be useful.",
		"ogImage":     "media/og.webp",
	})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("meta PUT = %d: %s", res.StatusCode, b)
	}
	data, _ := os.ReadFile(filepath.Join(srv.opts.SiteDir, "site.json"))
	if !strings.Contains(string(data), `"New Title"`) || !strings.Contains(string(data), `"ogImage": "media/og.webp"`) {
		t.Errorf("meta not persisted:\n%s", data)
	}
	page, _ := os.ReadFile(filepath.Join(srv.opts.SiteDir, "public", "index.html"))
	if !strings.Contains(string(page), "<title>New Title</title>") {
		t.Errorf("published page lacks new title")
	}
	if !strings.Contains(string(page), `property="og:image" content="media/og.webp"`) {
		t.Errorf("published page lacks og:image:\n%s", firstMeta(string(page)))
	}
}

func TestMetaPutRejectsExternalOgImage(t *testing.T) {
	srv, ts := m2Server(t)
	res := putJSON(t, srv, ts.URL+"/api/pages/home/meta", map[string]string{
		"title": "T", "ogImage": "http://evil.example/x.png",
	})
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("external ogImage = %d, want 400", res.StatusCode)
	}
}

// --- GET /api/check (§11) --------------------------------------------------------

func TestCheckEndpointReportsIssues(t *testing.T) {
	_, ts := m2Server(t)
	var rep struct {
		Issues []struct{ Rule, Page string } `json:"issues"`
		Ms     int64                         `json:"ms"`
	}
	getJSON(t, ts.URL+"/api/check", &rep)
	// The home page has no description → the lint flags it.
	found := false
	for _, i := range rep.Issues {
		if i.Page == "home" {
			found = true
		}
	}
	if !found {
		t.Errorf("check found no issue on a description-less page: %+v", rep.Issues)
	}
}

// --- stack slot save (§5.1/§9) ---------------------------------------------------

func TestStackSlotSaveKeepsBlocksDropsProse(t *testing.T) {
	srv, ts := m2Server(t)
	body := `<p>free prose</p><aside data-block="cta" data-variant="subtle"><p>go</p></aside><div data-block="columns" data-count="3"><p>x</p></div>`
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/fragments/home/side", strings.NewReader(body))
	req.Header.Set("X-Pal-CSRF", srv.csrf)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(res.Body)
	res.Body.Close()
	got := string(out)
	if strings.Contains(got, "free prose") {
		t.Errorf("stack slot kept free prose: %q", got)
	}
	if !strings.Contains(got, `data-block="cta"`) || !strings.Contains(got, `data-block="columns"`) {
		t.Errorf("stack slot lost a block: %q", got)
	}
}

func firstMeta(page string) string {
	i := strings.Index(page, "og:image")
	if i < 0 {
		return page[:min(len(page), 400)]
	}
	return page[max(0, i-40):min(len(page), i+60)]
}
