package update

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/desktop/internal/config"
	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
	"github.com/Mark7888/two-place-paste/pkg/tppclient/keystore"
)

const (
	testToken = "github_pat_test-token-value"
	nightSHA  = "0123456789abcdef0123456789abcdef01234567"
)

// memSecrets is a keystore in memory.
type memSecrets struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *memSecrets) Load(name string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.m[name]
	if !ok {
		return nil, keystore.ErrNotFound
	}
	return b, nil
}

func (s *memSecrets) Save(name string, b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string][]byte{}
	}
	s.m[name] = b
	return nil
}

func (s *memSecrets) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, name)
	return nil
}

// fakeActions is the GitHub API and the artifact storage host, as two
// servers, so a test can tell which one saw the token.
type fakeActions struct {
	t       *testing.T
	api     *httptest.Server
	storage *httptest.Server

	mu         sync.Mutex
	runs       []ghRun
	artifacts  map[int64][]ghArtifact
	blobs      map[int64][]byte // artifact id → zip
	pulls      []ghPull
	prFiles    []string
	tokenLeaks []string
	expires    string
}

func newFakeActions(t *testing.T) *fakeActions {
	f := &fakeActions{t: t, artifacts: map[int64][]ghArtifact{}, blobs: map[int64][]byte{}}
	f.storage = httptest.NewServer(http.HandlerFunc(f.serveStorage))
	f.api = httptest.NewServer(http.HandlerFunc(f.serveAPI))
	t.Cleanup(f.api.Close)
	t.Cleanup(f.storage.Close)
	return f
}

func (f *fakeActions) serveStorage(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if h := r.Header.Get("Authorization"); h != "" {
		f.tokenLeaks = append(f.tokenLeaks, h)
	}
	var id int64
	if _, err := fmt.Sscanf(r.URL.Path, "/blob/%d", &id); err != nil {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write(f.blobs[id])
}

func (f *fakeActions) serveAPI(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
		return
	}
	if f.expires != "" {
		w.Header().Set("Github-Authentication-Token-Expiration", f.expires)
	}
	repo := "/repos/" + Repository
	p := r.URL.Path
	switch {
	case p == repo+"/actions/runs":
		writeJSONResponse(w, map[string]int{"total_count": len(f.runs)})
	case strings.HasPrefix(p, repo+"/commits/") && strings.HasSuffix(p, "/pulls"):
		writeJSONResponse(w, f.pulls)
	case strings.HasPrefix(p, repo+"/commits/"):
		ref := strings.TrimPrefix(p, repo+"/commits/")
		if !strings.HasPrefix(nightSHA, ref) {
			http.NotFound(w, r)
			return
		}
		c := ghCommit{SHA: nightSHA, HTMLURL: "https://github.com/" + Repository + "/commit/" + nightSHA}
		c.Commit.Message = "Make the thing\n\nWith details."
		c.Commit.Author.Name = "Someone"
		writeJSONResponse(w, c)
	case p == repo+"/actions/workflows/desktop.yml/runs":
		var out []ghRun
		if r.URL.Query().Get("head_sha") == nightSHA {
			out = f.runs
		}
		writeJSONResponse(w, map[string]any{"workflow_runs": out})
	case strings.HasPrefix(p, repo+"/actions/runs/") && strings.HasSuffix(p, "/artifacts"):
		var id int64
		_, _ = fmt.Sscanf(strings.TrimPrefix(p, repo+"/actions/runs/"), "%d", &id)
		writeJSONResponse(w, map[string]any{"artifacts": f.artifacts[id]})
	case strings.HasPrefix(p, repo+"/actions/artifacts/") && strings.HasSuffix(p, "/zip"):
		var id int64
		_, _ = fmt.Sscanf(strings.TrimPrefix(p, repo+"/actions/artifacts/"), "%d", &id)
		http.Redirect(w, r, fmt.Sprintf("%s/blob/%d?sig=secret-signed-url", f.storage.URL, id), http.StatusFound)
	case strings.HasPrefix(p, repo+"/pulls/") && strings.HasSuffix(p, "/files"):
		var files []map[string]string
		if r.URL.Query().Get("page") == "1" {
			for _, name := range f.prFiles {
				files = append(files, map[string]string{"filename": name})
			}
		}
		writeJSONResponse(w, files)
	case strings.HasPrefix(p, repo+"/pulls/"):
		writeJSONResponse(w, ghPull{Number: 42, Title: "Add the thing"})
	default:
		http.NotFound(w, r)
	}
}

func writeJSONResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// addRun publishes one run's artifacts: the platform payload and the
// manifest, signed with key unless key is nil.
func (f *fakeActions) addRun(t *testing.T, run ghRun, key ed25519.PrivateKey, stamp int64, payload []byte) {
	t.Helper()
	sum := sha256.Sum256(payload)
	man := Manifest{
		Schema: 1, Platform: "desktop", Repository: Repository, Version: "1.0.1-dev.x",
		Stamp: stamp, Channel: "nightly", Commit: nightSHA,
		Files: []File{{Name: testAsset, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(payload))}},
	}
	manJSON, err := json.Marshal(man)
	if err != nil {
		t.Fatal(err)
	}
	manFiles := map[string][]byte{"manifest-desktop.json": manJSON}
	if key != nil {
		manFiles["manifest-desktop.json.sig"] = ed25519.Sign(key, manJSON)
	}
	payloadZip := zipOf(t, map[string][]byte{testAsset: payload, "TwoPlacePaste-Setup.exe": []byte("installer")})
	manifestZip := zipOf(t, manFiles)

	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, run)
	payloadID, manifestID := run.ID*10+1, run.ID*10+2
	f.blobs[payloadID], f.blobs[manifestID] = payloadZip, manifestZip
	digest := sha256.Sum256(payloadZip)
	f.artifacts[run.ID] = []ghArtifact{
		{ID: payloadID, Name: "tppdesktop-Windows-x64-1.0.1-dev.x", Digest: "sha256:" + hex.EncodeToString(digest[:]),
			ArchiveDownloadURL: fmt.Sprintf("%s/repos/%s/actions/artifacts/%d/zip", f.api.URL, Repository, payloadID)},
		{ID: manifestID, Name: "tppdesktop-manifest-1.0.1-dev.x",
			ArchiveDownloadURL: fmt.Sprintf("%s/repos/%s/actions/artifacts/%d/zip", f.api.URL, Repository, manifestID)},
	}
}

func zipOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, b := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func pushRun(id int64) ghRun {
	r := ghRun{ID: id, Status: "completed", Conclusion: "success", Event: "push", HeadBranch: "feature",
		HTMLURL: fmt.Sprintf("https://github.com/%s/actions/runs/%d", Repository, id), CreatedAt: "2026-10-09T10:00:00Z"}
	r.HeadRepository.FullName = Repository
	return r
}

func forkRun(id int64) ghRun {
	r := pushRun(id)
	r.Event = "pull_request"
	r.HeadRepository.FullName = "stranger/two-place-paste"
	r.HeadRepository.Owner.Login = "stranger"
	return r
}

type nightlyHarness struct {
	*harness
	actions *fakeActions
	secrets *memSecrets
}

func newNightlyHarness(t *testing.T) *nightlyHarness {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	actions := newFakeActions(t)
	secrets := &memSecrets{}
	h := &harness{key: priv, applier: &fakeApplier{}, quit: make(chan struct{}, 1)}
	h.m, err = New(Options{
		ConfigDir: t.TempDir(),
		CacheDir:  t.TempDir(),
		Applier:   h.applier,
		Quit:      func() { h.quit <- struct{}{} },
		Current:   stableBuild(100),
		Asset:     testAsset,
		APIBase:   actions.api.URL,
		Key:       pub,
		Client:    &http.Client{Timeout: 10 * time.Second},
		Secrets:   secrets,
		LookupEnv: func(string) (string, bool) { return "", false },
	})
	if err != nil {
		t.Fatal(err)
	}
	return &nightlyHarness{harness: h, actions: actions, secrets: secrets}
}

func (n *nightlyHarness) useNightly(t *testing.T) {
	t.Helper()
	if _, err := n.m.SetUpdateToken(context.Background(), testToken); err != nil {
		t.Fatalf("SetUpdateToken: %v", err)
	}
	n.m.SetPreferences(config.Settings{UpdateChannel: "nightly", NightlyCommit: nightSHA})
}

func TestNightlyNeedsAToken(t *testing.T) {
	n := newNightlyHarness(t)
	n.m.SetPreferences(config.Settings{UpdateChannel: "nightly", NightlyCommit: nightSHA})
	if _, err := n.m.Check(context.Background()); !errors.Is(err, ErrNoToken) {
		t.Fatalf("Check without a token: %v; want ErrNoToken", err)
	}
}

func TestATokenIsCheckedBeforeItIsStored(t *testing.T) {
	n := newNightlyHarness(t)
	n.actions.expires = "2027-01-05 14:31:04 UTC"
	if _, err := n.m.SetUpdateToken(context.Background(), "github_pat_wrong"); !isStatus(err, http.StatusBadRequest) {
		t.Fatalf("SetUpdateToken(wrong) = %v; want a 400", err)
	}
	if _, err := n.secrets.Load(tokenSecret); err == nil {
		t.Fatal("a rejected token was stored")
	}
	v, err := n.m.SetUpdateToken(context.Background(), "  "+testToken+"\n")
	if err != nil {
		t.Fatal(err)
	}
	if !v.Token.Set || v.Token.Source != "keystore" || v.Token.ExpiresAt == nil || v.Token.ExpiresAt.Year() != 2027 {
		t.Fatalf("token view = %+v", v.Token)
	}
	if b, _ := n.secrets.Load(tokenSecret); string(b) != testToken {
		t.Fatalf("stored %q", b)
	}
	// The view never carries the token itself.
	raw, _ := json.Marshal(v)
	if strings.Contains(string(raw), testToken) {
		t.Fatal("the update view contains the token")
	}

	v, err = n.m.DeleteUpdateToken(context.Background())
	if err != nil || v.Token.Set {
		t.Fatalf("after DeleteUpdateToken: %+v, %v", v.Token, err)
	}
}

func TestTheEnvironmentTokenWins(t *testing.T) {
	n := newNightlyHarness(t)
	n.m.opts.LookupEnv = func(k string) (string, bool) {
		if k == EnvToken {
			return testToken, true
		}
		return "", false
	}
	n.m.loadTokenState()
	if v := n.m.Status(context.Background()); !v.Token.Set || v.Token.Source != "environment" {
		t.Fatalf("token view = %+v", v.Token)
	}
	tok, err := n.m.readToken()
	if err != nil || tok != testToken {
		t.Fatalf("readToken = %q, %v", tok, err)
	}
}

func TestResolveCommit(t *testing.T) {
	n := newNightlyHarness(t)
	n.useNightly(t)
	full, err := n.m.ResolveCommit(context.Background(), "0123456789AB")
	if err != nil || full != nightSHA {
		t.Fatalf("ResolveCommit = %q, %v", full, err)
	}
	if _, err := n.m.ResolveCommit(context.Background(), "fffffff"); !isStatus(err, http.StatusBadRequest) {
		t.Fatalf("ResolveCommit(unknown) = %v; want a 400", err)
	}
	if _, err := n.m.ResolveCommit(context.Background(), "xyz"); !isStatus(err, http.StatusBadRequest) {
		t.Fatalf("ResolveCommit(not hex) = %v; want a 400", err)
	}
}

func TestACommitWithNoBuildIsAnError(t *testing.T) {
	n := newNightlyHarness(t)
	n.useNightly(t)
	_, err := n.m.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no desktop build exists for commit 0123456") {
		t.Fatalf("Check: %v", err)
	}
}

func TestASignedNightlyBuildInstallsWithoutConfirmation(t *testing.T) {
	n := newNightlyHarness(t)
	n.useNightly(t)
	n.actions.addRun(t, pushRun(7), n.key, 500, []byte("nightly build"))

	v, err := n.m.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c := v.Available
	if c == nil || !c.Signed || c.NeedsConfirmation || c.Commit != nightSHA {
		t.Fatalf("available = %+v", c)
	}
	if c.Origin == nil || c.Origin.Fork || c.Origin.CommitMessage != "Make the thing" {
		t.Fatalf("origin = %+v", c.Origin)
	}
	if _, err := n.m.Install(context.Background(), localui.InstallRequest{}); err != nil {
		t.Fatal(err)
	}
	<-n.quit
	if len(n.applier.installed) != 1 || string(n.applier.installed[0]) != "nightly build" {
		t.Fatalf("installed %q", n.applier.installed)
	}
	if len(n.actions.tokenLeaks) != 0 {
		t.Fatalf("the storage host was sent the token: %q", n.actions.tokenLeaks)
	}
}

func TestAForkBuildNeedsConfirmationAndItsDigest(t *testing.T) {
	n := newNightlyHarness(t)
	n.useNightly(t)
	n.actions.pulls = []ghPull{{Number: 42, Title: "Add the thing"}}
	n.actions.prFiles = []string{"desktop/main.go", ".github/workflows/desktop.yml"}
	n.actions.addRun(t, forkRun(8), nil, 600, []byte("fork build"))

	v, err := n.m.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c := v.Available
	if c == nil || c.Signed || !c.NeedsConfirmation {
		t.Fatalf("available = %+v; want an unsigned build that needs confirmation", c)
	}
	o := c.Origin
	if o == nil || !o.Fork || o.Repository != "stranger/two-place-paste" || o.PullRequest != 42 || !o.ChangesWorkflows {
		t.Fatalf("origin = %+v", o)
	}

	if _, err := n.m.Install(context.Background(), localui.InstallRequest{}); !isStatus(err, http.StatusConflict) {
		t.Fatalf("Install without confirmation: %v; want a 409", err)
	}

	// The artifact changes after the listing recorded its digest.
	n.actions.mu.Lock()
	n.actions.blobs[81] = zipOf(t, map[string][]byte{testAsset: []byte("swapped")})
	n.actions.mu.Unlock()
	if _, err := n.m.Install(context.Background(), localui.InstallRequest{Confirm: true}); !errors.Is(err, ErrChecksum) {
		t.Fatalf("Install of a changed artifact: %v; want ErrChecksum", err)
	}
	if len(n.applier.installed) != 0 {
		t.Fatal("a changed artifact was installed")
	}
}

func TestAConfirmedForkBuildInstalls(t *testing.T) {
	n := newNightlyHarness(t)
	n.useNightly(t)
	n.actions.addRun(t, forkRun(9), nil, 600, []byte("fork build"))
	if _, err := n.m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := n.m.Install(context.Background(), localui.InstallRequest{Confirm: true}); err != nil {
		t.Fatal(err)
	}
	<-n.quit
	if string(n.applier.installed[0]) != "fork build" {
		t.Fatalf("installed %q", n.applier.installed[0])
	}
}

func TestABadNightlySignatureIsNotTreatedAsUnsigned(t *testing.T) {
	n := newNightlyHarness(t)
	n.useNightly(t)
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	n.actions.addRun(t, pushRun(10), other, 700, []byte("forged"))
	if _, err := n.m.Check(context.Background()); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("Check: %v; want ErrBadSignature", err)
	}
}

func TestARunningBuildIsReportedNotInstalled(t *testing.T) {
	n := newNightlyHarness(t)
	n.useNightly(t)
	run := pushRun(11)
	run.Status, run.Conclusion = "in_progress", ""
	n.actions.mu.Lock()
	n.actions.runs = []ghRun{run}
	n.actions.mu.Unlock()

	v, err := n.m.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.State != localui.UpdateStateBuilding || v.Available == nil || !v.Available.Building {
		t.Fatalf("state %q, available %+v", v.State, v.Available)
	}
	if _, err := n.m.Install(context.Background(), localui.InstallRequest{Confirm: true}); !isStatus(err, http.StatusConflict) {
		t.Fatalf("Install of a running build: %v; want a 409", err)
	}
}

func TestARejectedTokenIsMarkedInvalid(t *testing.T) {
	n := newNightlyHarness(t)
	n.useNightly(t)
	if err := n.secrets.Save(tokenSecret, []byte("github_pat_revoked")); err != nil {
		t.Fatal(err)
	}
	if _, err := n.m.Check(context.Background()); !errors.Is(err, ErrTokenRejected) {
		t.Fatalf("Check: %v; want ErrTokenRejected", err)
	}
	if v := n.m.Status(context.Background()); !v.Token.Invalid {
		t.Fatal("the token was not marked invalid")
	}
}

// The token never appears in what the updater logs or returns.
func TestTheTokenStaysOutOfErrors(t *testing.T) {
	n := newNightlyHarness(t)
	n.useNightly(t)
	n.actions.addRun(t, pushRun(12), n.key, 800, []byte("build"))
	n.actions.storage.Close() // the download fails part way
	if _, err := n.m.Check(context.Background()); err == nil {
		// The manifest download is from storage too, so the check fails.
		t.Fatal("Check succeeded with the storage host down")
	} else if strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "secret-signed-url") {
		t.Fatalf("the error leaks a secret: %v", err)
	}
	if _, err := os.Stat(n.m.opts.CacheDir); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}
