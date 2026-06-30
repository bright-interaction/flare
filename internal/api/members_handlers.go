package api

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bright-interaction/flare/internal/auth"
	"github.com/bright-interaction/flare/internal/db/generated"
	"github.com/bright-interaction/flare/internal/id"
)

const inviteTTL = 7 * 24 * time.Hour

type memberResponse struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	IsYou     bool      `json:"is_you"`
	CreatedAt time.Time `json:"created_at"`
}

type inviteResponse struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	// AcceptURL + Emailed are only set on create, so an inviter can share the
	// link directly when email is not configured (self-host).
	AcceptURL string `json:"accept_url,omitempty"`
	Emailed   bool   `json:"emailed,omitempty"`
}

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	me := userIDFrom(ctx)
	users, err := s.q.ListUsersByOrg(ctx, orgIDFrom(ctx))
	if err != nil {
		slogError(w, "list members", err)
		return
	}
	out := make([]memberResponse, 0, len(users))
	for _, u := range users {
		out = append(out, memberResponse{ID: u.ID, Email: u.Email, Role: u.Role, IsYou: u.ID == me, CreatedAt: u.CreatedAt.Time})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleUpdateMemberRole(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Role string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !validRole(req.Role) {
		writeErr(w, http.StatusBadRequest, "role must be viewer, member, admin or owner")
		return
	}
	ctx := r.Context()
	org := orgIDFrom(ctx)
	targetID := chi.URLParam(r, "userID")
	if targetID == userIDFrom(ctx) {
		writeErr(w, http.StatusBadRequest, "you cannot change your own role")
		return
	}
	target, err := s.q.GetUserInOrg(ctx, generated.GetUserInOrgParams{ID: targetID, OrgID: org})
	if err != nil {
		writeErr(w, http.StatusNotFound, "member not found")
		return
	}
	// Only an owner can touch an owner or grant the owner role.
	if (target.Role == "owner" || req.Role == "owner") && roleFrom(ctx) != "owner" {
		writeErr(w, http.StatusForbidden, "only an owner can manage owners")
		return
	}
	if target.Role == "owner" && req.Role != "owner" && s.lastOwner(ctx, org) {
		writeErr(w, http.StatusBadRequest, "the organisation must keep at least one owner")
		return
	}
	if _, err := s.q.UpdateUserRole(ctx, generated.UpdateUserRoleParams{ID: targetID, OrgID: org, Role: req.Role}); err != nil {
		slogError(w, "update member role", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	org := orgIDFrom(ctx)
	targetID := chi.URLParam(r, "userID")
	if targetID == userIDFrom(ctx) {
		writeErr(w, http.StatusBadRequest, "you cannot remove yourself")
		return
	}
	target, err := s.q.GetUserInOrg(ctx, generated.GetUserInOrgParams{ID: targetID, OrgID: org})
	if err != nil {
		writeErr(w, http.StatusNotFound, "member not found")
		return
	}
	if target.Role == "owner" && roleFrom(ctx) != "owner" {
		writeErr(w, http.StatusForbidden, "only an owner can remove an owner")
		return
	}
	if target.Role == "owner" && s.lastOwner(ctx, org) {
		writeErr(w, http.StatusBadRequest, "the organisation must keep at least one owner")
		return
	}
	rows, err := s.q.DeleteUser(ctx, generated.DeleteUserParams{ID: targetID, OrgID: org})
	if err != nil {
		slogError(w, "remove member", err)
		return
	}
	if rows == 0 {
		writeErr(w, http.StatusNotFound, "member not found")
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

func (s *Server) lastOwner(ctx context.Context, org string) bool {
	owners, err := s.q.CountOwnersByOrg(ctx, org)
	return err == nil && owners <= 1
}

func (s *Server) handleInviteMember(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if !strings.Contains(req.Email, "@") {
		writeErr(w, http.StatusBadRequest, "a valid email is required")
		return
	}
	if req.Role == "" {
		req.Role = "member"
	}
	if !validRole(req.Role) {
		writeErr(w, http.StatusBadRequest, "role must be viewer, member, admin or owner")
		return
	}
	ctx := r.Context()
	if req.Role == "owner" && roleFrom(ctx) != "owner" {
		writeErr(w, http.StatusForbidden, "only an owner can invite an owner")
		return
	}
	if _, err := s.q.GetUserByEmail(ctx, req.Email); err == nil {
		writeErr(w, http.StatusConflict, "that email already has an account")
		return
	}

	token, err := id.Token(32)
	if err != nil {
		slogError(w, "invite token", err)
		return
	}
	inv, err := s.q.UpsertInvitation(ctx, generated.UpsertInvitationParams{
		ID:        id.New(),
		OrgID:     orgIDFrom(ctx),
		Email:     req.Email,
		Role:      req.Role,
		TokenHash: auth.HashAPIKey(token),
		InvitedBy: pgText(userIDFrom(ctx)),
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(inviteTTL), Valid: true},
	})
	if err != nil {
		slogError(w, "create invitation", err)
		return
	}

	link := strings.TrimRight(s.cfg.BaseURL, "/") + "/accept-invite?token=" + token
	emailed := false
	if s.mailer.Enabled() {
		go s.sendInviteEmail(req.Email, link)
		emailed = true
	}
	writeJSON(w, http.StatusCreated, inviteResponse{
		ID: inv.ID, Email: inv.Email, Role: inv.Role,
		ExpiresAt: inv.ExpiresAt.Time, CreatedAt: inv.CreatedAt.Time,
		AcceptURL: link, Emailed: emailed,
	})
}

func (s *Server) sendInviteEmail(to, link string) {
	subject := "You have been invited to Flare"
	text := fmt.Sprintf("You have been invited to a Flare workspace. Accept your invite (valid for 7 days):\n\n%s\n", link)
	body := fmt.Sprintf(`<div style="font:15px/1.6 -apple-system,Segoe UI,sans-serif;color:#18181b;max-width:520px">
<h2 style="margin:0 0 8px;font-size:18px">You're invited to Flare</h2>
<p style="margin:0 0 16px;color:#52525b">Set a password to join the workspace. This invite expires in 7 days.</p>
<p style="margin:0 0 16px"><a href="%s" style="display:inline-block;background:#f59e0b;color:#18181b;text-decoration:none;padding:10px 18px;border-radius:6px;font-weight:600">Accept invite</a></p>
</div>`, html.EscapeString(link))
	if err := s.mailer.Send(to, subject, body, text); err != nil {
		slog.Warn("invite email delivery failed", "error", err)
	}
}

func (s *Server) handleListInvites(w http.ResponseWriter, r *http.Request) {
	rows, err := s.q.ListInvitationsByOrg(r.Context(), orgIDFrom(r.Context()))
	if err != nil {
		slogError(w, "list invites", err)
		return
	}
	out := make([]inviteResponse, 0, len(rows))
	for _, inv := range rows {
		out = append(out, inviteResponse{ID: inv.ID, Email: inv.Email, Role: inv.Role, ExpiresAt: inv.ExpiresAt.Time, CreatedAt: inv.CreatedAt.Time})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRevokeInvite(w http.ResponseWriter, r *http.Request) {
	rows, err := s.q.DeleteInvitation(r.Context(), generated.DeleteInvitationParams{
		ID: chi.URLParam(r, "inviteID"), OrgID: orgIDFrom(r.Context()),
	})
	if err != nil {
		slogError(w, "revoke invite", err)
		return
	}
	if rows == 0 {
		writeErr(w, http.StatusNotFound, "invite not found")
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// handleAcceptInvite consumes an invite token, creates the user with the
// invited role, and signs them in. Public (no session yet).
func (s *Server) handleAcceptInvite(w http.ResponseWriter, r *http.Request) {
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
	inv, err := s.q.GetInvitationByToken(ctx, auth.HashAPIKey(strings.TrimSpace(req.Token)))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid or expired invite")
		return
	}
	if _, err := s.q.GetUserByEmail(ctx, inv.Email); err == nil {
		writeErr(w, http.StatusConflict, "an account for this email already exists")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		slogError(w, "hash password", err)
		return
	}
	user, err := s.q.CreateUser(ctx, generated.CreateUserParams{
		ID: id.New(), OrgID: inv.OrgID, Email: inv.Email, PasswordHash: hash, Role: inv.Role,
	})
	if err != nil {
		slogError(w, "accept invite: create user", err)
		return
	}
	if err := s.q.MarkInvitationAccepted(ctx, inv.ID); err != nil {
		slog.Warn("mark invitation accepted", "error", err)
	}
	if err := s.sessions.RenewToken(ctx); err == nil {
		s.sessions.Put(ctx, "user_id", user.ID)
		s.sessions.Put(ctx, "org_id", user.OrgID)
	}
	writeJSON(w, http.StatusOK, toUserResponse(user))
}
