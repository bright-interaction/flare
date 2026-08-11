package telemetry

import (
	"context"
	"time"
)

// Store is the pluggable read/analytics surface for telemetry. The dashboard
// read handlers depend only on this interface, so swapping the backing engine
// (Postgres today, a columnar store later) is a one-line change in main.go.
//
// Writes (ingest: UpsertIssue, InsertLogs/InsertSpans CopyFrom, status updates)
// deliberately do NOT go through Store; they stay on the generated queries.
// Store is read-only, mirroring the atomicsite analyticsdb split.
type Store interface {
	// q is an optional case-insensitive substring match on title/culprit.
	// CountIssues MUST apply the same status+q filter as ListIssues, or the
	// total contradicts the page.
	ListIssues(ctx context.Context, projectID, orgID string, limit, offset int32, status, q *string) ([]Issue, error)
	CountIssues(ctx context.Context, projectID, orgID string, status, q *string) (int64, error)
	GetIssue(ctx context.Context, issueID, orgID string) (Issue, error)
	ListEventsByIssue(ctx context.Context, issueID, orgID string, limit int32) ([]Event, error)
	SearchLogs(ctx context.Context, projectID, orgID string, f LogFilter) ([]Log, error)
	ListTraces(ctx context.Context, projectID, orgID string, since time.Time, limit int32) ([]TraceSummary, error)
	// GetTraceSpans returns at most limit spans. The trace id is caller-chosen
	// and ingest is rate-limited per DSN key rather than per trace, so an
	// uncapped read let one accumulated trace return 52 MB in a single tool
	// result.
	GetTraceSpans(ctx context.Context, traceID, projectID, orgID string, limit int32) ([]Span, error)
	// ListMetricNames returns at most limit distinct names. Metric-name
	// cardinality is attacker-chosen: 364,722 distinct names came out of one
	// 8 MiB request.
	ListMetricNames(ctx context.Context, projectID, orgID string, limit int32) ([]MetricName, error)
	QueryMetricSeries(ctx context.Context, projectID, orgID, name string, since time.Time, limit int32) ([]MetricPoint, error)
	Healthy(ctx context.Context) bool
}

// ErrNotFound is returned by GetIssue when no row matches in the caller's org.
type ErrNotFound struct{ What string }

func (e ErrNotFound) Error() string { return e.What + " not found" }
