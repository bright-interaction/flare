package api

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
)

func TestInlineScriptHashesMatchesBrowserDigest(t *testing.T) {
	// The digest a browser computes is over the exact bytes between the tags.
	body := "\n\t{ import('/app.js'); }\n"
	html := []byte(`<!doctype html><head></head><body>` +
		`<script src="/external.js"></script>` +
		`<script>` + body + `</script></body>`)

	hashes := inlineScriptHashes(html)
	if len(hashes) != 1 {
		t.Fatalf("want 1 inline hash (external excluded), got %d: %v", len(hashes), hashes)
	}
	sum := sha256.Sum256([]byte(body))
	want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	if hashes[0] != want {
		t.Fatalf("hash = %s, want %s", hashes[0], want)
	}
}

func TestBaseCSPLocksScriptsWithoutUnsafeInline(t *testing.T) {
	csp := baseCSP("'sha256-abc'")
	if !strings.Contains(csp, "script-src 'self' 'sha256-abc'") {
		t.Fatalf("script-src not locked to self+hash: %s", csp)
	}
	// scripts must NOT carry unsafe-inline when a hash is present
	scriptDir := csp[strings.Index(csp, "script-src"):]
	scriptDir = scriptDir[:strings.Index(scriptDir, ";")]
	if strings.Contains(scriptDir, "'unsafe-inline'") {
		t.Fatalf("script-src must not have unsafe-inline when hashed: %s", scriptDir)
	}
}
