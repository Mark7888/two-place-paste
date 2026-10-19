// Package sourcelink serves GET /source, the relay's link to its own source
// code.
//
// The relay is licensed under the AGPL-3.0, which asks that anyone who runs a
// modified relay for other people offers those people its source. The admin
// and pairing pages link here, and this redirects to TPP_SOURCE_URL, so an
// operator running a fork points every link at it by changing one setting.
//
// It is a redirect rather than a URL baked into each page so the pairing page
// can stay a static file.
package sourcelink

import "net/http"

// Server redirects GET /source to the configured source code URL.
type Server struct {
	url string
}

// New returns a Server that redirects to url. The config loader has already
// checked that url is an http:// or https:// URL.
func New(url string) *Server {
	return &Server{url: url}
}

// Register installs GET /source.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /source", s.handle)
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	// No-store so a changed TPP_SOURCE_URL takes effect at once, rather than
	// after a browser forgets a cached redirect.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, s.url, http.StatusFound)
}
