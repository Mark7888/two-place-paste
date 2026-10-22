package admin

import (
	"fmt"
	"io/fs"
	"net/http"

	webadmin "github.com/Mark7888/two-place-paste/server/web/admin"
)

// Register implements httpapi.Registrar (ROADMAP §4): it attaches this
// package's routes and nothing else. The router itself is a shared touchpoint
// and is not edited here.
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
	mux.HandleFunc("GET /admin/", s.handleIndex)
	mux.HandleFunc("GET /admin/style.css", s.handleStyle)
	mux.HandleFunc("GET /admin/app.js", s.handleScript)
	mux.HandleFunc("GET /admin/favicon.svg", s.handleIcon("favicon.svg", "image/svg+xml"))
	mux.HandleFunc("GET /admin/favicon.ico", s.handleIcon("favicon.ico", "image/x-icon"))
	mux.HandleFunc("GET /admin/apple-touch-icon.png", s.handleIcon("apple-touch-icon.png", "image/png"))
	mux.HandleFunc("GET /admin/login", s.handleLoginForm)
	mux.HandleFunc("POST /admin/login", s.handleLogin)
	mux.HandleFunc("POST /admin/logout", s.handleLogout)
	mux.HandleFunc("GET /admin/tokens", s.handleTokens)
	mux.HandleFunc("POST /admin/tokens", s.handleCreateToken)
	mux.HandleFunc("GET /admin/tokens/{token}/qr.png", s.handleTokenQR)
	mux.HandleFunc("POST /admin/tokens/{token}/revoke", s.handleRevokeToken)

	mux.HandleFunc("GET /admin/groups", s.handleGroupList)
	mux.HandleFunc("GET /admin/groups/{id}", s.handleGroup)
	mux.HandleFunc("POST /admin/groups/{id}/rename", s.handleRenameGroup)
	mux.HandleFunc("POST /admin/groups/{id}/tag", s.handleSetGroupTag)
	mux.HandleFunc("POST /admin/groups/{id}/limit", s.handleSetGroupLimit)
	mux.HandleFunc("GET /admin/groups/{id}/purge", s.handlePurgeConfirm)
	mux.HandleFunc("POST /admin/groups/{id}/purge", s.handlePurgeGroup)
	mux.HandleFunc("GET /admin/groups/{id}/delete", s.handleDeleteConfirm)
	mux.HandleFunc("POST /admin/groups/{id}/delete", s.handleDeleteGroup)

	mux.HandleFunc("GET /admin/tags", s.handleTags)
	mux.HandleFunc("POST /admin/tags", s.handleCreateTag)
	mux.HandleFunc("POST /admin/tags/{name}/limit", s.handleSetTagLimit)
	mux.HandleFunc("POST /admin/tags/{name}/delete", s.handleDeleteTag)

	mux.HandleFunc("GET /admin/settings", s.handleSettings)
	mux.HandleFunc("POST /admin/settings", s.handleSaveSettings)
	mux.HandleFunc("GET /admin/audit", s.handleAudit)
	mux.HandleFunc("POST /admin/sweep", s.handleSweep)

	mux.HandleFunc("GET /{token}", s.handleTokenGet)
	mux.HandleFunc("POST /{token}", s.handleTokenPost)
}

// webadminFile reads one embedded asset.
func webadminFile(name string) ([]byte, error) {
	b, err := fs.ReadFile(webadmin.FS, name)
	if err != nil {
		return nil, fmt.Errorf("read embedded admin asset %s: %w", name, err)
	}
	return b, nil
}
