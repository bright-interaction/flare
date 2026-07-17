package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bright-interaction/flare/internal/db/generated"
	"github.com/bright-interaction/flare/internal/id"
)

// maxArtifactBody bounds a source map upload. Maps for large bundles can be
// several MiB, so this is well above the 1 MiB ingest cap.
const maxArtifactBody = 30 << 20

type artifactResponse struct {
	ID        string    `json:"id"`
	Release   string    `json:"release"`
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
}

// handleUploadSourceMap stores (or replaces) one minified file's source map for
// a project + release. The `name` is the minified file as it appears in a stack
// frame (e.g. "app.min.js"); `content` is the .map JSON.
func (s *Server) handleUploadSourceMap(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxArtifactBody))
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "source map too large (max 30 MiB)")
		return
	}
	var req struct {
		Release string `json:"release"`
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Release = strings.TrimSpace(req.Release)
	req.Name = strings.TrimSpace(req.Name)
	if req.Release == "" || req.Name == "" || req.Content == "" {
		writeErr(w, http.StatusBadRequest, "release, name and content are required")
		return
	}
	if !strings.HasPrefix(strings.TrimSpace(req.Content), "{") {
		writeErr(w, http.StatusBadRequest, "content must be a source map JSON document")
		return
	}

	// Verify the project belongs to the caller's org BEFORE upserting (mirror the
	// monitors/metrics handlers). Without this a tenant could pass another org's project
	// id in the URL and overwrite that project's sourcemap (cross-tenant write), since the
	// upsert conflict targets (project_id, release, name).
	if _, err := s.q.GetProjectByID(r.Context(), generated.GetProjectByIDParams{
		ID: chi.URLParam(r, "id"), OrgScope: orgIDFrom(r.Context()),
	}); err != nil {
		writeErr(w, http.StatusNotFound, "project not found")
		return
	}

	row, err := s.q.UpsertSourceMap(r.Context(), generated.UpsertSourceMapParams{
		ID:        id.New(),
		ProjectID: chi.URLParam(r, "id"),
		OrgID:     orgIDFrom(r.Context()),
		Release:   req.Release,
		Name:      req.Name,
		Content:   req.Content,
	})
	if err != nil {
		slogError(w, "upsert source map", err)
		return
	}
	writeJSON(w, http.StatusCreated, artifactResponse{
		ID: row.ID, Release: row.Release, Name: row.Name, Size: int64(row.Size), CreatedAt: row.CreatedAt.Time,
	})
}

func (s *Server) handleListSourceMaps(w http.ResponseWriter, r *http.Request) {
	rows, err := s.q.ListSourceMapsByProject(r.Context(), generated.ListSourceMapsByProjectParams{
		ProjectID: chi.URLParam(r, "id"), OrgID: orgIDFrom(r.Context()),
	})
	if err != nil {
		slogError(w, "list source maps", err)
		return
	}
	out := make([]artifactResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, artifactResponse{ID: row.ID, Release: row.Release, Name: row.Name, Size: int64(row.Size), CreatedAt: row.CreatedAt.Time})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteSourceMap(w http.ResponseWriter, r *http.Request) {
	rows, err := s.q.DeleteSourceMap(r.Context(), generated.DeleteSourceMapParams{
		ID:        chi.URLParam(r, "artifactID"),
		ProjectID: chi.URLParam(r, "id"),
		OrgID:     orgIDFrom(r.Context()),
	})
	if err != nil {
		slogError(w, "delete source map", err)
		return
	}
	if rows == 0 {
		writeErr(w, http.StatusNotFound, "source map not found")
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}
