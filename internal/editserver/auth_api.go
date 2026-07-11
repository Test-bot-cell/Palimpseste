package editserver

// Remote authentication endpoints (§8, §14): a minimal single-admin login over
// argon2id, an opaque session cookie, and a rate-limited login door. These
// exist only in remote mode; in local (loopback) mode s.remote() is false and
// none of this runs.

import (
	"net/http"
	"time"

	"palimpseste/internal/auth"
)

const sessionCookie = "pal_session"

// handleLoginPage serves a tiny self-contained login form. No framework, no
// external asset — the same posture as the overlay.
func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if s.authenticated(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(loginHTML))
}

// handleLogin verifies the password under the rate limiter and, on success,
// mints a session cookie (HttpOnly, SameSite=Strict, Secure).
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	if !s.limiter.Allow(now) {
		http.Error(w, "trop de tentatives, réessayez plus tard", http.StatusTooManyRequests)
		return
	}
	// The login form is same-origin; an Origin header, when present, must match.
	if origin := r.Header.Get("Origin"); origin != "" && !s.sameOrigin(r, origin) {
		http.Error(w, "cross-origin login refused", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	password := r.PostFormValue("password")
	if !auth.VerifyPassword(password, s.opts.PasswordHash) {
		s.limiter.Fail(now)
		http.Error(w, "mot de passe incorrect", http.StatusUnauthorized)
		return
	}
	s.limiter.Succeed()

	token, err := s.sessions.Create(now)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		Expires:  now.Add(12 * time.Hour),
	})
	// Hand the fresh CSRF token to the now-authenticated client so its first
	// mutation carries it. (The token is per-process, injected into every page.)
	w.Header().Set("X-Pal-CSRF", s.csrf)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleLogout revokes the session.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.sessions.Revoke(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/_pal/login", http.StatusSeeOther)
}

// sameOrigin reports whether origin matches the request's own host.
func (s *Server) sameOrigin(r *http.Request, origin string) bool {
	// Host header is the authority the browser connected to; compare loosely by
	// suffix so scheme differences do not trip a legitimate same-host login.
	return origin == "https://"+r.Host || origin == "http://"+r.Host
}

const loginHTML = `<!doctype html>
<html lang="fr"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Palimpseste — connexion</title>
<style>
  body{font:15px/1.5 system-ui,sans-serif;background:#1e1e1e;color:#e6e6e6;display:grid;place-items:center;height:100vh;margin:0}
  form{background:#262626;padding:1.5rem;border-radius:10px;box-shadow:0 4px 24px rgba(0,0,0,.4);min-width:18rem}
  h1{font-size:1rem;margin:0 0 1rem}
  label{display:block;opacity:.8;margin-bottom:.3rem}
  input{width:100%;box-sizing:border-box;font:inherit;color:#e6e6e6;background:#333;border:1px solid #555;border-radius:6px;padding:.5rem}
  button{margin-top:1rem;width:100%;font:inherit;color:#fff;background:#3584e4;border:0;border-radius:6px;padding:.55rem;cursor:pointer}
</style></head>
<body><form method="post" action="/api/login">
  <h1>Palimpseste — édition distante</h1>
  <label for="p">Mot de passe administrateur</label>
  <input id="p" name="password" type="password" autofocus autocomplete="current-password">
  <button type="submit">Se connecter</button>
</form></body></html>`
