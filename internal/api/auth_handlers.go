package api

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

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

	ctx := r.Context()
	user, err := s.q.GetUserByEmail(ctx, req.Email)
	if err != nil || !auth.VerifyPassword(user.PasswordHash, req.Password) {
		// Same message either way: never reveal which half failed.
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

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
