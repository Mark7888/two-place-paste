package admin

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strings"

	webadmin "github.com/Mark7888/two-place-paste/server/web/admin"
)

// pagePolicy is the Content-Security-Policy of the admin UI. The app is one
// module script and one stylesheet from this origin; it fetches only this
// origin's JSON API and shows only this origin's images (the token QR codes).
// There is no inline script or style for the policy to have to allow, and
// nothing may frame the page.
const pagePolicy = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// assetTypes pins the content type of every kind of file the UI build
// produces. mime.TypeByExtension falls back to the host's own tables, which
// differ between machines and do not all know .ico.
var assetTypes = map[string]string{
	".js":   "text/javascript; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".svg":  "image/svg+xml",
	".ico":  "image/x-icon",
	".png":  "image/png",
	".txt":  "text/plain; charset=utf-8",
	".json": "application/json",
}

// uiAssets is the built admin UI, as the server serves it.
type uiAssets struct {
	files fs.FS

	// index is the app's one HTML page, served for every screen. Nil when the
	// binary was built without the UI; placeholderPage is served instead.
	index []byte
}

// loadUI resolves the UI to serve: the one given in Options, or the build
// embedded in the binary.
//
// A missing build is not an error. A developer who skipped the UI build gets a
// relay whose API and creation endpoint work and whose admin page says exactly
// which step is missing, rather than a binary that refuses to start.
func loadUI(files fs.FS, logger *slog.Logger) (uiAssets, error) {
	if files == nil {
		sub, err := fs.Sub(webadmin.FS, "dist")
		if err != nil {
			return uiAssets{}, fmt.Errorf("admin: open the embedded UI: %w", err)
		}
		files = sub
	}
	index, err := fs.ReadFile(files, "index.html")
	if errors.Is(err, fs.ErrNotExist) {
		logger.Warn("this binary was built without the admin UI; the admin page explains how to build it")
		return uiAssets{files: files}, nil
	}
	if err != nil {
		return uiAssets{}, fmt.Errorf("admin: read the UI's index.html: %w", err)
	}
	return uiAssets{files: files, index: index}, nil
}

// handlePage serves the app for one of its screens. Every screen but the
// sign-in one needs a session, and the check is made here as well as by the
// API: a signed-out visitor is sent to the sign-in screen before the app has
// loaded, rather than seeing a screen flash and disappear.
func (s *Server) handlePage(needSession bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := s.session(r)
		switch {
		case needSession && !ok:
			s.redirect(w, r, "/admin/login")
			return
		case !needSession && ok:
			// The sign-in screen, with a session already: go to the dashboard.
			s.redirect(w, r, "/admin/")
			return
		}
		s.writeIndex(w)
	}
}

func (s *Server) writeIndex(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", pagePolicy)
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if s.ui.index == nil {
		_, _ = w.Write([]byte(placeholderPage))
		return
	}
	_, _ = w.Write(s.ui.index)
}

// handleAsset serves one file of the UI build: the bundle under /admin/assets/
// and the favicons. All of it is public, because the sign-in screen needs it
// before a session exists, and none of it holds anything: the app gets every
// piece of data from the API.
func (s *Server) handleAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/admin/")
	// fs.ValidPath refuses "..", empty and absolute names, so nothing outside
	// the build can be named; the directory check keeps a listing from being
	// served.
	if !fs.ValidPath(name) || name == "index.html" {
		http.NotFound(w, r)
		return
	}
	b, err := fs.ReadFile(s.ui.files, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	contentType, ok := assetTypes[path.Ext(name)]
	if !ok {
		contentType = mime.TypeByExtension(path.Ext(name))
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("X-Content-Type-Options", "nosniff")
	// The bundle's file names are fixed rather than hashed, so a browser must
	// revalidate rather than keep an old build after an upgrade.
	h.Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}

// placeholderPage is what a binary built without the UI serves on every admin
// screen. It is deliberately blunt: an operator who sees it has a binary built
// from a checkout without its UI step, and a blank page would send them
// looking for a bug that is not there. It keeps the AGPL source link every
// page of the relay carries.
const placeholderPage = `<!doctype html>
<html lang="en">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow">
<title>Admin UI not built — TwoPlacePaste</title>
<h1>The admin UI is not in this binary</h1>
<p>The relay is running and its API is answering; what is missing is the built
React app that normally lives in <code>server/web/admin/dist</code>.</p>
<p>Build it with <code>npm ci &amp;&amp; npm run build</code> in <code>server/ui</code>,
then rebuild the binary. The Docker image always includes it.</p>
<p><a href="/source">Source code</a> — this relay is free software under the AGPL-3.0.</p>
</html>
`
