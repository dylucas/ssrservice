package auth

import (
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
	value, expires, err := sessions.Issue(7, "hash-a", now)
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
	if id != 7 || version != SessionVersion("hash-a") {
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
