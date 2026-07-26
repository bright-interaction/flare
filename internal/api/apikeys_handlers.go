package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bright-interaction/flare/internal/auth"
	"github.com/bright-interaction/flare/internal/db/generated"
	"github.com/bright-interaction/flare/internal/id"
)

type apiKeyResponse struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Role       string     `json:"role"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// handleCreateAPIKey mints an org-scoped API key. The plaintext is returned
// exactly once; only its hash is stored.
func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name          string `json:"name"`
		ExpiresInDays int    `json:"expires_in_days"`
		Role          string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "API key"
	}
	// Least privilege is the DEFAULT for a new key. Every key used to act as a
	// member, which includes irreversible project deletion, so a token minted
	// for a dashboard could erase a project. Callers opt UP to member
	// explicitly; owner/admin are not mintable at all, because a long-lived
	// token must never be able to change who can log in.
	role := strings.TrimSpace(req.Role)
	if role == "" {
		role = "viewer"
	}
	if role != "viewer" && role != "member" {
		writeErr(w, http.StatusBadRequest, "role must be viewer or member")
		return
	}

	plaintext, hash, prefix, err := auth.GenerateAPIKey()
	if err != nil {
		slogError(w, "generate api key", err)
		return
	}
	var expires pgtype.Timestamptz
	if req.ExpiresInDays > 0 {
		expires = pgtype.Timestamptz{Time: time.Now().AddDate(0, 0, req.ExpiresInDays), Valid: true}
	}

	k, err := s.q.CreateAPIKey(r.Context(), generated.CreateAPIKeyParams{
		ID: id.New(), OrgID: orgIDFrom(r.Context()), Name: name,
		KeyHash: hash, KeyPrefix: prefix, ExpiresAt: expires, Role: role,
	})
	if err != nil {
		slogError(w, "create api key", err)
		return
	}
	s.audit(r.Context(), "apikey.create", k.Name)
	// key (plaintext) is shown once and never again.
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": k.ID, "name": k.Name, "prefix": k.KeyPrefix, "role": k.Role, "key": plaintext,
	})
}

func (s *Server) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.q.ListAPIKeysByOrg(r.Context(), orgIDFrom(r.Context()))
	if err != nil {
		slogError(w, "list api keys", err)
		return
	}
	out := make([]apiKeyResponse, 0, len(keys))
	for _, k := range keys {
		var last *time.Time
		if k.LastUsedAt.Valid {
			t := k.LastUsedAt.Time
			last = &t
		}
		out = append(out, apiKeyResponse{
			ID: k.ID, Name: k.Name, Prefix: k.KeyPrefix, Role: k.Role, CreatedAt: k.CreatedAt.Time, LastUsedAt: last,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeleteAPIKey revokes an org-scoped API key immediately.
func (s *Server) handleDeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	rows, err := s.q.DeleteAPIKey(r.Context(), generated.DeleteAPIKeyParams{
		ID:    chi.URLParam(r, "id"),
		OrgID: orgIDFrom(r.Context()),
	})
	if err != nil {
		slogError(w, "delete api key", err)
		return
	}
	if rows == 0 {
		writeErr(w, http.StatusNotFound, "api key not found")
		return
	}
	s.audit(r.Context(), "apikey.revoke", chi.URLParam(r, "id"))
	writeJSON(w, http.StatusNoContent, nil)
}
