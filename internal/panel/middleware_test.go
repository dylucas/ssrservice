package panel

import "testing"

// TestRedactSubscriptionPath guards the access-log redaction of subscription
// tokens.
//
// The previous implementation concatenated the base path and the "sub/" prefix
// without a separator, so a panel mounted at "/panel" logged the token of
// "/panel/sub/<token>" verbatim while a root deployment redacted it. That is a
// silent credential leak, and it is invisible unless a sub-path deployment is
// actually exercised.
func TestRedactSubscriptionPath(t *testing.T) {
	const token = "S3cretSubToken"
	cases := []struct {
		name     string
		basePath string
		path     string
		want     string
	}{
		{"root subscription", "/", "/sub/" + token, "/sub/<redacted>"},
		{"sub path subscription", "/panel", "/panel/sub/" + token, "/panel/sub/<redacted>"},
		{"nested base path", "/a/b", "/a/b/sub/" + token, "/a/b/sub/<redacted>"},
		{"base path with trailing slash", "/panel/", "/panel/sub/" + token, "/panel/sub/<redacted>"},
		{"root api untouched", "/", "/api/v1/clients", "/api/v1/clients"},
		{"sub path api untouched", "/panel", "/panel/api/v1/subscriptions", "/panel/api/v1/subscriptions"},
		{"similar prefix is not a match", "/panel", "/panel/subscriptions/x", "/panel/subscriptions/x"},
		{"root similar prefix is not a match", "/", "/submarine", "/submarine"},
		{"plain page untouched", "/panel", "/panel/clients", "/panel/clients"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactSubscriptionPath(tc.basePath, tc.path); got != tc.want {
				t.Fatalf("redactSubscriptionPath(%q, %q) = %q, want %q",
					tc.basePath, tc.path, got, tc.want)
			}
		})
	}
}
