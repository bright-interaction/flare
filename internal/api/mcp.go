package api

// Flare's own Model Context Protocol server: a JSON-RPC 2.0 (Streamable HTTP)
// endpoint at /api/mcp that lets an AI operator (Claude) investigate and act on
// errors directly - list issues, read a stack trace, search logs, run AI
// triage, resolve issues - instead of going through curl or the DB. It is a
// thin protocol translator over the SAME org-scoped Store/queries the REST API
// uses (no business logic duplicated), mounted inside requireAuth so every call
// arrives with a resolved org in context. Allow-list driven: only the tools
// registered in mcpToolset are exposed.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"

	"github.com/bright-interaction/flare/internal/db/generated"
	"github.com/bright-interaction/flare/internal/telemetry"
)

// mcpRatePerMin caps MCP requests per org per minute. Generous for an AI
// operator's investigation cadence, but bounds a member key looping an
// expensive tool (e.g. triage_issue with refresh=true drives a fresh outbound
// BYOAI completion each call). Keyed on org so a whole tenant shares the
// budget.
const mcpRatePerMin = 120

// mcpUserError carries a caller-facing message that is safe to return verbatim
// (validation / not-found). Any other error a tool handler returns is treated
// as internal: logged server-side and replaced with a generic message so DB
// driver detail and upstream-provider response bodies never reach the client,
// matching the REST slogError contract.
type mcpUserError struct{ msg string }

func (e mcpUserError) Error() string { return e.msg }

func userErr(format string, a ...any) error { return mcpUserError{msg: fmt.Sprintf(format, a...)} }

const mcpProtocol = "2025-03-26"

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id,omitempty"`
	Result  any       `json:"result,omitempty"`
	Error   *mcpError `json:"error,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpToolResult struct {
	Content []mcpContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

// mcpTool is one registered tool. handler receives the caller's org and the raw
// arguments and returns a value that is JSON-encoded into the tool result.
type mcpTool struct {
	Name        string                                                                   `json:"name"`
	Description string                                                                   `json:"description"`
	InputSchema json.RawMessage                                                          `json:"inputSchema"`
	Handler     func(ctx context.Context, org string, args json.RawMessage) (any, error) `json:"-"`
	// RequiresWrite gates the tool on the caller's role, mirroring the REST
	// member+ write group: a viewer (or a viewer-scoped API key) is refused
	// before dispatch, so a read-only credential cannot mutate through MCP.
	RequiresWrite bool `json:"-"`
}

// mcpHandler is the http.Handler mounted at /api/mcp. POST carries JSON-RPC.
func (s *Server) mcpHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "MCP is POST-only JSON-RPC", http.StatusMethodNotAllowed)
			return
		}
		org := orgIDFrom(r.Context())
		if org == "" {
			mcpWriteErr(w, nil, -32600, "not authenticated")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			mcpWriteErr(w, nil, -32700, "read body failed")
			return
		}
		var req mcpRequest
		if err := json.Unmarshal(body, &req); err != nil {
			mcpWriteErr(w, nil, -32700, "invalid JSON")
			return
		}
		if req.ID == nil { // notification
			w.WriteHeader(http.StatusNoContent)
			return
		}
		result, rpcErr := s.mcpDispatch(r.Context(), org, req)
		resp := mcpResponse{JSONRPC: "2.0", ID: req.ID}
		if rpcErr != nil {
			resp.Error = rpcErr
		} else {
			resp.Result = result
		}
		writeJSON(w, http.StatusOK, resp)
	})
}

// rateLimitMCP throttles MCP requests per org. It runs after requireAuth, so
// the org is already in context; a member key looping an expensive tool
// (triage_issue refresh=true) is bounded to mcpRatePerMin for the whole tenant.
func (s *Server) rateLimitMCP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.mcpLimiter.Allow("org:" + orgIDFrom(r.Context())) {
			w.Header().Set("Retry-After", "60")
			writeErr(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) mcpDispatch(ctx context.Context, org string, req mcpRequest) (any, *mcpError) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": mcpProtocol,
			"serverInfo":      map[string]string{"name": "flare", "version": "1.0.0"},
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"instructions": "Flare observability MCP. Start with `overview` for estate-wide health, then `list_issues` for a project and `get_issue` for a stack trace. `search_logs` reads the logs pillar. Writes: `update_issue_status` (resolve/ignore) and `triage_issue` (needs a BYOAI model configured in Flare settings). Everything is scoped to your org.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		tools := s.mcpToolset()
		out := make([]mcpTool, 0, len(tools))
		for _, t := range tools {
			out = append(out, t)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return map[string]any{"tools": out}, nil
	case "tools/call":
		return s.mcpCall(ctx, org, req.Params)
	}
	return nil, &mcpError{Code: -32601, Message: "method not found: " + req.Method}
}

func (s *Server) mcpCall(ctx context.Context, org string, params json.RawMessage) (any, *mcpError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &mcpError{Code: -32602, Message: "invalid params"}
	}
	tool, ok := s.mcpToolset()[p.Name]
	if !ok {
		return mcpToolResult{IsError: true, Content: []mcpContent{{Type: "text", Text: "unknown tool: " + p.Name}}}, nil
	}
	if tool.RequiresWrite && !roleAtLeast(roleFrom(ctx), "member") {
		return mcpToolResult{IsError: true, Content: []mcpContent{{Type: "text", Text: "this tool needs a member+ role; the credential is read-only"}}}, nil
	}
	val, err := tool.Handler(ctx, org, p.Arguments)
	if err != nil {
		return mcpToolResult{IsError: true, Content: []mcpContent{{Type: "text", Text: s.mcpErrText(p.Name, err)}}}, nil
	}
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: mcpJSON(val)}}}, nil
}

// mcpErrText renders a tool error for the client, returning only messages that
// are safe to expose: the two triage sentinels (which are caller-actionable and
// carry no upstream detail), authored validation/not-found messages
// (mcpUserError, telemetry.ErrNotFound), and nothing else. Any other error
// (raw pgx/DB failure, wrapped provider response body) is logged server-side
// and replaced with a generic string.
func (s *Server) mcpErrText(tool string, err error) string {
	switch {
	case errors.Is(err, errTriageNotConfigured):
		return errTriageNotConfigured.Error()
	case errors.Is(err, errTriageEndpoint):
		return errTriageEndpoint.Error()
	}
	var ue mcpUserError
	if errors.As(err, &ue) {
		return ue.Error()
	}
	var nf telemetry.ErrNotFound
	if errors.As(err, &nf) {
		return err.Error()
	}
	slog.Error("mcp tool failed", "tool", tool, "err", err)
	return "internal error"
}

func mcpWriteErr(w http.ResponseWriter, id any, code int, msg string) {
	writeJSON(w, http.StatusOK, mcpResponse{JSONRPC: "2.0", ID: id, Error: &mcpError{Code: code, Message: msg}})
}

func mcpJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func schema(raw string) json.RawMessage { return json.RawMessage(raw) }

// resolveProjectRef finds a project by its id OR its slug, scoped to org.
func (s *Server) resolveProjectRef(ctx context.Context, org, ref string) (*generated.Project, error) {
	if ref == "" {
		return nil, userErr("project is required (id or slug)")
	}
	if p, err := s.q.GetProjectByID(ctx, generated.GetProjectByIDParams{ID: ref, OrgScope: org}); err == nil {
		return p, nil
	}
	if p, err := s.q.GetProjectBySlug(ctx, generated.GetProjectBySlugParams{OrgID: org, Slug: ref}); err == nil {
		return p, nil
	}
	return nil, userErr("project %q not found", ref)
}

// mcpToolset builds the allow-listed tool catalog. Handlers reuse the same
// org-scoped Store/queries as the REST API.
func (s *Server) mcpToolset() map[string]mcpTool {
	tools := map[string]mcpTool{
		"overview": {
			Name:        "overview",
			Description: "Estate-wide error health: total events in 24h, unresolved count, new-today count, the top unresolved issues, and per-project open-issue counts.",
			InputSchema: schema(`{"type":"object","properties":{}}`),
			Handler: func(ctx context.Context, org string, _ json.RawMessage) (any, error) {
				ev, _ := s.q.OverviewEventCount24h(ctx, org)
				un, _ := s.q.OverviewUnresolvedCount(ctx, org)
				nt, _ := s.q.OverviewNewIssuesToday(ctx, org)
				top, _ := s.q.OverviewTopIssues(ctx, org)
				perProj, _ := s.q.OverviewProjectUnresolved(ctx, org)
				projects, _ := s.q.ListProjectsByOrg(ctx, org)
				byID := make(map[string]*generated.Project, len(projects))
				for _, p := range projects {
					byID[p.ID] = p
				}
				type projUnresolved struct {
					ProjectID  string `json:"project_id"`
					Name       string `json:"name"`
					Slug       string `json:"slug"`
					Unresolved int64  `json:"unresolved"`
				}
				pu := make([]projUnresolved, 0, len(perProj))
				for _, r := range perProj {
					row := projUnresolved{ProjectID: r.ProjectID, Unresolved: r.Count}
					if p := byID[r.ProjectID]; p != nil {
						row.Name, row.Slug = p.Name, p.Slug
					}
					pu = append(pu, row)
				}
				return map[string]any{
					"events_24h": ev, "unresolved": un, "new_today": nt,
					"top_issues": top, "projects": pu,
				}, nil
			},
		},
		"list_projects": {
			Name:        "list_projects",
			Description: "List the org's projects (id, name, slug, platform).",
			InputSchema: schema(`{"type":"object","properties":{}}`),
			Handler: func(ctx context.Context, org string, _ json.RawMessage) (any, error) {
				projects, err := s.q.ListProjectsByOrg(ctx, org)
				if err != nil {
					return nil, err
				}
				out := make([]map[string]string, 0, len(projects))
				for _, p := range projects {
					out = append(out, map[string]string{"id": p.ID, "name": p.Name, "slug": p.Slug, "platform": p.Platform})
				}
				return out, nil
			},
		},
		"list_issues": {
			Name:        "list_issues",
			Description: "List a project's issues. project = id or slug. status = unresolved (default) | resolved | ignored | all. limit default 25.",
			InputSchema: schema(`{"type":"object","properties":{"project":{"type":"string"},"status":{"type":"string"},"limit":{"type":"integer"}},"required":["project"]}`),
			Handler: func(ctx context.Context, org string, args json.RawMessage) (any, error) {
				var a struct {
					Project string `json:"project"`
					Status  string `json:"status"`
					Limit   int32  `json:"limit"`
				}
				_ = json.Unmarshal(args, &a)
				proj, err := s.resolveProjectRef(ctx, org, a.Project)
				if err != nil {
					return nil, err
				}
				if a.Limit <= 0 || a.Limit > 200 {
					a.Limit = 25
				}
				var statusFilter *string
				if a.Status != "" && a.Status != "all" {
					statusFilter = &a.Status
				}
				issues, err := s.store.ListIssues(ctx, proj.ID, org, a.Limit, 0, statusFilter)
				if err != nil {
					return nil, err
				}
				out := make([]issueResponse, 0, len(issues))
				for _, i := range issues {
					out = append(out, toIssueResponse(i))
				}
				return out, nil
			},
		},
		"get_issue": {
			Name:        "get_issue",
			Description: "Full detail for one issue by id: metadata plus its most recent events, including exception type/value and the stack trace. The core investigation tool.",
			InputSchema: schema(`{"type":"object","properties":{"issue_id":{"type":"string"},"events":{"type":"integer"}},"required":["issue_id"]}`),
			Handler: func(ctx context.Context, org string, args json.RawMessage) (any, error) {
				var a struct {
					IssueID string `json:"issue_id"`
					Events  int32  `json:"events"`
				}
				_ = json.Unmarshal(args, &a)
				issue, err := s.store.GetIssue(ctx, a.IssueID, org)
				if err != nil {
					var nf telemetry.ErrNotFound
					if errors.As(err, &nf) {
						return nil, userErr("issue %q not found", a.IssueID)
					}
					return nil, err
				}
				if a.Events <= 0 || a.Events > 20 {
					a.Events = 3
				}
				events, _ := s.store.ListEventsByIssue(ctx, a.IssueID, org, a.Events)
				return map[string]any{
					"issue":  toIssueResponse(issue),
					"events": s.toEventResponses(ctx, a.IssueID, org, events, issue.Sensitive != ""),
				}, nil
			},
		},
		"search_logs": {
			Name:        "search_logs",
			Description: "Search a project's log records. project = id or slug. Optional severity and query (substring). limit default 50. Returns [] until services ship logs (OTLP) to Flare.",
			InputSchema: schema(`{"type":"object","properties":{"project":{"type":"string"},"severity":{"type":"string"},"query":{"type":"string"},"limit":{"type":"integer"}},"required":["project"]}`),
			Handler: func(ctx context.Context, org string, args json.RawMessage) (any, error) {
				var a struct {
					Project  string `json:"project"`
					Severity string `json:"severity"`
					Query    string `json:"query"`
					Limit    int32  `json:"limit"`
				}
				_ = json.Unmarshal(args, &a)
				proj, err := s.resolveProjectRef(ctx, org, a.Project)
				if err != nil {
					return nil, err
				}
				if a.Limit <= 0 || a.Limit > 500 {
					a.Limit = 50
				}
				f := telemetry.LogFilter{Limit: a.Limit}
				if a.Severity != "" {
					f.Severity = &a.Severity
				}
				if a.Query != "" {
					f.Query = &a.Query
				}
				logs, err := s.store.SearchLogs(ctx, proj.ID, org, f)
				if err != nil {
					return nil, err
				}
				return toLogResponses(logs), nil
			},
		},
		"update_issue_status": {
			Name:          "update_issue_status",
			RequiresWrite: true,
			Description:   "Set an issue's status: unresolved | resolved | ignored. Write operation.",
			InputSchema:   schema(`{"type":"object","properties":{"issue_id":{"type":"string"},"status":{"type":"string"}},"required":["issue_id","status"]}`),
			Handler: func(ctx context.Context, org string, args json.RawMessage) (any, error) {
				var a struct {
					IssueID string `json:"issue_id"`
					Status  string `json:"status"`
				}
				_ = json.Unmarshal(args, &a)
				switch a.Status {
				case "unresolved", "resolved", "ignored":
				default:
					return nil, userErr("status must be unresolved, resolved, or ignored")
				}
				i, err := s.q.UpdateIssueStatus(ctx, generated.UpdateIssueStatusParams{ID: a.IssueID, OrgID: org, Status: a.Status})
				if err != nil {
					return nil, userErr("issue %q not found", a.IssueID)
				}
				return genIssueToResponse(i), nil
			},
		},
		"triage_issue": {
			Name:          "triage_issue",
			RequiresWrite: true,
			Description:   "Run (or return cached) AI triage for an issue: a plain-language cause and fix built from the scrubbed stack trace. Requires a BYOAI model configured in Flare settings; returns an error naming that if not. refresh=true forces a re-run.",
			InputSchema:   schema(`{"type":"object","properties":{"issue_id":{"type":"string"},"refresh":{"type":"boolean"}},"required":["issue_id"]}`),
			Handler: func(ctx context.Context, org string, args json.RawMessage) (any, error) {
				var a struct {
					IssueID string `json:"issue_id"`
					Refresh bool   `json:"refresh"`
				}
				_ = json.Unmarshal(args, &a)
				text, cached, err := s.triageIssue(ctx, org, a.IssueID, a.Refresh)
				if err != nil {
					return nil, err
				}
				return map[string]any{"triage": text, "cached": cached}, nil
			},
		},
	}
	return tools
}
