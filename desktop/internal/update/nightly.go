package update

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Mark7888/two-place-paste/desktop/internal/config"
	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
)

// desktopWorkflow is the workflow whose runs build the desktop app.
const desktopWorkflow = "desktop.yml"

// The JSON shapes Nightly reads; only the fields it uses.
type (
	ghCommit struct {
		SHA     string `json:"sha"`
		HTMLURL string `json:"html_url"`
		Commit  struct {
			Message string `json:"message"`
			Author  struct {
				Name string `json:"name"`
			} `json:"author"`
		} `json:"commit"`
		Author *struct {
			Login string `json:"login"`
		} `json:"author"`
	}
	ghRun struct {
		ID             int64  `json:"id"`
		Status         string `json:"status"`
		Conclusion     string `json:"conclusion"`
		Event          string `json:"event"`
		HeadBranch     string `json:"head_branch"`
		HTMLURL        string `json:"html_url"`
		CreatedAt      string `json:"created_at"`
		HeadRepository struct {
			FullName string `json:"full_name"`
			Owner    struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"head_repository"`
		PullRequests []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
	}
	ghArtifact struct {
		ID                 int64  `json:"id"`
		Name               string `json:"name"`
		Expired            bool   `json:"expired"`
		ArchiveDownloadURL string `json:"archive_download_url"`
		Digest             string `json:"digest"`
	}
	ghPull struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
	}
)

// ResolveCommit turns an abbreviated hash into the full one, through the API.
// The service calls it before saving a Nightly commit, so the settings file
// only ever holds full hashes.
func (m *Manager) ResolveCommit(ctx context.Context, hash string) (string, error) {
	hash = strings.ToLower(strings.TrimSpace(hash))
	if len(hash) < 7 || strings.Trim(hash, "0123456789abcdef") != "" {
		return "", localui.Errorf(http.StatusBadRequest, nil, "%q is not a commit hash; use at least 7 hex characters", hash)
	}
	token, err := m.readToken()
	if err != nil {
		return "", localui.Errorf(http.StatusBadRequest, err, "%v", err)
	}
	var c ghCommit
	err = m.api(token).get(ctx, "/repos/"+Repository+"/commits/"+hash, &c)
	m.noteTokenError(err)
	switch {
	case errors.Is(err, errNotFound):
		return "", localui.Errorf(http.StatusBadRequest, err,
			"no commit %s in %s (a short hash that matches several commits needs more characters)", hash, Repository)
	case err != nil:
		return "", localui.Errorf(http.StatusBadGateway, err, "the commit could not be looked up: %v", err)
	case !config.IsFullSHA(c.SHA):
		return "", localui.Errorf(http.StatusBadGateway, nil, "GitHub returned %q for %s", c.SHA, hash)
	}
	return c.SHA, nil
}

// findNightly returns the build of one commit (plan §3.8). There is nothing
// newer to look for: the commit is fixed, so the only questions are whether it
// has a build, whether that build is signed, and where it came from.
func (m *Manager) findNightly(ctx context.Context, commit string) (*candidate, error) {
	if commit == "" {
		return nil, errors.New("choose the commit to install: enter its hash above")
	}
	token, err := m.readToken()
	if err != nil {
		return nil, err
	}
	api := m.api(token)
	cand, err := m.nightlyCandidate(ctx, api, commit)
	m.noteTokenError(err)
	return cand, err
}

func (m *Manager) nightlyCandidate(ctx context.Context, api githubAPI, sha string) (*candidate, error) {
	var commit ghCommit
	if err := api.get(ctx, "/repos/"+Repository+"/commits/"+sha, &commit); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, fmt.Errorf("commit %s is not in %s", short(sha), Repository)
		}
		return nil, err
	}

	var runs struct {
		WorkflowRuns []ghRun `json:"workflow_runs"`
	}
	path := fmt.Sprintf("/repos/%s/actions/workflows/%s/runs?head_sha=%s&per_page=30", Repository, desktopWorkflow, sha)
	if err := api.get(ctx, path, &runs); err != nil {
		return nil, err
	}
	if len(runs.WorkflowRuns) == 0 {
		return nil, fmt.Errorf("no desktop build exists for commit %s. Only commits on the default branch "+
			"and the latest commit of an open pull request are built, and only when they change desktop/", short(sha))
	}

	var done, running []ghRun
	for _, r := range runs.WorkflowRuns {
		switch {
		case r.Status == "completed" && r.Conclusion == "success":
			done = append(done, r)
		case r.Status != "completed":
			running = append(running, r)
		}
	}
	if len(done) == 0 {
		if len(running) > 0 {
			origin := m.origin(ctx, api, commit, running[0])
			return &candidate{view: localui.UpdateCandidate{
				BuildView: localui.BuildView{Version: "building", Channel: config.ChannelNightly, Commit: sha},
				Building:  true,
				Origin:    origin,
			}}, nil
		}
		return nil, fmt.Errorf("the build of commit %s failed: %s", short(sha), runs.WorkflowRuns[0].HTMLURL)
	}

	// A push run in this repository had the signing key; a fork's
	// pull_request run never did. Prefer the one that can be signed, then
	// the newest.
	sort.SliceStable(done, func(i, j int) bool {
		si, sj := sameRepo(done[i]), sameRepo(done[j])
		if si != sj {
			return si
		}
		return done[i].CreatedAt > done[j].CreatedAt
	})
	var firstErr error
	for _, run := range done {
		cand, err := m.runCandidate(ctx, api, commit, run)
		if err == nil {
			return cand, nil
		}
		if errors.Is(err, ErrBadSignature) || errors.Is(err, ErrTokenRejected) {
			// A signature that fails is tampering, not a reason to try the
			// next run; a dead token fails them all.
			return nil, err
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, firstErr
}

// runCandidate is the build one successful run made.
func (m *Manager) runCandidate(ctx context.Context, api githubAPI, commit ghCommit, run ghRun) (*candidate, error) {
	var list struct {
		Artifacts []ghArtifact `json:"artifacts"`
	}
	if err := api.get(ctx, fmt.Sprintf("/repos/%s/actions/runs/%d/artifacts?per_page=100", Repository, run.ID), &list); err != nil {
		return nil, err
	}
	platform := strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(m.asset, "TwoPlacePaste-"), ".zip"), ".exe")
	payload, okPayload := findArtifact(list.Artifacts, "tppdesktop-"+platform+"-")
	manifestArt, okManifest := findArtifact(list.Artifacts, "tppdesktop-manifest-")
	switch {
	case !okPayload || !okManifest:
		return nil, fmt.Errorf("the build of %s has no %s artifact: %s", short(commit.SHA), platform, run.HTMLURL)
	case payload.Expired || manifestArt.Expired:
		return nil, fmt.Errorf("the build of %s has expired; re-run it to install it: %s", short(commit.SHA), run.HTMLURL)
	}

	man, signed, err := m.nightlyManifest(ctx, api, manifestArt)
	if err != nil {
		return nil, err
	}
	if man.Commit != "" && man.Commit != commit.SHA {
		return nil, fmt.Errorf("the build's manifest is for commit %s, not %s", short(man.Commit), short(commit.SHA))
	}
	file, ok := man.File(m.asset)
	if !ok {
		return nil, fmt.Errorf("the build of %s has no %s", short(commit.SHA), m.asset)
	}
	if man.Stamp == m.opts.Current.Stamp {
		return nil, nil
	}
	if !signed && !strings.HasPrefix(payload.Digest, "sha256:") {
		return nil, errors.New("GitHub gave no checksum for this unsigned build, so it cannot be verified")
	}

	view := localui.UpdateCandidate{
		BuildView: localui.BuildView{
			Version: man.Version, Channel: config.ChannelNightly, Commit: commit.SHA, Stamp: man.Stamp,
		},
		Signed:            signed,
		NeedsConfirmation: !signed,
		Origin:            m.origin(ctx, api, commit, run),
	}
	if !signed && view.Origin != nil && view.Origin.PullRequest == 0 {
		// The pull request could not be found, so whether it changed the
		// workflows cannot be told either; assume it might have.
		view.Origin.ChangesWorkflows = true
	}
	return &candidate{
		view: view,
		fetch: func(ctx context.Context, dir string) (string, error) {
			return m.fetchArtifact(ctx, dir, payload, file, signed)
		},
	}, nil
}

// nightlyManifest reads a run's manifest artifact. A signed one must verify;
// an unsigned one (a fork's pull request, which never sees the key) is parsed
// as-is and reported unsigned.
func (m *Manager) nightlyManifest(ctx context.Context, api githubAPI, art ghArtifact) (Manifest, bool, error) {
	var buf strings.Builder
	if err := api.downloadArtifact(ctx, art.ArchiveDownloadURL, &buf); err != nil {
		return Manifest{}, false, err
	}
	z, err := zip.NewReader(strings.NewReader(buf.String()), int64(buf.Len()))
	if err != nil {
		return Manifest{}, false, fmt.Errorf("read the manifest artifact: %w", err)
	}
	data, err := readZipFile(z, "manifest-desktop.json", maxManifestBytes)
	if err != nil {
		return Manifest{}, false, err
	}
	sig, err := readZipFile(z, "manifest-desktop.json.sig", maxManifestBytes)
	if errors.Is(err, os.ErrNotExist) {
		man, err := parseManifest(data)
		return man, false, err
	}
	if err != nil {
		return Manifest{}, false, err
	}
	man, err := VerifyManifest(data, sig, m.opts.Key)
	return man, true, err
}

// fetchArtifact downloads the build's artifact and extracts the one file this
// platform installs. A signed build's file is checked against its manifest; an
// unsigned build's whole artifact is checked against the SHA-256 GitHub
// recorded when the run uploaded it, which is all there is to check it by.
func (m *Manager) fetchArtifact(ctx context.Context, dir string, art ghArtifact, want File, signed bool) (string, error) {
	token, err := m.readToken()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	zf, err := os.CreateTemp(dir, "artifact-*.zip")
	if err != nil {
		return "", fmt.Errorf("create a file in %s: %w", dir, err)
	}
	defer func() {
		_ = zf.Close()
		_ = os.Remove(zf.Name())
	}()
	h := sha256.New()
	if err := m.api(token).downloadArtifact(ctx, art.ArchiveDownloadURL, io.MultiWriter(zf, h)); err != nil {
		return "", err
	}
	if !signed && "sha256:"+hex.EncodeToString(h.Sum(nil)) != art.Digest {
		return "", fmt.Errorf("%w: the artifact's SHA-256 is not the one GitHub recorded", ErrChecksum)
	}
	info, err := zf.Stat()
	if err != nil {
		return "", fmt.Errorf("stat the artifact: %w", err)
	}
	z, err := zip.NewReader(zf, info.Size())
	if err != nil {
		return "", fmt.Errorf("read the artifact: %w", err)
	}
	for _, f := range z.File {
		if filepath.Base(f.Name) != want.Name {
			continue
		}
		r, err := f.Open()
		if err != nil {
			return "", fmt.Errorf("open %s in the artifact: %w", want.Name, err)
		}
		defer func() { _ = r.Close() }()
		// Checked against the manifest either way: for an unsigned build that
		// adds nothing to the digest check above, but costs nothing either.
		return saveVerified(r, dir, want)
	}
	return "", fmt.Errorf("the artifact has no %s", want.Name)
}

// origin collects what the confirmation screen shows about a commit. Every
// lookup is best effort: a missing detail is left out, never an error.
func (m *Manager) origin(ctx context.Context, api githubAPI, commit ghCommit, run ghRun) *localui.BuildOrigin {
	o := &localui.BuildOrigin{
		Repository:    run.HeadRepository.FullName,
		Branch:        run.HeadBranch,
		Fork:          !sameRepo(run),
		CommitMessage: firstLine(commit.Commit.Message),
		Author:        commit.Commit.Author.Name,
		URL:           commit.HTMLURL,
		RunURL:        run.HTMLURL,
	}
	if o.Repository == "" {
		o.Repository = Repository
	}
	if commit.Author != nil && commit.Author.Login != "" {
		o.Author = commit.Author.Login
	}

	pr := ghPull{}
	if len(run.PullRequests) > 0 {
		pr.Number = run.PullRequests[0].Number
	}
	if pr.Number == 0 {
		var pulls []ghPull
		if err := api.get(ctx, "/repos/"+Repository+"/commits/"+commit.SHA+"/pulls", &pulls); err == nil && len(pulls) > 0 {
			pr = pulls[0]
		}
	}
	if pr.Number == 0 && o.Fork && run.HeadRepository.Owner.Login != "" && run.HeadBranch != "" {
		var pulls []ghPull
		q := fmt.Sprintf("/repos/%s/pulls?state=all&head=%s:%s", Repository, run.HeadRepository.Owner.Login, run.HeadBranch)
		if err := api.get(ctx, q, &pulls); err == nil && len(pulls) > 0 {
			pr = pulls[0]
		}
	}
	if pr.Number == 0 {
		return o
	}
	o.PullRequest = pr.Number
	if pr.Title == "" {
		var full ghPull
		if err := api.get(ctx, fmt.Sprintf("/repos/%s/pulls/%d", Repository, pr.Number), &full); err == nil {
			pr.Title = full.Title
		}
	}
	o.PullRequestTitle = pr.Title
	o.ChangesWorkflows = m.changesWorkflows(ctx, api, pr.Number)
	return o
}

// changesWorkflows reports whether a pull request touches .github/workflows.
// When it cannot tell, it says yes: the warning is the safe side to be wrong on.
func (m *Manager) changesWorkflows(ctx context.Context, api githubAPI, pr int) bool {
	for page := 1; page <= 10; page++ {
		var files []struct {
			Filename         string `json:"filename"`
			PreviousFilename string `json:"previous_filename"`
		}
		if err := api.get(ctx, fmt.Sprintf("/repos/%s/pulls/%d/files?per_page=100&page=%d", Repository, pr, page), &files); err != nil {
			return true
		}
		for _, f := range files {
			if strings.HasPrefix(f.Filename, ".github/workflows/") || strings.HasPrefix(f.PreviousFilename, ".github/workflows/") {
				return true
			}
		}
		if len(files) < 100 {
			return false
		}
	}
	return true
}

func sameRepo(r ghRun) bool {
	return r.HeadRepository.FullName == "" || strings.EqualFold(r.HeadRepository.FullName, Repository)
}

// findArtifact returns the newest artifact whose name starts with prefix.
func findArtifact(list []ghArtifact, prefix string) (ghArtifact, bool) {
	var found ghArtifact
	ok := false
	for _, a := range list {
		if strings.HasPrefix(a.Name, prefix) && (!ok || a.ID > found.ID) {
			found, ok = a, true
		}
	}
	return found, ok
}

func readZipFile(z *zip.Reader, name string, limit int64) ([]byte, error) {
	for _, f := range z.File {
		if filepath.Base(f.Name) != name {
			continue
		}
		r, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", name, err)
		}
		defer func() { _ = r.Close() }()
		b, err := io.ReadAll(io.LimitReader(r, limit+1))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		if int64(len(b)) > limit {
			return nil, fmt.Errorf("%s is too large", name)
		}
		return b, nil
	}
	return nil, fmt.Errorf("%s: %w", name, os.ErrNotExist)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
