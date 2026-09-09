package localui

import (
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// dist holds the built React UI (desktop/ui), copied here by its build.
//
// The "all:" prefix matters: without it the .gitkeep that keeps this directory
// in the repository is skipped, and a checkout with no UI build would not
// compile at all. With it, a developer who has not run the UI build still gets
// a binary — one that serves placeholderPage and says exactly what is missing.
//
//go:embed all:dist
var dist embed.FS

// embeddedAssets serves the built UI, falling back to a page that explains
// itself when there is no build to serve.
func embeddedAssets(logger *slog.Logger) http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		logger.Error("the embedded UI directory is unreadable", "error", err)
		return http.HandlerFunc(placeholderPage)
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		logger.Warn("this binary was built without the UI; serving the placeholder page instead")
		return http.HandlerFunc(placeholderPage)
	}
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		logger.Error("the embedded index.html is unreadable", "error", err)
		return http.HandlerFunc(placeholderPage)
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A single-page app owns its routes: anything that is not a real file
		// is the app itself.
		//
		// The page is written here rather than handed to the file server,
		// which answers a request for "/index.html" with a redirect to "./" —
		// and a redirect would drop the token the tray put in the query
		// string, landing the user on a page that cannot talk to the service.
		if r.URL.Path != "/index.html" && (strings.HasPrefix(r.URL.Path, "/assets/") || hasFile(sub, r.URL.Path)) {
			// A real file, or a request for one that should 404 rather than
			// quietly returning the app.
			files.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(index)
	})
}

func hasFile(fsys fs.FS, path string) bool {
	name := strings.TrimPrefix(path, "/")
	if name == "" {
		return false
	}
	info, err := fs.Stat(fsys, name)
	return err == nil && !info.IsDir()
}

// placeholderPage is what a binary built without the UI serves. It is
// deliberately blunt: a developer who sees it has skipped a build step, and a
// blank page would send them looking for a bug that is not there.
func placeholderPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<!doctype html>
<meta charset="utf-8">
<title>TwoPlacePaste — UI not built</title>
<style>body{font:14px/1.6 system-ui,sans-serif;margin:3rem auto;max-width:38rem;padding:0 1rem}code{background:#eee;padding:.1rem .3rem;border-radius:3px}</style>
<h1>The UI is not in this binary</h1>
<p>The service is running and its API is answering; what is missing is the built
React app that normally lives in <code>desktop/internal/localui/dist</code>.</p>
<p>Build it with <code>npm ci &amp;&amp; npm run build</code> in <code>desktop/ui</code>,
then rebuild the binary — or run the dev server and start the service with
<code>TPP_DESKTOP_UI_DEV=http://127.0.0.1:5173</code>.</p>
`))
}

// devProxy forwards everything that is not the API to a running Vite server.
func devProxy(target string) (http.Handler, error) {
	u, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("localui: parse the dev server URL %q: %w", target, err)
	}
	if u.Scheme != "http" || u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" {
		// A dev mode that can be pointed at an arbitrary host is a way to make
		// this service fetch and serve someone else's page under its own
		// origin, token and all.
		return nil, fmt.Errorf("localui: the dev server must be http on localhost, got %q", target)
	}
	return httputil.NewSingleHostReverseProxy(u), nil
}
