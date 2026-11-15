package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP parameters. The panel implements the RFC 6238 defaults that every
// authenticator app supports: SHA-1, six digits, a 30 second step. The window is
// widened by one step on each side (TOTPSkewSteps) because a phone whose clock
// is half a minute off would otherwise be locked out of its own panel.
const (
	TOTPDigits    = 6
	TOTPPeriod    = 30 * time.Second
	TOTPSkewSteps = 1
	// totpSecretBytes is the recommended secret size for HMAC-SHA1 in RFC 4226
	// and what every authenticator app expects (20 bytes = 32 base32 chars).
	totpSecretBytes = 20
)

// TOTPIssuer labels the account inside the authenticator app.
const TOTPIssuer = "sspanel"

// totpEncoding is unpadded uppercase base32, the encoding authenticator apps
// read from an otpauth:// URI and from a typed secret.
var totpEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateTOTPSecret returns a fresh random TOTP secret in unpadded base32.
func GenerateTOTPSecret() (string, error) {
	buf := make([]byte, totpSecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate totp secret: %w", err)
	}
	return totpEncoding.EncodeToString(buf), nil
}

// NormalizeTOTPSecret accepts a secret typed or pasted by an operator: it
// removes spaces and hyphens and upper-cases it, so a secret copied out of an
// authenticator app still decodes.
func NormalizeTOTPSecret(secret string) string {
	cleaned := strings.ToUpper(strings.TrimSpace(secret))
	cleaned = strings.NewReplacer(" ", "", "\t", "", "-", "", "_", "").Replace(cleaned)
	return strings.TrimRight(cleaned, "=")
}

// decodeTOTPSecret decodes a base32 secret, tolerating case and separators.
func decodeTOTPSecret(secret string) ([]byte, error) {
	normalized := NormalizeTOTPSecret(secret)
	if normalized == "" {
		return nil, errors.New("empty totp secret")
	}
	key, err := totpEncoding.DecodeString(normalized)
	if err != nil {
		return nil, fmt.Errorf("decode totp secret: %w", err)
	}
	if len(key) == 0 {
		return nil, errors.New("empty totp key")
	}
	return key, nil
}

// TOTPStep returns the time step a clock time belongs to.
func TOTPStep(at time.Time) int64 {
	return at.Unix() / int64(TOTPPeriod/time.Second)
}

// TOTPCodeAt computes the six digit code for one time step. It is exported so
// tests and the enrollment dialog can mirror what an authenticator app shows.
func TOTPCodeAt(secret string, step int64) (string, error) {
	key, err := decodeTOTPSecret(secret)
	if err != nil {
		return "", err
	}
	if step < 0 {
		return "", errors.New("negative totp step")
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	// Dynamic truncation, RFC 4226 section 5.3.
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", TOTPDigits, value%1_000_000), nil
}

// TOTPCode returns the code valid at the given instant.
func TOTPCode(secret string, at time.Time) (string, error) {
	return TOTPCodeAt(secret, TOTPStep(at))
}

// VerifyTOTP checks code against the steps around at and returns the step that
// matched. The comparison is constant time, and every candidate step is tried
// without an early exit so the response time does not reveal which one hit.
func VerifyTOTP(secret, code string, at time.Time) (int64, bool) {
	code = strings.TrimSpace(code)
	if len(code) != TOTPDigits {
		return 0, false
	}
	for _, digit := range code {
		if digit < '0' || digit > '9' {
			return 0, false
		}
	}
	current := TOTPStep(at)
	var matched int64
	found := 0
	for delta := -int64(TOTPSkewSteps); delta <= int64(TOTPSkewSteps); delta++ {
		step := current + delta
		if step < 0 {
			continue
		}
		expected, err := TOTPCodeAt(secret, step)
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			matched = step
			found = 1
		}
	}
	return matched, found == 1
}

// TOTPURI renders the otpauth:// URI an authenticator app scans.
func TOTPURI(secret, account string) string {
	label := url.PathEscape(TOTPIssuer + ":" + account)
	query := url.Values{}
	query.Set("secret", NormalizeTOTPSecret(secret))
	query.Set("issuer", TOTPIssuer)
	query.Set("algorithm", "SHA1")
	query.Set("digits", fmt.Sprint(TOTPDigits))
	query.Set("period", fmt.Sprint(int(TOTPPeriod/time.Second)))
	return "otpauth://totp/" + label + "?" + query.Encode()
}

// Recovery code parameters. Ten codes of 60 bits each is far more than a
// guessing attack can walk through, and the alphabet omits characters that are
// easy to confuse when an operator reads a code off a screen.
const (
	RecoveryCodeCount  = 10
	RecoveryCodeLength = 12
)

const recoveryAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// recoveryCodeDomain separates a recovery code hash from any other hash in the
// system so a hash of the same string could never be reused elsewhere.
const recoveryCodeDomain = "sspanel-recovery-code:v1:"

// GenerateRecoveryCodes returns n fresh recovery codes in canonical form
// (uppercase, no separators).
func GenerateRecoveryCodes(n int) ([]string, error) {
	if n <= 0 {
		n = RecoveryCodeCount
	}
	codes := make([]string, 0, n)
	seen := make(map[string]bool, n)
	for len(codes) < n {
		code, err := randomRecoveryCode()
		if err != nil {
			return nil, err
		}
		if seen[code] {
			continue
		}
		seen[code] = true
		codes = append(codes, code)
	}
	return codes, nil
}

func randomRecoveryCode() (string, error) {
	// The alphabet has 32 characters and 32 divides 256, so mapping each random
	// byte through the modulo is exactly uniform (no rejection sampling needed).
	buf := make([]byte, RecoveryCodeLength)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate recovery code: %w", err)
	}
	var b strings.Builder
	b.Grow(RecoveryCodeLength)
	for _, value := range buf {
		b.WriteByte(recoveryAlphabet[int(value)%len(recoveryAlphabet)])
	}
	return b.String(), nil
}

// NormalizeRecoveryCode puts a typed or pasted recovery code into canonical
// form: separators removed, uppercase. A recovery code is case insensitive
// because an operator may type it from a printed sheet.
func NormalizeRecoveryCode(code string) string {
	normalized := strings.ToUpper(strings.TrimSpace(code))
	normalized = strings.NewReplacer(" ", "", "\t", "", "-", "", "_", "").Replace(normalized)
	return normalized
}

// HashRecoveryCode returns the stored form of a recovery code. Only the hash is
// kept: the codes are credentials and the database already holds the material
// to forge a session, so a leak must not also hand out a login bypass. SHA-256
// without a salt is enough here because the input has 60 bits of entropy and is
// never stored anywhere in plain form.
func HashRecoveryCode(code string) string {
	sum := sha256.Sum256([]byte(recoveryCodeDomain + NormalizeRecoveryCode(code)))
	return hex.EncodeToString(sum[:])
}

// LooksLikeTOTPCode reports whether value is a six digit TOTP code rather than
// a recovery code, so the login step knows which store to consult.
func LooksLikeTOTPCode(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != TOTPDigits {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

// FormatRecoveryCode renders a canonical recovery code in the grouped form an
// operator reads: XXXX-XXXX-XXXX.
func FormatRecoveryCode(code string) string {
	normalized := NormalizeRecoveryCode(code)
	if len(normalized) != RecoveryCodeLength {
		return normalized
	}
	return normalized[0:4] + "-" + normalized[4:8] + "-" + normalized[8:12]
}
