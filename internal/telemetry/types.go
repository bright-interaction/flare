// Package telemetry defines Flare's read/analytics Store contract and the
// domain types it returns, decoupled from the sqlc-generated package (plain Go
// types, no pgtype) so a future columnar backend can satisfy the same
// interface without dragging Postgres specifics into the handlers.
package telemetry

import (
	"encoding/json"
	"time"
)

type Issue struct {
	ID          string
	ProjectID   string
	OrgID       string
	Fingerprint string
	Title       string
	Culprit     string
	Level       string
	Status      string
	Platform    string
	FirstSeen    time.Time
	LastSeen     time.Time
	EventCount   int64
	GithubURL    string
	FirstRelease string
	AITriage     string
	Sensitive    string
}

type Event struct {
	ID             string
	Level          string
	Message        string
	ExceptionType  string
	ExceptionValue string
	Platform       string
	Environment    string
	Release        string
	Stacktrace     json.RawMessage
	ReceivedAt     time.Time
}

type Log struct {
	ID         string
	Severity   string
	Body       string
	Attributes json.RawMessage
	TraceID    string
	SpanID     string
	ObservedAt time.Time
}

type TraceSummary struct {
	TraceID    string
	RootName   string
	SpanCount  int64
	HasError   bool
	DurationMs float64
	Started    time.Time
}

type Span struct {
	SpanID       string
	ParentSpanID string
	Name         string
	Kind         string
	Status       string
	StartUnixMs  int64
	DurationMs   float64
	Attributes   json.RawMessage
}

// LogFilter carries the optional log-search predicates (nil = no filter),
// matching the existing sqlc.narg semantics in the handlers.
type LogFilter struct {
	Severity *string
	Query    *string
	TraceID  *string
	Since    *time.Time
	Limit    int32
}
