// Package id centralizes identifier and ingest-token generation.
package id

import (
	"crypto/rand"
	"encoding/hex"
	"math/big"
	"strconv"

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

// Numeric returns an n-digit random numeric string with no leading zero. Used
// for the project id in a Sentry-style DSN: @sentry/* SDKs reject a DSN whose
// last path segment is not numeric ("Invalid projectId"), so the cuid primary
// key cannot go in the DSN.
func Numeric(n int) (string, error) {
	if n < 1 {
		n = 1
	}
	first, err := rand.Int(rand.Reader, big.NewInt(9))
	if err != nil {
		return "", err
	}
	out := strconv.FormatInt(first.Int64()+1, 10) // 1-9, never a leading zero
	for i := 1; i < n; i++ {
		d, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", err
		}
		out += strconv.FormatInt(d.Int64(), 10)
	}
	return out, nil
}
