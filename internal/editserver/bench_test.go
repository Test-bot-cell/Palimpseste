package editserver

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

// §15 — the editing-side budgets. The save→page-regenerated cycle is the one
// the operator feels on every keystrokeful of work; it is bounded end to end:
// HTTP round-trip, sanitisation, fragment write, commit probe, incremental
// (memoised) rebuild of the published tree.

func benchPut(tb testing.TB, srv *Server, ts *httptest.Server, body string) {
	tb.Helper()
	req, _ := http.NewRequest(http.MethodPut,
		ts.URL+"/api/fragments/home/main", strings.NewReader(body))
	req.Header.Set("X-Pal-CSRF", srv.csrf)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		tb.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		tb.Fatalf("PUT = %d", res.StatusCode)
	}
}

func BenchmarkSaveCycle(b *testing.B) {
	srv, err := New(Options{SiteDir: newTestSite(b)})
	if err != nil {
		b.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	benchPut(b, srv, ts, "<p>warm</p>") // prime cache + publish layout
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		i++
		benchPut(b, srv, ts, "<p>edit "+string(rune('a'+i%26))+"</p>")
	}
}

// §15: "Cycle sauvegarde → page régénérée (bout en bout) < 100 ms".
func TestBudgetSaveCycle(t *testing.T) {
	srv, err := New(Options{SiteDir: newTestSite(t)})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	benchPut(t, srv, ts, "<p>warm</p>") // first save pays cold caches; the budget bounds the steady state

	best := time.Duration(1<<63 - 1)
	for i := 0; i < 5; i++ {
		body := "<p>mesure " + strings.Repeat("x", i+1) + "</p>"
		start := time.Now()
		benchPut(t, srv, ts, body)
		if d := time.Since(start); d < best {
			best = d
		}
	}
	if budget := 100 * time.Millisecond; best > budget {
		t.Errorf("save cycle best-of-5 = %v, budget %v (§15)", best, budget)
	}
}

// §15: "Binaire au repos (mode edit) : 15–30 Mo RAM". Bounded on the live heap
// after a save cycle and a GC — what the resident editor actually retains.
func TestBudgetIdleMemory(t *testing.T) {
	srv, err := New(Options{SiteDir: newTestSite(t)})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	benchPut(t, srv, ts, "<p>warm</p>")

	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	if budget := uint64(30 << 20); ms.HeapAlloc > budget {
		t.Errorf("idle heap = %d MiB, budget 30 MiB (§15)", ms.HeapAlloc>>20)
	}
}
