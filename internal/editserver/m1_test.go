package editserver

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- §14: strict identifier regex, before any path work -----------------------

func TestFragmentIdentifiersValidatedByRegex(t *testing.T) {
	srv, ts := newTestServer(t)

	// Raw ".." and embedded slashes die in the mux's own path normalisation
	// (an earlier wall); everything else must reach the handler and die on the
	// regex. Either way: no hostile identifier shape may ever succeed.
	bad := []struct{ page, slot string }{
		{"..", "main"},
		{"home", ".."},
		{"%2e%2e", "main"},   // encoded traversal reaches the handler → regex
		{"home", "a%2E%2Eb"}, // mixed-case encoding too
		{"home", "a..b"},
		{"ho%20me", "main"},
		{"home", "_global:"},
		{"home", "_global:na%2Fv"},
		{"-home", "main"},
		{"home", ".hidden"},
	}
	for _, c := range bad {
		req, _ := http.NewRequest(http.MethodPut,
			ts.URL+"/api/fragments/"+c.page+"/"+c.slot, strings.NewReader("<p>x</p>"))
		req.Header.Set("X-Pal-CSRF", srv.csrf)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode < 400 {
			t.Errorf("PUT %s/%s = %d, want a refusal (§14)", c.page, c.slot, res.StatusCode)
		}
	}

	// The legitimate shapes still pass: dots, dashes, the _global: prefix.
	for _, slot := range []string{"main", "_global:footer", "hero"} {
		req, _ := http.NewRequest(http.MethodPut,
			ts.URL+"/api/fragments/home/"+slot, strings.NewReader("<p>ok</p>"))
		req.Header.Set("X-Pal-CSRF", srv.csrf)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("PUT home/%s = %d, want 200", slot, res.StatusCode)
		}
	}
}

// --- §5.1: a plain slot stores one line of bare text ---------------------------

func TestPlainSlotStoresBareText(t *testing.T) {
	srv, ts := newTestServer(t)
	req, _ := http.NewRequest(http.MethodPut,
		ts.URL+"/api/fragments/home/tagline",
		strings.NewReader("<h1>Grand <em>titre</em></h1>\nsur deux lignes"))
	req.Header.Set("X-Pal-CSRF", srv.csrf)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()

	got := string(body)
	if got != "Grand titre sur deux lignes" {
		t.Errorf("plain slot canonical form = %q, want bare single-line text", got)
	}
	stored, err := os.ReadFile(filepath.Join(srv.opts.SiteDir, "content", "home", "tagline.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != got {
		t.Errorf("stored %q differs from response %q", stored, got)
	}
}

// --- §8: the save chain ends with an incremental build -------------------------

// A successful PUT must leave a freshly published public/ tree (builds/<hash> +
// symlink, §3) whose page carries the new prose, and announce the build on the
// SSE stream.
func TestSaveTriggersIncrementalBuild(t *testing.T) {
	srv, ts := newTestServer(t)

	// Subscribe to events first so the build broadcast cannot be missed. The
	// stream is cancelled with the test, or httptest.Server.Close would wait
	// forever on the never-ending SSE response.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	evCh := make(chan string, 16)
	sseReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events", nil)
	go func() {
		res, err := http.DefaultClient.Do(sseReq)
		if err != nil {
			return
		}
		defer res.Body.Close()
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "event: ") {
				evCh <- strings.TrimPrefix(line, "event: ")
			}
		}
	}()
	time.Sleep(100 * time.Millisecond) // let the subscription register

	req, _ := http.NewRequest(http.MethodPut,
		ts.URL+"/api/fragments/home/main", strings.NewReader("<p>Rebuilt prose.</p>"))
	req.Header.Set("X-Pal-CSRF", srv.csrf)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PUT = %d", res.StatusCode)
	}

	page, err := os.ReadFile(filepath.Join(srv.opts.SiteDir, "public", "index.html"))
	if err != nil {
		t.Fatalf("public/ not published after save: %v", err)
	}
	if !strings.Contains(string(page), "Rebuilt prose.") {
		t.Errorf("published page does not carry the saved prose")
	}
	if _, err := os.Readlink(filepath.Join(srv.opts.SiteDir, "public")); err != nil {
		t.Errorf("public is not the §3 symlink: %v", err)
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case name := <-evCh:
			if name == "build" {
				return // the SSE stream announced the build (§8)
			}
		case <-deadline:
			t.Fatal("no build event on /api/events after a save")
		}
	}
}

// --- §4 media serving in edit mode ---------------------------------------------

func TestMediaServedConfined(t *testing.T) {
	srv, ts := newTestServer(t)
	mediaFile := filepath.Join(srv.opts.SiteDir, "media", "logo.webp")
	if err := os.MkdirAll(filepath.Dir(mediaFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mediaFile, []byte("RIFFfake"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := http.Get(ts.URL + "/media/logo.webp")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("GET /media/logo.webp = %d", res.StatusCode)
	}

	for _, path := range []string{
		"/media/../site.json", // traversal (cleaned to /site.json by the client anyway; belt)
		"/media/",             // no directory listings
		"/media/absent.webp",
	} {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode == http.StatusOK {
			t.Errorf("GET %s = 200, want a refusal", path)
		}
	}
}

// --- §9: the canonical echo closes the loop over the new normalisations --------

// What the browser's execCommand produces (b/i, divs) comes back as the
// contract's semantic form — the author's formatting survives the round-trip
// instead of being destroyed.
func TestExecCommandMarkupRoundTripsSemantically(t *testing.T) {
	srv, ts := newTestServer(t)
	req, _ := http.NewRequest(http.MethodPut,
		ts.URL+"/api/fragments/home/main",
		strings.NewReader(`<div>premier <b>gras</b> et <i>italique</i></div><div>second paragraphe</div>`))
	req.Header.Set("X-Pal-CSRF", srv.csrf)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()

	got := string(body)
	want := `<p>premier <strong>gras</strong> et <em>italique</em></p><p>second paragraphe</p>`
	if got != want {
		t.Errorf("round-trip = %q, want %q", got, want)
	}
}
