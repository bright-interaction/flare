// Package auth holds session, password, and API-key primitives for Flare.
package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/alexedwards/scs/v2"
	"golang.org/x/crypto/bcrypt"

	"github.com/bright-interaction/flare/internal/id"
)

// NewSessionManager builds the scs session manager. The pgxstore is attached
// by the caller (it needs the live pool).
func NewSessionManager(lifetime, idleTimeout time.Duration, secure bool) *scs.SessionManager {
	m := scs.New()
	m.Lifetime = lifetime
	m.IdleTimeout = idleTimeout
	m.Cookie.Name = "flare_session"
	m.Cookie.HttpOnly = true
	m.Cookie.Secure = secure
	m.Cookie.SameSite = http.SameSiteStrictMode
	m.Cookie.Path = "/"
	return m
}

// HashPassword returns a bcrypt hash (cost 12, per security rules).
func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
	return string(b), err
}

// VerifyPassword reports whether pw matches the stored bcrypt hash.
func VerifyPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// HashAPIKey returns the lookup hash stored for an API key. Keys are never
// stored in plaintext; the hash column is what we query.
func HashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// GenerateAPIKey mints a new API key, returning the plaintext (shown once),
// its lookup hash, and a short display prefix.
func GenerateAPIKey() (plaintext, hash, prefix string, err error) {
	tok, err := id.Token(24)
	if err != nil {
		return "", "", "", err
	}
	plaintext = "flr_" + tok
	hash = HashAPIKey(plaintext)
	prefix = plaintext[:12]
	return plaintext, hash, prefix, nil
}
