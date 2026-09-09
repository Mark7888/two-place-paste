package localui

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
)

// tokenHeader is the header the UI sends its token in. The query string is
// accepted too, but only for the two requests a browser makes without
// JavaScript: the initial navigation and the WebSocket upgrade.
const tokenHeader = "X-TPP-Token"

// NewToken returns a per-launch bearer token: 32 bytes from the CSPRNG in
// base64url.
//
// Per launch, and never persisted. It is the difference between "the tray
// opened this UI" and "a page in the browser found port 47821", and a token
// written to disk would survive to be read by anything on the machine.
func NewToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("localui: generate the launch token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// allowedOrigins are the only origins this server answers. They are built from
// the port actually bound, so a service that fell back to an overridden port
// does not accept the default one's origin.
func allowedOrigins(port int) []string {
	return []string{
		fmt.Sprintf("http://127.0.0.1:%d", port),
		fmt.Sprintf("http://localhost:%d", port),
		fmt.Sprintf("http://[::1]:%d", port),
	}
}

// originPatterns are the host:port forms the WebSocket library checks against.
func originPatterns(port int) []string {
	return []string{
		fmt.Sprintf("127.0.0.1:%d", port),
		fmt.Sprintf("localhost:%d", port),
		fmt.Sprintf("[::1]:%d", port),
	}
}

// originPolicy says how strict the Origin check is for a route.
type originPolicy int

const (
	// originRequired is for everything scripted: the API and the event
	// stream. A browser attaches Origin to all of them, so a missing header
	// means the request did not come from the app.
	originRequired originPolicy = iota

	// originMatchIfPresent is for the two navigations a browser makes with no
	// Origin at all — opening /app from the tray, and loading its assets. A
	// foreign Origin is still refused; only its absence is tolerated, and the
	// token is required either way.
	originMatchIfPresent
)

// guard applies the token and Origin checks to one handler.
func (s *Server) guard(policy originPolicy, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")

		if err := s.checkOrigin(r, policy); err != nil {
			s.logger.WarnContext(r.Context(), "refused a localhost request",
				"reason", "origin", "path", r.URL.Path, "origin", r.Header.Get("Origin"))
			writeError(w, http.StatusForbidden, "forbidden origin")
			return
		}
		if !s.checkToken(r) {
			s.logger.WarnContext(r.Context(), "refused a localhost request",
				"reason", "token", "path", r.URL.Path)
			writeError(w, http.StatusUnauthorized, "missing or invalid token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) checkOrigin(r *http.Request, policy originPolicy) error {
	origin := r.Header.Get("Origin")
	if origin == "" {
		if policy == originMatchIfPresent {
			return nil
		}
		return fmt.Errorf("localui: request to %s carries no Origin", r.URL.Path)
	}
	for _, allowed := range s.origins {
		if strings.EqualFold(origin, allowed) {
			return nil
		}
	}
	return fmt.Errorf("localui: request to %s carries a foreign Origin", r.URL.Path)
}

// checkToken accepts the token from a header or, for a navigation and the
// WebSocket upgrade, the query string. The comparison is constant-time: a
// timing oracle on a token this valuable is worth closing even on localhost.
func (s *Server) checkToken(r *http.Request) bool {
	got := r.Header.Get(tokenHeader)
	if got == "" {
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			got = strings.TrimPrefix(auth, "Bearer ")
		}
	}
	if got == "" {
		got = r.URL.Query().Get("token")
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}
