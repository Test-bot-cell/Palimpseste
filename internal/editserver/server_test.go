package editserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
)

// --- test fixture ------------------------------------------------------------

// newTestSite writes a minimal but complete site (site.json + a theme with one
// template and a stylesheet + a handful of fragments) into a temp dir and
// returns its path.
func newTestSite(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()

	write(t, filepath.Join(dir, "site.json"), `{
	  "name": "Test Site",
	  "theme": "mini",
	  "lang": "en",
	  "pages": [
	    {"id":"home","route":"/","template":"page","title":"Home",
	     "slots":{"nav":"_global:nav","footer":"_global:footer"}},
	    {"id":"about","route":"/about","template":"page","title":"About",
	     "slots":{"nav":"_global:nav","footer":"_global:footer"}}
	  ]
	}`)

	themeDir := filepath.Join(dir, "themes", "mini")
	write(t, filepath.Join(themeDir, "theme.json"), `{
	  "name": "mini",
	  "version": "0.0.1",
	  "slots": {
	    "nav": {"type":"nav"},
	    "hero": {"type":"richtext"},
	    "tagline": {"type":"plain"},
	    "main": {"type":"richtext"},
	    "footer": {"type":"richtext"}
	  },
	  "styles": ["styles/base.css"]
	}`)
	write(t, filepath.Join(themeDir, "templates", "page.html"), `<!doctype html>
<html>
  <head><meta charset="utf-8"><title>placeholder</title></head>
  <body>
    <header><nav data-slot="nav" aria-label="Primary"></nav></header>
    <main>
      <section class="hero" data-slot="hero"></section>
      <p data-slot="tagline"></p>
      <article data-slot="main"></article>
    </main>
    <footer data-slot="footer"></footer>
  </body>
</html>`)
	write(t, filepath.Join(themeDir, "styles", "base.css"), `body{margin:0}.hero{padding:2rem}`)

	write(t, filepath.Join(dir, "content", "home", "hero.html"), `<h1>Welcome</h1>`)
	write(t, filepath.Join(dir, "content", "home", "main.html"), `<p>Home body.</p>`)
	write(t, filepath.Join(dir, "content", "about", "main.html"), `<p>About body.</p>`)
	write(t, filepath.Join(dir, "content", "_global", "nav.html"), `<a href="/">Home</a>`)
	write(t, filepath.Join(dir, "content", "_global", "footer.html"), `<p>© Test</p>`)
	return dir
}

func write(t testing.TB, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newTestServer builds a Server over a fresh test site and an httptest server
// in front of it. The watcher is not running (handler tests don't need it; it is
// created in Serve).
func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	dir := newTestSite(t)
	srv, err := New(Options{SiteDir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return srv, ts
}

// --- edit page ---------------------------------------------------------------

func TestEditPageKeepsSlotsAndInjectsOverlay(t *testing.T) {
	_, ts := newTestServer(t)
	body, ct := getString(t, ts.URL+"/")

	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}
	for _, slot := range []string{"nav", "hero", "main", "footer"} {
		if !strings.Contains(body, `data-slot="`+slot+`"`) {
			t.Errorf("edit page dropped slot marker %q", slot)
		}
	}
	for _, want := range []string{
		`id="_pal-config"`,
		`src="/_pal/app.js"`,
		`href="/_pal/theme.css"`,
		`<h1>Welcome</h1>`, // the real fragment content is materialized in place
	} {
		if !strings.Contains(body, want) {
			t.Errorf("edit page missing %q", want)
		}
	}
}

func TestEditPageAppliesSEO(t *testing.T) {
	_, ts := newTestServer(t)
	body, _ := getString(t, ts.URL+"/about")
	if !strings.Contains(body, "<title>About</title>") {
		t.Errorf("SEO title not applied:\n%s", body)
	}
}

func TestUnknownRouteIs404(t *testing.T) {
	_, ts := newTestServer(t)
	res, err := http.Get(ts.URL + "/does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
}

func TestSecurityHeaders(t *testing.T) {
	_, ts := newTestServer(t)
	res, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got := res.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
	if got := res.Header.Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'none'") {
		t.Errorf("CSP = %q, want frame-ancestors 'none'", got)
	}
}

// --- assets ------------------------------------------------------------------

func TestOverlayAssetsServed(t *testing.T) {
	_, ts := newTestServer(t)

	js, jsCT := getString(t, ts.URL+overlayJSPath)
	if !strings.Contains(jsCT, "javascript") {
		t.Errorf("app.js content-type = %q", jsCT)
	}
	if !strings.Contains(js, "_pal-config") {
		t.Errorf("app.js does not look like the overlay script")
	}
	// The overlay ships as TypeScript and is transpiled in-process (§9, §17):
	// what reaches the browser must be plain JavaScript, types erased.
	if strings.Contains(js, ": string") || strings.Contains(js, "OverlayConfig") {
		t.Errorf("app.js still carries TypeScript syntax")
	}
}

func TestThemeCSSServed(t *testing.T) {
	_, ts := newTestServer(t)
	body, ct := getString(t, ts.URL+themeCSSPath)
	if !strings.Contains(ct, "text/css") {
		t.Errorf("theme.css content-type = %q", ct)
	}
	if !strings.Contains(body, ".hero") {
		t.Errorf("theme.css missing bundled rule:\n%s", body)
	}
}

// --- pages API ---------------------------------------------------------------

func TestPagesAPIReturnsSortedJSON(t *testing.T) {
	_, ts := newTestServer(t)
	body, ct := getString(t, ts.URL+"/api/pages")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("pages content-type = %q", ct)
	}
	var pages []pageEntry
	if err := json.Unmarshal([]byte(body), &pages); err != nil {
		t.Fatalf("decode pages: %v", err)
	}
	if len(pages) != 2 || pages[0].ID != "about" || pages[1].ID != "home" {
		t.Errorf("pages not sorted by id: %+v", pages)
	}
}

// --- fragment read -----------------------------------------------------------

func TestFragmentGetReturnsStoredHTML(t *testing.T) {
	_, ts := newTestServer(t)

	body, ct := getString(t, ts.URL+"/api/fragments/home/hero")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("fragment content-type = %q, want text/html", ct)
	}
	if body != `<h1>Welcome</h1>` {
		t.Errorf("fragment body = %q, want <h1>Welcome</h1>", body)
	}
}

func TestFragmentGetEmptyWhenUnwritten(t *testing.T) {
	_, ts := newTestServer(t)
	// about/hero has no fragment on disk and no override: a well-formed but empty
	// slot, so the read is a 200 with an empty body rather than a 404.
	res, err := http.Get(ts.URL + "/api/fragments/about/hero")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var b bytes.Buffer
	b.ReadFrom(res.Body)
	if b.Len() != 0 {
		t.Errorf("unwritten slot body = %q, want empty", b.String())
	}
}

func TestFragmentGetUnknownPageIs404(t *testing.T) {
	_, ts := newTestServer(t)
	res, err := http.Get(ts.URL + "/api/fragments/nope/hero")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
}

// --- fragment write: the round-trip -----------------------------------------

func TestFragmentRoundTripSanitizesAndStores(t *testing.T) {
	srv, ts := newTestServer(t)

	resp := putFragment(t, ts, srv.csrf, "", "home", "hero",
		`<h2 class="evil" onclick="x()">Hi<script>alert(1)</script></h2>`)
	if resp.status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.status, resp.body)
	}
	if !strings.HasPrefix(resp.contentType, "text/html") {
		t.Errorf("response content-type = %q, want text/html", resp.contentType)
	}
	// The response is the canonical, sanitized HTML — not a JSON envelope.
	if resp.body != `<h2>Hi</h2>` {
		t.Errorf("sanitized html = %q, want <h2>Hi</h2>", resp.body)
	}
	// The bytes on disk are exactly the sanitized HTML — the round-trip closes.
	onDisk := readFile(t, filepath.Join(srv.opts.SiteDir, "content", "home", "hero.html"))
	if onDisk != `<h2>Hi</h2>` {
		t.Errorf("stored fragment = %q, want <h2>Hi</h2>", onDisk)
	}
}

func TestFragmentHonoursOverrideTarget(t *testing.T) {
	srv, ts := newTestServer(t)
	// nav is redirected to _global:nav for the home page.
	resp := putFragment(t, ts, srv.csrf, "", "home", "nav", `<a href="/about">About</a>`)
	if resp.status != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.status, resp.body)
	}
	onDisk := readFile(t, filepath.Join(srv.opts.SiteDir, "content", "_global", "nav.html"))
	if onDisk != `<a href="/about">About</a>` {
		t.Errorf("override target = %q", onDisk)
	}
}

func TestFragmentRequiresValidCSRF(t *testing.T) {
	srv, ts := newTestServer(t)
	for _, tc := range []struct{ name, token string }{
		{"missing", ""},
		{"wrong", "deadbeef"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := putFragment(t, ts, tc.token, "", "home", "hero", "<p>x</p>")
			if resp.status != http.StatusForbidden {
				t.Errorf("status = %d, want 403", resp.status)
			}
			// The forbidden write must not touch disk.
			if got := readFile(t, filepath.Join(srv.opts.SiteDir, "content", "home", "hero.html")); got != "<h1>Welcome</h1>" {
				t.Errorf("fragment changed despite rejected write: %q", got)
			}
		})
	}
}

func TestFragmentRejectsCrossOrigin(t *testing.T) {
	srv, ts := newTestServer(t)

	evil := putFragment(t, ts, srv.csrf, "http://evil.example", "home", "hero", "<p>x</p>")
	if evil.status != http.StatusForbidden {
		t.Errorf("cross-origin status = %d, want 403", evil.status)
	}
	// A loopback Origin with a valid token is allowed.
	ok := putFragment(t, ts, srv.csrf, "http://127.0.0.1:12345", "home", "hero", "<p>ok</p>")
	if ok.status != http.StatusOK {
		t.Errorf("loopback-origin status = %d, want 200: %s", ok.status, ok.body)
	}
}

func TestFragmentTraversalRejected(t *testing.T) {
	srv, ts := newTestServer(t)
	// The slot travels as a single %2F-escaped path segment; the mux hands the
	// decoded "../../escape" to the handler, where WriteFragment's guard rejects it.
	resp := putFragment(t, ts, srv.csrf, "", "home", "../../escape", "<p>x</p>")
	if resp.status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.status)
	}
	if _, err := os.Stat(filepath.Join(srv.opts.SiteDir, "escape.html")); err == nil {
		t.Fatal("traversal wrote a file outside the content root")
	}
}

func TestFragmentUnknownPageAndSlot(t *testing.T) {
	srv, ts := newTestServer(t)

	if resp := putFragment(t, ts, srv.csrf, "", "nope", "hero", "<p>x</p>"); resp.status != http.StatusNotFound {
		t.Errorf("unknown page status = %d, want 404", resp.status)
	}
	if resp := putFragment(t, ts, srv.csrf, "", "home", "  ", "<p>x</p>"); resp.status != http.StatusBadRequest {
		t.Errorf("blank slot status = %d, want 400", resp.status)
	}
}

// --- versioning: each save is a commit (§13) --------------------------------

func TestFragmentSaveRecordsCommit(t *testing.T) {
	dir := newTestSite(t)
	if _, err := git.PlainInit(dir, false); err != nil {
		t.Fatalf("git init: %v", err)
	}
	srv, err := New(Options{SiteDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !srv.hist.Enabled() {
		t.Fatal("history recorder not enabled inside a git work tree")
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	if resp := putFragment(t, ts, srv.csrf, "", "home", "hero", `<h2>Hi</h2>`); resp.status != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.status, resp.body)
	}

	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("head after save: %v", err)
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(commit.Message); got != "edit(home/hero)" {
		t.Errorf("commit message = %q, want edit(home/hero)", got)
	}

	// Re-saving byte-identical content must not manufacture a second commit.
	if resp := putFragment(t, ts, srv.csrf, "", "home", "hero", `<h2>Hi</h2>`); resp.status != http.StatusOK {
		t.Fatalf("second save status = %d: %s", resp.status, resp.body)
	}
	head2, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	if head2.Hash() != head.Hash() {
		t.Error("identical re-save produced a redundant commit")
	}
}

// --- construction guards -----------------------------------------------------

func TestNewRejectsNonLoopback(t *testing.T) {
	dir := newTestSite(t)
	if _, err := New(Options{SiteDir: dir, Addr: "0.0.0.0:7777"}); err == nil {
		t.Fatal("expected non-loopback address to be rejected")
	}
}

func TestNewReportsSiteErrors(t *testing.T) {
	if _, err := New(Options{SiteDir: t.TempDir()}); err == nil {
		t.Fatal("expected error for a directory with no site.json")
	}
}

// --- pure helpers ------------------------------------------------------------

func TestNormalizeRoute(t *testing.T) {
	cases := map[string]string{
		"/":                 "/",
		"":                  "/",
		"/about":            "/about",
		"/about/":           "/about",
		"/about/index.html": "/about",
		"/index.html":       "/",
	}
	for in, want := range cases {
		if got := normalizeRoute(in); got != want {
			t.Errorf("normalizeRoute(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsLoopbackOrigin(t *testing.T) {
	yes := []string{"http://127.0.0.1:7777", "http://localhost:1234", "http://[::1]:9"}
	no := []string{"http://evil.example", "https://drakeos.org", "http://10.0.0.5", "garbage"}
	for _, o := range yes {
		if !isLoopbackOrigin(o) {
			t.Errorf("isLoopbackOrigin(%q) = false, want true", o)
		}
	}
	for _, o := range no {
		if isLoopbackOrigin(o) {
			t.Errorf("isLoopbackOrigin(%q) = true, want false", o)
		}
	}
}

// --- watcher + hub -----------------------------------------------------------

func TestWatcherFiresOnRealFileNotDotfile(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.html"), "one")
	w, err := newWatcher(dir)
	if err != nil {
		t.Fatalf("newWatcher: %v", err)
	}
	defer w.Close()

	fired := make(chan struct{}, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Watch(ctx, func() { fired <- struct{}{} })

	// A hidden scratch file (like atomicWrite's .pal-*.tmp) must not fire: it is
	// exactly the churn the watcher is designed to ignore.
	write(t, filepath.Join(dir, ".pal-scratch.tmp"), "temp")
	select {
	case <-fired:
		t.Error("watcher fired for a dotfile write")
	case <-time.After(500 * time.Millisecond): // comfortably past the 150ms debounce
	}

	// A real new fragment must fire (once the debounce settles).
	write(t, filepath.Join(dir, "b.html"), "two")
	select {
	case <-fired:
	case <-time.After(3 * time.Second):
		t.Error("watcher did not fire for a real file write")
	}
}

func TestHubBroadcastReachesSubscriber(t *testing.T) {
	h := newHub()
	ch := h.subscribe()
	h.broadcast(event{Name: "reload", Data: "1"})
	select {
	case ev := <-ch:
		if ev.Name != "reload" || ev.Data != "1" {
			t.Errorf("event = %+v, want reload/1", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("broadcast did not reach subscriber")
	}
	h.unsubscribe(ch)
	if _, open := <-ch; open {
		t.Error("channel not closed after unsubscribe")
	}
}

// --- live reload over SSE (integration) -------------------------------------

func TestServeBroadcastsReloadOnFileChange(t *testing.T) {
	dir := newTestSite(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Options{SiteDir: dir, Addr: ln.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, ln) }()

	base := "http://" + ln.Addr().String()

	// Open the SSE stream and wait for the priming comment, which guarantees our
	// subscription is registered before we trigger a change.
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/events", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	// A single goroutine owns the reader; the test consumes decoded lines from
	// the channel, so nothing ever touches the stream concurrently.
	lines := make(chan string, 16)
	go func() {
		br := bufio.NewReader(res.Body)
		for {
			line, err := br.ReadString('\n')
			if line != "" {
				lines <- strings.TrimRight(line, "\r\n")
			}
			if err != nil {
				close(lines)
				return
			}
		}
	}()

	readUntil(t, lines, ": connected")

	// Change a watched file; the fsnotify watcher should notice and broadcast.
	write(t, filepath.Join(dir, "content", "home", "extra.html"), "<p>new</p>")
	readUntil(t, lines, "event: reload")

	cancel()
	if err := <-serveErr; err != nil {
		t.Errorf("Serve returned %v", err)
	}
}

// readUntil consumes decoded SSE lines until one equals want, or fails on
// timeout / stream close.
func readUntil(t *testing.T, lines <-chan string, want string) {
	t.Helper()
	deadline := time.After(8 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("stream closed before seeing %q", want)
			}
			if line == want {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %q", want)
		}
	}
}

// --- request helpers ---------------------------------------------------------

func getString(t *testing.T, url string) (body, contentType string) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d", url, res.StatusCode)
	}
	var b bytes.Buffer
	if _, err := b.ReadFrom(res.Body); err != nil {
		t.Fatal(err)
	}
	return b.String(), res.Header.Get("Content-Type")
}

type putResult struct {
	status      int
	body        string
	contentType string
}

// putFragment PUTs raw HTML to /api/fragments/{page}/{slot}. page and slot are
// path-escaped, so a slot such as "../../escape" travels as one %2F-encoded
// segment and reaches the handler intact for the traversal guard to reject.
func putFragment(t *testing.T, ts *httptest.Server, csrf, origin, page, slot, html string) putResult {
	t.Helper()
	target := ts.URL + "/api/fragments/" + url.PathEscape(page) + "/" + url.PathEscape(slot)
	httpReq, err := http.NewRequest(http.MethodPut, target, strings.NewReader(html))
	if err != nil {
		t.Fatal(err)
	}
	httpReq.Header.Set("Content-Type", "text/html; charset=utf-8")
	if csrf != "" {
		httpReq.Header.Set("X-Pal-CSRF", csrf)
	}
	if origin != "" {
		httpReq.Header.Set("Origin", origin)
	}
	res, err := ts.Client().Do(httpReq)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var b bytes.Buffer
	b.ReadFrom(res.Body)
	return putResult{status: res.StatusCode, body: b.String(), contentType: res.Header.Get("Content-Type")}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
