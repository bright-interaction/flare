package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/bright-interaction/flare/internal/db/generated"
	"github.com/bright-interaction/flare/internal/id"
)

type createProjectRequest struct {
	Name     string `json:"name"`
	Platform string `json:"platform"`
}

type projectResponse struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Slug         string `json:"slug"`
	Platform     string `json:"platform"`
	DSN          string `json:"dsn"`
	OTLPEndpoint string `json:"otlp_endpoint"`
}

func (s *Server) toProjectResponse(p *generated.Project) projectResponse {
	return projectResponse{
		ID:           p.ID,
		Name:         p.Name,
		Slug:         p.Slug,
		Platform:     p.Platform,
		DSN:          s.dsn(p.PublicKey, p.ID),
		OTLPEndpoint: s.otlpEndpoint(),
	}
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	platform := strings.TrimSpace(req.Platform)
	if platform == "" {
		platform = "other"
	}

	publicKey, err := id.Token(16)
	if err != nil {
		slogError(w, "generate ingest key", err)
		return
	}
	slug := slugify(req.Name)
	if slug == "" {
		slug = "project"
	}
	slug = slug + "-" + id.New()[:6]

	p, err := s.q.CreateProject(r.Context(), generated.CreateProjectParams{
		ID:        id.New(),
		OrgID:     orgIDFrom(r.Context()),
		Name:      req.Name,
		Slug:      slug,
		Platform:  platform,
		PublicKey: publicKey,
	})
	if err != nil {
		slogError(w, "create project", err)
		return
	}
	writeJSON(w, http.StatusCreated, s.toProjectResponse(p))
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.q.ListProjectsByOrg(r.Context(), orgIDFrom(r.Context()))
	if err != nil {
		slogError(w, "list projects", err)
		return
	}
	out := make([]projectResponse, 0, len(projects))
	for _, p := range projects {
		out = append(out, s.toProjectResponse(p))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.q.GetProjectByID(r.Context(), generated.GetProjectByIDParams{
		ID:       chi.URLParam(r, "id"),
		OrgScope: orgIDFrom(r.Context()),
	})
	if err != nil {
		writeErr(w, http.StatusNotFound, "project not found")
		return
	}
	writeJSON(w, http.StatusOK, s.toProjectResponse(p))
}
