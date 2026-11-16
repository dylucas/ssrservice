package auth

import (
	"errors"
	"testing"
	"time"
)

func TestPasswordHashAndVerify(t *testing.T) {
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if hash == "correct horse battery" {
		t.Fatal("password stored in plain text")
	}
	if !VerifyPassword(hash, "correct horse battery") {
		t.Fatal("correct password rejected")
	}
	if VerifyPassword(hash, "wrong") {
		t.Fatal("wrong password accepted")
	}
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("short password should be rejected")
	}
}

func TestSessions(t *testing.T) {
	sessions, err := NewSessions([]byte("0123456789abcdef0123456789abcdef"), time.Hour)
	if err != nil {
		t.Fatalf("new sessions: %v", err)
	}
	now := time.Now()
	value, expires, err := sessions.Issue(7, "hash-a", false, now)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if !expires.After(now) {
		t.Fatal("session must expire in the future")
	}
	id, version, err := sessions.Parse(value, now)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if id != 7 || version != SessionVersion("hash-a", false) {
		t.Fatalf("unexpected payload id=%d version=%q", id, version)
	}

	// Tampering invalidates the cookie.
	if _, _, err := sessions.Parse(value+"x", now); err == nil {
		t.Fatal("tampered cookie accepted")
	}
	// Expiry is enforced.
	if _, _, err := sessions.Parse(value, now.Add(2*time.Hour)); err == nil {
		t.Fatal("expired session accepted")
	}
	// A different secret cannot verify.
	other, _ := NewSessions([]byte("ffffffffffffffffffffffffffffffff"), time.Hour)
	if _, _, err := other.Parse(value, now); err == nil {
		t.Fatal("cookie verified with a different secret")
	}
}

func TestLoginLimiter(t *testing.T) {
	limiter := NewLoginLimiter(2, time.Minute)
	now := time.Now()
	if !limiter.Allow("1.2.3.4", now) {
		t.Fatal("first attempt should be allowed")
	}
	limiter.Fail("1.2.3.4", now)
	limiter.Fail("1.2.3.4", now)
	if limiter.Allow("1.2.3.4", now) {
		t.Fatal("third attempt should be blocked")
	}
	if !limiter.Allow("5.6.7.8", now) {
		t.Fatal("other IPs must not be affected")
	}
	// The window expires.
	if !limiter.Allow("1.2.3.4", now.Add(2*time.Minute)) {
		t.Fatal("attempt after the window should be allowed")
	}
	limiter.Reset("1.2.3.4")
	if !limiter.Allow("1.2.3.4", now) {
		t.Fatal("reset should clear failures")
	}
}

func TestRandomPassword(t *testing.T) {
	a, err := RandomPassword(20)
	if err != nil {
		t.Fatalf("random: %v", err)
	}
	b, err := RandomPassword(20)
	if err != nil {
		t.Fatalf("random: %v", err)
	}
	if len(a) != 20 || a == b {
		t.Fatalf("unexpected passwords %q %q", a, b)
	}
}

func TestChallengeTokens(t *testing.T) {
	sessions, err := NewSessions([]byte("0123456789abcdef0123456789abcdef"), time.Hour)
	if err != nil {
		t.Fatalf("new sessions: %v", err)
	}
	now := time.Now()
	value, expires, err := sessions.IssueChallenge(9, "hash-a", false, now)
	if err != nil {
		t.Fatalf("issue challenge: %v", err)
	}
	if !expires.After(now) {
		t.Fatal("challenge must expire in the future")
	}
	// The window is minutes, not the session lifetime.
	if expires.Sub(now) > 10*time.Minute {
		t.Fatalf("challenge lifetime %v is too long", expires.Sub(now))
	}
	id, version, err := sessions.ParseChallenge(value, now)
	if err != nil {
		t.Fatalf("parse challenge: %v", err)
	}
	if id != 9 || version != SessionVersion("hash-a", false) {
		t.Fatalf("unexpected challenge payload id=%d version=%q", id, version)
	}

	// A challenge does not double as a session cookie and vice versa: the two
	// carry different grants and must never be interchangeable.
	if _, _, err := sessions.Parse(value, now); err == nil {
		t.Fatal("challenge accepted as a session")
	}
	session, _, err := sessions.Issue(9, "hash-a", false, now)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	if _, _, err := sessions.ParseChallenge(session, now); err == nil {
		t.Fatal("session accepted as a challenge")
	}

	// Tampering and a different signing key are rejected.
	if _, _, err := sessions.ParseChallenge(value+"x", now); err == nil {
		t.Fatal("tampered challenge accepted")
	}
	other, _ := NewSessions([]byte("ffffffffffffffffffffffffffffffff"), time.Hour)
	if _, _, err := other.ParseChallenge(value, now); err == nil {
		t.Fatal("challenge verified with a different secret")
	}

	// Expiry is reported separately so the login page can send the operator back
	// to the password step instead of showing "invalid credentials".
	if _, _, err := sessions.ParseChallenge(value, now.Add(6*time.Minute)); !errors.Is(err, ErrExpiredChallenge) {
		t.Fatalf("expired challenge error = %v, want ErrExpiredChallenge", err)
	}

	// The session version ties the challenge to the password it was issued for.
	if version == SessionVersion("hash-b", false) {
		t.Fatal("session version must depend on the password hash")
	}
}

// TestSessionVersionTracksTwoStepVerification guards the eviction rule: enabling
// or removing the second factor has to invalidate sessions issued before the
// change, otherwise a cookie stolen while the account had one factor keeps
// working after the operator adds the second.
func TestSessionVersionTracksTwoStepVerification(t *testing.T) {
	withPassword := SessionVersion("hash-a", false)
	withTOTP := SessionVersion("hash-a", true)
	if withPassword == withTOTP {
		t.Fatal("session version must change when two-step verification changes")
	}
	if SessionVersion("hash-b", true) == withTOTP {
		t.Fatal("session version must still change when the password changes")
	}
	// The value is stable for the same inputs, and short enough to ride in a
	// cookie payload.
	if SessionVersion("hash-a", true) != withTOTP {
		t.Fatal("session version is not deterministic")
	}
	if len(withTOTP) > 16 {
		t.Fatalf("session version is %d characters", len(withTOTP))
	}
}
