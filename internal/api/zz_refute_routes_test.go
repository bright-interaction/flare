package api

import (
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"
)

// TestRefuteChangePasswordRoute enumerates every route the production router
// registers and reports any surface that could change a password.
func TestRefuteChangePasswordRoute(t *testing.T) {
	build := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html></html>")}}
	passthrough := func(next http.Handler) http.Handler { return next }
	h := (&Server{}).Routes(build, passthrough)

	r, ok := h.(chi.Routes)
	if !ok {
		t.Fatalf("router is not chi.Routes: %T", h)
	}
	var all []string
	err := chi.Walk(r, func(method string, route string, handler http.Handler, mw ...func(http.Handler) http.Handler) error {
		all = append(all, method+" "+route)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	for _, rt := range all {
		t.Log(rt)
	}
	for _, rt := range all {
		l := strings.ToLower(rt)
		if strings.Contains(l, "password") {
			t.Logf("PASSWORD SURFACE: %s", rt)
		}
	}
}
