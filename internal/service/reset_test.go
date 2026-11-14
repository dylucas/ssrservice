package service

import (
	"testing"
	"time"
)

func TestLastResetBoundary(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, loc)
	got := time.Unix(LastResetBoundary(now, loc, 1), 0).In(loc)
	want := time.Date(2026, 3, 1, 0, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("got %s want %s", got, want)
	}

	// Before the reset day: use the previous month, including February.
	now = time.Date(2026, 3, 2, 0, 30, 0, 0, loc)
	got = time.Unix(LastResetBoundary(now, loc, 5), 0).In(loc)
	want = time.Date(2026, 2, 5, 0, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("got %s want %s", got, want)
	}

	// Exactly at the boundary counts as reached.
	now = time.Date(2026, 1, 1, 0, 0, 0, 0, loc)
	got = time.Unix(LastResetBoundary(now, loc, 1), 0).In(loc)
	if !got.Equal(now) {
		t.Fatalf("got %s want %s", got, now)
	}
}

func TestValidHost(t *testing.T) {
	valid := []string{"example.com", "1.2.3.4", "sub.domain", "2001:db8::1"}
	for _, host := range valid {
		if !validHost(host) {
			t.Fatalf("%q should be valid", host)
		}
	}
	invalid := []string{"", "bad host", "http://x", "a/b"}
	for _, host := range invalid {
		if validHost(host) {
			t.Fatalf("%q should be invalid", host)
		}
	}
}

func TestValidateListenAddress(t *testing.T) {
	if err := validateListenAddress("127.0.0.1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := validateListenAddress("0.0.0.0"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, bad := range []string{"", "127.0.0.1:8388", "host name"} {
		if err := validateListenAddress(bad); err == nil {
			t.Fatalf("%q should be rejected", bad)
		}
	}
}
