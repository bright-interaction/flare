package api

import (
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bright-interaction/flare/internal/auth"
	"github.com/bright-interaction/flare/internal/db/generated"
	"github.com/bright-interaction/flare/internal/id"
)

var slugStrip = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugStrip.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

type registerRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	OrgName  string `json:"org_name"`
}

type userResponse struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	OrgID string `json:"org_id"`
	Role  string `json:"role"`
}

func toUserResponse(u *generated.User) userResponse {
	return userResponse{ID: u.ID, Email: u.Email, OrgID: u.OrgID, Role: u.Role}
}

// handleRegister creates a new org plus its first (owner) user and logs them
// in. This is the self-host first-run path; further members are invited.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if !strings.Contains(req.Email, "@") || len(req.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "valid email and password (min 8 chars) required")
		return
	}
	orgName := strings.TrimSpace(req.OrgName)
	if orgName == "" {
		orgName = req.Email
	}

	ctx := r.Context()
	if _, err := s.q.GetUserByEmail(ctx, req.Email); err == nil {
		writeErr(w, http.StatusConflict, "email already registered")
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		slogError(w, "lookup user", err)
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		slogError(w, "hash password", err)
		return
	}

	slug := slugify(orgName)
	if slug == "" {
		slug = "org"
	}
	slug = slug + "-" + id.New()[:6]

	org, err := s.q.CreateOrg(ctx, generated.CreateOrgParams{ID: id.New(), Name: orgName, Slug: slug})
	if err != nil {
		slogError(w, "create org", err)
		return
	}
	user, err := s.q.CreateUser(ctx, generated.CreateUserParams{
		ID: id.New(), OrgID: org.ID, Email: req.Email, PasswordHash: hash, Role: "owner",
	})
	if err != nil {
		slogError(w, "create user", err)
		return
	}

	if err := s.sessions.RenewToken(ctx); err == nil {
		s.sessions.Put(ctx, "user_id", user.ID)
		s.sessions.Put(ctx, "org_id", org.ID)
	}
	writeJSON(w, http.StatusCreated, toUserResponse(user))
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	// Brute-force lockout: 5 failed attempts per email+IP within 15 minutes.
	lockKey := "login:" + req.Email + "|" + clientIP(r)
	if s.loginLimiter.Blocked(lockKey) {
		w.Header().Set("Retry-After", "900")
		writeErr(w, http.StatusTooManyRequests, "too many failed attempts, try again later")
		return
	}

	ctx := r.Context()
	user, err := s.q.GetUserByEmail(ctx, req.Email)
	if err != nil || !auth.VerifyPassword(user.PasswordHash, req.Password) {
		s.loginLimiter.Record(lockKey)
		// Once the failures cross the lockout threshold, that is a brute-force
		// signal worth surfacing (the throttle in recordSecurityEvent keeps a
		// sustained attack from flooding).
		if s.loginLimiter.Blocked(lockKey) {
			s.recordSecurityEvent("", "login-lockout", "repeated failed logins for "+req.Email+" from "+clientIP(r), clientIP(r))
		}
		// Same message either way: never reveal which half failed.
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	s.loginLimiter.Reset(lockKey) // clear the counter on success
	if err := s.sessions.RenewToken(ctx); err == nil {
		s.sessions.Put(ctx, "user_id", user.ID)
		s.sessions.Put(ctx, "org_id", user.OrgID)
	}
	writeJSON(w, http.StatusOK, toUserResponse(user))
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.sessions.Destroy(r.Context()); err != nil {
		slogError(w, "destroy session", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := s.sessions.GetString(ctx, "user_id")
	if uid == "" {
		writeErr(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	user, err := s.q.GetUserByID(ctx, uid)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	writeJSON(w, http.StatusOK, toUserResponse(user))
}

// handleForgotPassword always responds 200 (never reveals whether the email
// exists). When the account exists and SMTP is configured, it mails a
// single-use, 1-hour reset link. Only the token hash is stored.
func (s *Server) handleForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ok := func() { writeJSON(w, http.StatusOK, map[string]string{"status": "ok"}) }
	email := strings.ToLower(strings.TrimSpace(req.Email))

	ctx := r.Context()
	user, err := s.q.GetUserByEmail(ctx, email)
	if err != nil {
		ok() // unknown address: respond identically, send nothing.
		return
	}
	if !s.mailer.Enabled() {
		slog.Warn("password reset requested but SMTP not configured", "email", email)
		ok()
		return
	}

	token, err := id.Token(32)
	if err != nil {
		slogError(w, "reset token", err)
		return
	}
	if err := s.q.CreatePasswordResetToken(ctx, generated.CreatePasswordResetTokenParams{
		ID:        id.New(),
		UserID:    user.ID,
		TokenHash: auth.HashAPIKey(token),
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		slogError(w, "create reset token", err)
		return
	}

	link := strings.TrimRight(s.cfg.BaseURL, "/") + "/reset-password?token=" + token
	go s.sendResetEmail(user.Email, link)
	ok()
}

func (s *Server) sendResetEmail(to, link string) {
	subject := "Reset your Flare password"
	text := fmt.Sprintf("Reset your Flare password with this link (valid for 1 hour):\n\n%s\n\nIf you did not request this, ignore this email.\n", link)
	body := fmt.Sprintf(`<div style="font:15px/1.6 -apple-system,Segoe UI,sans-serif;color:#18181b;max-width:520px">
<h2 style="margin:0 0 8px;font-size:18px">Reset your password</h2>
<p style="margin:0 0 16px;color:#52525b">Click below to set a new password. This link expires in 1 hour.</p>
<p style="margin:0 0 16px"><a href="%s" style="display:inline-block;background:#f59e0b;color:#18181b;text-decoration:none;padding:10px 18px;border-radius:6px;font-weight:600">Reset password</a></p>
<p style="margin:0;color:#a1a1aa;font-size:13px">If you did not request this, you can ignore this email.</p>
</div>`, html.EscapeString(link))
	if err := s.mailer.Send(to, subject, body, text); err != nil {
		slog.Warn("reset email delivery failed", "error", err)
	}
}

// handleResetPassword consumes a valid token and sets the new password.
func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	ctx := r.Context()
	prt, err := s.q.GetPasswordResetToken(ctx, auth.HashAPIKey(strings.TrimSpace(req.Token)))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid or expired reset link")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		slogError(w, "hash password", err)
		return
	}
	if err := s.q.UpdateUserPassword(ctx, generated.UpdateUserPasswordParams{ID: prt.UserID, PasswordHash: hash}); err != nil {
		slogError(w, "update password", err)
		return
	}
	if err := s.q.MarkPasswordResetTokenUsed(ctx, prt.ID); err != nil {
		slog.Warn("mark reset token used", "error", err)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
