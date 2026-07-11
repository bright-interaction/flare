package api

import (
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func pgText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: true}
}

// tsPtr renders a nullable timestamptz as an RFC3339 string pointer (nil when
// NULL), so JSON responses carry a clean ISO timestamp or null.
func tsPtr(t pgtype.Timestamptz) *string {
	if !t.Valid {
		return nil
	}
	s := t.Time.UTC().Format(time.RFC3339)
	return &s
}

// textStr returns the string of a nullable text column, or "" when NULL.
func textStr(t pgtype.Text) string {
	if !t.Valid {
		return ""
	}
	return t.String
}
