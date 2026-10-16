package update

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
	"github.com/Mark7888/two-place-paste/pkg/tppclient/keystore"
)

// The Nightly token (plan §3.9). A fine-grained token with Actions: Read-only
// on this one repository; nothing else.
const (
	// tokenSecret is its name in the keystore, next to the device key: the
	// login keychain on macOS, a DPAPI-sealed file on Windows.
	tokenSecret = "github-token"

	// EnvToken overrides the stored token, for running from a checkout. It is
	// read, never written anywhere.
	EnvToken = "TPP_DESKTOP_GITHUB_TOKEN"
)

// Secrets is where the token is kept. keystore.Store is one.
type Secrets interface {
	Load(name string) ([]byte, error)
	Save(name string, secret []byte) error
	Delete(name string) error
}

// tokenState is what the manager knows about the token without reading it.
type tokenState struct {
	set     bool
	source  string
	invalid bool

	// checked is set once GitHub has answered a call made with the token,
	// which is when its expiry is known: expires, or zero for a token made
	// with no expiration.
	checked bool
	expires time.Time
}

func (t tokenState) view() localui.TokenView {
	v := localui.TokenView{Set: t.set, Source: t.source, Invalid: t.invalid}
	switch {
	case !t.set || !t.checked:
	case t.expires.IsZero():
		v.NoExpiry = true
	default:
		e := t.expires
		v.ExpiresAt = &e
	}
	return v
}

// loadTokenState notes whether a token is available, so the UI can say so
// without the keystore being read on every status request.
//
// The stored token's expiry, read from the state file into m.token before
// this runs, is kept; the environment's token starts unchecked, since the
// file's expiry belongs to the stored one.
func (m *Manager) loadTokenState() {
	saved := m.token
	m.token = tokenState{}
	if v, ok := m.opts.LookupEnv(EnvToken); ok && strings.TrimSpace(v) != "" {
		m.token = tokenState{set: true, source: "environment"}
		return
	}
	if m.opts.Secrets == nil {
		return
	}
	b, err := m.opts.Secrets.Load(tokenSecret)
	if err == nil && len(b) > 0 {
		m.token = tokenState{set: true, source: "keystore", checked: saved.checked, expires: saved.expires}
	}
}

// noteTokenExpiry records what GitHub said about the token's expiry, and
// keeps it for the next launch when the token is the stored one.
func (m *Manager) noteTokenExpiry(expires time.Time) {
	m.mu.Lock()
	changed := !m.token.checked || !m.token.expires.Equal(expires)
	m.token.checked, m.token.expires = true, expires
	if changed {
		m.saveLocked()
	}
	m.mu.Unlock()
	if changed {
		m.changed()
	}
}

// learnTokenExpiry asks GitHub about a token whose expiry is not known yet:
// one saved by a build that did not record it, or one from the environment.
// A Nightly check would learn it too, but a token kept for later, with the
// channel on Stable, would otherwise never say when it runs out.
func (m *Manager) learnTokenExpiry(ctx context.Context) {
	m.mu.Lock()
	need := m.token.set && !m.token.checked && !m.token.invalid
	m.mu.Unlock()
	if !need {
		return
	}
	token, err := m.readToken()
	if err != nil {
		return
	}
	var probe struct {
		TotalCount int `json:"total_count"`
	}
	if err := m.api(token).get(ctx, tokenProbe, &probe); err != nil {
		m.noteTokenError(err)
		m.opts.Logger.Debug("could not read the GitHub token's expiry", "error", err)
	}
}

// tokenProbe is the cheapest call that proves a token can do what Nightly
// needs: list this repository's workflow runs.
const tokenProbe = "/repos/" + Repository + "/actions/runs?per_page=1"

// readToken returns the token for one call. It is never logged and never
// leaves this package except as an Authorization header to the API.
func (m *Manager) readToken() (string, error) {
	if v, ok := m.opts.LookupEnv(EnvToken); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v), nil
	}
	if m.opts.Secrets == nil {
		return "", ErrNoToken
	}
	b, err := m.opts.Secrets.Load(tokenSecret)
	switch {
	case errors.Is(err, keystore.ErrNotFound), err == nil && len(b) == 0:
		return "", ErrNoToken
	case err != nil:
		return "", fmt.Errorf("read the GitHub token from the keystore: %w", err)
	}
	return string(b), nil
}

// api is the GitHub API client with the given token.
func (m *Manager) api(token string) githubAPI {
	return githubAPI{
		client:   m.opts.Client,
		base:     m.opts.APIBase,
		token:    token,
		onExpiry: m.noteTokenExpiry,
	}
}

// noteTokenError marks the token invalid after GitHub rejected it, so the UI
// asks for a new one instead of the updater retrying a dead one.
func (m *Manager) noteTokenError(err error) {
	if errors.Is(err, ErrTokenRejected) {
		m.mu.Lock()
		m.token.invalid = true
		m.mu.Unlock()
	}
}

// SetUpdateToken checks a token against GitHub and, if it can read this
// repository's Actions, stores it in the keystore.
func (m *Manager) SetUpdateToken(ctx context.Context, token string) (localui.UpdateView, error) {
	token = strings.TrimSpace(token)
	switch {
	case token == "" || strings.ContainsAny(token, " \t\r\n"):
		return localui.UpdateView{}, localui.Errorf(http.StatusBadRequest, nil, "that is not a GitHub token")
	case m.opts.Secrets == nil:
		return localui.UpdateView{}, localui.Errorf(http.StatusNotImplemented, nil, "this build has no keystore to keep a token in")
	}

	var probe struct {
		TotalCount int `json:"total_count"`
	}
	// The probe's answer is about the candidate, not about the token in use
	// until it is stored, so it is held aside rather than recorded.
	probeAPI := m.api(token)
	var probed bool
	var expires time.Time
	probeAPI.onExpiry = func(t time.Time) { probed, expires = true, t }
	err := probeAPI.get(ctx, tokenProbe, &probe)
	switch {
	case errors.Is(err, ErrTokenRejected):
		return localui.UpdateView{}, localui.Errorf(http.StatusBadRequest, err, "GitHub rejected that token")
	case errors.Is(err, errNotFound):
		return localui.UpdateView{}, localui.Errorf(http.StatusBadRequest, err,
			"that token cannot see %s; give it access to this repository", Repository)
	case err != nil:
		return localui.UpdateView{}, localui.Errorf(http.StatusBadGateway, err, "the token could not be checked: %v", err)
	}

	if err := m.opts.Secrets.Save(tokenSecret, []byte(token)); err != nil {
		return localui.UpdateView{}, localui.Errorf(http.StatusInternalServerError, err, "the token could not be stored")
	}
	m.mu.Lock()
	m.token = tokenState{}
	m.loadTokenState()
	if m.token.source == "keystore" {
		m.token.checked, m.token.expires = probed, expires
	}
	m.saveLocked()
	v := m.viewLocked()
	m.mu.Unlock()
	m.opts.Logger.Info("GitHub token stored", "source", v.Token.Source)
	m.changed()
	return v, nil
}

// DeleteUpdateToken removes the stored token.
func (m *Manager) DeleteUpdateToken(context.Context) (localui.UpdateView, error) {
	if m.opts.Secrets != nil {
		if err := m.opts.Secrets.Delete(tokenSecret); err != nil {
			return localui.UpdateView{}, localui.Errorf(http.StatusInternalServerError, err, "the token could not be removed")
		}
	}
	m.mu.Lock()
	m.token = tokenState{}
	m.loadTokenState() // the environment one, if any, still applies
	m.saveLocked()
	v := m.viewLocked()
	m.mu.Unlock()
	m.opts.Logger.Info("GitHub token removed")
	m.changed()
	return v, nil
}
