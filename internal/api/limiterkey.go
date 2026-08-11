package api

import (
	"crypto/sha256"
	"encoding/hex"
)

// limiterEmailKey turns a caller-supplied email into a FIXED-WIDTH rate-limiter
// bucket key.
//
// The email arrives straight off an unauthenticated request body with no length
// or shape check, and it was concatenated into the login and password-reset
// limiter keys verbatim. Limiter.Allow creates the map entry before any
// database work and holds it for the whole 15-minute window, so each distinct
// oversized email retained its own multi-megabyte key:
//
//	 500 unauthenticated POSTs ->   483 MiB retained
//	2000 unauthenticated POSTs ->  1924 MiB retained
//
// Linear, unauthenticated, and no account needed. maxKeys does not help because
// it bounds the NUMBER of entries, not their size, so 200,000 entries of 1 MiB
// each is an authorised ~195 GiB.
//
// Hashing rather than truncating: two different long emails must not collapse
// into one bucket, or one attacker's traffic would lock out another user's
// login. sha256 keeps buckets distinct at a fixed 32 bytes. The value is only
// ever a map key, never logged or displayed, so it does not need to be legible.
//
// internal/api/ingest_handlers.go already had this lesson as plausibleIngestKey,
// with a comment naming the same failure. That fix landed on the ingest limiter
// and on neither of the auth ones.
func limiterEmailKey(email string) string {
	sum := sha256.Sum256([]byte(email))
	return hex.EncodeToString(sum[:16])
}
