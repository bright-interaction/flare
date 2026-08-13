package flarereport

import (
	"strings"

	sentry "github.com/getsentry/sentry-go"
)

// authHeaders are the request headers whose value is a credential.
//
// X-Flare-Key and X-Sentry-Auth are on this list and are on no sibling's,
// because they are how Flare's OWN ingest endpoint is authenticated
// (ingest_handlers.go reads the key from exactly those places) and sentry-go's
// built-in sensitiveHeaders set does not contain either. Every service in the
// estate that ships to Flare should carry them too.
var authHeaders = []string{
	"Authorization", "Cookie", "Set-Cookie",
	"X-Api-Key", "X-API-Key", "X-Auth-Token",
	"X-Flare-Key", "X-Sentry-Auth", "Mcp-Session-Id",
}

// scrubRequestEvent strips request context that must not reach the event store:
// the query string, the cookies, and auth headers. Method and path are kept, so
// triage still knows what was being called.
//
// Flare was the ONE service in the estate with no request scrub at all, which
// is the worst place for the gap to be, because the events it reports go into
// Flare's own project inside the OPERATOR's org. sentry-go v0.47.0 sets
// QueryString from r.URL.RawQuery unconditionally, regardless of SendDefaultPII.
// So a tenant of org B sending /store/?sentry_key=<org B key> shaped to panic in
// the ingest path wrote org B's ingest credential into another tenant's project,
// at rest, in every backup, and surviving org B's own erasure because the row
// belongs to a different org. The OIDC variant put a live authorization code and
// its CSRF state in the same place.
//
// Shared by BeforeSend (errors) and BeforeSendTransaction (traces): transaction
// envelopes route through a SEPARATE hook, so registering only the first covers
// half the traffic.
func scrubRequestEvent(event *sentry.Event) *sentry.Event {
	if event == nil || event.Request == nil {
		return event
	}
	event.Request.QueryString = ""
	event.Request.Cookies = ""
	for _, h := range authHeaders {
		delete(event.Request.Headers, h)
	}
	if i := strings.IndexByte(event.Request.URL, '?'); i >= 0 {
		event.Request.URL = event.Request.URL[:i]
	}
	return event
}
