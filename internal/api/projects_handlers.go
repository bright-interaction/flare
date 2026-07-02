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
		DSN:          s.dsn(p.PublicKey, p.DsnID),
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
	dsnID, err := id.Numeric(12)
	if err != nil {
		slogError(w, "generate dsn id", err)
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
		DsnID:     dsnID,
	})
	if err != nil {
		slogError(w, "create project", err)
		return
	}
	s.audit(r.Context(), "project.create", req.Name)
	writeJSON(w, http.StatusCreated, s.toProjectResponse(p))
}

// handleDSNRedirect maps a numeric DSN id to the dashboard project page. The
// DSN that Cloud injects carries the numeric dsn_id, not the cuid the SPA
// routes on, so the Observability deep-link points here and we 302 to the real
// project. Unauthenticated: the target page enforces the session itself.
func (s *Server) handleDSNRedirect(w http.ResponseWriter, r *http.Request) {
	p, err := s.q.GetProjectByDsnID(r.Context(), chi.URLParam(r, "dsnID"))
	if err != nil {
		http.Redirect(w, r, "/projects", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/projects/"+p.ID, http.StatusFound)
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

// handleDeleteProject permanently removes a project and all of its telemetry.
// issues + alert_rules cascade via FK; the partitioned hot tables (events,
// logs, spans) have no FK to projects, so they are deleted explicitly in the
// same transaction so no tenant data is left orphaned.
func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	pid := chi.URLParam(r, "id")
	org := orgIDFrom(r.Context())

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		slogError(w, "delete project: begin tx", err)
		return
	}
	defer tx.Rollback(r.Context())
	qtx := s.q.WithTx(tx)

	if err := qtx.DeleteProjectEvents(r.Context(), generated.DeleteProjectEventsParams{ProjectID: pid, OrgID: org}); err != nil {
		slogError(w, "delete project events", err)
		return
	}
	if err := qtx.DeleteProjectLogs(r.Context(), generated.DeleteProjectLogsParams{ProjectID: pid, OrgID: org}); err != nil {
		slogError(w, "delete project logs", err)
		return
	}
	if err := qtx.DeleteProjectSpans(r.Context(), generated.DeleteProjectSpansParams{ProjectID: pid, OrgID: org}); err != nil {
		slogError(w, "delete project spans", err)
		return
	}
	rows, err := qtx.DeleteProject(r.Context(), generated.DeleteProjectParams{ID: pid, OrgID: org})
	if err != nil {
		slogError(w, "delete project", err)
		return
	}
	if rows == 0 {
		writeErr(w, http.StatusNotFound, "project not found")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		slogError(w, "delete project: commit", err)
		return
	}
	s.audit(r.Context(), "project.delete", pid)
	writeJSON(w, http.StatusNoContent, nil)
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
	dsnID, err := id.Numeric(12)
	if err != nil {
		slogError(w, "provision dsn id", err)
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
		ID: id.New(), OrgID: org, Name: name, Slug: slug, Platform: platform, PublicKey: publicKey, DsnID: dsnID,
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
	// Audit the CREATE side of provision. The 2026-06-30 deletion incident
	// could not be reconstructed partly because provisioned projects left no
	// trail; name+id in one target keeps both greppable.
	s.audit(r.Context(), "project.provision", name+" ("+p.ID+")")
	writeJSON(w, http.StatusCreated, s.toProjectResponse(p))
}
