package tppclient_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// relay is a real TwoPlacePaste server, with a real Redis behind it, run for
// one test.
//
// The client is tested against the actual relay rather than a stand-in: a fake
// server would encode this package's assumptions about the protocol, which is
// exactly what the test is supposed to check. The server is built and run as a
// subprocess because /server is another module and its packages are internal
// to it — this phase reads none of them (ROADMAP P5, "must not touch").
type relay struct {
	baseURL   string
	adminPass string
	client    *http.Client
}

// serverBinary builds cmd/tpp once per test binary.
var serverBinary struct {
	sync.Once
	path string
	err  error
}

func buildServer() (string, error) {
	serverBinary.Do(func() {
		dir, err := os.MkdirTemp("", "tpp-server-build-")
		if err != nil {
			serverBinary.err = err
			return
		}
		out := filepath.Join(dir, "tpp")
		cmd := exec.Command("go", "build", "-o", out, "./cmd/tpp")
		cmd.Dir = filepath.Join("..", "..", "server")
		if combined, err := cmd.CombinedOutput(); err != nil {
			serverBinary.err = fmt.Errorf("build the relay: %w\n%s", err, combined)
			return
		}
		serverBinary.path = out
	})
	return serverBinary.path, serverBinary.err
}

// startRelay brings up Redis and the relay, and tears both down with the test.
// It skips when the machine cannot run them, which is the same contract the
// server's own Redis-backed tests use. env adds KEY=value settings for the
// relay.
func startRelay(t *testing.T, env ...string) *relay {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping: -short, and this test runs a real server")
	}
	for _, bin := range []string{"redis-server", "go"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("skipping: %s is not installed, so a real relay cannot be run", bin)
		}
	}

	dir := t.TempDir()
	redisAddr := startRedis(t, dir)

	binary, err := buildServer()
	if err != nil {
		t.Fatalf("build the relay: %v", err)
	}

	addr := freeAddr(t)
	base := "http://" + addr
	const adminPass = "integration-test-password"

	cmd := exec.Command(binary)
	// The working directory is the temp dir so that no .env from the checkout
	// leaks into the test's configuration.
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"TPP_HTTP_ADDR="+addr,
		"TPP_PUBLIC_BASE_URL="+base,
		"ADMIN_PASSWORD="+adminPass,
		"TPP_REDIS_ADDR="+redisAddr,
		"TPP_BLOB_ROOT="+filepath.Join(dir, "blobs"),
		"TPP_BLOB_SWEEP_INTERVAL=10m",
		"TPP_LOG_LEVEL=warn",
	)
	cmd.Env = append(cmd.Env, env...)
	logs := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the relay: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _, _ = cmd.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
		}
		if t.Failed() {
			t.Logf("relay log:\n%s", logs.String())
		}
	})

	r := &relay{baseURL: base, adminPass: adminPass, client: &http.Client{Timeout: 10 * time.Second}}
	waitFor(t, "the relay to answer /healthz", func() bool {
		resp, err := r.client.Get(base + "/healthz")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
	return r
}

// startRedis runs a Redis with the deployment's own policy: persistence off
// (a test needs none) but volatile-lru, because allkeys-lru would evict group
// records and make this test flake in a way production would too (SPEC §4.2).
func startRedis(t *testing.T, dir string) string {
	t.Helper()
	addr := freeAddr(t)
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	cmd := exec.Command("redis-server",
		"--bind", "127.0.0.1",
		"--port", port,
		"--save", "",
		"--appendonly", "no",
		"--maxmemory-policy", "volatile-lru",
		"--dir", dir,
	)
	logs := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		t.Skipf("skipping: redis-server would not start: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	waitFor(t, "redis to accept connections", func() bool {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	})
	return addr
}

// creationURL signs in to the admin, generates a creation token and returns
// the URL an operator would hand a user (SPEC §3.1, §4.4).
func (r *relay) creationURL(t *testing.T, name string) string {
	t.Helper()
	var created struct {
		URL string `json:"url"`
	}
	r.admin(t).call(t, http.MethodPost, "/admin/api/tokens", map[string]string{"name": name}, http.StatusCreated, &created)
	// The token is the URL's last path element. The relay builds the URL
	// from its public base, so it is rebuilt here on the address this test
	// dialled.
	i := strings.LastIndex(created.URL, "/")
	if i < 0 || i == len(created.URL)-1 {
		t.Fatalf("creation URL %q has no token", created.URL)
	}
	return r.baseURL + created.URL[i:]
}

// adminAPI is a signed-in session on the relay's admin JSON API.
type adminAPI struct {
	baseURL string
	client  *http.Client
	csrf    string
}

// admin signs in to the relay's admin API.
func (r *relay) admin(t *testing.T) *adminAPI {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	a := &adminAPI{baseURL: r.baseURL, client: &http.Client{Jar: jar, Timeout: 10 * time.Second}}
	var sess struct {
		CSRF string `json:"csrf"`
	}
	a.call(t, http.MethodPost, "/admin/api/login", map[string]string{"password": r.adminPass}, http.StatusOK, &sess)
	if sess.CSRF == "" {
		t.Fatal("admin login returned no CSRF token")
	}
	a.csrf = sess.CSRF
	return a
}

// call sends one JSON request and expects the given status. out, when not
// nil, receives the decoded response.
func (a *adminAPI) call(t *testing.T, method, path string, in any, want int, out any) {
	t.Helper()
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatalf("encode %s %s: %v", method, path, err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, a.baseURL+path, body)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if a.csrf != "" {
		req.Header.Set("X-CSRF-Token", a.csrf)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s %s: %v", method, path, err)
	}
	if resp.StatusCode != want {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, resp.StatusCode, want, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("decode %s %s: %v", method, path, err)
		}
	}
}

// groupHandle finds the admin's handle for the group with this name. A
// group's id never reaches the admin API; its handle stands in for it.
func (a *adminAPI) groupHandle(t *testing.T, name string) string {
	t.Helper()
	var list struct {
		Rows []struct {
			Handle string `json:"handle"`
			Name   string `json:"name"`
		} `json:"rows"`
	}
	a.call(t, http.MethodGet, "/admin/api/groups", nil, http.StatusOK, &list)
	for _, g := range list.Rows {
		if g.Name == name {
			return g.Handle
		}
	}
	t.Fatalf("no group named %q in the admin's list", name)
	return ""
}

// freeAddr returns a loopback address nothing is listening on. There is an
// inherent race between closing the listener and the server binding it; on a
// test machine it is not a real one.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("release the port: %v", err)
	}
	return addr
}

// waitFor polls until cond holds, and fails the test if it never does.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// testContext bounds every integration test, so a hang is a failure with a
// message rather than a stuck CI job.
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// lockedBuffer collects a subprocess's output from its own goroutine.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
