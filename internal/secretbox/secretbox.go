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
	"errors"
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

// Encrypt returns a versioned base64 ciphertext. Empty input and the disabled
// cipher pass through unchanged.
//
// This is the REQUEST-side helper: it ALWAYS encrypts non-empty input, including
// input that already carries the prefix. Anything a client can influence must
// come through here.
//
// It used to skip input that already looked encrypted, which made the write path
// trust a string the client controls. Integration secrets are client-settable
// (a GitHub token, an AI provider key, an OIDC client secret), so an attacker
// holding ciphertext lifted from a stolen database could submit it prefixed,
// have it stored verbatim, and have the server decrypt it for them. The AI
// config makes that a clean exfiltration: the client picks BOTH base_url and
// api_key, so the decrypted victim key gets sent as a Bearer token to a host the
// attacker chose. See TestEncryptIsNotADecryptionOracle.
//
// There is deliberately NO idempotent counterpart here. An update that keeps an
// existing secret must carry the stored value through untouched rather than
// re-encrypt it; see api.secretColumnForUpdate. Exporting a "skip it if it
// already looks encrypted" helper from this package would just be the oracle
// again, one call site away.
func (c *Cipher) Encrypt(plaintext string) string {
	if !c.Enabled() || plaintext == "" {
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

// ErrUndecryptable is returned when a value carries the version prefix and
// cannot be opened: a rotated or mistyped FLARE_SECRET_KEY, a backup restored
// against a differently-keyed instance, or a corrupted column.
var ErrUndecryptable = errors.New("secretbox: value cannot be decrypted with the configured key")

// Decrypt reverses Encrypt.
//
// It FAILS CLOSED. It used to return the input on every failure path, which
// meant a wrong key made it hand back the CIPHERTEXT as if it were the
// credential. The server booted green and then:
//
//	ai_handlers     sent   x-api-key: enc:v1:<base64>   to the BYOAI endpoint
//	github_handlers sent   Authorization: Bearer enc:v1:... to api.github.com
//	oidc_handlers   POSTed client_secret=enc:v1:...     to the IdP
//
// so the ciphertext of every org's live credential ended up in three third
// parties' access logs, every integration was silently broken, and nothing in
// any error named the cause. This is the estate's own recorded shape: a read
// helper that returns a fallback on failure.
//
// A value WITHOUT the prefix is legacy plaintext from before encryption was
// introduced and is still returned as-is; that is a real state on deployed
// databases, not an error.
func (c *Cipher) Decrypt(s string) (string, error) {
	if !strings.HasPrefix(s, prefix) || !c.Enabled() {
		return s, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, prefix))
	if err != nil || len(raw) < c.aead.NonceSize() {
		return "", ErrUndecryptable
	}
	nonce, ct := raw[:c.aead.NonceSize()], raw[c.aead.NonceSize():]
	pt, err := c.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", ErrUndecryptable
	}
	return string(pt), nil
}
