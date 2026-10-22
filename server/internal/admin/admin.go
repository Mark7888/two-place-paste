// Package admin is the operator-facing surface: the login-protected admin
// screens, and the creation-token endpoint clients POST to (SPEC §3.1, §4.4).
//
// The screens are a React app (server/ui), built into server/web/admin and
// embedded in the binary. This package serves it and the JSON API under
// /admin/api/ that is all it talks to; every decision — who is signed in,
// what is valid, what is shown — is made here, and the app only renders it.
//
// The dashboard shows what the relay can know without knowing any content:
// byte counts, entry counts, device counts, the state of its storage and of
// Redis. A page per group renames it, tags it, sets its storage limit, lists
// its devices, and purges or deletes it; the tag screen groups groups and
// gives a whole tag a storage limit; the token screen mints, lists and revokes
// creation tokens; the settings screen holds runtime settings; and the audit
// screen lists what admins did (docs/internal/plans/admin-group-management.md).
// There is no content access, and none can be added: the server holds only
// ciphertext (SPEC §2.3), and nothing here shows an entry id, a content type or
// a filename.
//
// Two routes are public and both are attack surface:
//
//   - /admin/api/login is reachable by anyone, so it is rate limited per IP
//     and globally, and a lockout is logged.
//   - /<token> is the creation endpoint. GET returns 404 so that a crawler or
//     a link preview can never burn a token; only POST consumes one
//     (SPEC §3.1).
package admin

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Mark7888/two-place-paste/server/internal/blob"
	"github.com/Mark7888/two-place-paste/server/internal/entries"
	"github.com/Mark7888/two-place-paste/server/internal/quota"
	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// Store is the persistent-zone subset this package needs, declared at the
// consumer (docs/internal/conventions.md §7).
type Store interface {
	// CreateTokenWith mints a creation token with an admin-chosen name, an
	// optional note, starting tag and expiry.
	CreateTokenWith(ctx context.Context, t store.NewToken) (store.Token, error)

	// ListTokens returns every creation token, newest first.
	ListTokens(ctx context.Context) ([]store.Token, error)

	// GetToken returns one token by value. The QR handler uses it so that a
	// stale page cannot render a code for a token that no longer exists.
	GetToken(ctx context.Context, value string) (store.Token, error)

	// RevokeToken deletes an unused token so its URL creates nothing.
	RevokeToken(ctx context.Context, value string) error

	// ListDevices returns a group's roster. The dashboard counts it and
	// reads its last-seen times; it never shows a key.
	ListDevices(ctx context.Context, groupID string) ([]store.Device, error)

	// CreateGroup consumes a token and registers the creating device as one
	// atomic operation (SPEC §3.1 steps 4-5).
	CreateGroup(ctx context.Context, token string, dev store.NewDevice) (store.GroupCreation, error)

	// Groups, tags, settings and the audit log
	// (docs/internal/plans/admin-group-management.md).
	GetGroup(ctx context.Context, groupID string) (store.Group, error)
	ListGroups(ctx context.Context) ([]store.Group, error)
	RenameGroup(ctx context.Context, groupID, name string) error
	SetGroupTag(ctx context.Context, groupID, tag string) error
	SetGroupLimit(ctx context.Context, groupID string, limit store.LimitOverride) error
	DeleteGroup(ctx context.Context, groupID string) (store.DeletedGroup, error)
	ListTags(ctx context.Context) ([]store.Tag, error)
	GetTag(ctx context.Context, name string) (store.Tag, error)
	CreateTag(ctx context.Context, name string) (store.Tag, error)
	SetTagLimit(ctx context.Context, name string, limit store.LimitOverride) error
	DeleteTag(ctx context.Context, name string) error
	GetSettings(ctx context.Context) (store.Settings, error)
	PutSettings(ctx context.Context, s store.Settings) error
	AppendAudit(ctx context.Context, e store.AuditEntry) error
	ListAudit(ctx context.Context, limit int) ([]store.AuditEntry, error)
}

// Live is the transport, as far as the admin screens need it: who is online,
// and telling a group's devices that it was renamed or deleted.
type Live interface {
	OnlineDevices(groupID string) map[string]bool
	AnnounceRename(ctx context.Context, groupID, name string)
	CloseGroup(groupID string)
}

// Purger deletes every entry of a group now.
type Purger interface {
	PurgeGroup(ctx context.Context, groupID string) (entries.PurgeResult, error)
}

// GroupLimits is the quota.Ledger subset that applies storage limit
// overrides.
type GroupLimits interface {
	SetGroupLimit(ctx context.Context, groupID string, limit int64, set bool) error
}

// RedisInfo is the read-only slice of a Redis client the health card uses.
type RedisInfo interface {
	ConfigGet(ctx context.Context, parameter string) *redis.MapStringStringCmd
	Info(ctx context.Context, section ...string) *redis.StringCmd
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

	// maxTokenNameLen bounds what an admin can store as a token label, and
	// maxGroupNameLen a group's display name. A group starts with its token's
	// name, so the two bounds are the same.
	maxTokenNameLen = 120
	maxGroupNameLen = maxTokenNameLen

	// maxNoteLen bounds a token's note, and maxTagNameLen a tag's name.
	maxNoteLen    = 500
	maxTagNameLen = 40

	// maxFormBodyBytes bounds the body of every admin API request that
	// changes something, but the login. The largest is a new token: a name
	// and a note.
	maxFormBodyBytes = 64 << 10

	// maxLoginBodyBytes bounds the login request body. It has one field.
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

	// Dashboard is what the dashboard reads beyond the store. Its zero value
	// shows groups and devices only.
	Dashboard Dashboard

	// Live reaches connected devices. Without it the device list shows
	// nobody online, and renames and deletions reach devices only when they
	// next connect.
	Live Live

	// Purger deletes a group's entries, for "Purge entries" and for group
	// deletion. Without it purging is unavailable.
	Purger Purger

	// Limits applies storage limit overrides to the ledger. Without it
	// overrides are stored but change nothing.
	Limits GroupLimits

	// Sweep asks for a blob sweep now and reports whether one was queued.
	// Without it the dashboard has no "Sweep now" button.
	Sweep func() bool

	// Redis feeds the health card. Without it the card shows only the
	// server's own version and uptime.
	Redis RedisInfo

	// Version describes the running build, e.g. "v1.4.0 (3f2a9c1)".
	Version string

	// StartedAt is when the server started, for its uptime.
	StartedAt time.Time

	// Assets is the built admin UI: index.html, assets/ and the favicons.
	// Nil means the build embedded in the binary (server/web/admin); tests
	// set it so that they do not depend on a UI build.
	Assets fs.FS
}

// Dashboard wires the dashboard to the storage subsystems. Every field is
// optional; a missing one hides its section.
type Dashboard struct {
	// Ledger reports blob storage usage and evictions.
	Ledger StorageLedger

	// Entries reports per-group entry counts.
	Entries EntryStats

	// SweepStatus reports the most recent blob sweep.
	SweepStatus func() blob.SweepStatus

	// Uploads reports chunked uploads in progress and the bytes they hold
	// reserved.
	Uploads func() (count int, reserved int64)

	// Backend and Location describe where blobs live, e.g. "s3" and
	// "https://s3.example.com/bucket/". They are shown as given and must
	// hold no secret.
	Backend  string
	Location string

	// The configured limits, in bytes. Zero total or group is unlimited.
	TotalLimit     int64
	GroupLimit     int64
	EntryMaxBytes  int64
	InlineMaxBytes int64
}

// StorageLedger is the quota.Ledger subset the dashboard reads.
type StorageLedger interface {
	Usage(ctx context.Context) (quota.Usage, error)
	GroupUsage(ctx context.Context, groupID string) (quota.Usage, error)
	Evictions(ctx context.Context) (quota.Evictions, error)
}

// EntryStats is the entries.Store subset the dashboard reads.
type EntryStats interface {
	GroupStats(ctx context.Context, groupID string) (entries.GroupStats, error)
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
	store     Store
	logger    *slog.Logger
	now       func() time.Time
	dashboard Dashboard

	live      Live
	purger    Purger
	limits    GroupLimits
	sweep     func() bool
	redis     RedisInfo
	version   string
	startedAt time.Time

	baseURL  string
	password []byte
	sessions *sessionStore
	logins   *limiter
	ui       uiAssets

	// secureCookies mirrors the public base URL's scheme. SPEC §4.4 requires
	// Secure, and a deployment always terminates TLS in front of the server
	// (SPEC §4.1) — but a plain-http base URL is the local-development case,
	// where a Secure cookie is simply never sent back and the UI cannot be
	// used at all. Deriving the flag keeps that working without adding a
	// configuration knob an operator could point the wrong way in production.
	secureCookies bool
}

// New returns an admin server over st. It fails rather than starting with a
// credential it cannot verify or a UI build it cannot read — both are
// programmer or operator errors that must surface at startup. A binary built
// without the UI at all still starts; see loadUI.
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

	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	ui, err := loadUI(opts.Assets, logger)
	if err != nil {
		return nil, err
	}
	ttl := opts.SessionTTL
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}

	startedAt := opts.StartedAt
	if startedAt.IsZero() {
		startedAt = now()
	}

	return &Server{
		store:         st,
		dashboard:     opts.Dashboard,
		live:          opts.Live,
		purger:        opts.Purger,
		limits:        opts.Limits,
		sweep:         opts.Sweep,
		redis:         opts.Redis,
		version:       opts.Version,
		startedAt:     startedAt,
		logger:        logger,
		now:           now,
		baseURL:       base,
		password:      []byte(opts.Password),
		sessions:      newSessionStore(ttl, now),
		logins:        newLimiter(defaultLoginRate, defaultLoginBurst, defaultGlobalLoginRate, defaultGlobalLoginBurst, now),
		ui:            ui,
		secureCookies: u.Scheme == "https",
	}, nil
}

// creationURL is the URL an admin hands a user for a given token (SPEC §3.1
// step 2). It is also what the token's QR code encodes.
func (s *Server) creationURL(token string) string {
	return s.baseURL + "/" + url.PathEscape(token)
}
