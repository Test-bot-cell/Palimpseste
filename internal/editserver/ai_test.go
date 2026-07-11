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

// aiServer builds an edit server whose assistant points at a mock
// OpenAI-compatible provider.
func aiServer(t *testing.T, reply string) (*Server, *httptest.Server, *httptest.Server) {
	t.Helper()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{"choices": []map[string]any{{"message": map[string]any{"content": reply}}}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Setenv("PALIMPSESTE_AI_ENDPOINT", provider.URL+"/v1")
	t.Setenv("PALIMPSESTE_AI_MODEL", "mock")
	t.Setenv("PALIMPSESTE_AI_KEY", "")
	t.Setenv("PALIMPSESTE_AI_VISION_MODEL", "")

	srv, err := New(Options{SiteDir: newTestSite(t)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close(); provider.Close() })
	return srv, ts, provider
}

// §12: the assistant returns proposals and writes NOTHING. This is the
// load-bearing guarantee — we snapshot the whole site tree before and after a
// suggestion and require it byte-identical.
func TestAISuggestNeverWrites(t *testing.T) {
	srv, ts, _ := aiServer(t, "Une méta-description fidèle du contenu de la page d'accueil.")

	before := snapshotTree(t, srv.opts.SiteDir)

	body, _ := json.Marshal(map[string]string{"kind": "description", "page": "home"})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/ai/suggest", strings.NewReader(string(body)))
	req.Header.Set("X-Pal-CSRF", srv.csrf)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("suggest = %d: %s", res.StatusCode, out)
	}

	var got struct {
		Kind      string   `json:"kind"`
		Proposals []string `json:"proposals"`
	}
	json.Unmarshal(out, &got)
	if len(got.Proposals) != 1 || !strings.Contains(got.Proposals[0], "méta-description") {
		t.Errorf("proposals = %v", got.Proposals)
	}

	after := snapshotTree(t, srv.opts.SiteDir)
	if before != after {
		t.Error("the assistant mutated the site (§12 forbids any AI write)")
	}
}

// Unconfigured, the endpoint reports 404 — the feature does not exist (§12).
func TestAISuggestUnconfiguredIs404(t *testing.T) {
	t.Setenv("PALIMPSESTE_AI_ENDPOINT", "")
	t.Setenv("PALIMPSESTE_AI_MODEL", "")
	srv, ts := newTestServer(t)
	if srv.ai != nil {
		t.Fatal("assistant should be nil without configuration")
	}
	body, _ := json.Marshal(map[string]string{"kind": "title", "page": "home"})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/ai/suggest", strings.NewReader(string(body)))
	req.Header.Set("X-Pal-CSRF", srv.csrf)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("unconfigured suggest = %d, want 404", res.StatusCode)
	}
}

// A suggestion still requires the CSRF token — it is a mutation-gated action
// even though it mutates nothing (it spends a provider call).
func TestAISuggestRequiresCSRF(t *testing.T) {
	_, ts, _ := aiServer(t, "x")
	body, _ := json.Marshal(map[string]string{"kind": "title", "page": "home"})
	res, err := http.Post(ts.URL+"/api/ai/suggest", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("suggest without CSRF = %d, want 403", res.StatusCode)
	}
}

// snapshotTree returns a stable digest of every file under dir (path + bytes),
// so any write — content, data, media, site.json — is detected.
func snapshotTree(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		b.WriteString(rel)
		b.WriteByte('\n')
		b.Write(data)
		b.WriteByte(0)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}
