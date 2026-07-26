package api

import "github.com/bright-interaction/flare/internal/secretbox"

// secretColumnForUpdate picks what to write into an at-rest secret column on an
// update where the client may omit the secret to keep the existing one.
//
// The two inputs are at different trust levels and that is the whole point:
//
//   - supplied comes off the request body. It is untrusted, so it ALWAYS goes
//     through Encrypt, even if it already looks like ciphertext. Skipping that
//     is a decryption oracle: an attacker who lifted ciphertext from a stolen
//     database copy could submit it prefixed, have it stored verbatim, and let
//     the server decrypt it for them on the next read.
//   - stored was read out of the database and is already encrypted at rest, so
//     it is carried through untouched. Re-encrypting it would double-wrap and
//     orphan the row, which is how the naive version of this fix breaks every
//     saved config on its next update.
//
// Callers must still reject the case where both are empty; that is a validation
// error with a message specific to the surface, not a storage decision.
func secretColumnForUpdate(c *secretbox.Cipher, supplied, stored string) string {
	if supplied == "" {
		return stored
	}
	return c.Encrypt(supplied)
}
