// Package auth is the single-admin authentication for remote edit mode (§8,
// §14). Local editing binds loopback and needs none; the moment the editor
// listens on a routable address it demands a password. The scheme is
// deliberately small: argon2id over a password whose hash lives outside the
// repository (env or config, never site.json), an opaque session cookie
// (HttpOnly, Secure, SameSite=Strict), CSRF on every mutation, and a login
// rate-limit with progressive lockout so a stolen address is not a brute-force
// oracle.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters (§14): OWASP-ish defaults — 64 MiB, 3 passes, 4 lanes.
// Encoded into every hash so a future tuning stays verifiable against old ones.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
	saltLen      = 16
)

// HashPassword returns a self-describing argon2id hash string:
//
//	argon2id$v=19$m=65536,t=3,p=4$<salt b64>$<key b64>
//
// suitable for storing in an env var or config file (never the repo).
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword checks a password against an encoded hash in constant time.
func VerifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "argon2id" {
		return false
	}
	var mem, t, p uint32
	if _, err := fmt.Sscanf(parts[2], "m=%d,t=%d,p=%d", &mem, &t, &p); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, t, mem, uint8(p), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// --- sessions -------------------------------------------------------------------

// Sessions is an in-memory session store: opaque tokens with an expiry. The
// admin is single, so this is a small set; it is cleared on process restart,
// which is the correct security posture for an ephemeral editor.
type Sessions struct {
	mu  sync.Mutex
	tok map[string]time.Time // token → expiry
	ttl time.Duration
}

// NewSessions builds a store whose sessions live for ttl.
func NewSessions(ttl time.Duration) *Sessions {
	return &Sessions{tok: map[string]time.Time{}, ttl: ttl}
}

// Create mints a new session token valid for the store's ttl. now is injected
// so the store stays testable without a wall clock.
func (s *Sessions) Create(now time.Time) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	s.mu.Lock()
	s.tok[token] = now.Add(s.ttl)
	s.mu.Unlock()
	return token, nil
}

// Valid reports whether token names a live session, sweeping it if expired.
func (s *Sessions) Valid(token string, now time.Time) bool {
	if token == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.tok[token]
	if !ok {
		return false
	}
	if now.After(exp) {
		delete(s.tok, token)
		return false
	}
	return true
}

// Revoke drops a session (logout).
func (s *Sessions) Revoke(token string) {
	s.mu.Lock()
	delete(s.tok, token)
	s.mu.Unlock()
}

// --- login rate limit -----------------------------------------------------------

// Limiter throttles login attempts with progressive lockout (§14): after a
// threshold of failures the door closes for a window that grows with each
// further failure, so brute force is not merely slow but self-defeating.
type Limiter struct {
	mu        sync.Mutex
	failures  int
	lockUntil time.Time
}

// NewLimiter builds a login limiter.
func NewLimiter() *Limiter { return &Limiter{} }

const (
	freeAttempts = 5               // failures before lockout begins
	baseLock     = 5 * time.Second // first lockout window
	maxLock      = 15 * time.Minute
)

// Allow reports whether a login attempt may proceed now.
func (l *Limiter) Allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return now.After(l.lockUntil)
}

// Fail records a failed attempt, extending the lockout once past the free
// threshold; the window doubles with each excess failure, capped.
func (l *Limiter) Fail(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures++
	if l.failures <= freeAttempts {
		return
	}
	excess := l.failures - freeAttempts
	window := baseLock << (excess - 1)
	if window > maxLock || window <= 0 {
		window = maxLock
	}
	l.lockUntil = now.Add(window)
}

// Succeed resets the limiter after a successful login.
func (l *Limiter) Succeed() {
	l.mu.Lock()
	l.failures = 0
	l.lockUntil = time.Time{}
	l.mu.Unlock()
}
