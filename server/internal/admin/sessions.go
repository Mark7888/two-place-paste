package admin

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"sync"
	"time"
)

// sessionIDBytes is the entropy behind a session cookie and its CSRF token:
// 256 bits of crypto/rand, the same budget as a device identifier.
const sessionIDBytes = 32

// session is one logged-in admin.
type session struct {
	id        string
	csrf      string
	expiresAt time.Time
}

// sessionStore keeps sessions in memory. A restart logs every admin out, which
// is the correct trade for a single-screen operator UI: it costs one login and
// removes a persistence format, a migration and an eviction policy from the
// threat surface.
type sessionStore struct {
	ttl time.Duration
	now func() time.Time

	mu sync.Mutex
	// byID is keyed by session id. Entries are pruned lazily on every lookup
	// and creation, so an abandoned session cannot outlive its TTL by more
	// than the time until the next request.
	byID map[string]session
}

func newSessionStore(ttl time.Duration, now func() time.Time) *sessionStore {
	return &sessionStore{ttl: ttl, now: now, byID: make(map[string]session)}
}

// create mints a session and returns it.
func (s *sessionStore) create() session {
	sess := session{
		id:        randomID(),
		csrf:      randomID(),
		expiresAt: s.now().Add(s.ttl),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	s.byID[sess.id] = sess
	return sess
}

// get returns the session for id, if it exists and has not expired.
func (s *sessionStore) get(id string) (session, bool) {
	if id == "" {
		return session{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()

	sess, ok := s.byID[id]
	if !ok {
		return session{}, false
	}
	// The map lookup already matched; this is the constant-time comparison
	// docs/conventions.md §10 requires of session identifiers, kept so that a
	// future lookup by any other means inherits it.
	if subtle.ConstantTimeCompare([]byte(sess.id), []byte(id)) != 1 {
		return session{}, false
	}
	return sess, true
}

// destroy removes a session; logging out must not depend on the cookie
// expiring.
func (s *sessionStore) destroy(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, id)
}

func (s *sessionStore) pruneLocked() {
	now := s.now()
	for id, sess := range s.byID {
		if !sess.expiresAt.After(now) {
			delete(s.byID, id)
		}
	}
}

// randomID returns 256 bits of crypto/rand as a URL-safe string.
func randomID() string {
	var b [sessionIDBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand.Read does not fail on any supported platform; a failure
		// here is a broken machine, not a request-path condition
		// (docs/conventions.md §4).
		panic("admin: crypto/rand: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
