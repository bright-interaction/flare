// Package id centralizes identifier and ingest-token generation.
package id

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/nrednav/cuid2"
)

// New returns a collision-resistant sortable-ish unique id (cuid2), used for
// every primary key in Flare.
func New() string { return cuid2.Generate() }

// Token returns a random hex string of nbytes entropy. Used for project
// ingest keys (the public half of a DSN) and similar opaque tokens.
func Token(nbytes int) (string, error) {
	b := make([]byte, nbytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
