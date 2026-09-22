// Package pairlink serves the pairing hand-off page (SPEC §3.2).
//
// A pairing code is a base64url string. Shown as a QR code it is unreadable to
// anything but this system's own clients: a general-purpose scanner — Google
// Lens, a phone's camera app — renders it as text to copy and offers nothing
// to open. So the clients wrap a code in a link to the relay it already names,
// and this package is what that link lands on.
//
// Two properties are worth being explicit about, because both are the reason
// this shape was chosen over the obvious ones:
//
//   - The code is in the URL's **fragment**, which a browser never sends. This
//     server therefore does not receive the code its own page hands over, and
//     cannot log it, cache it or leak it in a Referer. The handler below reads
//     no code because there is no code to read.
//   - The page is pinned to no domain. Every deployment is somebody's own
//     relay, so the link is built at run time from the server URL already in
//     the pairing payload. Nothing here — and nothing in the app's manifest —
//     names a host.
//
// The page is public and unauthenticated by necessity: whoever is scanning has
// no account here and may not even have the app. It exposes nothing, because
// it is three static files.
package pairlink

import (
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"

	webpair "github.com/Mark7888/two-place-paste/server/web/pair"
)

// Server serves the hand-off page.
type Server struct {
	logger *slog.Logger
}

// New builds a Server. A nil logger means slog.Default.
func New(logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{logger: logger}
}

// Register implements httpapi.Registrar.
//
// "GET /pair" is more specific than the creation endpoint's "GET /{token}", so
// net/http prefers it and the wildcard never shadows it — the same rule that
// keeps /ws, /healthz and /admin/ reachable.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /pair", s.handlePage)
	mux.HandleFunc("GET /pair/style.css", s.asset("style.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /pair/app.js", s.asset("app.js", "text/javascript; charset=utf-8"))
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	body, err := read("page.html")
	if err != nil {
		s.logger.ErrorContext(r.Context(), "read the pairing page", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	// One stylesheet and one script, both from this origin, and nothing else.
	// No inline script anywhere on the page, so the policy needs no nonce and
	// no 'unsafe-inline'.
	h.Set("Content-Security-Policy",
		"default-src 'none'; style-src 'self'; script-src 'self'; form-action 'none'; base-uri 'none'; frame-ancestors 'none'")
	// The fragment never reaches this server, and this makes sure it never
	// leaves the browser by another route either.
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	// The page is the same bytes for everyone; what makes a visit unique is
	// the fragment, which is not part of the response.
	h.Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

func (s *Server) asset(name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := read(name)
		if err != nil {
			s.logger.ErrorContext(r.Context(), "read a pairing page asset", "asset", name, "error", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(body)
	}
}

func read(name string) ([]byte, error) {
	b, err := fs.ReadFile(webpair.FS, name)
	if err != nil {
		return nil, fmt.Errorf("read embedded pairing asset %s: %w", name, err)
	}
	return b, nil
}
