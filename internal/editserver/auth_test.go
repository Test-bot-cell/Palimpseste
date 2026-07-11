package editserver

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"palimpseste/internal/auth"
)

func remoteServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	hash, err := auth.HashPassword("open-sesame")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Options{SiteDir: newTestSite(t), Addr: "127.0.0.1:0", PasswordHash: hash})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return srv, ts
}

// §14: a non-loopback bind is refused unless a password is configured.
func TestNonLoopbackRequiresPassword(t *testing.T) {
	if _, err := New(Options{SiteDir: newTestSite(t), Addr: "0.0.0.0:7777"}); err == nil {
		t.Error("non-loopback bind accepted without a password")
	}
	hash, _ := auth.HashPassword("x")
	if _, err := New(Options{SiteDir: newTestSite(t), Addr: "0.0.0.0:7777", PasswordHash: hash}); err != nil {
		t.Errorf("authenticated non-loopback bind refused: %v", err)
	}
}

// Remote mode gates every real route behind a session; only the login surface
// is public.
func TestRemoteModeGatesRoutes(t *testing.T) {
	_, ts := remoteServer(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// API without a session: 401.
	res, err := client.Get(ts.URL + "/api/pages")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated /api/pages = %d, want 401", res.StatusCode)
	}

	// A page GET redirects to the login page.
	res, err = client.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/_pal/login" {
		t.Errorf("unauthenticated page GET = %d → %q, want 303 → /_pal/login", res.StatusCode, res.Header.Get("Location"))
	}

	// The login page itself is reachable.
	res, err = client.Get(ts.URL + "/_pal/login")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("login page = %d, want 200", res.StatusCode)
	}
}

// A correct login mints a session cookie that then opens the gated routes;
// logout closes them again.
func TestLoginOpensThenLogoutCloses(t *testing.T) {
	_, ts := remoteServer(t)
	jar := &sessionJar{}
	client := &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	// Wrong password: 401, no cookie.
	res := postForm(t, client, ts.URL+"/api/login", url.Values{"password": {"nope"}})
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d, want 401", res.StatusCode)
	}

	// Correct password: redirect + session cookie.
	res = postForm(t, client, ts.URL+"/api/login", url.Values{"password": {"open-sesame"}})
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("login = %d, want 303", res.StatusCode)
	}
	if jar.cookie == "" {
		t.Fatal("no session cookie set on login")
	}

	// Now the API is open.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/pages", nil)
	req.Header.Set("Cookie", sessionCookie+"="+jar.cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("authenticated /api/pages = %d, want 200", res.StatusCode)
	}

	// Logout revokes it.
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/api/logout", nil)
	req.Header.Set("Cookie", sessionCookie+"="+jar.cookie)
	res, _ = http.DefaultClient.Do(req)
	res.Body.Close()

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/api/pages", nil)
	req.Header.Set("Cookie", sessionCookie+"="+jar.cookie)
	res, _ = http.DefaultClient.Do(req)
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("post-logout /api/pages = %d, want 401", res.StatusCode)
	}
}

// A local (loopback, no password) server never gates anything — the M1 posture
// is preserved.
func TestLocalModeUngated(t *testing.T) {
	_, ts := newTestServer(t)
	res, err := http.Get(ts.URL + "/api/pages")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("local /api/pages = %d, want 200 (no auth locally)", res.StatusCode)
	}
}

func postForm(t *testing.T, c *http.Client, urlStr string, v url.Values) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, urlStr, strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// sessionJar captures the session cookie the server sets.
type sessionJar struct {
	cookie string
}

func (j *sessionJar) SetCookies(_ *url.URL, cookies []*http.Cookie) {
	for _, c := range cookies {
		if c.Name == sessionCookie && c.Value != "" {
			j.cookie = c.Value
		}
	}
}
func (j *sessionJar) Cookies(*url.URL) []*http.Cookie { return nil }
