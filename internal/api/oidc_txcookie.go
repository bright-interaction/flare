package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The OIDC transaction (state + org) cannot ride in the main session cookie:
// that cookie is SameSite=Strict, and the IdP's redirect back to
// /api/auth/oidc/callback is a cross-site top-level navigation, so the browser
// withholds it. The callback then saw an empty state every time and SSO could
// never complete in a real browser.
//
// It gets its own short-lived cookie instead: SameSite=Lax (sent on top-level
// cross-site GET navigations, which is exactly the IdP return and nothing else),
// HttpOnly, Secure in production, scoped to the callback path, and authenticated
// with HMAC-SHA256 over FLARE_SECRET_KEY so its contents cannot be forged. The
// main session cookie stays Strict.
const (
	oidcTxCookie = "flare_oidc_tx"
	oidcTxPath   = "/api/auth/oidc"
	oidcTxTTL    = 10 * time.Minute
)

func (s *Server) oidcTxMAC(state, org, exp string) string {
	m := hmac.New(sha256.New, []byte(s.cfg.SecretKey))
	// Length-prefixed so no combination of field values can produce the same
	// signed bytes as a different combination.
	for _, part := range []string{state, org, exp} {
		m.Write([]byte(strconv.Itoa(len(part))))
		m.Write([]byte(":"))
		m.Write([]byte(part))
	}
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// setOIDCTx issues the transaction cookie for one SSO attempt.
func (s *Server) setOIDCTx(w http.ResponseWriter, state, org string) {
	exp := strconv.FormatInt(time.Now().Add(oidcTxTTL).Unix(), 10)
	val := strings.Join([]string{
		base64.RawURLEncoding.EncodeToString([]byte(state)),
		base64.RawURLEncoding.EncodeToString([]byte(org)),
		exp,
		s.oidcTxMAC(state, org, exp),
	}, ".")
	http.SetCookie(w, &http.Cookie{
		Name:     oidcTxCookie,
		Value:    val,
		Path:     oidcTxPath,
		MaxAge:   int(oidcTxTTL.Seconds()),
		HttpOnly: true,
		Secure:   s.cfg.IsProduction(),
		SameSite: http.SameSiteLaxMode,
	})
}

// clearOIDCTx expires the transaction cookie. Always called on the callback,
// whatever the outcome, so a state is strictly single-use.
func (s *Server) clearOIDCTx(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     oidcTxCookie,
		Value:    "",
		Path:     oidcTxPath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.IsProduction(),
		SameSite: http.SameSiteLaxMode,
	})
}

// readOIDCTx returns the state and org from a valid, unexpired, correctly
// signed transaction cookie. Any tampering, expiry, or malformed value yields
// ("", "").
func (s *Server) readOIDCTx(r *http.Request) (state, org string) {
	c, err := r.Cookie(oidcTxCookie)
	if err != nil || c.Value == "" {
		return "", ""
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 4 {
		return "", ""
	}
	sb, err1 := base64.RawURLEncoding.DecodeString(parts[0])
	ob, err2 := base64.RawURLEncoding.DecodeString(parts[1])
	if err1 != nil || err2 != nil {
		return "", ""
	}
	// Verify the signature BEFORE trusting any field, including the expiry.
	if subtle.ConstantTimeCompare([]byte(s.oidcTxMAC(string(sb), string(ob), parts[2])), []byte(parts[3])) != 1 {
		return "", ""
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", ""
	}
	return string(sb), string(ob)
}
