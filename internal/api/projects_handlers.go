package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

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

type provisionRequest struct {
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	Platform string `json:"platform"`
}

// handleProvisionProject get-or-creates a project by (org, slug) with a STABLE
// slug. It is idempotent so an external system (Cloud) can call it on every
// deploy without creating duplicates. Authenticated by an org API key.
func (s *Server) handleProvisionProject(w http.ResponseWriter, r *http.Request) {
	var req provisionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	org := orgIDFrom(r.Context())
	source := req.Slug
	if source == "" {
		source = req.Name
	}
	slug := slugify(source)
	if slug == "" {
		writeErr(w, http.StatusBadRequest, "name or slug is required")
		return
	}

	if p, err := s.q.GetProjectBySlug(r.Context(), generated.GetProjectBySlugParams{OrgID: org, Slug: slug}); err == nil {
		writeJSON(w, http.StatusOK, s.toProjectResponse(p))
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		slogError(w, "provision lookup", err)
		return
	}

	publicKey, err := id.Token(16)
	if err != nil {
		slogError(w, "provision key", err)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = slug
	}
	platform := strings.TrimSpace(req.Platform)
	if platform == "" {
		platform = "other"
	}
	p, err := s.q.CreateProject(r.Context(), generated.CreateProjectParams{
		ID: id.New(), OrgID: org, Name: name, Slug: slug, Platform: platform, PublicKey: publicKey,
	})
	if err != nil {
		// Lost a race with a concurrent deploy: fall back to the existing row.
		if p2, e2 := s.q.GetProjectBySlug(r.Context(), generated.GetProjectBySlugParams{OrgID: org, Slug: slug}); e2 == nil {
			writeJSON(w, http.StatusOK, s.toProjectResponse(p2))
			return
		}
		slogError(w, "provision create", err)
		return
	}
	writeJSON(w, http.StatusCreated, s.toProjectResponse(p))
}
