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

// originPolicy says how a request that carries no Origin header is treated. A
// foreign Origin is refused under either.
type originPolicy int

const (
	// originScripted is for everything the page's own JavaScript drives: the
	// API and the event stream.
	//
	// It does not demand an Origin, because a browser does not send one. The
	// Fetch standard attaches Origin to a cross-origin request, and to any
	// request whose method is neither GET nor HEAD; a same-origin fetch that
	// only reads attaches nothing at all. Demanding it refused every read the
	// app makes, which is most of them — the status poll, the history page,
	// the device roster and the settings screen.
	//
	// What is demanded instead is that nothing about the request says it came
	// from somewhere else: a matching Origin when one is sent, a
	// Sec-Fetch-Site of "same-origin" when the browser sends that, and a
	// method a browser is allowed to omit Origin for. A cross-origin request
	// carries one of those three tells in every browser in service, and none
	// of them carries this launch's token.
	originScripted originPolicy = iota

	// originMatchIfPresent is for the requests a browser makes for the page
	// itself — opening /app from the tray, and the script and stylesheet it
	// pulls in. A foreign Origin is still refused; only its absence is
	// tolerated.
	originMatchIfPresent
)

// tokenPolicy says whether a route requires the launch token.
type tokenPolicy int

const (
	// tokenRequired covers every route that reads or changes something: the
	// API and the event stream.
	tokenRequired tokenPolicy = iota

	// tokenOptional is the static shell — the page, its script, its stylesheet.
	//
	// Not a relaxation, but the only thing that works: a browser cannot attach
	// a token to them. The tray's URL puts the token in the query string of
	// the navigation and the page sends it as a header on every API call, but
	// a <script> or <link> the page pulls in has no header and no query string
	// of its own. Guarding those with the token protected nothing — the bundle
	// is the same bytes for every user, sitting in the binary and on disk —
	// and cost the whole app: the script answered 401, the page never booted,
	// and every request the user could see said "missing or invalid token".
	//
	// Serving the shell unguarded is also what makes a plain browser reload
	// work. The page erases the token from the address bar on load, so a
	// refresh arrives without one; it now reaches the app, which says so and
	// points at the tray, instead of replacing the UI with a JSON error.
	tokenOptional
)

// guard applies the token and Origin checks to one handler.
func (s *Server) guard(origins originPolicy, tokens tokenPolicy, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")

		if err := s.checkOrigin(r, origins); err != nil {
			s.logger.WarnContext(r.Context(), "refused a localhost request",
				"reason", "origin", "path", r.URL.Path, "origin", r.Header.Get("Origin"))
			writeError(w, http.StatusForbidden, "forbidden origin")
			return
		}
		if tokens == tokenRequired && !s.checkToken(r) {
			s.logger.WarnContext(r.Context(), "refused a localhost request",
				"reason", "token", "path", r.URL.Path)
			writeError(w, http.StatusUnauthorized, "missing or invalid token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) checkOrigin(r *http.Request, policy originPolicy) error {
	if origin := r.Header.Get("Origin"); origin != "" {
		for _, allowed := range s.origins {
			if strings.EqualFold(origin, allowed) {
				return nil
			}
		}
		return fmt.Errorf("localui: request to %s carries a foreign Origin", r.URL.Path)
	}
	if policy == originMatchIfPresent {
		return nil
	}

	// No Origin. A browser omits it on a same-origin read and on nothing else,
	// so a write without one did not come from the app.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return fmt.Errorf("localui: %s to %s carries no Origin", r.Method, r.URL.Path)
	}
	// Sec-Fetch-Site is the browser's own account of where the request came
	// from, and it cannot be set by the page that made it. Absent means a
	// client that does not send it at all, which the token still has to
	// satisfy.
	switch site := r.Header.Get("Sec-Fetch-Site"); site {
	case "", "same-origin", "none":
		return nil
	default:
		return fmt.Errorf("localui: request to %s is %s", r.URL.Path, site)
	}
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
