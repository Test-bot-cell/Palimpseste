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

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// gitTestServer builds a server whose site is a real git repo, so history and
// revert have commits to work with.
func gitTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	dir := newTestSite(t)
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := repo.Config()
	cfg.User.Name, cfg.User.Email = "Tester", "t@example.org"
	repo.SetConfig(cfg)
	// Seed a first commit so the tree has history.
	wt, _ := repo.Worktree()
	if _, err := wt.Add("."); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit("seed", &git.CommitOptions{
		All:    true,
		Author: &object.Signature{Name: "Tester", Email: "t@example.org"},
	}); err != nil {
		t.Fatalf("seed commit: %v", err)
	}

	srv, err := New(Options{SiteDir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return srv, ts
}

func put(t *testing.T, srv *Server, ts *httptest.Server, page, slot, body string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/fragments/"+page+"/"+slot, strings.NewReader(body))
	req.Header.Set("X-Pal-CSRF", srv.csrf)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PUT %s/%s = %d: %s", page, slot, res.StatusCode, out)
	}
	return string(out)
}

// §13: history is a view over git log; a revert restores a past version through
// the ordinary write path.
func TestHistoryAndRevert(t *testing.T) {
	srv, ts := gitTestServer(t)

	// Three successive edits → three commits touching home/main.
	put(t, srv, ts, "home", "main", "<p>Version une.</p>")
	put(t, srv, ts, "home", "main", "<p>Version deux.</p>")
	put(t, srv, ts, "home", "main", "<p>Version trois.</p>")

	// History lists them, newest first.
	res, err := http.Get(ts.URL + "/api/history/home/main")
	if err != nil {
		t.Fatal(err)
	}
	var hist struct {
		Enabled   bool `json:"enabled"`
		Revisions []struct {
			Hash, Message, Author string
		} `json:"revisions"`
	}
	json.NewDecoder(res.Body).Decode(&hist)
	res.Body.Close()

	if !hist.Enabled {
		t.Fatal("history should be enabled under git")
	}
	if len(hist.Revisions) < 3 {
		t.Fatalf("expected ≥3 revisions, got %d: %+v", len(hist.Revisions), hist.Revisions)
	}
	if hist.Revisions[0].Message != "edit(home/main)" || hist.Revisions[0].Author != "Tester" {
		t.Errorf("newest revision = %+v", hist.Revisions[0])
	}

	// Revert to the oldest of our three edits (the "Version une" commit).
	target := hist.Revisions[len(hist.Revisions)-1]
	// Find the commit whose stored content is "Version une" by walking back.
	var oneHash string
	for _, rv := range hist.Revisions {
		body := revertPreview(t, srv, ts, rv.Hash)
		if strings.Contains(body, "Version une") {
			oneHash = rv.Hash
			break
		}
	}
	if oneHash == "" {
		oneHash = target.Hash
	}

	reqBody, _ := json.Marshal(map[string]string{"hash": oneHash})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/revert/home/main", strings.NewReader(string(reqBody)))
	req.Header.Set("X-Pal-CSRF", srv.csrf)
	rres, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	restored, _ := io.ReadAll(rres.Body)
	rres.Body.Close()
	if rres.StatusCode != http.StatusOK {
		t.Fatalf("revert = %d: %s", rres.StatusCode, restored)
	}
	if !strings.Contains(string(restored), "Version une") {
		t.Errorf("revert did not restore the old content: %q", restored)
	}

	// The on-disk fragment now holds the reverted content, and a fresh commit
	// records the revert.
	onDisk, _ := os.ReadFile(filepath.Join(srv.opts.SiteDir, "content", "home", "main.html"))
	if !strings.Contains(string(onDisk), "Version une") {
		t.Errorf("disk not updated by revert: %q", onDisk)
	}
	res2, _ := http.Get(ts.URL + "/api/history/home/main")
	var hist2 struct {
		Revisions []struct{ Message string } `json:"revisions"`
	}
	json.NewDecoder(res2.Body).Decode(&hist2)
	res2.Body.Close()
	if !strings.HasPrefix(hist2.Revisions[0].Message, "revert(home/main") {
		t.Errorf("newest commit after revert = %q, want a revert(...) message", hist2.Revisions[0].Message)
	}
}

// revertPreview reads a fragment's bytes at a commit via the history layer
// (used by the test to locate the right commit).
func revertPreview(t *testing.T, srv *Server, _ *httptest.Server, hash string) string {
	t.Helper()
	path := filepath.Join(srv.opts.SiteDir, "content", "home", "main.html")
	b, err := srv.hist.FileAt(path, hash)
	if err != nil {
		return ""
	}
	return string(b)
}

// A revert on a site with no git repo is a clean 400, not a crash.
func TestRevertWithoutGitIsRejected(t *testing.T) {
	srv, ts := newTestServer(t) // no git init
	reqBody, _ := json.Marshal(map[string]string{"hash": "abc1234"})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/revert/home/main", strings.NewReader(string(reqBody)))
	req.Header.Set("X-Pal-CSRF", srv.csrf)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("revert without git = %d, want 400", res.StatusCode)
	}
}
