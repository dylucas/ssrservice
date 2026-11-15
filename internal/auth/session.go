package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvalidSession is returned when a session cookie cannot be verified.
var ErrInvalidSession = errors.New("invalid session")

// SessionCookieName is the name of the panel session cookie.
const SessionCookieName = "sspanel_session"

// challengeTTL bounds how long a second-factor challenge stays usable. It is
// short on purpose: the token is not a session, it only carries the fact that
// the password step succeeded, and a minute-scale window is enough to type six
// digits.
const challengeTTL = 5 * time.Minute

// challengePrefix distinguishes a second-factor challenge from a session cookie.
// The two are signed with the same key but never accepted in each other's place.
const challengePrefix = "c1."

type sessionPayload struct {
	AdminID int64  `json:"a"`
	Expires int64  `json:"e"`
	Version string `json:"v"`
}

// Sessions issues and verifies signed session cookies.
type Sessions struct {
	secret []byte
	ttl    time.Duration
}

// NewSessions creates a session manager. secret must be at least 32 bytes.
func NewSessions(secret []byte, ttl time.Duration) (*Sessions, error) {
	if len(secret) < 16 {
		return nil, errors.New("session secret must be at least 16 bytes")
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &Sessions{secret: secret, ttl: ttl}, nil
}

// SessionVersion derives a short value from the password hash so that changing
// the password invalidates existing sessions.
func SessionVersion(passwordHash string) string {
	sum := sha256.Sum256([]byte("sspanel-session-version:" + passwordHash))
	return base64.RawURLEncoding.EncodeToString(sum[:8])
}

// Issue creates a signed cookie value for the administrator.
func (s *Sessions) Issue(adminID int64, passwordHash string, now time.Time) (value string, expires time.Time, err error) {
	expires = now.Add(s.ttl)
	payload := sessionPayload{
		AdminID: adminID,
		Expires: expires.Unix(),
		Version: SessionVersion(passwordHash),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("encode session: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	sig := s.sign(encoded)
	return "v1." + encoded + "." + sig, expires, nil
}

// Parse verifies a cookie value and returns the admin id it belongs to. The
// caller still has to compare the session version with the current password
// hash (see SessionVersion).
func (s *Sessions) Parse(value string, now time.Time) (adminID int64, version string, err error) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] != "v1" {
		return 0, "", ErrInvalidSession
	}
	expected := s.sign(parts[1])
	if subtle.ConstantTimeCompare([]byte(expected), []byte(parts[2])) != 1 {
		return 0, "", ErrInvalidSession
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0, "", ErrInvalidSession
	}
	var payload sessionPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return 0, "", ErrInvalidSession
	}
	if payload.AdminID <= 0 || payload.Expires < now.Unix() {
		return 0, "", ErrInvalidSession
	}
	return payload.AdminID, payload.Version, nil
}

// TTL returns the configured session lifetime.
func (s *Sessions) TTL() time.Duration { return s.ttl }

func (s *Sessions) sign(encoded string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(encoded))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// ErrExpiredChallenge is returned for a challenge that was signed correctly but
// whose validity window has passed. It is separate from ErrInvalidSession so the
// login page can send the operator back to the password step instead of telling
// them their credentials are wrong.
var ErrExpiredChallenge = errors.New("challenge expired")

// challengePayload is the signed body of a second-factor challenge. Bound to the
// administrator and to the session version, so a password change (or a different
// account) invalidates every challenge issued before it.
type challengePayload struct {
	AdminID int64  `json:"a"`
	Expires int64  `json:"e"`
	Version string `json:"v"`
}

// IssueChallenge signs a short lived token proving that the password step for
// adminID succeeded. The token is not a session: it grants nothing until a valid
// TOTP code or recovery code is supplied with it.
func (s *Sessions) IssueChallenge(adminID int64, passwordHash string, now time.Time) (value string, expires time.Time, err error) {
	expires = now.Add(challengeTTL)
	payload := challengePayload{
		AdminID: adminID,
		Expires: expires.Unix(),
		Version: SessionVersion(passwordHash),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("encode challenge: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	return challengePrefix + encoded + "." + s.sign(encoded), expires, nil
}

// ParseChallenge verifies a challenge token and returns the administrator and
// session version it was bound to.
func (s *Sessions) ParseChallenge(value string, now time.Time) (adminID int64, version string, err error) {
	if !strings.HasPrefix(value, challengePrefix) {
		return 0, "", ErrInvalidSession
	}
	parts := strings.Split(strings.TrimPrefix(value, challengePrefix), ".")
	if len(parts) != 2 {
		return 0, "", ErrInvalidSession
	}
	expected := s.sign(parts[0])
	if subtle.ConstantTimeCompare([]byte(expected), []byte(parts[1])) != 1 {
		return 0, "", ErrInvalidSession
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return 0, "", ErrInvalidSession
	}
	var payload challengePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return 0, "", ErrInvalidSession
	}
	if payload.AdminID <= 0 {
		return 0, "", ErrInvalidSession
	}
	if payload.Expires < now.Unix() {
		return 0, "", ErrExpiredChallenge
	}
	return payload.AdminID, payload.Version, nil
}
