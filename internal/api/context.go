package api

import "context"

type ctxKey int

const (
	ctxUserID ctxKey = iota
	ctxOrgID
)

func userIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxUserID).(string)
	return v
}

func orgIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxOrgID).(string)
	return v
}
