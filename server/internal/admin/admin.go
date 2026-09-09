// Package admin is the operator-facing surface: the login-protected token
// screen and the creation-token endpoint clients POST to (SPEC §3.1, §4.4).
//
// The whole UI is one screen listing creation tokens plus a button that mints
// another. That is not minimalism for its own sake: the server holds only
// ciphertext, so there is nothing else an admin screen could truthfully show,
// and adding content access or statistics later would require the relay to
// learn something SPEC §2.3 says it must not.
//
// Two routes are public and both are attack surface:
//
//   - /admin/login is reachable by anyone, so it is rate limited per IP and
//     globally, and a lockout is logged.
//   - /<token> is the creation endpoint. GET returns 404 so that a crawler or
//     a link preview can never burn a token; only POST consumes one
//     (SPEC §3.1).
package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// Store is the persistent-zone subset this package needs, declared at the
// consumer (docs/conventions.md §7).
type Store interface {
	// CreateToken mints a creation token with an admin-chosen name.
	CreateToken(ctx context.Context, name string) (store.Token, error)

	// ListTokens returns every creation token, newest first.
	ListTokens(ctx context.Context) ([]store.Token, error)

	// GetToken returns one token by value. The QR handler uses it so that a
	// stale page cannot render a code for a token that no longer exists.
	GetToken(ctx context.Context, value string) (store.Token, error)

	// CreateGroup consumes a token and registers the creating device as one
	// atomic operation (SPEC §3.1 steps 4-5).
	CreateGroup(ctx context.Context, token string, dev store.NewDevice) (store.GroupCreation, error)
}

// Defaults for the tunables below. They are deliberately not configurable:
// every one of them is a security property rather than a deployment choice.
const (
	// defaultLoginRate and defaultLoginBurst bound login attempts from one IP:
	// a burst of 5, refilled at one attempt every 12 seconds.
	defaultLoginRate  = time.Second * 12
	defaultLoginBurst = 5

	// defaultGlobalLoginBurst bounds attempts across all clients, so a
	// distributed attempt cannot multiply the per-IP allowance by its number
	// of source addresses.
	defaultGlobalLoginRate  = time.Second * 2
	defaultGlobalLoginBurst = 30

	// sessionCookieName is scoped to /admin, so it is never sent to the
	// creation-token endpoint or the WebSocket transport.
	sessionCookieName = "tpp_admin_session"
	sessionCookiePath = "/admin"

	// maxTokenNameLen bounds what an admin can store as a token label.
	maxTokenNameLen = 120

	// maxLoginBodyBytes bounds the login form body. The form has one field.
	maxLoginBodyBytes = 4 << 10

	// maxCreateBodyBytes bounds the group-creation request body: two public
	// keys' worth of base64 plus a device name (SPEC §3.1 step 4).
	maxCreateBodyBytes = 16 << 10
)

// Options configures a Server.
type Options struct {
	// PublicBaseURL is the externally reachable origin. Creation URLs and
	// their QR codes are built from it (SPEC §4.4), and its scheme decides
	// whether the session cookie is marked Secure — see New.
	PublicBaseURL string

	// SessionTTL bounds a session's lifetime.
	SessionTTL time.Duration

	// Password is the plaintext admin password. Exactly one of Password and
	// PasswordHash is set; the config loader enforces that.
	Password string

	// PasswordHash is a pre-hashed admin password. Verifying one is deferred
	// work (SPEC §9); New reports that rather than silently accepting any
	// password.
	PasswordHash string

	// Logger receives request-scoped logs. Defaults to slog.Default().
	Logger *slog.Logger

	// Now supplies UTC timestamps; tests replace it.
	Now func() time.Time
}

// ErrHashedPasswordUnsupported is returned by New when ADMIN_PASSWORD_HASH is
// configured. The config loader accepts the variable so that moving to hashed
// storage stays a config change (SPEC §4.4), but no hash format has been
// chosen yet and verifying one is on the deferred backlog. Failing at startup
// is the only honest answer: the alternative is a login screen whose password
// check cannot succeed, or worse, one that skips it.
var ErrHashedPasswordUnsupported = errors.New("admin: ADMIN_PASSWORD_HASH is not supported yet; set ADMIN_PASSWORD (hashed passwords are deferred work, SPEC §9)")

// Server serves the admin UI and the creation-token endpoint.
type Server struct {
	store  Store
	logger *slog.Logger
	now    func() time.Time

	baseURL   string
	password  []byte
	sessions  *sessionStore
	logins    *limiter
	templates *templateSet

	// secureCookies mirrors the public base URL's scheme. SPEC §4.4 requires
	// Secure, and a deployment always terminates TLS in front of the server
	// (SPEC §4.1) — but a plain-http base URL is the local-development case,
	// where a Secure cookie is simply never sent back and the UI cannot be
	// used at all. Deriving the flag keeps that working without adding a
	// configuration knob an operator could point the wrong way in production.
	secureCookies bool
}

// New returns an admin server over st. It fails rather than starting with a
// credential it cannot verify or templates it cannot parse — both are
// programmer or operator errors that must surface at startup.
func New(st Store, opts Options) (*Server, error) {
	if st == nil {
		return nil, errors.New("admin: store is required")
	}
	if opts.PasswordHash != "" {
		return nil, ErrHashedPasswordUnsupported
	}
	if opts.Password == "" {
		return nil, errors.New("admin: password is required")
	}

	base := strings.TrimRight(opts.PublicBaseURL, "/")
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("admin: public base URL %q is not an absolute http(s) URL", opts.PublicBaseURL)
	}

	tmpl, err := parseTemplates()
	if err != nil {
		return nil, err
	}

	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	ttl := opts.SessionTTL
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}

	return &Server{
		store:         st,
		logger:        logger,
		now:           now,
		baseURL:       base,
		password:      []byte(opts.Password),
		sessions:      newSessionStore(ttl, now),
		logins:        newLimiter(defaultLoginRate, defaultLoginBurst, defaultGlobalLoginRate, defaultGlobalLoginBurst, now),
		templates:     tmpl,
		secureCookies: u.Scheme == "https",
	}, nil
}

// creationURL is the URL an admin hands a user for a given token (SPEC §3.1
// step 2). It is also what the token's QR code encodes.
func (s *Server) creationURL(token string) string {
	return s.baseURL + "/" + url.PathEscape(token)
}
