package update

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Repository is where builds are published.
const Repository = "Mark7888/two-place-paste"

// DefaultReleaseBase is the root every release download URL hangs off.
const DefaultReleaseBase = "https://github.com/" + Repository + "/releases"

// maxManifestBytes bounds what is read for a manifest or its signature: a
// manifest lists a handful of files and is well under a kilobyte.
const maxManifestBytes = 64 << 10

// ErrNoRelease reports a channel that has nothing published yet: no release
// has been tagged, or no push has reached the default branch since Beta began.
var ErrNoRelease = errors.New("update: nothing has been published on this channel yet")

// releaseSource reads Stable and Beta: a signed manifest and the files it
// lists, from plain release-download URLs. No API call and no token, so no
// rate limit (plan §3.3).
type releaseSource struct {
	client *http.Client
	base   string
	key    ed25519.PublicKey
}

// releaseDir is the URL prefix a channel's files are downloaded from.
func (r releaseSource) releaseDir(channel string) (string, error) {
	switch channel {
	case "stable":
		// "latest" never resolves to a draft or a prerelease.
		return r.base + "/latest/download", nil
	case "beta":
		return r.base + "/download/channel-beta", nil
	}
	return "", fmt.Errorf("update: %q is not a release channel", channel)
}

// latest returns the channel's current signed manifest and the URL prefix of
// its files.
func (r releaseSource) latest(ctx context.Context, channel string) (Manifest, string, error) {
	dir, err := r.releaseDir(channel)
	if err != nil {
		return Manifest{}, "", err
	}
	data, err := r.get(ctx, dir+"/manifest-desktop.json")
	if err != nil {
		return Manifest{}, "", err
	}
	sig, err := r.get(ctx, dir+"/manifest-desktop.json.sig")
	if err != nil {
		return Manifest{}, "", err
	}
	m, err := VerifyManifest(data, sig, r.key)
	if err != nil {
		return Manifest{}, "", err
	}
	// The signature proves CI made the manifest; this proves it was made for
	// this channel and not replayed from another one.
	if m.Channel != channel {
		return Manifest{}, "", fmt.Errorf("update: the %s release holds a %s manifest", channel, m.Channel)
	}
	if m.Repository != "" && !strings.EqualFold(m.Repository, Repository) {
		return Manifest{}, "", fmt.Errorf("update: the manifest is from %s, not %s", m.Repository, Repository)
	}
	return m, dir, nil
}

func (r releaseSource) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("update: build the request for %s: %w", url, err)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("update: fetch %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNoRelease
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("update: fetch %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("update: read %s: %w", url, err)
	}
	if len(b) > maxManifestBytes {
		return nil, fmt.Errorf("update: %s is larger than a manifest can be", url)
	}
	return b, nil
}
