package secretbox

import (
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	c := New("a-strong-random-key-value")

	ct := c.Encrypt("sk-live-supersecret-123")
	if ct == "sk-live-supersecret-123" || !strings.HasPrefix(ct, prefix) {
		t.Fatalf("value was not encrypted: %q", ct)
	}
	if got := c.Decrypt(ct); got != "sk-live-supersecret-123" {
		t.Fatalf("round-trip failed: %q", got)
	}
	// Idempotent: encrypting an already-encrypted value is a no-op.
	if again := c.Encrypt(ct); again != ct {
		t.Errorf("double-encryption changed the value")
	}
	// Legacy plaintext (no prefix) and empty pass through unchanged.
	if got := c.Decrypt("legacy-plaintext"); got != "legacy-plaintext" {
		t.Errorf("legacy plaintext should read through: %q", got)
	}
	if c.Encrypt("") != "" {
		t.Errorf("empty should pass through")
	}
	// A different key must not decrypt.
	if got := New("some-other-key").Decrypt(ct); got == "sk-live-supersecret-123" {
		t.Errorf("ciphertext decrypted under the wrong key")
	}
}

func TestDisabledCipherPassesThrough(t *testing.T) {
	d := New("") // no key -> disabled
	if d.Enabled() {
		t.Fatal("empty key should be disabled")
	}
	if d.Encrypt("secret") != "secret" {
		t.Errorf("disabled Encrypt must pass through")
	}
	if d.Decrypt("enc:v1:whatever") != "enc:v1:whatever" {
		t.Errorf("disabled Decrypt must pass through")
	}
}
