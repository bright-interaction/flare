package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bright-interaction/flare/internal/auth"
	"github.com/bright-interaction/flare/internal/db/generated"
	"github.com/bright-interaction/flare/internal/id"
)

type apiKeyResponse struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// handleCreateAPIKey mints an org-scoped API key. The plaintext is returned
// exactly once; only its hash is stored.
func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name          string `json:"name"`
		ExpiresInDays int    `json:"expires_in_days"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "API key"
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
		KeyHash: hash, KeyPrefix: prefix, ExpiresAt: expires,
	})
	if err != nil {
		slogError(w, "create api key", err)
		return
	}
	// key (plaintext) is shown once and never again.
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": k.ID, "name": k.Name, "prefix": k.KeyPrefix, "key": plaintext,
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
			ID: k.ID, Name: k.Name, Prefix: k.KeyPrefix, CreatedAt: k.CreatedAt.Time, LastUsedAt: last,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
