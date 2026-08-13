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
	if got, err := c.Decrypt(ct); err != nil || got != "sk-live-supersecret-123" {
		t.Fatalf("round-trip failed: %q (%v)", got, err)
	}
	// Encrypt is NOT idempotent, deliberately. It must re-encrypt even something
	// that already looks encrypted, or a client can post lifted ciphertext and
	// have the server decrypt it (see TestEncryptIsNotADecryptionOracle). Keeping
	// an existing secret is the caller's job via api.secretColumnForUpdate.
	if viaRequest := c.Encrypt(ct); viaRequest == ct {
		t.Errorf("Encrypt passed ciphertext through: decryption oracle")
	}
	// Legacy plaintext (no prefix) and empty pass through unchanged.
	if got, _ := c.Decrypt("legacy-plaintext"); got != "legacy-plaintext" {
		t.Errorf("legacy plaintext should read through: %q", got)
	}
	if c.Encrypt("") != "" {
		t.Errorf("empty should pass through")
	}
	// A different key must not decrypt, and must not hand back the CIPHERTEXT
	// either: that is what sent "enc:v1:<base64>" to three third parties as if
	// it were the credential.
	got, err := New("some-other-key").Decrypt(ct)
	if got == "sk-live-supersecret-123" {
		t.Errorf("ciphertext decrypted under the wrong key")
	}
	if err == nil {
		t.Errorf("a wrong key must be an error, not a silent fallback")
	}
	if got == ct {
		t.Errorf("Decrypt handed back the ciphertext as if it were the credential")
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
	if got, err := d.Decrypt("enc:v1:whatever"); err != nil || got != "enc:v1:whatever" {
		t.Errorf("disabled Decrypt must pass through, got %q (%v)", got, err)
	}
}
