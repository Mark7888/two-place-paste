package ws

import "net/http"

// Register implements httpapi.Registrar (ROADMAP §4): it attaches this
// package's single route to the mux and nothing else.
//
// The router itself is a shared touchpoint. This phase does not edit
// internal/httpapi/router.go and does not wire anything in cmd/tpp/main.go;
// both of those belong to the phase that assembles the server.
func (s *Server) Register(mux *http.ServeMux) {
	mux.Handle("GET "+s.path, s)
}
