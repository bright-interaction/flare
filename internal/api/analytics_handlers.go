package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// handleLogVolume returns hourly log counts by severity (columnar, DuckDB over
// the hot+cold union). 503 when analytics is disabled.
func (s *Server) handleLogVolume(w http.ResponseWriter, r *http.Request) {
	if s.analytics == nil {
		writeErr(w, http.StatusServiceUnavailable, "analytics not available")
		return
	}
	buckets, err := s.analytics.LogVolumeByHour(r.Context(), chi.URLParam(r, "id"), orgIDFrom(r.Context()), sinceHours(r))
	if err != nil {
		slogError(w, "analytics log volume", err)
		return
	}
	writeJSON(w, http.StatusOK, buckets)
}

// handleSpanLatency returns p50/p95/p99 span duration per operation.
func (s *Server) handleSpanLatency(w http.ResponseWriter, r *http.Request) {
	if s.analytics == nil {
		writeErr(w, http.StatusServiceUnavailable, "analytics not available")
		return
	}
	rows, err := s.analytics.SpanLatencyPercentiles(r.Context(), chi.URLParam(r, "id"), orgIDFrom(r.Context()), sinceHours(r))
	if err != nil {
		slogError(w, "analytics span latency", err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

// sinceHours reads ?hours= (default 24, max 720 = 30 days hot window).
func sinceHours(r *http.Request) int {
	if v, err := strconv.Atoi(r.URL.Query().Get("hours")); err == nil && v > 0 && v <= 720 {
		return v
	}
	return 24
}
