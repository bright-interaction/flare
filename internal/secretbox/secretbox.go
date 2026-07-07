// Package secretbox encrypts integration secrets (BYOAI key, OIDC client
// secret, GitHub token, channel webhook URLs) at rest with AES-256-GCM, so a
// database dump or backup leak does not expose live third-party credentials.
//
// It is deliberately fail-safe and non-breaking:
//   - No key configured -> Encrypt is a pass-through (values stay plaintext) and
//     the service still boots. Encryption simply activates once the key exists.
//   - Decrypt returns any value lacking the version prefix as-is, so rows
//     written before the key was provisioned keep reading; they get encrypted on
//     their next write.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"log/slog"
	"strings"
)

const prefix = "enc:v1:"

type Cipher struct {
	aead    cipher.AEAD
	enabled bool
}

// New derives an AES-256-GCM cipher from secret (any non-empty string; the AES
// key is sha256(secret)). An empty or invalid secret yields a disabled cipher
// rather than an error, so a missing key degrades to plaintext-at-rest instead
// of taking down auth/triage/alerts.
func New(secret string) *Cipher {
	if strings.TrimSpace(secret) == "" {
		slog.Warn("FLARE_SECRET_KEY not set: integration secrets are stored UNENCRYPTED at rest")
		return &Cipher{}
	}
	sum := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		slog.Error("secretbox: cipher init failed, encryption disabled", "err", err)
		return &Cipher{}
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		slog.Error("secretbox: gcm init failed, encryption disabled", "err", err)
		return &Cipher{}
	}
	return &Cipher{aead: aead, enabled: true}
}

// Enabled reports whether a usable key is configured.
func (c *Cipher) Enabled() bool { return c != nil && c.enabled }

// Encrypt returns a versioned base64 ciphertext. Empty input, an
// already-encrypted value, and the disabled cipher all pass through unchanged.
func (c *Cipher) Encrypt(plaintext string) string {
	if !c.Enabled() || plaintext == "" || strings.HasPrefix(plaintext, prefix) {
		return plaintext
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		slog.Error("secretbox: nonce read failed; storing plaintext", "err", err)
		return plaintext
	}
	ct := c.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return prefix + base64.StdEncoding.EncodeToString(ct)
}

// Decrypt reverses Encrypt. A value without the version prefix is legacy
// plaintext and returned unchanged; a value that cannot be decrypted is also
// returned unchanged (never panics, never drops the row).
func (c *Cipher) Decrypt(s string) string {
	if !strings.HasPrefix(s, prefix) || !c.Enabled() {
		return s
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, prefix))
	if err != nil || len(raw) < c.aead.NonceSize() {
		return s
	}
	nonce, ct := raw[:c.aead.NonceSize()], raw[c.aead.NonceSize():]
	pt, err := c.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return s
	}
	return string(pt)
}
