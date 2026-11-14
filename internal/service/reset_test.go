package service

import (
	"strings"
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

// TestValidateHostValue covers the strict host validation used for the inbound
// domain and the global public_host setting.
func TestValidateHostValue(t *testing.T) {
	valid := []string{
		"example.com",
		"sub.domain.example.com",
		"hk-node-1.example.com",
		"1.2.3.4",
		"2001:db8::1",
		"a.b",
		strings.Repeat("a", 63) + ".com",
	}
	for _, host := range valid {
		if err := validateHostValue(host); err != nil {
			t.Fatalf("%q should be valid: %v", host, err)
		}
	}

	// Each entry documents a value that would break an ss:// link if accepted.
	// validateHostValue expects an already normalized value: callers run
	// normalizeHost first, so a value of spaces arrives as "" there.
	invalid := map[string]string{
		"":                                "empty",
		" ":                               "invalid character",
		"bad host":                        "invalid character",
		"http://example.com":              "invalid character",
		"example.com/x":                   "invalid character",
		"example.com:8388":                "invalid character",
		"[::1]":                           "invalid character",
		"a..b":                            "empty labels",
		"example..com":                    "empty labels",
		"-bad.example.com":                "hyphen",
		"bad-.example.com":                "hyphen",
		"bad_host.example.com":            "invalid character",
		"fe80::1%en0":                     "zone",
		strings.Repeat("a", 64):           "at most 63",
		strings.Repeat("a", 250) + ".com": "at most 253",
	}
	for host, hint := range invalid {
		err := validateHostValue(host)
		if err == nil {
			t.Fatalf("%q should be rejected", host)
		}
		if !strings.Contains(err.Error(), hint) {
			t.Fatalf("error for %q = %v, want it to mention %q", host, err, hint)
		}
	}
}

// TestNormalizeHost documents the canonical form stored in the database.
func TestNormalizeHost(t *testing.T) {
	cases := map[string]string{
		"  Example.COM.  ": "example.com",
		"example.com":      "example.com",
		".":                "",
		"":                 "",
		"1.2.3.4":          "1.2.3.4",
	}
	for input, want := range cases {
		if got := normalizeHost(input); got != want {
			t.Fatalf("normalizeHost(%q) = %q, want %q", input, got, want)
		}
	}
}
