package tppclient_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
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
// server's own Redis-backed tests use.
func startRelay(t *testing.T) *relay {
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

// creationURL signs in to the admin UI, generates a creation token and returns
// the URL an operator would hand a user (SPEC §3.1, §4.4).
func (r *relay) creationURL(t *testing.T, name string) string {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	c := &http.Client{
		Jar:     jar,
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := c.PostForm(r.baseURL+"/admin/login", url.Values{"password": {r.adminPass}})
	if err != nil {
		t.Fatalf("admin login: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("admin login = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	body := r.get(t, c, "/admin/")
	csrf := between(t, body, `name="csrf" value="`, `"`)

	resp, err = c.PostForm(r.baseURL+"/admin/tokens", url.Values{"name": {name}, "csrf": {csrf}})
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create token = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	// The token screen renders each unused token's QR image at a URL that
	// carries the token value, which is the only place the value appears in
	// the markup.
	body = r.get(t, c, "/admin/")
	value := between(t, body, "/admin/tokens/", "/qr.png")
	return r.baseURL + "/" + value
}

func (r *relay) get(t *testing.T, c *http.Client, path string) string {
	t.Helper()
	resp, err := c.Get(r.baseURL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", path, resp.StatusCode)
	}
	return string(body)
}

func between(t *testing.T, haystack, prefix, suffix string) string {
	t.Helper()
	i := strings.Index(haystack, prefix)
	if i < 0 {
		t.Fatalf("no %q in the admin page", prefix)
	}
	rest := haystack[i+len(prefix):]
	j := strings.Index(rest, suffix)
	if j < 0 {
		t.Fatalf("%q is not terminated by %q", prefix, suffix)
	}
	return rest[:j]
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
