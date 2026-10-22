package admin

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// csrfHeader carries the session's CSRF token on every request that changes
// something. SameSite=Strict already keeps a cross-site request from carrying
// the session cookie; the header is the second lock, because the first one is
// a browser policy rather than something the server enforces. A cross-site
// page cannot set a custom header without a CORS preflight, which this server
// never answers.
const csrfHeader = "X-CSRF-Token"

// apiHandler is an admin API endpoint behind a live session.
type apiHandler func(w http.ResponseWriter, r *http.Request, sess session)

// read wraps an endpoint that only reads: it needs a session and nothing more.
func (s *Server) read(h apiHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.session(r)
		if !ok {
			writeJSONError(w, http.StatusUnauthorized, "Your session has ended. Sign in again.")
			return
		}
		h(w, r, sess)
	}
}

// write wraps an endpoint that changes something: a live session, a matching
// CSRF header and a bounded body.
func (s *Server) write(h apiHandler) http.HandlerFunc {
	return s.read(func(w http.ResponseWriter, r *http.Request, sess session) {
		if !s.checkCSRF(w, r, sess) {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxFormBodyBytes)
		h(w, r, sess)
	})
}

// checkCSRF verifies the request's CSRF header against the session's token.
// On false a response has already been written.
func (s *Server) checkCSRF(w http.ResponseWriter, r *http.Request, sess session) bool {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(csrfHeader)), []byte(sess.csrf)) == 1 {
		return true
	}
	// The route's pattern, not its path: a path can hold a token value, which
	// is a credential and never logged (docs/internal/conventions.md §2).
	s.logger.WarnContext(r.Context(), "admin request rejected: bad csrf token",
		slog.String("client_ip", clientIP(r)), slog.String("route", r.Pattern))
	writeJSONError(w, http.StatusForbidden, "This page is out of date. Reload it and try again.")
	return false
}

// decodeJSON reads a request body into v. On false a 400 has already been
// written. The body is bounded by the caller (write, or handleLogin).
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeJSONError(w, http.StatusBadRequest, "That request could not be read.")
		return false
	}
	return true
}

// handleAPINotFound answers every path under /admin/api/ that no endpoint
// claims, in JSON like the rest of the API.
func (s *Server) handleAPINotFound(w http.ResponseWriter, _ *http.Request) {
	writeJSONError(w, http.StatusNotFound, "There is no such admin endpoint.")
}

// limitJSON is a storage limit override as the API carries it.
type limitJSON struct {
	Enabled bool  `json:"enabled"`
	Bytes   int64 `json:"bytes"`
}

// limitRequest is the body of a storage limit change: the checkbox and the
// size field, as the admin typed it. An empty size keeps the stored bytes.
type limitRequest struct {
	Enabled bool   `json:"enabled"`
	Size    string `json:"size"`
}

// timeOrNil is t, or nil when t is zero, so the API says null rather than
// the year 1.
func timeOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
