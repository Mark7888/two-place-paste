package admin

import "net/http"

// Register implements httpapi.Registrar (ROADMAP §4): it attaches this
// package's routes and nothing else. The router itself is a shared touchpoint
// and is not edited here.
//
// The admin surface is three kinds of route:
//
//   - the app's screens, which all serve the same index.html and leave the
//     rest to the React app (server/ui). Only the screens listed here exist;
//     any other path under /admin/ is a 404, as it always was.
//   - its static files: the bundle under /admin/assets/ and the favicons.
//   - the JSON API under /admin/api/, which is all the app talks to.
//
// Groups and tokens are named in every path by their handle (handle.go),
// never by their id or value.
//
// The two single-segment patterns are what make SPEC §3.1 structural: "GET
// /{token}" exists only to answer 404, and only "POST /{token}" reaches the
// store. Every route registered by another component — "GET /ws", "GET
// /healthz", everything under "/admin/" — is a more specific pattern, so
// net/http prefers it and the wildcard never shadows it.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		s.redirect(w, r, "/admin/")
	})

	// The app's screens (server/ui/src/App.tsx lists the same ones).
	mux.HandleFunc("GET /admin/login", s.handlePage(false))
	for _, p := range []string{
		"/admin/{$}",
		"/admin/groups",
		"/admin/groups/{handle}",
		"/admin/groups/{handle}/purge",
		"/admin/groups/{handle}/delete",
		"/admin/tags",
		"/admin/tokens",
		"/admin/audit",
		"/admin/settings",
	} {
		mux.HandleFunc("GET "+p, s.handlePage(true))
	}

	mux.HandleFunc("GET /admin/assets/", s.handleAsset)
	mux.HandleFunc("GET /admin/favicon.svg", s.handleAsset)
	mux.HandleFunc("GET /admin/favicon.ico", s.handleAsset)
	mux.HandleFunc("GET /admin/apple-touch-icon.png", s.handleAsset)
	// The installable app: its manifest, icons and service worker. The worker
	// is served from /admin/ so that its scope is the admin and nothing else.
	mux.HandleFunc("GET /admin/manifest.webmanifest", s.handleAsset)
	mux.HandleFunc("GET /admin/sw.js", s.handleAsset)
	mux.HandleFunc("GET /admin/icon-192.png", s.handleAsset)
	mux.HandleFunc("GET /admin/icon-512.png", s.handleAsset)
	mux.HandleFunc("GET /admin/icon-maskable-512.png", s.handleAsset)

	// The QR image is an <img>, so it is a plain session-checked GET outside
	// the JSON API.
	mux.HandleFunc("GET /admin/tokens/{handle}/qr.png", s.handleTokenQR)

	mux.HandleFunc("/admin/api/", s.handleAPINotFound)
	mux.HandleFunc("POST /admin/api/login", s.handleLogin)
	mux.HandleFunc("POST /admin/api/logout", s.handleLogout)
	mux.HandleFunc("GET /admin/api/session", s.read(s.handleSession))

	mux.HandleFunc("GET /admin/api/dashboard", s.read(s.handleDashboard))
	mux.HandleFunc("POST /admin/api/sweep", s.write(s.handleSweep))

	mux.HandleFunc("GET /admin/api/tokens", s.read(s.handleTokens))
	mux.HandleFunc("POST /admin/api/tokens", s.write(s.handleCreateToken))
	mux.HandleFunc("GET /admin/api/tokens/{handle}/link", s.read(s.handleTokenLink))
	mux.HandleFunc("DELETE /admin/api/tokens/{handle}", s.write(s.handleRevokeToken))

	mux.HandleFunc("GET /admin/api/groups", s.read(s.handleGroupList))
	mux.HandleFunc("GET /admin/api/groups/{handle}", s.read(s.handleGroup))
	mux.HandleFunc("DELETE /admin/api/groups/{handle}", s.write(s.handleDeleteGroup))
	mux.HandleFunc("PUT /admin/api/groups/{handle}/name", s.write(s.handleRenameGroup))
	mux.HandleFunc("PUT /admin/api/groups/{handle}/tag", s.write(s.handleSetGroupTag))
	mux.HandleFunc("PUT /admin/api/groups/{handle}/limit", s.write(s.handleSetGroupLimit))
	mux.HandleFunc("POST /admin/api/groups/{handle}/purge", s.write(s.handlePurgeGroup))

	mux.HandleFunc("GET /admin/api/tags", s.read(s.handleTags))
	mux.HandleFunc("POST /admin/api/tags", s.write(s.handleCreateTag))
	mux.HandleFunc("PUT /admin/api/tags/{name}/limit", s.write(s.handleSetTagLimit))
	mux.HandleFunc("DELETE /admin/api/tags/{name}", s.write(s.handleDeleteTag))

	mux.HandleFunc("GET /admin/api/settings", s.read(s.handleSettings))
	mux.HandleFunc("PUT /admin/api/settings", s.write(s.handleSaveSettings))

	mux.HandleFunc("GET /admin/api/audit", s.read(s.handleAudit))

	mux.HandleFunc("GET /{token}", s.handleTokenGet)
	mux.HandleFunc("POST /{token}", s.handleTokenPost)
}
