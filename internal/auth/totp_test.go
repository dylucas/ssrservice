package auth

import (
	"encoding/base32"
	"strings"
	"testing"
	"time"
)

// rfcSecret is the shared secret from Appendix B of RFC 6238 ("12345678901234567890"),
// encoded the way an authenticator app stores it.
func rfcSecret(t *testing.T) string {
	t.Helper()
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
}

// TestTOTPRFC6238Vectors pins the algorithm to the RFC test vectors. The RFC
// prints eight digits; its last six are the six-digit code every authenticator
// shows, which is the form this server accepts.
func TestTOTPRFC6238Vectors(t *testing.T) {
	secret := rfcSecret(t)
	cases := []struct {
		unix int64
		want string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
		{20000000000, "353130"},
	}
	for _, tc := range cases {
		got, err := TOTPCode(secret, time.Unix(tc.unix, 0))
		if err != nil {
			t.Fatalf("TOTPCode(%d): %v", tc.unix, err)
		}
		if got != tc.want {
			t.Fatalf("TOTPCode(%d) = %q, want %q", tc.unix, got, tc.want)
		}
	}
}

func TestTOTPSecretShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		secret, err := GenerateTOTPSecret()
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		// 20 bytes encodes to 32 base32 characters without padding.
		if len(secret) != 32 {
			t.Fatalf("secret %q has length %d, want 32", secret, len(secret))
		}
		if strings.ContainsAny(secret, "=abcdefghijklmnopqrstuvwxyz") {
			t.Fatalf("secret %q is not unpadded uppercase base32", secret)
		}
		if seen[secret] {
			t.Fatalf("secret repeated: %q", secret)
		}
		seen[secret] = true
		if _, err := TOTPCode(secret, time.Now()); err != nil {
			t.Fatalf("generated secret does not decode: %v", err)
		}
	}
}

func TestNormalizeTOTPSecret(t *testing.T) {
	secret := rfcSecret(t)
	// lower case, spaces and a hyphen are what a human copy/paste produces.
	messy := strings.ToLower(secret[:8]) + " " + strings.ToLower(secret[8:16]) + "-" + secret[16:]
	if got := NormalizeTOTPSecret(messy); got != secret {
		t.Fatalf("NormalizeTOTPSecret() = %q, want %q", got, secret)
	}
	if got := NormalizeTOTPSecret("  " + secret + "=  "); got != secret {
		t.Fatalf("padding was not trimmed: %q", got)
	}
}

func TestVerifyTOTPSkewWindow(t *testing.T) {
	secret := rfcSecret(t)
	now := time.Unix(1_700_000_000, 0)

	code, err := TOTPCode(secret, now)
	if err != nil {
		t.Fatalf("code: %v", err)
	}
	step, ok := VerifyTOTP(secret, code, now)
	if !ok {
		t.Fatalf("current code rejected")
	}
	if want := TOTPStep(now); step != want {
		t.Fatalf("matched step = %d, want %d", step, want)
	}

	// One step either way is accepted (a phone clock can be half a minute off).
	for _, delta := range []time.Duration{-TOTPPeriod, TOTPPeriod} {
		shifted, err := TOTPCode(secret, now.Add(delta))
		if err != nil {
			t.Fatalf("shifted code: %v", err)
		}
		if _, ok := VerifyTOTP(secret, shifted, now); !ok {
			t.Fatalf("code from %v rejected", delta)
		}
	}
	// Two steps away is not.
	for _, delta := range []time.Duration{-2 * TOTPPeriod, 2 * TOTPPeriod} {
		shifted, err := TOTPCode(secret, now.Add(delta))
		if err != nil {
			t.Fatalf("shifted code: %v", err)
		}
		if _, ok := VerifyTOTP(secret, shifted, now); ok {
			t.Fatalf("code from %v accepted", delta)
		}
	}
	// A code from another secret is rejected.
	other, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	foreign, err := TOTPCode(other, now)
	if err != nil {
		t.Fatalf("foreign code: %v", err)
	}
	if _, ok := VerifyTOTP(secret, foreign, now); ok {
		t.Fatalf("code from another secret accepted")
	}
}

func TestVerifyTOTPRejectsMalformedInput(t *testing.T) {
	secret := rfcSecret(t)
	now := time.Unix(1_700_000_000, 0)
	for _, code := range []string{"", "12345", "1234567", "abcdef", "12 345", "-12345"} {
		if _, ok := VerifyTOTP(secret, code, now); ok {
			t.Fatalf("malformed code %q accepted", code)
		}
	}
	if _, ok := VerifyTOTP("not-base32-!!!", "123456", now); ok {
		t.Fatalf("undecodable secret accepted")
	}
}

func TestTOTPURIEscapesAccount(t *testing.T) {
	secret := rfcSecret(t)
	uri := TOTPURI(secret, "admin")
	for _, want := range []string{
		"otpauth://totp/sspanel:admin?",
		"secret=" + secret,
		"issuer=sspanel",
		"algorithm=SHA1",
		"digits=6",
		"period=30",
	} {
		if !strings.Contains(uri, want) {
			t.Fatalf("URI %q is missing %q", uri, want)
		}
	}
	// A user name with a space or slash must not break the label.
	encoded := TOTPURI(secret, "a/b c")
	if strings.Contains(encoded, "a/b") {
		t.Fatalf("account was not escaped: %q", encoded)
	}
}

func TestRecoveryCodeGeneration(t *testing.T) {
	codes, err := GenerateRecoveryCodes(RecoveryCodeCount)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(codes) != RecoveryCodeCount {
		t.Fatalf("got %d codes, want %d", len(codes), RecoveryCodeCount)
	}
	seen := map[string]bool{}
	for _, code := range codes {
		if len(code) != RecoveryCodeLength {
			t.Fatalf("code %q has length %d", code, len(code))
		}
		for _, r := range code {
			if !strings.ContainsRune(recoveryAlphabet, r) {
				t.Fatalf("code %q uses %q outside the alphabet", code, r)
			}
		}
		if seen[code] {
			t.Fatalf("duplicate recovery code %q", code)
		}
		seen[code] = true
	}
	// The alphabet excludes the characters that are ambiguous with each other
	// (0/O, 1/I). L stays: it is only confusable with 1, which is not in the
	// alphabet at all.
	for _, bad := range []rune{'0', 'O', '1', 'I'} {
		if strings.ContainsRune(recoveryAlphabet, bad) {
			t.Fatalf("alphabet contains confusable character %q", bad)
		}
	}
}

func TestRecoveryCodeNormalizeAndHash(t *testing.T) {
	code := "ABCD-EFGH-JKMN"
	canonical := NormalizeRecoveryCode(code)
	if canonical != "ABCDEFGHJKMN" {
		t.Fatalf("NormalizeRecoveryCode() = %q", canonical)
	}
	// Every way of writing the code hashes the same.
	for _, variant := range []string{"abcdefghjkmn", "abcdefgh jkmn", " ABCD-EFGH-JKMN ", "ABCD_EFGH_JKMN"} {
		if HashRecoveryCode(variant) != HashRecoveryCode(code) {
			t.Fatalf("variant %q does not hash like the canonical code", variant)
		}
	}
	if HashRecoveryCode(code) == HashRecoveryCode("ABCD-EFGH-JKMP") {
		t.Fatalf("different codes hash the same")
	}
	if strings.Contains(HashRecoveryCode(code), canonical) {
		t.Fatalf("hash embeds the plaintext code")
	}
}

func TestFormatRecoveryCode(t *testing.T) {
	if got := FormatRecoveryCode("ABCDEFGHJKMN"); got != "ABCD-EFGH-JKMN" {
		t.Fatalf("FormatRecoveryCode() = %q", got)
	}
	// Input that is not a full length code is passed through normalized.
	if got := FormatRecoveryCode("abc-def"); got != "ABCDEF" {
		t.Fatalf("FormatRecoveryCode(short) = %q", got)
	}
}

func TestLooksLikeTOTPCode(t *testing.T) {
	for _, value := range []string{"123456", " 123456 "} {
		if !LooksLikeTOTPCode(value) {
			t.Fatalf("%q should look like a TOTP code", value)
		}
	}
	for _, value := range []string{"", "12345", "1234567", "ABCD-EFGH-JKMN", "12345a"} {
		if LooksLikeTOTPCode(value) {
			t.Fatalf("%q should not look like a TOTP code", value)
		}
	}
}
