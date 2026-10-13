package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultAPIBase is the GitHub REST API. The Nightly token is only ever sent
// here: never to the storage host artifacts download from, never in a URL.
const DefaultAPIBase = "https://api.github.com"

// maxAPIBytes bounds one API response; listings here are tens of kilobytes.
const maxAPIBytes = 4 << 20

// Errors from the API, worded for the person reading the Updates section.
var (
	// ErrTokenRejected is a 401: the token was revoked, expired, or mistyped.
	ErrTokenRejected = errors.New("GitHub rejected the token; it may have expired or been revoked")
	// ErrNoToken is a Nightly check with no token stored.
	ErrNoToken  = errors.New("the Nightly channel needs a GitHub token; add one below")
	errNotFound = errors.New("not found")
)

// githubAPI is an authenticated client for the few REST calls Nightly makes.
type githubAPI struct {
	client *http.Client
	base   string
	token  string

	// onExpiry receives the token's expiry from each response, when GitHub
	// sends one.
	onExpiry func(time.Time)
}

// get fetches path (relative to the API base) into out.
func (g githubAPI) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.base+path, nil)
	if err != nil {
		return fmt.Errorf("build the request for %s: %w", path, err)
	}
	resp, err := g.do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := g.status(resp, path); err != nil {
		return err
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIBytes))
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

// do sends req with the token, which it adds only for the API's own host.
func (g githubAPI) do(req *http.Request) (*http.Response, error) {
	base, err := url.Parse(g.base)
	if err != nil {
		return nil, fmt.Errorf("parse the API base: %w", err)
	}
	if req.URL.Host != base.Host || req.URL.Scheme != base.Scheme {
		return nil, fmt.Errorf("refusing to send the token to %s", req.URL.Host)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+g.token)
	resp, err := g.client.Do(req)
	if err != nil {
		// The URL is the API's, never one with a secret in it.
		return nil, fmt.Errorf("call GitHub: %w", err)
	}
	if exp := parseTokenExpiry(resp.Header.Get("Github-Authentication-Token-Expiration")); !exp.IsZero() && g.onExpiry != nil {
		g.onExpiry(exp)
	}
	return resp, nil
}

func (g githubAPI) status(resp *http.Response, path string) error {
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		return ErrTokenRejected
	case http.StatusNotFound, http.StatusUnprocessableEntity:
		return errNotFound
	case http.StatusForbidden, http.StatusTooManyRequests:
		if resp.Header.Get("X-Ratelimit-Remaining") == "0" {
			return errors.New("GitHub's rate limit is used up; try again in a while")
		}
		return fmt.Errorf("GitHub refused %s (%s); the token may lack the Actions: Read permission", path, resp.Status)
	default:
		return fmt.Errorf("GitHub answered %s for %s", resp.Status, path)
	}
}

// downloadArtifact fetches an artifact's zip in two requests: the API, with
// the token, answers with a redirect to a short-lived signed storage URL,
// which is then fetched with a fresh request carrying no token at all. Go
// would drop the Authorization header on a cross-host redirect by itself;
// doing it explicitly means the token's safety does not rest on that.
func (g githubAPI) downloadArtifact(ctx context.Context, archiveURL string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, archiveURL, nil)
	if err != nil {
		return fmt.Errorf("build the artifact request: %w", err)
	}
	noFollow := *g.client
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	api := g
	api.client = &noFollow
	resp, err := api.do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusSeeOther &&
		resp.StatusCode != http.StatusTemporaryRedirect {
		if err := g.status(resp, "the artifact"); err != nil {
			return err
		}
		return fmt.Errorf("GitHub answered %s instead of a download link", resp.Status)
	}
	loc, err := resp.Location()
	if err != nil {
		return fmt.Errorf("GitHub's download link is unusable: %w", err)
	}

	dl, err := http.NewRequestWithContext(ctx, http.MethodGet, loc.String(), nil)
	if err != nil {
		return fmt.Errorf("build the storage request: %w", err)
	}
	plain := *g.client
	plain.CheckRedirect = nil
	got, err := plain.Do(dl)
	if err != nil {
		// The storage URL is signed; the error must not echo it into a log,
		// and a *url.Error's message carries the whole URL.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("download the artifact from %s: %w", loc.Host, err)
	}
	defer func() { _ = got.Body.Close() }()
	if got.StatusCode != http.StatusOK {
		return fmt.Errorf("download the artifact: %s", got.Status)
	}
	if _, err := io.Copy(w, io.LimitReader(got.Body, maxPayloadBytes)); err != nil {
		return fmt.Errorf("download the artifact: %w", err)
	}
	return nil
}

// parseTokenExpiry reads GitHub's github-authentication-token-expiration
// header, which comes in more than one layout.
func parseTokenExpiry(v string) time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}
	}
	for _, layout := range []string{"2006-01-02 15:04:05 MST", "2006-01-02 15:04:05 -0700", time.RFC3339} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
