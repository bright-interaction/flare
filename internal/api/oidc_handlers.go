package api

import (
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/bright-interaction/flare/internal/auth"
	"github.com/bright-interaction/flare/internal/db/generated"
	"github.com/bright-interaction/flare/internal/id"
	"github.com/bright-interaction/flare/internal/oidc"
)

type oidcConfigResponse struct {
	Enabled     bool   `json:"enabled"`
	Issuer      string `json:"issuer"`
	ClientID    string `json:"client_id"`
	DefaultRole string `json:"default_role"`
	RedirectURI string `json:"redirect_uri"`
	LoginURL    string `json:"login_url"`
}

func (s *Server) oidcRedirectURI() string {
	return strings.TrimRight(s.cfg.BaseURL, "/") + "/api/auth/oidc/callback"
}

func (s *Server) handleGetOIDCConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	org := orgIDFrom(ctx)
	resp := oidcConfigResponse{RedirectURI: s.oidcRedirectURI()}
	if o, err := s.q.GetOrgByID(ctx, org); err == nil {
		resp.LoginURL = strings.TrimRight(s.cfg.BaseURL, "/") + "/api/auth/oidc/start?org=" + o.Slug
	}
	cfg, err := s.q.GetOIDCConfig(ctx, org)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if err != nil {
		slogError(w, "get oidc config", err)
		return
	}
	resp.Enabled = cfg.Enabled
	resp.Issuer = cfg.Issuer
	resp.ClientID = cfg.ClientID
	resp.DefaultRole = cfg.DefaultRole
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSetOIDCConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Issuer       string `json:"issuer"`
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		DefaultRole  string `json:"default_role"`
		Enabled      bool   `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Issuer = strings.TrimRight(strings.TrimSpace(req.Issuer), "/")
	req.ClientID = strings.TrimSpace(req.ClientID)
	req.ClientSecret = strings.TrimSpace(req.ClientSecret)
	if !strings.HasPrefix(req.Issuer, "https://") || req.ClientID == "" {
		writeErr(w, http.StatusBadRequest, "issuer (https) and client_id are required")
		return
	}
	// Reject issuers whose host is a loopback/private/link-local IP literal
	// at CONFIGURE time, not just at discovery time. The SSRF-guarded dialer
	// already blocks the fetch, but accepting https://169.254.169.254 as a
	// stored issuer passed a live probe on 2026-06-30; validation belongs at
	// the trust boundary too.
	if u, err := url.Parse(req.Issuer); err != nil || u.Hostname() == "" {
		writeErr(w, http.StatusBadRequest, "issuer is not a valid URL")
		return
	} else if ip := net.ParseIP(u.Hostname()); ip != nil &&
		(ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
			ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()) {
		writeErr(w, http.StatusBadRequest, "issuer host must be a public address")
		return
	}
	// A blank secret on update keeps the stored one (so admins can toggle
	// enabled or change the role without re-entering it).
	if req.ClientSecret == "" {
		existing, err := s.q.GetOIDCConfig(r.Context(), orgIDFrom(r.Context()))
		if err != nil || existing.ClientSecret == "" {
			writeErr(w, http.StatusBadRequest, "a client_secret is required")
			return
		}
		req.ClientSecret = existing.ClientSecret
	}
	if req.DefaultRole == "" {
		req.DefaultRole = "member"
	}
	// JIT users must never be auto-elevated to owner.
	if !validRole(req.DefaultRole) || req.DefaultRole == "owner" {
		writeErr(w, http.StatusBadRequest, "default role must be viewer, member or admin")
		return
	}
	if err := s.q.UpsertOIDCConfig(r.Context(), generated.UpsertOIDCConfigParams{
		OrgID:        orgIDFrom(r.Context()),
		Issuer:       req.Issuer,
		ClientID:     req.ClientID,
		ClientSecret: s.secrets.Encrypt(req.ClientSecret),
		DefaultRole:  req.DefaultRole,
		Enabled:      req.Enabled,
	}); err != nil {
		slogError(w, "save oidc config", err)
		return
	}
	s.audit(r.Context(), "sso.configure", req.Issuer)
	s.handleGetOIDCConfig(w, r)
}

func (s *Server) handleDeleteOIDCConfig(w http.ResponseWriter, r *http.Request) {
	if err := s.q.DeleteOIDCConfig(r.Context(), orgIDFrom(r.Context())); err != nil {
		slogError(w, "delete oidc config", err)
		return
	}
	s.audit(r.Context(), "sso.disconnect", "")
	writeJSON(w, http.StatusNoContent, nil)
}

// ssoError redirects the browser back to the login page with an error code.
func ssoError(w http.ResponseWriter, r *http.Request, code string) {
	http.Redirect(w, r, "/login?error="+code, http.StatusFound)
}

// handleOIDCStart begins the SSO flow for the org named by ?org=<slug>.
func (s *Server) handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	org, err := s.q.GetOrgBySlug(ctx, strings.TrimSpace(r.URL.Query().Get("org")))
	if err != nil {
		ssoError(w, r, "sso_unknown_org")
		return
	}
	cfg, err := s.q.GetOIDCConfig(ctx, org.ID)
	if err != nil || !cfg.Enabled {
		ssoError(w, r, "sso_disabled")
		return
	}
	provider, err := oidc.Discover(ctx, cfg.Issuer)
	if err != nil {
		ssoError(w, r, "sso_discovery")
		return
	}
	state := oidc.RandomState()
	s.sessions.Put(ctx, "oidc_state", state)
	s.sessions.Put(ctx, "oidc_org", org.ID)
	http.Redirect(w, r, provider.AuthCodeURL(cfg.ClientID, s.oidcRedirectURI(), state), http.StatusFound)
}

// handleOIDCCallback completes the SSO flow: validate state, exchange the code,
// read userinfo, match or JIT-provision the user, and sign them in.
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	qstate := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	sessState := s.sessions.GetString(ctx, "oidc_state")
	sessOrg := s.sessions.GetString(ctx, "oidc_org")
	// One-time use: drop the state regardless of outcome.
	s.sessions.Remove(ctx, "oidc_state")
	s.sessions.Remove(ctx, "oidc_org")

	if code == "" || qstate == "" || sessState == "" || sessOrg == "" ||
		subtle.ConstantTimeCompare([]byte(qstate), []byte(sessState)) != 1 {
		ssoError(w, r, "sso_state")
		return
	}
	cfg, err := s.q.GetOIDCConfig(ctx, sessOrg)
	if err != nil || !cfg.Enabled {
		ssoError(w, r, "sso_disabled")
		return
	}
	provider, err := oidc.Discover(ctx, cfg.Issuer)
	if err != nil {
		ssoError(w, r, "sso_discovery")
		return
	}
	accessToken, err := provider.Exchange(ctx, cfg.ClientID, s.secrets.Decrypt(cfg.ClientSecret), code, s.oidcRedirectURI())
	if err != nil {
		ssoError(w, r, "sso_exchange")
		return
	}
	info, err := provider.FetchUserInfo(ctx, accessToken)
	if err != nil || info.Email == "" {
		ssoError(w, r, "sso_userinfo")
		return
	}
	// Never trust an unverified email to match or create an account.
	if !info.EmailVerified {
		ssoError(w, r, "sso_unverified")
		return
	}
	email := strings.ToLower(strings.TrimSpace(info.Email))

	user, err := s.q.GetUserByEmail(ctx, email)
	if err == nil {
		// Email is globally unique; it must belong to the org that started SSO.
		if user.OrgID != sessOrg {
			ssoError(w, r, "sso_other_org")
			return
		}
	} else if errors.Is(err, pgx.ErrNoRows) {
		// Just-in-time provisioning with the configured default role.
		token, terr := id.Token(24)
		if terr != nil {
			ssoError(w, r, "sso_provision")
			return
		}
		hash, herr := auth.HashPassword(token) // unusable random password; SSO only
		if herr != nil {
			ssoError(w, r, "sso_provision")
			return
		}
		user, err = s.q.CreateUser(ctx, generated.CreateUserParams{
			ID: id.New(), OrgID: sessOrg, Email: email, PasswordHash: hash, Role: cfg.DefaultRole,
		})
		if err != nil {
			ssoError(w, r, "sso_provision")
			return
		}
		s.audit(ctx, "sso.provision", email)
	} else {
		ssoError(w, r, "sso_lookup")
		return
	}

	s.establishSession(ctx, user.ID, user.OrgID)
	http.Redirect(w, r, "/projects", http.StatusFound)
}
