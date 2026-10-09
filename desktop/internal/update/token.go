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
	expires time.Time
}

func (t tokenState) view() localui.TokenView {
	v := localui.TokenView{Set: t.set, Source: t.source, Invalid: t.invalid}
	if !t.expires.IsZero() {
		e := t.expires
		v.ExpiresAt = &e
	}
	return v
}

// loadTokenState notes whether a token is available, so the UI can say so
// without the keystore being read on every status request.
func (m *Manager) loadTokenState() {
	if v, ok := m.opts.LookupEnv(EnvToken); ok && strings.TrimSpace(v) != "" {
		m.token = tokenState{set: true, source: "environment"}
		return
	}
	if m.opts.Secrets == nil {
		return
	}
	b, err := m.opts.Secrets.Load(tokenSecret)
	if err == nil && len(b) > 0 {
		m.token = tokenState{set: true, source: "keystore"}
	}
}

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
		client: m.opts.Client,
		base:   m.opts.APIBase,
		token:  token,
		onExpiry: func(t time.Time) {
			m.mu.Lock()
			m.token.expires = t
			m.mu.Unlock()
		},
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
	err := m.api(token).get(ctx, "/repos/"+Repository+"/actions/runs?per_page=1", &probe)
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
	expires := m.token.expires
	m.loadTokenState()
	m.token.expires = expires
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
	v := m.viewLocked()
	m.mu.Unlock()
	m.opts.Logger.Info("GitHub token removed")
	m.changed()
	return v, nil
}
