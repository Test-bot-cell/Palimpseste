package auth

import (
	"testing"
	"time"
)

func TestHashVerifyRoundTrip(t *testing.T) {
	h, err := HashPassword("corr3ct h0rse")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword("corr3ct h0rse", h) {
		t.Error("correct password rejected")
	}
	if VerifyPassword("wrong", h) {
		t.Error("wrong password accepted")
	}
}

func TestHashIsSaltedAndSelfDescribing(t *testing.T) {
	a, _ := HashPassword("same")
	b, _ := HashPassword("same")
	if a == b {
		t.Error("identical passwords produced identical hashes (no salt)")
	}
	for _, h := range []string{a, b} {
		if h[:9] != "argon2id$" {
			t.Errorf("hash not self-describing: %s", h)
		}
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	for _, bad := range []string{"", "plain", "argon2id$bad", "md5$x$y$z$w"} {
		if VerifyPassword("x", bad) {
			t.Errorf("malformed hash %q accepted", bad)
		}
	}
}

func TestSessionsLifecycle(t *testing.T) {
	s := NewSessions(time.Hour)
	now := time.Unix(1_700_000_000, 0)
	tok, err := s.Create(now)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Valid(tok, now) {
		t.Error("fresh session invalid")
	}
	if s.Valid(tok, now.Add(2*time.Hour)) {
		t.Error("expired session still valid")
	}
	// Expiry swept it; even at a valid time it is gone now.
	tok2, _ := s.Create(now)
	s.Revoke(tok2)
	if s.Valid(tok2, now) {
		t.Error("revoked session still valid")
	}
	if s.Valid("", now) || s.Valid("garbage", now) {
		t.Error("bogus token accepted")
	}
}

// §14: progressive lockout — free attempts, then a growing window.
func TestLimiterProgressiveLockout(t *testing.T) {
	l := NewLimiter()
	now := time.Unix(1_700_000_000, 0)

	// The first freeAttempts failures do not lock the door.
	for i := 0; i < freeAttempts; i++ {
		if !l.Allow(now) {
			t.Fatalf("locked out early at attempt %d", i)
		}
		l.Fail(now)
	}
	// The next failure triggers a lockout.
	l.Fail(now)
	if l.Allow(now) {
		t.Error("door not locked after exceeding free attempts")
	}
	if !l.Allow(now.Add(baseLock + time.Second)) {
		t.Error("door not reopened after the window")
	}

	// The window grows with each further failure.
	l.Fail(now)
	l.Fail(now)
	if l.Allow(now.Add(baseLock + time.Second)) {
		t.Error("window did not grow with further failures")
	}

	// A success resets everything.
	l.Succeed()
	if !l.Allow(now) {
		t.Error("successful login did not reset the limiter")
	}
}
