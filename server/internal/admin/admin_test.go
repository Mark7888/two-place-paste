package admin_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/admin"
	"github.com/Mark7888/two-place-paste/server/internal/httpapi"
	"github.com/Mark7888/two-place-paste/server/internal/store"
)

const testPassword = "correct horse battery staple"

// clock is a manually advanced UTC clock. Nothing in these tests sleeps
// (docs/conventions.md §5).
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock {
	return &clock{t: time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)}
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fixture is an admin server behind the real router, so route precedence —
// "GET /ws" and "GET /healthz" against the "GET /{token}" wildcard — is
// exercised exactly as it is in production.
type fixture struct {
	t     *testing.T
	srv   *httptest.Server
	store *store.MemoryStore
	clk   *clock
}

func newFixture(t *testing.T, baseURL string) *fixture {
	t.Helper()

	st := store.NewMemoryStore()
	clk := newClock()
	a, err := admin.New(st, admin.Options{
		PublicBaseURL: baseURL,
		SessionTTL:    2 * time.Hour,
		Password:      testPassword,
		Logger:        slog.New(slog.DiscardHandler),
		Now:           clk.now,
	})
	if err != nil {
		t.Fatalf("admin.New() error = %v", err)
	}

	// Stand-in for the WebSocket transport's registrar: this test only cares
	// that its route keeps winning over the token wildcard.
	wsReg := httpapi.RegistrarFunc(func(mux *http.ServeMux) {
		mux.HandleFunc("GET /ws", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ws"))
		})
	})

	srv := httptest.NewServer(httpapi.New(wsReg, a))
	t.Cleanup(srv.Close)
	return &fixture{t: t, srv: srv, store: st, clk: clk}
}

// client returns an HTTP client that keeps cookies but does not follow
// redirects, so a test can assert on the redirect itself.
func (f *fixture) client() *http.Client {
	f.t.Helper()
	c := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	c.Jar = newJar()
	return c
}

func (f *fixture) do(c *http.Client, req *http.Request) *http.Response {
	f.t.Helper()
	resp, err := c.Do(req)
	if err != nil {
		f.t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	f.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func (f *fixture) get(c *http.Client, path string) *http.Response {
	f.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, f.srv.URL+path, nil)
	if err != nil {
		f.t.Fatalf("new request: %v", err)
	}
	return f.do(c, req)
}

func (f *fixture) postForm(c *http.Client, path string, form url.Values) *http.Response {
	f.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, f.srv.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		f.t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return f.do(c, req)
}

// login signs in and returns the authenticated client plus the session's CSRF
// token, read back out of the token screen.
func (f *fixture) login(c *http.Client) string {
	f.t.Helper()
	resp := f.postForm(c, "/admin/login", url.Values{"password": {testPassword}})
	if resp.StatusCode != http.StatusSeeOther {
		f.t.Fatalf("POST /admin/login status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}
	return f.csrf(c)
}

func (f *fixture) csrf(c *http.Client) string {
	f.t.Helper()
	body := readBody(f.t, f.get(c, "/admin/"))
	const marker = `name="csrf" value="`
	i := strings.Index(body, marker)
	if i < 0 {
		f.t.Fatalf("token screen carries no csrf token")
	}
	rest := body[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		f.t.Fatalf("csrf token is unterminated")
	}
	return rest[:j]
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

// jar is a cookie jar that ignores domains and paths: the fixture talks to one
// host and wants every cookie echoed back so that Path and Secure can be
// asserted on the Set-Cookie header rather than enforced by the jar.
type jar struct {
	mu      sync.Mutex
	cookies map[string]*http.Cookie
}

func newJar() *jar { return &jar{cookies: make(map[string]*http.Cookie)} }

func (j *jar) SetCookies(_ *url.URL, cs []*http.Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, c := range cs {
		if c.MaxAge < 0 || c.Value == "" {
			delete(j.cookies, c.Name)
			continue
		}
		j.cookies[c.Name] = c
	}
}

func (j *jar) Cookies(*url.URL) []*http.Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]*http.Cookie, 0, len(j.cookies))
	for _, c := range j.cookies {
		out = append(out, &http.Cookie{Name: c.Name, Value: c.Value})
	}
	return out
}

func TestNewRejectsHashedPassword(t *testing.T) {
	t.Parallel()

	_, err := admin.New(store.NewMemoryStore(), admin.Options{
		PublicBaseURL: "https://tpp.example.com",
		PasswordHash:  "$2y$10$whatever",
	})
	if err == nil {
		t.Fatal("admin.New() with ADMIN_PASSWORD_HASH error = nil, want ErrHashedPasswordUnsupported")
	}
	if !strings.Contains(err.Error(), "ADMIN_PASSWORD_HASH") {
		t.Errorf("admin.New() error = %v, want it to name ADMIN_PASSWORD_HASH", err)
	}
}

// TestSessionCookieFlags pins the flags SPEC §4.4 and docs/conventions.md §10
// require. Secure follows the public base URL's scheme, so both are asserted.
func TestSessionCookieFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		baseURL    string
		wantSecure bool
	}{
		{name: "https deployment", baseURL: "https://tpp.example.com", wantSecure: true},
		{name: "http development", baseURL: "http://127.0.0.1:8080", wantSecure: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t, tt.baseURL)
			resp := f.postForm(f.client(), "/admin/login", url.Values{"password": {testPassword}})

			var c *http.Cookie
			for _, got := range resp.Cookies() {
				if got.Name == "tpp_admin_session" {
					c = got
				}
			}
			if c == nil {
				t.Fatal("login set no session cookie")
			}
			if !c.HttpOnly {
				t.Error("session cookie HttpOnly = false, want true")
			}
			if c.SameSite != http.SameSiteStrictMode {
				t.Errorf("session cookie SameSite = %v, want %v", c.SameSite, http.SameSiteStrictMode)
			}
			if c.Secure != tt.wantSecure {
				t.Errorf("session cookie Secure = %v, want %v", c.Secure, tt.wantSecure)
			}
			if c.Path != "/admin" {
				t.Errorf("session cookie Path = %q, want %q", c.Path, "/admin")
			}
			if c.MaxAge <= 0 || time.Duration(c.MaxAge)*time.Second > 2*time.Hour {
				t.Errorf("session cookie MaxAge = %d, want a positive value no greater than the 2h session TTL", c.MaxAge)
			}
		})
	}
}

// TestLoginRateLimit proves the publicly reachable login endpoint locks out
// after a burst and recovers as the bucket refills.
func TestLoginRateLimit(t *testing.T) {
	t.Parallel()

	f := newFixture(t, "https://tpp.example.com")
	c := f.client()
	bad := url.Values{"password": {"wrong"}}

	// The per-IP burst is 5; the sixth attempt within the window is refused
	// before the password is looked at.
	for i := range 5 {
		if got := f.postForm(c, "/admin/login", bad).StatusCode; got != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want %d", i+1, got, http.StatusUnauthorized)
		}
	}
	if got := f.postForm(c, "/admin/login", bad).StatusCode; got != http.StatusTooManyRequests {
		t.Fatalf("attempt 6 status = %d, want %d", got, http.StatusTooManyRequests)
	}

	// A correct password does not bypass the lockout.
	if got := f.postForm(c, "/admin/login", url.Values{"password": {testPassword}}).StatusCode; got != http.StatusTooManyRequests {
		t.Fatalf("correct password while locked out status = %d, want %d", got, http.StatusTooManyRequests)
	}

	// One refill interval later exactly one attempt is available again.
	f.clk.advance(12 * time.Second)
	if got := f.postForm(c, "/admin/login", url.Values{"password": {testPassword}}).StatusCode; got != http.StatusSeeOther {
		t.Fatalf("after refill status = %d, want %d", got, http.StatusSeeOther)
	}
}

// TestUnauthenticatedAccess: nothing on the admin surface is reachable without
// a session.
func TestUnauthenticatedAccess(t *testing.T) {
	t.Parallel()

	f := newFixture(t, "https://tpp.example.com")
	c := f.client()

	if resp := f.get(c, "/admin/"); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/admin/login" {
		t.Errorf("GET /admin/ = %d %q, want %d %q",
			resp.StatusCode, resp.Header.Get("Location"), http.StatusSeeOther, "/admin/login")
	}
	if resp := f.postForm(c, "/admin/tokens", url.Values{"name": {"sneaky"}}); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("POST /admin/tokens = %d, want a redirect to the login page", resp.StatusCode)
	}
	if got, err := f.store.ListTokens(context.Background()); err != nil || len(got) != 0 {
		t.Errorf("tokens after unauthenticated POST = %d, want 0 (err = %v)", len(got), err)
	}
	if resp := f.get(c, "/admin/tokens/whatever/qr.png"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET qr.png without a session = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestTokenCreationRequiresCSRF(t *testing.T) {
	t.Parallel()

	f := newFixture(t, "https://tpp.example.com")
	c := f.client()
	f.login(c)

	if resp := f.postForm(c, "/admin/tokens", url.Values{"name": {"no csrf"}}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /admin/tokens without csrf = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	if got, _ := f.store.ListTokens(context.Background()); len(got) != 0 {
		t.Errorf("tokens created without csrf = %d, want 0", len(got))
	}
}

// TestTokenScreen walks the operator's whole job: sign in, mint a token, see it
// listed with its creation URL and a scannable QR code.
func TestTokenScreen(t *testing.T) {
	t.Parallel()

	f := newFixture(t, "https://tpp.example.com")
	c := f.client()
	csrf := f.login(c)

	if resp := f.postForm(c, "/admin/tokens", url.Values{"name": {"Anna — laptop"}, "csrf": {csrf}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /admin/tokens = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	tokens, err := f.store.ListTokens(context.Background())
	if err != nil || len(tokens) != 1 {
		t.Fatalf("ListTokens() = %d tokens, %v; want 1, nil", len(tokens), err)
	}
	tok := tokens[0]

	body := readBody(t, f.get(c, "/admin/"))
	for _, want := range []string{"Anna — laptop", "https://tpp.example.com/" + tok.Value, "/admin/tokens/" + tok.Value + "/qr.png"} {
		if !strings.Contains(body, want) {
			t.Errorf("token screen does not contain %q", want)
		}
	}

	resp := f.get(c, "/admin/tokens/"+tok.Value+"/qr.png")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET qr.png = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got := resp.Header.Get("Content-Type"); got != "image/png" {
		t.Errorf("qr.png Content-Type = %q, want %q", got, "image/png")
	}
	if png := readBody(t, resp); !strings.HasPrefix(png, "\x89PNG\r\n\x1a\n") {
		t.Error("qr.png body is not a PNG")
	}

	// A consumed token has no onboarding value left, so it has no QR code.
	if _, err := f.store.CreateGroup(context.Background(), tok.Value, store.NewDevice{
		PublicKey: []byte("pk"), WrappedGroupKey: []byte("wk"),
	}); err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}
	if got := f.get(c, "/admin/tokens/"+tok.Value+"/qr.png").StatusCode; got != http.StatusNotFound {
		t.Errorf("GET qr.png for a used token = %d, want %d", got, http.StatusNotFound)
	}
}

// TestCreationTokenEndpoint is the SPEC §3.1 guarantee: a GET can never burn a
// token, a POST consumes exactly one, and a second POST gets nothing.
func TestCreationTokenEndpoint(t *testing.T) {
	t.Parallel()

	f := newFixture(t, "https://tpp.example.com")
	tok, err := f.store.CreateToken(context.Background(), "Anna")
	if err != nil {
		t.Fatalf("CreateToken() error = %v", err)
	}
	anon := f.client()

	// Every GET a crawler or link preview could make is a 404, and the token
	// survives all of them.
	for range 3 {
		if got := f.get(anon, "/"+tok.Value).StatusCode; got != http.StatusNotFound {
			t.Fatalf("GET /<token> = %d, want %d", got, http.StatusNotFound)
		}
	}
	if after, err := f.store.GetToken(context.Background(), tok.Value); err != nil || after.Used {
		t.Fatalf("token after GETs: used = %v, err = %v; want used = false", after.Used, err)
	}

	body := fmt.Sprintf(`{"device_name":"laptop","device_public_key":%q,"wrapped_group_key":%q}`,
		base64.StdEncoding.EncodeToString([]byte("public key")),
		base64.StdEncoding.EncodeToString([]byte("wrapped group key")))

	resp := f.postJSON(anon, "/"+tok.Value, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /<token> = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var created struct {
		GroupID  string `json:"group_id"`
		DeviceID string `json:"device_id"`
		Epoch    uint64 `json:"epoch"`
	}
	if err := json.Unmarshal([]byte(readBody(t, resp)), &created); err != nil {
		t.Fatalf("decode creation response: %v", err)
	}
	if created.GroupID != tok.Value {
		t.Errorf("group_id = %q, want the token value %q (SPEC §3.1: the token becomes the group id)", created.GroupID, tok.Value)
	}
	if created.DeviceID == "" || created.Epoch != 1 {
		t.Errorf("creation response device_id = %q, epoch = %d; want a device id and epoch 1", created.DeviceID, created.Epoch)
	}

	// One token, one group.
	if got := f.postJSON(anon, "/"+tok.Value, body).StatusCode; got != http.StatusConflict {
		t.Errorf("second POST /<token> = %d, want %d", got, http.StatusConflict)
	}
	// And the group it made is not reachable by GET either.
	if got := f.get(anon, "/"+tok.Value).StatusCode; got != http.StatusNotFound {
		t.Errorf("GET /<token> after creation = %d, want %d", got, http.StatusNotFound)
	}
}

func TestCreationTokenEndpointRejections(t *testing.T) {
	t.Parallel()

	f := newFixture(t, "https://tpp.example.com")
	c := f.client()

	tests := []struct {
		name string
		path string
		body string
		want int
	}{
		{
			name: "unknown token is indistinguishable from an unknown path",
			path: "/does-not-exist",
			body: `{"device_public_key":"cGs=","wrapped_group_key":"d2s="}`,
			want: http.StatusNotFound,
		},
		{
			name: "missing keys",
			path: "/does-not-exist",
			body: `{"device_name":"laptop"}`,
			want: http.StatusBadRequest,
		},
		{
			name: "malformed json",
			path: "/does-not-exist",
			body: `not json`,
			want: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := f.postJSON(c, tt.path, tt.body).StatusCode; got != tt.want {
				t.Errorf("POST %s = %d, want %d", tt.path, got, tt.want)
			}
		})
	}
}

// TestWildcardDoesNotShadowOtherRoutes: the token wildcard is a single-segment
// pattern registered alongside the transport's and the router's own routes.
func TestWildcardDoesNotShadowOtherRoutes(t *testing.T) {
	t.Parallel()

	f := newFixture(t, "https://tpp.example.com")
	c := f.client()

	for _, path := range []string{"/healthz", "/ws"} {
		if got := f.get(c, path).StatusCode; got != http.StatusOK {
			t.Errorf("GET %s = %d, want %d", path, got, http.StatusOK)
		}
	}
}

func (f *fixture) postJSON(c *http.Client, path, body string) *http.Response {
	f.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, f.srv.URL+path, strings.NewReader(body))
	if err != nil {
		f.t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return f.do(c, req)
}
