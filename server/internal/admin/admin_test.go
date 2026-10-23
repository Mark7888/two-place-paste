package admin_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/admin"
	"github.com/Mark7888/two-place-paste/server/internal/httpapi"
	"github.com/Mark7888/two-place-paste/server/internal/store"
)

const testPassword = "correct horse battery staple"

// testUI stands in for the React build, so these tests neither need nor see
// whatever `npm run build` last left in server/web/admin/dist.
var testUI = fstest.MapFS{
	"index.html":            {Data: []byte(`<!doctype html><title>admin</title><div id="root"></div><script type="module" src="/admin/assets/app.js"></script>`)},
	"assets/app.js":         {Data: []byte(`console.log("app")`)},
	"assets/app.css":        {Data: []byte(`body{}`)},
	"favicon.svg":           {Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)},
	"favicon.ico":           {Data: []byte{0, 0, 1, 0}},
	"apple-touch-icon.png":  {Data: []byte("\x89PNG\r\n\x1a\n")},
	"manifest.webmanifest":  {Data: []byte(`{"name":"TwoPlacePaste admin"}`)},
	"sw.js":                 {Data: []byte(`self.addEventListener("fetch", () => {})`)},
	"icon-192.png":          {Data: []byte("\x89PNG\r\n\x1a\n")},
	"icon-512.png":          {Data: []byte("\x89PNG\r\n\x1a\n")},
	"icon-maskable-512.png": {Data: []byte("\x89PNG\r\n\x1a\n")},
}

// clock is a manually advanced UTC clock. Nothing in these tests sleeps
// (docs/internal/conventions.md §5).
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
	admin *admin.Server
}

func newFixture(t *testing.T, baseURL string) *fixture {
	t.Helper()
	return newFixtureWithDashboard(t, baseURL, admin.Dashboard{})
}

func newFixtureWithDashboard(t *testing.T, baseURL string, dash admin.Dashboard) *fixture {
	t.Helper()
	return newFixtureWithOptions(t, baseURL, func(o *admin.Options) { o.Dashboard = dash })
}

// newFixtureWithOptions lets a test wire the optional parts of the admin
// server — the transport, the purger, the ledger — before it is built.
func newFixtureWithOptions(t *testing.T, baseURL string, configure func(*admin.Options)) *fixture {
	t.Helper()

	st := store.NewMemoryStore()
	clk := newClock()
	opts := admin.Options{
		PublicBaseURL: baseURL,
		SessionTTL:    2 * time.Hour,
		Password:      testPassword,
		Logger:        slog.New(slog.DiscardHandler),
		Now:           clk.now,
		Assets:        testUI,
	}
	if configure != nil {
		configure(&opts)
	}
	a, err := admin.New(st, opts)
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
	return &fixture{t: t, srv: srv, store: st, clk: clk, admin: a}
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

// call makes an admin API request the way the app does: a JSON body, and the
// session's CSRF token in its header when csrf is not empty.
func (f *fixture) call(c *http.Client, csrf, method, path string, body any) *http.Response {
	f.t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			f.t.Fatalf("encode request body: %v", err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, f.srv.URL+path, r)
	if err != nil {
		f.t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	return f.do(c, req)
}

// tryLogin posts a password to the sign-in endpoint.
func (f *fixture) tryLogin(c *http.Client, password string) *http.Response {
	f.t.Helper()
	return f.call(c, "", http.MethodPost, "/admin/api/login", map[string]string{"password": password})
}

// login signs in and returns the session's CSRF token.
func (f *fixture) login(c *http.Client) string {
	f.t.Helper()
	resp := f.tryLogin(c, testPassword)
	if resp.StatusCode != http.StatusOK {
		f.t.Fatalf("POST /admin/api/login status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	got := readJSON[struct {
		CSRF string `json:"csrf"`
	}](f.t, resp)
	if got.CSRF == "" {
		f.t.Fatal("login returned no csrf token")
	}
	return got.CSRF
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

// readJSON decodes a response body.
func readJSON[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("decode %s %s response: %v", resp.Request.Method, resp.Request.URL.Path, err)
	}
	return v
}

// apiError is the message of a refused API request.
func apiError(t *testing.T, resp *http.Response) string {
	t.Helper()
	return readJSON[struct {
		Error string `json:"error"`
	}](t, resp).Error
}

func wantStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		t.Fatalf("%s %s = %d, want %d", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, want)
	}
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

// TestSessionCookieFlags pins the flags SPEC §4.4 and docs/internal/conventions.md §10
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
			resp := f.tryLogin(f.client(), testPassword)

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
			// The cookie is the session; the body hands out only the CSRF
			// token, never the session id.
			if body := readBody(t, resp); strings.Contains(body, c.Value) {
				t.Error("the login response body carries the session id")
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

	// The per-IP burst is 5; the sixth attempt within the window is refused
	// before the password is looked at.
	for i := range 5 {
		resp := f.tryLogin(c, "wrong")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want %d", i+1, resp.StatusCode, http.StatusUnauthorized)
		}
		if got := apiError(t, resp); got != "Incorrect password." {
			t.Errorf("attempt %d error = %q, want %q", i+1, got, "Incorrect password.")
		}
	}
	resp := f.tryLogin(c, "wrong")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("attempt 6 status = %d, want %d", resp.StatusCode, http.StatusTooManyRequests)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("a rate-limited sign-in has no Retry-After header")
	}

	// A correct password does not bypass the lockout.
	if got := f.tryLogin(c, testPassword).StatusCode; got != http.StatusTooManyRequests {
		t.Fatalf("correct password while locked out status = %d, want %d", got, http.StatusTooManyRequests)
	}

	// One refill interval later exactly one attempt is available again.
	f.clk.advance(12 * time.Second)
	if got := f.tryLogin(c, testPassword).StatusCode; got != http.StatusOK {
		t.Fatalf("after refill status = %d, want %d", got, http.StatusOK)
	}
}

func TestLoginRejectsUnreadableBody(t *testing.T) {
	t.Parallel()

	f := newFixture(t, "https://tpp.example.com")
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, f.srv.URL+"/admin/api/login", strings.NewReader("password=x"))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if got := f.do(f.client(), req).StatusCode; got != http.StatusBadRequest {
		t.Errorf("POST /admin/api/login with a form body = %d, want %d", got, http.StatusBadRequest)
	}
}

// TestUnauthenticatedAccess: nothing on the admin surface is reachable without
// a session.
func TestUnauthenticatedAccess(t *testing.T) {
	t.Parallel()

	f := newFixture(t, "https://tpp.example.com")
	c := f.client()

	for _, path := range []string{"/admin/", "/admin/tokens", "/admin/groups/abc", "/admin/settings"} {
		if resp := f.get(c, path); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/admin/login" {
			t.Errorf("GET %s = %d %q, want %d %q",
				path, resp.StatusCode, resp.Header.Get("Location"), http.StatusSeeOther, "/admin/login")
		}
	}
	for _, path := range []string{"/admin/api/session", "/admin/api/dashboard", "/admin/api/tokens", "/admin/api/audit"} {
		if got := f.get(c, path).StatusCode; got != http.StatusUnauthorized {
			t.Errorf("GET %s = %d, want %d", path, got, http.StatusUnauthorized)
		}
	}
	if got := f.call(c, "", http.MethodPost, "/admin/api/tokens", map[string]string{"name": "sneaky"}).StatusCode; got != http.StatusUnauthorized {
		t.Errorf("POST /admin/api/tokens = %d, want %d", got, http.StatusUnauthorized)
	}
	if got, err := f.store.ListTokens(context.Background()); err != nil || len(got) != 0 {
		t.Errorf("tokens after unauthenticated POST = %d, want 0 (err = %v)", len(got), err)
	}
	if resp := f.get(c, "/admin/tokens/whatever/qr.png"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET qr.png without a session = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

// TestSessionLifecycle: the session endpoint answers with the CSRF token the
// login handed out, the sign-in screen sends a signed-in admin on, and
// logging out ends the session on the server.
func TestSessionLifecycle(t *testing.T) {
	t.Parallel()

	f := newFixture(t, "https://tpp.example.com")
	c := f.client()

	if resp := f.get(c, "/admin/login"); resp.StatusCode != http.StatusOK {
		t.Errorf("GET /admin/login signed out = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	csrf := f.login(c)

	resp := f.get(c, "/admin/api/session")
	wantStatus(t, resp, http.StatusOK)
	if got := readJSON[struct {
		CSRF string `json:"csrf"`
	}](t, resp).CSRF; got != csrf {
		t.Errorf("session csrf = %q, want the login's %q", got, csrf)
	}
	if resp := f.get(c, "/admin/login"); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/admin/" {
		t.Errorf("GET /admin/login signed in = %d %q, want a redirect to /admin/", resp.StatusCode, resp.Header.Get("Location"))
	}

	// Logging out needs the CSRF header, so another site cannot do it.
	wantStatus(t, f.call(c, "", http.MethodPost, "/admin/api/logout", nil), http.StatusForbidden)
	wantStatus(t, f.get(c, "/admin/api/session"), http.StatusOK)

	wantStatus(t, f.call(c, csrf, http.MethodPost, "/admin/api/logout", nil), http.StatusNoContent)
	wantStatus(t, f.get(c, "/admin/api/session"), http.StatusUnauthorized)
}

// TestSessionExpires: a session is refused once its TTL has passed.
func TestSessionExpires(t *testing.T) {
	t.Parallel()

	f := newFixture(t, "https://tpp.example.com")
	c := f.client()
	f.login(c)
	f.clk.advance(2*time.Hour + time.Second)
	wantStatus(t, f.get(c, "/admin/api/dashboard"), http.StatusUnauthorized)
}

// TestChangesRequireCSRF: every request that changes something is refused
// without the session's CSRF header, or with someone else's.
func TestChangesRequireCSRF(t *testing.T) {
	t.Parallel()

	f := newFixture(t, "https://tpp.example.com")
	c := f.client()
	f.login(c)
	other := f.login(f.client())

	for _, csrf := range []string{"", other} {
		resp := f.call(c, csrf, http.MethodPost, "/admin/api/tokens", map[string]string{"name": "no csrf"})
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("POST /admin/api/tokens with csrf %q = %d, want %d", csrf, resp.StatusCode, http.StatusForbidden)
		}
	}
	if got, _ := f.store.ListTokens(context.Background()); len(got) != 0 {
		t.Errorf("tokens created without csrf = %d, want 0", len(got))
	}
}

// TestTokenScreen walks the operator's whole job: sign in, mint a token, see it
// listed, and get its creation URL and a scannable QR code on request.
func TestTokenScreen(t *testing.T) {
	t.Parallel()

	f := newFixture(t, "https://tpp.example.com")
	c := f.client()
	csrf := f.login(c)

	resp := f.call(c, csrf, http.MethodPost, "/admin/api/tokens", map[string]string{"name": "Anna — laptop"})
	wantStatus(t, resp, http.StatusCreated)
	created := readJSON[tokenRow](t, resp)

	tokens, err := f.store.ListTokens(context.Background())
	if err != nil || len(tokens) != 1 {
		t.Fatalf("ListTokens() = %d tokens, %v; want 1, nil", len(tokens), err)
	}
	tok := tokens[0]
	handle := admin.HandleOf(tok.Value)
	// The new token comes back with its URL, so the screen can show its code
	// at once without the value ever being put in a URL.
	if created.Handle != handle || created.URL != "https://tpp.example.com/"+tok.Value || created.Status != "unused" {
		t.Errorf("created token = %+v, want handle %q, its URL and status unused", created, handle)
	}

	// The list names the token by its handle and holds no credential.
	resp = f.get(c, "/admin/api/tokens")
	body := readBody(t, resp)
	if strings.Contains(body, tok.Value) {
		t.Errorf("the token list holds a token value: %s", body)
	}
	var list tokenList
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("decode token list: %v", err)
	}
	if len(list.Tokens) != 1 || list.Tokens[0].Name != "Anna — laptop" || list.Tokens[0].Handle != handle || list.Tokens[0].URL != "" {
		t.Errorf("token list = %+v, want the new token by handle, without its URL", list.Tokens)
	}

	// The URL is handed out on request, one token at a time.
	resp = f.get(c, "/admin/api/tokens/"+handle+"/link")
	wantStatus(t, resp, http.StatusOK)
	if got := readJSON[map[string]string](t, resp)["url"]; got != created.URL {
		t.Errorf("link = %q, want %q", got, created.URL)
	}
	wantStatus(t, f.get(f.client(), "/admin/api/tokens/"+handle+"/link"), http.StatusUnauthorized)
	wantStatus(t, f.get(c, "/admin/api/tokens/nope/link"), http.StatusNotFound)

	resp = f.get(c, "/admin/tokens/"+handle+"/qr.png")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET qr.png = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got := resp.Header.Get("Content-Type"); got != "image/png" {
		t.Errorf("qr.png Content-Type = %q, want %q", got, "image/png")
	}
	if png := readBody(t, resp); !strings.HasPrefix(png, "\x89PNG\r\n\x1a\n") {
		t.Error("qr.png body is not a PNG")
	}
	// The raw value does not work as a handle.
	if got := f.get(c, "/admin/tokens/"+tok.Value+"/qr.png").StatusCode; got != http.StatusNotFound {
		t.Errorf("GET qr.png by token value = %d, want %d", got, http.StatusNotFound)
	}

	// A consumed token has no onboarding value left: no QR code, no link.
	if _, err := f.store.CreateGroup(context.Background(), tok.Value, store.NewDevice{
		PublicKey: []byte("pk"), WrappedGroupKey: []byte("wk"),
	}); err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}
	if got := f.get(c, "/admin/tokens/"+handle+"/qr.png").StatusCode; got != http.StatusNotFound {
		t.Errorf("GET qr.png for a used token = %d, want %d", got, http.StatusNotFound)
	}
	wantStatus(t, f.get(c, "/admin/api/tokens/"+handle+"/link"), http.StatusNotFound)
	list = readJSON[tokenList](t, f.get(c, "/admin/api/tokens"))
	if got := list.Tokens[0]; got.Status != "used" || got.GroupHandle != handle {
		t.Errorf("used token = %+v, want status used and its group's handle", got)
	}
}

// tokenRow and tokenList are the token API's JSON, as a test reads it.
type tokenRow struct {
	Handle      string     `json:"handle"`
	Name        string     `json:"name"`
	Note        string     `json:"note"`
	Tag         string     `json:"tag"`
	Status      string     `json:"status"`
	ExpiresAt   *time.Time `json:"expires_at"`
	URL         string     `json:"url"`
	GroupHandle string     `json:"group_handle"`
}

type tokenList struct {
	Tokens []tokenRow `json:"rows"`
	Total  int        `json:"total"`
	Tags   []string   `json:"tags"`
}

// TestRevokeToken: revoking an unused token removes it and its creation URL
// stops working; a used token is refused and left alone.
func TestRevokeToken(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	f := newFixture(t, "https://tpp.example.com")
	c := f.client()
	csrf := f.login(c)

	unused, err := f.store.CreateToken(ctx, "spare")
	if err != nil {
		t.Fatalf("CreateToken() error = %v", err)
	}
	revokePath := "/admin/api/tokens/" + admin.HandleOf(unused.Value)

	wantStatus(t, f.call(c, "", http.MethodDelete, revokePath, nil), http.StatusForbidden)
	if _, err := f.store.GetToken(ctx, unused.Value); err != nil {
		t.Fatalf("token revoked without csrf: GetToken() error = %v", err)
	}
	wantStatus(t, f.call(f.client(), csrf, http.MethodDelete, revokePath, nil), http.StatusUnauthorized)

	wantStatus(t, f.call(c, csrf, http.MethodDelete, revokePath, nil), http.StatusNoContent)
	if _, err := f.store.GetToken(ctx, unused.Value); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetToken(revoked) error = %v, want %v", err, store.ErrNotFound)
	}
	resp := f.postJSON(f.client(), "/"+unused.Value, `{"device_public_key":"cGs=","wrapped_group_key":"d2s="}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST of a revoked creation URL = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
	resp = f.call(c, csrf, http.MethodDelete, revokePath, nil)
	wantStatus(t, resp, http.StatusNotFound)
	if got := apiError(t, resp); got != "That token no longer exists." {
		t.Errorf("second revoke error = %q", got)
	}

	used, err := f.store.CreateToken(ctx, "spent")
	if err != nil {
		t.Fatalf("CreateToken() error = %v", err)
	}
	if _, err := f.store.ConsumeToken(ctx, used.Value); err != nil {
		t.Fatalf("ConsumeToken() error = %v", err)
	}
	wantStatus(t, f.call(c, csrf, http.MethodDelete, "/admin/api/tokens/"+admin.HandleOf(used.Value), nil), http.StatusConflict)
	if got, err := f.store.GetToken(ctx, used.Value); err != nil || !got.Used {
		t.Errorf("used token after a refused revoke = {Used:%v}, %v; want it untouched", got.Used, err)
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

// TestAppShell covers how the React app is served: one page for every
// screen, behind the policy that allows only this origin's own files, and
// the bundle and icons it loads.
func TestAppShell(t *testing.T) {
	t.Parallel()

	f := newFixture(t, "https://tpp.example.com")
	c := f.client()
	index := string(testUI["index.html"].Data)

	// The sign-in screen is the one screen served without a session.
	resp := f.get(c, "/admin/login")
	wantStatus(t, resp, http.StatusOK)
	if got := readBody(t, resp); got != index {
		t.Errorf("GET /admin/login served %q, want the app's index.html", got)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "script-src 'self'", "style-src 'self'", "connect-src 'self'", "manifest-src 'self'", "worker-src 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("Content-Security-Policy %q does not contain %q", csp, want)
		}
	}
	// Whatever else changes, inline script must never become allowed: the
	// whole app is one bundle from this origin precisely so it does not have
	// to be.
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("Content-Security-Policy %q relaxes script execution", csp)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("index Cache-Control = %q, want no-store", got)
	}

	f.login(c)
	for _, path := range []string{"/admin/", "/admin/groups", "/admin/groups/abc", "/admin/groups/abc/purge", "/admin/groups/abc/delete", "/admin/tags", "/admin/tokens", "/admin/audit", "/admin/settings"} {
		resp := f.get(c, path)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want %d", path, resp.StatusCode, http.StatusOK)
			continue
		}
		if got := readBody(t, resp); got != index {
			t.Errorf("GET %s did not serve the app", path)
		}
	}
	// Only the app's screens exist: anything else under /admin/ is a 404, as
	// is a JSON path no endpoint claims.
	for _, path := range []string{"/admin/nope", "/admin/groups/abc/nope", "/admin/index.html", "/admin/assets/", "/admin/assets/missing.js", "/admin/api/nope"} {
		if got := f.get(c, path).StatusCode; got != http.StatusNotFound {
			t.Errorf("GET %s = %d, want %d", path, got, http.StatusNotFound)
		}
	}
	if got := f.get(c, "/admin").Header.Get("Location"); got != "/admin/" {
		t.Errorf("GET /admin redirects to %q, want /admin/", got)
	}

	// The bundle and the icons are public: the sign-in screen loads them
	// before a session exists.
	anon := f.client()
	for _, tc := range []struct{ path, contentType string }{
		{"/admin/assets/app.js", "text/javascript; charset=utf-8"},
		{"/admin/assets/app.css", "text/css; charset=utf-8"},
		{"/admin/favicon.svg", "image/svg+xml"},
		{"/admin/favicon.ico", "image/x-icon"},
		{"/admin/apple-touch-icon.png", "image/png"},
		// The installable app's files, also public: a browser fetches the
		// manifest and the worker without the page's cookies in mind.
		{"/admin/manifest.webmanifest", "application/manifest+json"},
		{"/admin/sw.js", "text/javascript; charset=utf-8"},
		{"/admin/icon-192.png", "image/png"},
		{"/admin/icon-512.png", "image/png"},
		{"/admin/icon-maskable-512.png", "image/png"},
	} {
		resp := f.get(anon, tc.path)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want %d", tc.path, resp.StatusCode, http.StatusOK)
			continue
		}
		if got := resp.Header.Get("Content-Type"); got != tc.contentType {
			t.Errorf("GET %s Content-Type = %q, want %q", tc.path, got, tc.contentType)
		}
		if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("GET %s X-Content-Type-Options = %q, want nosniff", tc.path, got)
		}
	}
}

// TestPlaceholderWithoutUIBuild: a binary built without the UI still serves
// every screen, with a page that says what is missing and keeps the AGPL
// source link every page of the relay carries.
func TestPlaceholderWithoutUIBuild(t *testing.T) {
	t.Parallel()

	f := newFixtureWithOptions(t, "https://tpp.example.com", func(o *admin.Options) { o.Assets = fstest.MapFS{} })
	c := f.client()

	resp := f.get(c, "/admin/login")
	wantStatus(t, resp, http.StatusOK)
	body := readBody(t, resp)
	for _, want := range []string{"The admin UI is not in this binary", "server/ui", `href="/source"`} {
		if !strings.Contains(body, want) {
			t.Errorf("placeholder page does not contain %q", want)
		}
	}
	// The API works regardless.
	f.login(c)
	wantStatus(t, f.get(c, "/admin/api/dashboard"), http.StatusOK)
}

// TestRefusedRequestLogsNoToken: a request refused for its CSRF header is
// logged by route, never by path, because a path can carry a token value.
func TestRefusedRequestLogsNoToken(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	var mu sync.Mutex
	f := newFixtureWithOptions(t, "https://tpp.example.com", func(o *admin.Options) {
		o.Logger = slog.New(slog.NewTextHandler(&lockedWriter{w: &logs, mu: &mu}, nil))
	})
	c := f.client()
	f.login(c)
	tok, err := f.store.CreateToken(context.Background(), "spare")
	if err != nil {
		t.Fatalf("CreateToken() error = %v", err)
	}
	wantStatus(t, f.call(c, "", http.MethodDelete, "/admin/api/tokens/"+admin.HandleOf(tok.Value), nil), http.StatusForbidden)

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(logs.String(), "bad csrf token") {
		t.Fatalf("the refusal was not logged: %s", logs.String())
	}
	if strings.Contains(logs.String(), tok.Value) {
		t.Errorf("the log holds the token value: %s", logs.String())
	}
}

type lockedWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
