package api

import (
	"net/http"
	"sort"
	"time"
)

const overviewBuckets = 24 // one per hour, last 24h

type overviewHourBucket struct {
	Hour  string `json:"hour"` // RFC3339, top of the hour, UTC
	Count int64  `json:"count"`
}

type overviewIssue struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Level       string `json:"level"`
	EventCount  int64  `json:"event_count"`
	ProjectID   string `json:"project_id"`
	ProjectName string `json:"project_name"`
}

type overviewProject struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Slug       string  `json:"slug"`
	Events24h  int64   `json:"events_24h"`
	Unresolved int64   `json:"unresolved"`
	Status     string  `json:"status"` // ok | spike | quiet
	Volume     []int64 `json:"volume"` // overviewBuckets hourly counts
}

type overviewResponse struct {
	Events24h  int64                `json:"events_24h"`
	Unresolved int64                `json:"unresolved"`
	NewToday   int64                `json:"new_today"`
	Volume     []overviewHourBucket `json:"volume"`
	TopIssues  []overviewIssue      `json:"top_issues"`
	Projects   []overviewProject    `json:"projects"`
}

// handleOverview returns the org-wide dashboard: 24h event volume, headline
// counts, the top unresolved issues, and a per-project health card with an
// hourly sparkline. Everything is org-scoped; a member API key or a session
// can read it.
func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	org := orgIDFrom(ctx)

	// Bucket 0 is the oldest hour, bucket overviewBuckets-1 is the current
	// (partial) hour. Everything aligns to the top of the hour in UTC.
	base := time.Now().UTC().Truncate(time.Hour).Add(-time.Duration(overviewBuckets-1) * time.Hour)
	bucketIndex := func(t time.Time) int {
		return int(t.UTC().Truncate(time.Hour).Sub(base) / time.Hour)
	}

	events24h, err := s.q.OverviewEventCount24h(ctx, org)
	if err != nil {
		slogError(w, "overview event count", err)
		return
	}
	unresolved, err := s.q.OverviewUnresolvedCount(ctx, org)
	if err != nil {
		slogError(w, "overview unresolved count", err)
		return
	}
	newToday, err := s.q.OverviewNewIssuesToday(ctx, org)
	if err != nil {
		slogError(w, "overview new today", err)
		return
	}

	// Org-wide hourly volume, zero-filled.
	volume := make([]overviewHourBucket, overviewBuckets)
	for i := range volume {
		volume[i] = overviewHourBucket{Hour: base.Add(time.Duration(i) * time.Hour).Format(time.RFC3339)}
	}
	volRows, err := s.q.OverviewEventVolumeByHour(ctx, org)
	if err != nil {
		slogError(w, "overview volume", err)
		return
	}
	for _, row := range volRows {
		if !row.Hour.Valid {
			continue
		}
		if i := bucketIndex(row.Hour.Time); i >= 0 && i < overviewBuckets {
			volume[i].Count = row.Count
		}
	}

	topRows, err := s.q.OverviewTopIssues(ctx, org)
	if err != nil {
		slogError(w, "overview top issues", err)
		return
	}
	top := make([]overviewIssue, 0, len(topRows))
	for _, row := range topRows {
		top = append(top, overviewIssue{
			ID: row.ID, Title: row.Title, Level: row.Level,
			EventCount: row.EventCount, ProjectID: row.ProjectID, ProjectName: row.ProjectName,
		})
	}

	// Per-project: hourly volume + unresolved count, keyed by project id.
	projVol := map[string][]int64{}
	projTotal := map[string]int64{}
	pvRows, err := s.q.OverviewProjectVolumeByHour(ctx, org)
	if err != nil {
		slogError(w, "overview project volume", err)
		return
	}
	for _, row := range pvRows {
		if !row.Hour.Valid {
			continue
		}
		i := bucketIndex(row.Hour.Time)
		if i < 0 || i >= overviewBuckets {
			continue
		}
		b := projVol[row.ProjectID]
		if b == nil {
			b = make([]int64, overviewBuckets)
			projVol[row.ProjectID] = b
		}
		b[i] = row.Count
		projTotal[row.ProjectID] += row.Count
	}
	projUnres := map[string]int64{}
	puRows, err := s.q.OverviewProjectUnresolved(ctx, org)
	if err != nil {
		slogError(w, "overview project unresolved", err)
		return
	}
	for _, row := range puRows {
		projUnres[row.ProjectID] = row.Count
	}

	projects, err := s.q.ListProjectsByOrg(ctx, org)
	if err != nil {
		slogError(w, "overview projects", err)
		return
	}
	cards := make([]overviewProject, 0, len(projects))
	for _, p := range projects {
		vol := projVol[p.ID]
		if vol == nil {
			vol = make([]int64, overviewBuckets)
		}
		total := projTotal[p.ID]
		cards = append(cards, overviewProject{
			ID: p.ID, Name: p.Name, Slug: p.Slug,
			Events24h:  total,
			Unresolved: projUnres[p.ID],
			Status:     projectStatus(vol, total),
			Volume:     vol,
		})
	}
	// Busiest projects first, then alphabetical for the quiet tail.
	sort.SliceStable(cards, func(i, j int) bool {
		if cards[i].Events24h != cards[j].Events24h {
			return cards[i].Events24h > cards[j].Events24h
		}
		return cards[i].Name < cards[j].Name
	})

	writeJSON(w, http.StatusOK, overviewResponse{
		Events24h: events24h, Unresolved: unresolved, NewToday: newToday,
		Volume: volume, TopIssues: top, Projects: cards,
	})
}

// projectStatus flags a project card. "quiet" = no events in 24h; "spike" =
// the current hour is both meaningful (>=5) and at least 3x the trailing
// hourly average; otherwise "ok". vol is the zero-filled hourly series with
// the current (partial) hour last.
func projectStatus(vol []int64, total int64) string {
	if total == 0 {
		return "quiet"
	}
	if len(vol) < 2 {
		return "ok"
	}
	last := vol[len(vol)-1]
	var prior int64
	for _, c := range vol[:len(vol)-1] {
		prior += c
	}
	avg := float64(prior) / float64(len(vol)-1)
	if last >= 5 && float64(last) >= 3*avg {
		return "spike"
	}
	return "ok"
}
