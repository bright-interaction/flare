package secretbox

import (
	"strings"
	"testing"
)

// TestEncryptIsNotADecryptionOracle locks the CRITICAL fix.
//
// Encrypt used to pass through any input that already carried the "enc:v1:"
// prefix, so an update handler could re-store a value it never decrypted. That
// made the write path trust a string the client fully controls. Every caller of
// Encrypt takes a secret straight off a request body: a GitHub token, an AI
// provider api_key, an OIDC client_secret.
//
// So an attacker holding ciphertext lifted from an offline copy of the database
// (stolen backup, disk snapshot) but NOT the FLARE_SECRET_KEY could submit it
// prefixed and have it stored verbatim. The AI config turns that into clean
// exfiltration, because base_url is client-controlled too: the server decrypts
// the victim's key and sends it as a Bearer token to a host the attacker chose.
//
// The invariant: what a client sends is DATA, never a storage-format
// instruction. Encrypt always encrypts non-empty input.
func TestEncryptIsNotADecryptionOracle(t *testing.T) {
	c := New("test-flare-secret-key")
	if !c.Enabled() {
		t.Fatal("ABORT: cipher disabled, every assertion below would be vacuous")
	}

	const victimSecret = "ghp_victim_token_do_not_leak"
	lifted := c.Encrypt(victimSecret)

	// Guard the setup: if this is not real ciphertext the test proves nothing.
	if !strings.HasPrefix(lifted, prefix) || lifted == victimSecret {
		t.Fatalf("ABORT: lifted value is not ciphertext (%q)", lifted)
	}
	if got := c.Decrypt(lifted); got != victimSecret {
		t.Fatalf("ABORT: lifted blob does not decrypt under this key (%q), the oracle assertion would be vacuous", got)
	}
	t.Logf("attacker submits api_key = %s... (len %d)", lifted[:20], len(lifted))

	// THE ATTACK: submit the lifted ciphertext as a secret on a config the
	// attacker owns.
	stored := c.Encrypt(lifted)
	if stored == lifted {
		t.Fatal("ORACLE OPEN: attacker-supplied ciphertext stored verbatim; the read path would decrypt the victim's secret")
	}

	// The read path must hand back the attacker's own literal string.
	out := c.Decrypt(stored)
	if strings.Contains(out, victimSecret) {
		t.Fatalf("ORACLE OPEN: decrypt returned the victim's plaintext %q", victimSecret)
	}
	if out != lifted {
		t.Fatalf("round-trip mangled attacker input: got %q want %q", out, lifted)
	}
	if strings.Contains(stored, strings.TrimPrefix(lifted, prefix)) {
		t.Fatal("ORACLE OPEN: the attacker's blob survives verbatim inside the stored value")
	}
}

// TestEncryptHasNoIdempotentEscapeHatch pins the design decision that keeps the
// oracle closed: this package deliberately exposes no "encrypt only if it does
// not already look encrypted" helper.
//
// Such a helper reads as the safer, more general one, so the next person will
// reach for it, and one call on a request body reopens the oracle. Keeping an
// existing secret on update is the caller's job and is done by carrying the
// stored value through untouched (api.secretColumnForUpdate), never by asking
// the cipher to guess from the string.
func TestEncryptHasNoIdempotentEscapeHatch(t *testing.T) {
	c := New("test-flare-secret-key")

	const secret = "sk-live-provider-key"
	stored := c.Encrypt(secret)

	// Encrypting an already-encrypted value MUST change it. If this ever passes
	// through again, the oracle is back.
	if again := c.Encrypt(stored); again == stored {
		t.Fatal("Encrypt passed ciphertext through: the oracle is open again")
	}

	// Empty stays empty (no secret configured means no credential).
	if out := c.Encrypt(""); out != "" {
		t.Fatalf("Encrypt should skip empty, got %q", out)
	}

	// A disabled cipher (no FLARE_SECRET_KEY) degrades to plaintext at rest
	// rather than mangling the value, which is the documented tradeoff.
	off := New("")
	if out := off.Encrypt(secret); out != secret {
		t.Fatalf("disabled Encrypt should pass through, got %q", out)
	}
	if out := off.Decrypt(stored); out != stored {
		t.Fatalf("disabled Decrypt should pass through, got %q", out)
	}
}
