package api

import (
	_ "embed"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// placeholderHTML is served when no built frontend is present, so a fresh
// install shows something useful instead of a blank 404.
//
//go:embed placeholder.html
var placeholderHTML []byte

// newStaticHandler serves the built single-page app from webRoot.
//
// Two rules matter. Hashed build assets under /assets are immutable and cached
// for a year; everything else, above all index.html, must not be cached or a
// deploy leaves users on the previous build. Any path that is not a real file
// falls through to index.html so client-side routes survive a refresh.
func newStaticHandler(webRoot string, log *slog.Logger) http.Handler {
	if webRoot == "" {
		return placeholderHandler()
	}

	index := filepath.Join(webRoot, "index.html")
	if _, err := os.Stat(index); err != nil {
		log.Warn("no built frontend found, serving the placeholder page",
			"web_root", webRoot, "hint", "run npm run build in web/")
		return placeholderHandler()
	}

	files := http.FileServer(http.Dir(webRoot))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A request that reached the SPA handler but asks for an API path is a
		// routing mistake, not a page: answering with index.html would hand
		// the client HTML where it expects JSON.
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeJSON(w, http.StatusNotFound, ErrorBody{
				Error: "No such endpoint.", Code: "not_found",
			})
			return
		}

		clean := path.Clean("/" + r.URL.Path)
		candidate := filepath.Join(webRoot, filepath.FromSlash(clean))

		// Reject anything that escapes the web root, whatever the input.
		if !strings.HasPrefix(candidate, webRoot) {
			http.NotFound(w, r)
			return
		}

		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			serveIndex(w, r, index)
			return
		}

		if strings.HasPrefix(clean, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, index string) {
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeFile(w, r, index)
}

func placeholderHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeJSON(w, http.StatusNotFound, ErrorBody{
				Error: "No such endpoint.", Code: "not_found",
			})
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(placeholderHTML)
	})
}
