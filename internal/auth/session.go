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
