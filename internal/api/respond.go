package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("encode response", "error", err)
	}
}

// writeErr returns a generic client-facing message; details stay server-side.
func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// slogError logs the real cause server-side and returns a generic 500 so
// internals never leak to the client.
func slogError(w http.ResponseWriter, msg string, err error) {
	slog.Error(msg, "error", err)
	writeErr(w, http.StatusInternalServerError, "internal error")
}

// maxJSONBody bounds a decoded request body.
const maxJSONBody = 1 << 20

func decodeJSON(r *http.Request, dst any) error {
	// nil ResponseWriter is safe (the cap is still enforced) but loses the
	// connection-close signal, so an oversized body is read to the cap and the
	// client is never told why. There is one body-cap idiom in this tree now
	// and it is this one, spelled the same way everywhere.
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxJSONBody))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
