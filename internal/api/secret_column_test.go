package api

import (
	"strings"
	"testing"

	"github.com/bright-interaction/flare/internal/secretbox"
)

// TestSecretColumnForUpdate covers the exact decision the AI and OIDC config
// handlers make, which is where two different trust levels meet.
//
// It locks BOTH failure modes at once:
//
//  1. The oracle. A client-supplied value that already looks encrypted must
//     still be encrypted. Storing it verbatim let an attacker who lifted
//     ciphertext from a stolen database submit it and have the server decrypt
//     it for them. For the AI config that is exfiltration, since base_url is
//     client-controlled: the decrypted victim key goes out as a Bearer token to
//     an attacker-chosen host.
//
//  2. The regression the naive fix causes. "Blank keeps the stored one" feeds a
//     value that is ALREADY encrypted. Re-encrypting it double-wraps the row, so
//     the next Decrypt yields "enc:v1:..." instead of the key and every saved AI
//     and OIDC config silently breaks on its next update.
//
// Both must hold at the same time. Fixing either one alone is a bug.
func TestSecretColumnForUpdate(t *testing.T) {
	c := secretbox.New("test-flare-secret-key")
	if !c.Enabled() {
		t.Fatal("ABORT: cipher disabled, the assertions below would be vacuous")
	}

	const realKey = "sk-live-provider-key"
	atRest := c.Encrypt(realKey)
	if atRest == realKey || c.Decrypt(atRest) != realKey {
		t.Fatalf("ABORT: setup did not produce usable ciphertext (%q)", atRest)
	}

	// 1. Client supplies a new key: encrypted, and it round-trips.
	col := secretColumnForUpdate(c, realKey, "")
	if col == realKey {
		t.Fatal("supplied key was stored in cleartext")
	}
	if got := c.Decrypt(col); got != realKey {
		t.Fatalf("supplied key did not round-trip: %q", got)
	}

	// 2. Blank supplied: the stored ciphertext is carried through UNTOUCHED and
	// still decrypts. This is the double-encryption regression guard.
	col = secretColumnForUpdate(c, "", atRest)
	if col != atRest {
		t.Fatal("stored secret was re-encrypted on a keep-existing update: double-wrapped and orphaned")
	}
	if got := c.Decrypt(col); got != realKey {
		t.Fatalf("stored secret no longer decrypts after a keep-existing update: %q", got)
	}

	// Repeated keep-existing updates stay stable (admins toggling `enabled`).
	for i := 0; i < 3; i++ {
		col = secretColumnForUpdate(c, "", col)
	}
	if got := c.Decrypt(col); got != realKey {
		t.Fatalf("secret broke after repeated keep-existing updates: %q", got)
	}

	// 3. THE ATTACK: client supplies ciphertext lifted from a stolen DB copy.
	// It must be treated as data and encrypted, never stored verbatim.
	lifted := c.Encrypt("victim-github-token")
	col = secretColumnForUpdate(c, lifted, "")
	if col == lifted {
		t.Fatal("ORACLE OPEN: attacker-supplied ciphertext stored verbatim; the read path would decrypt the victim's secret")
	}
	if got := c.Decrypt(col); got != lifted {
		t.Fatalf("attacker input should come back as their own literal string, got %q", got)
	}
	if strings.Contains(c.Decrypt(col), "victim-github-token") {
		t.Fatal("ORACLE OPEN: the victim's plaintext was returned")
	}

	// The attacker's blob must not survive verbatim inside what we stored.
	if strings.Contains(col, strings.TrimPrefix(lifted, "enc:v1:")) {
		t.Fatal("ORACLE OPEN: attacker blob embedded verbatim in the stored column")
	}
}
