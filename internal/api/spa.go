package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// spaHandler serves the embedded SvelteKit build. Unknown non-asset paths
// fall back to index.html so client-side routing works on deep links.
func spaHandler(build fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(build))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if clean == "" {
			clean = "index.html"
		}
		if _, err := fs.Stat(build, clean); err != nil {
			// Not a real file: hand the SPA shell to the client router.
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			http.ServeFileFS(w, r2, build, "index.html")
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}
