-- name: InsertSpans :copyfrom
INSERT INTO spans (trace_id, span_id, parent_span_id, project_id, org_id, name, kind, status, start_time, end_time, duration_ms, attributes)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12);

-- name: ListTraces :many
SELECT
    s.trace_id,
    min(s.start_time)::timestamptz AS started,
    max(s.end_time)::timestamptz AS ended,
    count(*) AS span_count,
    bool_or(s.status ILIKE '%error%') AS has_error,
    (SELECT r.name FROM spans r
       WHERE r.trace_id = s.trace_id AND r.org_id = s.org_id AND r.project_id = s.project_id
       ORDER BY (r.parent_span_id = '') DESC, r.start_time
       LIMIT 1) AS root_name
FROM spans s
WHERE s.project_id = $1 AND s.org_id = $2
GROUP BY s.trace_id, s.org_id
ORDER BY started DESC
LIMIT $3;

-- name: GetTraceSpans :many
-- project_id scoped: trace_id is not project-unique (a distributed trace can
-- span projects), so org_id alone would mix projects' spans.
SELECT * FROM spans
WHERE trace_id = $1 AND project_id = $2 AND org_id = $3
ORDER BY start_time;
