package admin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/admin"
	"github.com/Mark7888/two-place-paste/server/internal/admin/apiv1"
	"github.com/Mark7888/two-place-paste/server/internal/apikey"
	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// newKey creates an API key with the given scopes and returns it.
func (f *fixture) newKey(scopes ...string) string {
	f.t.Helper()
	key, _, err := apikey.Create(context.Background(), f.store, apikey.New{
		Name: "test key", Scopes: scopes, ExpiryDays: 30, CreatedBy: "admin",
	}, f.clk.now())
	if err != nil {
		f.t.Fatalf("apikey.Create() error = %v", err)
	}
	return key
}

// api makes a REST API request with key (none when empty) from client
// address ip (the loopback test client's own when empty).
func (f *fixture) api(key, ip, method, path string, body any) *http.Response {
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
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if ip != "" {
		req.Header.Set("X-Forwarded-For", ip)
	}
	return f.do(http.DefaultClient, req)
}

type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// wantProblem checks that resp is a problem with the given status and code.
func wantProblem(t *testing.T, resp *http.Response, status int, code string) problem {
	t.Helper()
	if resp.StatusCode != status {
		t.Fatalf("%s %s = %d, want %d", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, status)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
	p := readJSON[problem](t, resp)
	if p.Code != code || p.Status != status || p.Type != "about:blank" || p.Detail == "" {
		t.Errorf("problem = %+v, want code %q status %d with a detail", p, code, status)
	}
	return p
}

func TestAPIAuthentication(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "https://tpp.example.com")

	expired := f.newKey("read") // made with a 30-day lifetime
	f.clk.advance(31 * 24 * time.Hour)
	good := f.newKey("read")

	revoked := f.newKey("read")
	id, _ := apikey.Parse(revoked)
	if err := f.store.DeleteAPIKey(context.Background(), id); err != nil {
		t.Fatalf("DeleteAPIKey() error = %v", err)
	}
	wrongSecret := good[:len(good)-4] + "aaaa"
	if wrongSecret == good {
		wrongSecret = good[:len(good)-4] + "bbbb"
	}

	tests := []struct {
		name   string
		header string
	}{
		{"no header", ""},
		{"basic scheme", "Basic " + good},
		{"no scheme", good},
		{"malformed key", "Bearer tpp_ak_nope"},
		{"unknown id", "Bearer tpp_ak_zzzzzzzz_" + strings.Repeat("a", 52)},
		{"wrong secret", "Bearer " + wrongSecret},
		{"expired", "Bearer " + expired},
		{"revoked", "Bearer " + revoked},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, f.srv.URL+"/api/v1/dashboard", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			// Each case from its own address, so the failure limit is not
			// what is being tested.
			req.Header.Set("X-Forwarded-For", "198.51.100."+strconv.Itoa(i+1))
			resp := f.do(http.DefaultClient, req)
			wantProblem(t, resp, http.StatusUnauthorized, "unauthorized")
			if got := resp.Header.Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer") {
				t.Errorf("WWW-Authenticate = %q, want a Bearer challenge", got)
			}
		})
	}

	t.Run("valid", func(t *testing.T) {
		resp := f.api(good, "198.51.100.200", http.MethodGet, "/api/v1/dashboard", nil)
		wantStatus(t, resp, http.StatusOK)
		if got := resp.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", got)
		}
		_ = readJSON[apiv1.Dashboard](t, resp)
	})
	t.Run("lower-case scheme", func(t *testing.T) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, f.srv.URL+"/api/v1/dashboard", nil)
		req.Header.Set("Authorization", "bearer "+good)
		wantStatus(t, f.do(http.DefaultClient, req), http.StatusOK)
	})
}

func TestAPIIgnoresTheAdminSession(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "https://tpp.example.com")
	c := f.client()
	csrf := f.login(c)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, f.srv.URL+"/api/v1/dashboard", nil)
	req.Header.Set("X-CSRF-Token", csrf)
	wantProblem(t, f.do(c, req), http.StatusUnauthorized, "unauthorized")
}

func TestAPIScopes(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "https://tpp.example.com")
	read, tokens, adm := f.newKey("read"), f.newKey("tokens"), f.newKey("admin")

	tests := []struct {
		method, path string
		body         any
		read         int
		tokens       int
		admin        int
	}{
		{http.MethodGet, "/api/v1/groups", nil, 200, 403, 200},
		{http.MethodGet, "/api/v1/settings", nil, 200, 403, 200},
		{http.MethodGet, "/api/v1/audit", nil, 200, 403, 200},
		{http.MethodGet, "/api/v1/tokens", nil, 403, 200, 200},
		{http.MethodPatch, "/api/v1/settings", map[string]int{"stale_after_days": 30}, 403, 403, 200},
		{http.MethodPost, "/api/v1/tags", map[string]string{"name": "x"}, 403, 403, 201},
	}
	for _, tt := range tests {
		for _, k := range []struct {
			name, key string
			want      int
		}{{"read", read, tt.read}, {"tokens", tokens, tt.tokens}, {"admin", adm, tt.admin}} {
			resp := f.api(k.key, "", tt.method, tt.path, tt.body)
			if resp.StatusCode != k.want {
				t.Errorf("%s %s with a %s key = %d, want %d", tt.method, tt.path, k.name, resp.StatusCode, k.want)
			}
			if k.want == http.StatusForbidden {
				wantProblem(t, resp, http.StatusForbidden, "forbidden")
			}
		}
	}
}

func TestAPIDescriptionIsPublic(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "https://tpp.example.com")

	resp := f.api("", "", http.MethodGet, "/api/v1/openapi.yaml", nil)
	wantStatus(t, resp, http.StatusOK)
	if got := readBody(t, resp); got != string(apiv1.SpecYAML) {
		t.Error("openapi.yaml is not the embedded description")
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/yaml") {
		t.Errorf("openapi.yaml Content-Type = %q", ct)
	}

	resp = f.api("", "", http.MethodGet, "/api/v1/openapi.json", nil)
	wantStatus(t, resp, http.StatusOK)
	doc := readJSON[map[string]any](t, resp)
	if doc["openapi"] != "3.1.0" {
		t.Errorf("openapi.json openapi = %v, want 3.1.0", doc["openapi"])
	}
	info, _ := doc["info"].(map[string]any)
	if v, _ := info["version"].(string); !strings.HasPrefix(v, "1.") {
		t.Errorf("info.version = %q, want the API version 1.x, not the relay build", v)
	}
}

func TestAPIUnknownPath(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "https://tpp.example.com")

	// Without a key nothing is revealed, not even whether the path exists.
	wantProblem(t, f.api("", "", http.MethodGet, "/api/v1/nope", nil), http.StatusUnauthorized, "unauthorized")
	wantProblem(t, f.api(f.newKey("admin"), "", http.MethodGet, "/api/v1/nope", nil), http.StatusNotFound, "not_found")
}

func TestAPIRateLimit(t *testing.T) {
	t.Parallel()
	f := newFixtureWithOptions(t, "https://tpp.example.com", func(o *admin.Options) {
		o.APIRates = admin.APIRates{ReadPerSecond: 1, ReadBurst: 2, WritePerSecond: 1, WriteBurst: 1}
	})
	key := f.newKey("admin")

	for i, wantRemaining := range []string{"1", "0"} {
		resp := f.api(key, "", http.MethodGet, "/api/v1/settings", nil)
		wantStatus(t, resp, http.StatusOK)
		if got := resp.Header.Get("RateLimit-Remaining"); got != wantRemaining {
			t.Errorf("request %d RateLimit-Remaining = %q, want %q", i+1, got, wantRemaining)
		}
		if got := resp.Header.Get("RateLimit-Limit"); got != "2" {
			t.Errorf("RateLimit-Limit = %q, want 2", got)
		}
	}
	resp := f.api(key, "", http.MethodGet, "/api/v1/settings", nil)
	wantProblem(t, resp, http.StatusTooManyRequests, "rate_limited")
	if got := resp.Header.Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q, want 1", got)
	}

	// Writes have their own budget.
	wantStatus(t, f.api(key, "", http.MethodPatch, "/api/v1/settings", map[string]int{"stale_after_days": 30}), http.StatusOK)
	wantProblem(t, f.api(key, "", http.MethodPatch, "/api/v1/settings", map[string]int{"stale_after_days": 30}), http.StatusTooManyRequests, "rate_limited")

	// Another key is not affected.
	wantStatus(t, f.api(f.newKey("read"), "", http.MethodGet, "/api/v1/settings", nil), http.StatusOK)

	f.clk.advance(time.Second)
	wantStatus(t, f.api(key, "", http.MethodGet, "/api/v1/settings", nil), http.StatusOK)
}

func TestAPIFailedAuthenticationIsLimited(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "https://tpp.example.com")
	good := f.newKey("read")
	const attacker = "203.0.113.9"

	for range 10 {
		wantProblem(t, f.api("tpp_ak_nope", attacker, http.MethodGet, "/api/v1/settings", nil), http.StatusUnauthorized, "unauthorized")
	}
	wantProblem(t, f.api("tpp_ak_nope", attacker, http.MethodGet, "/api/v1/settings", nil), http.StatusTooManyRequests, "rate_limited")
	// Locked out: not even a good key is looked at from that address.
	wantProblem(t, f.api(good, attacker, http.MethodGet, "/api/v1/settings", nil), http.StatusTooManyRequests, "rate_limited")
	// Others are not.
	wantStatus(t, f.api(good, "203.0.113.10", http.MethodGet, "/api/v1/settings", nil), http.StatusOK)

	entries, err := f.store.ListAudit(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListAudit() error = %v", err)
	}
	if len(entries) == 0 || entries[0].Action != "apikey.limited" || entries[0].IP != attacker {
		t.Errorf("audit log = %+v, want an apikey.limited entry from %s", entries, attacker)
	}

	f.clk.advance(time.Minute)
	wantStatus(t, f.api(good, attacker, http.MethodGet, "/api/v1/settings", nil), http.StatusOK)
}

func TestAPIRecordsLastUse(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "https://tpp.example.com")
	key := f.newKey("read")
	id, _ := apikey.Parse(key)
	lastUsed := func() time.Time {
		k, err := f.store.GetAPIKey(context.Background(), id)
		if err != nil {
			t.Fatalf("GetAPIKey() error = %v", err)
		}
		return k.LastUsedAt
	}

	first := f.clk.now()
	wantStatus(t, f.api(key, "192.0.2.1", http.MethodGet, "/api/v1/settings", nil), http.StatusOK)
	if got := lastUsed(); !got.Equal(first) {
		t.Errorf("last used = %v, want %v", got, first)
	}
	f.clk.advance(30 * time.Second)
	wantStatus(t, f.api(key, "192.0.2.1", http.MethodGet, "/api/v1/settings", nil), http.StatusOK)
	if got := lastUsed(); !got.Equal(first) {
		t.Errorf("last used after 30 s = %v, want %v (written at most once a minute)", got, first)
	}
	f.clk.advance(time.Minute)
	wantStatus(t, f.api(key, "192.0.2.1", http.MethodGet, "/api/v1/settings", nil), http.StatusOK)
	if got := lastUsed(); !got.Equal(f.clk.now()) {
		t.Errorf("last used = %v, want %v", got, f.clk.now())
	}
}

func TestAPITokens(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "https://tpp.example.com")
	key := f.newKey("tokens")

	resp := f.api(key, "", http.MethodPost, "/api/v1/tokens", map[string]any{"name": "Anna's laptop", "expires_in_days": 7})
	wantStatus(t, resp, http.StatusCreated)
	created := readJSON[apiv1.CreatedToken](t, resp)
	if !strings.HasPrefix(created.Url, "https://tpp.example.com/") || created.Token.Status != apiv1.TokenStatusUnused || created.Token.ExpiresAt == nil {
		t.Fatalf("created token = %+v", created)
	}
	value := strings.TrimPrefix(created.Url, "https://tpp.example.com/")
	handle := created.Token.Handle

	resp = f.api(key, "", http.MethodGet, "/api/v1/tokens", nil)
	wantStatus(t, resp, http.StatusOK)
	if body := readBody(t, resp); strings.Contains(body, value) {
		t.Error("the token list holds a token value")
	}

	resp = f.api(key, "", http.MethodGet, "/api/v1/tokens/"+handle+"/link", nil)
	wantStatus(t, resp, http.StatusOK)
	if got := readJSON[apiv1.TokenLink](t, resp).Url; got != created.Url {
		t.Errorf("link = %q, want %q", got, created.Url)
	}
	resp = f.api(key, "", http.MethodGet, "/api/v1/tokens/"+handle+"/qr.png", nil)
	wantStatus(t, resp, http.StatusOK)
	if body := readBody(t, resp); !strings.HasPrefix(body, "\x89PNG") {
		t.Error("qr.png is not a PNG")
	}

	for _, tt := range []struct {
		body any
		want int
	}{
		{map[string]any{"name": ""}, http.StatusBadRequest},
		{map[string]any{"name": "x", "expires_in_days": 3}, http.StatusBadRequest},
		{map[string]any{"name": "x", "tag": "no-such-tag"}, http.StatusBadRequest},
	} {
		wantProblem(t, f.api(key, "", http.MethodPost, "/api/v1/tokens", tt.body), tt.want, "invalid_request")
	}
	wantProblem(t, f.api(key, "", http.MethodPost, "/api/v1/tokens", "not an object"), http.StatusBadRequest, "invalid_request")

	wantStatus(t, f.api(key, "", http.MethodDelete, "/api/v1/tokens/"+handle, nil), http.StatusNoContent)
	wantProblem(t, f.api(key, "", http.MethodDelete, "/api/v1/tokens/"+handle, nil), http.StatusNotFound, "not_found")
	wantProblem(t, f.api(key, "", http.MethodGet, "/api/v1/tokens/"+handle+"/link", nil), http.StatusNotFound, "not_found")

	// A used token cannot be revoked.
	tok, err := f.store.CreateToken(context.Background(), "used")
	if err != nil {
		t.Fatalf("CreateToken() error = %v", err)
	}
	if _, err := f.store.ConsumeToken(context.Background(), tok.Value); err != nil {
		t.Fatalf("ConsumeToken() error = %v", err)
	}
	wantProblem(t, f.api(key, "", http.MethodDelete, "/api/v1/tokens/"+admin.HandleOf(tok.Value), nil), http.StatusConflict, "conflict")

	// The audit log names the key, never its secret.
	id, _ := apikey.Parse(key)
	entries, err := f.store.ListAudit(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListAudit() error = %v", err)
	}
	var found bool
	for _, e := range entries {
		if strings.Contains(e.Detail+e.Target+e.ActorName, key) {
			t.Errorf("audit entry %+v holds the key", e)
		}
		if e.Action == "token.create" {
			found = true
			if e.Actor != "api_key" || e.ActorID != id || e.ActorName != "test key" {
				t.Errorf("token.create actor = %q %q %q, want api_key %q %q", e.Actor, e.ActorID, e.ActorName, id, "test key")
			}
		}
	}
	if !found {
		t.Error("no token.create audit entry")
	}
}

func TestAPIGroups(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "https://tpp.example.com")
	key := f.newKey("admin")
	ctx := context.Background()

	tok, err := f.store.CreateToken(ctx, "Family")
	if err != nil {
		t.Fatalf("CreateToken() error = %v", err)
	}
	if _, err := f.store.CreateGroup(ctx, tok.Value, store.NewDevice{Name: "laptop", PublicKey: []byte("pk"), WrappedGroupKey: []byte("wk")}); err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}
	handle := admin.HandleOf(tok.Value)
	wantStatus(t, f.api(key, "", http.MethodPost, "/api/v1/tags", map[string]string{"name": "home"}), http.StatusCreated)

	resp := f.api(key, "", http.MethodGet, "/api/v1/groups", nil)
	wantStatus(t, resp, http.StatusOK)
	body := readBody(t, resp)
	if strings.Contains(body, tok.Value) {
		t.Error("the group list holds a group id")
	}
	var page apiv1.GroupPage
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatalf("decode group list: %v", err)
	}
	if page.Total != 1 || page.Items[0].Handle != handle || page.Items[0].DeviceCount != 1 {
		t.Fatalf("group list = %+v", page)
	}

	// One bad field changes nothing.
	resp = f.api(key, "", http.MethodPatch, "/api/v1/groups/"+handle, map[string]any{"name": "Renamed", "tag": "no-such-tag"})
	wantProblem(t, resp, http.StatusBadRequest, "invalid_request")
	if g, _ := f.store.GetGroup(ctx, tok.Value); g.Name != "Family" {
		t.Errorf("name after a refused change = %q, want Family", g.Name)
	}
	wantProblem(t, f.api(key, "", http.MethodPatch, "/api/v1/groups/"+handle, map[string]any{}), http.StatusBadRequest, "invalid_request")

	resp = f.api(key, "", http.MethodPatch, "/api/v1/groups/"+handle, map[string]any{
		"name": "Renamed", "tag": "home", "limit": map[string]any{"enabled": true, "bytes": 1 << 20},
	})
	wantStatus(t, resp, http.StatusOK)
	detail := readJSON[apiv1.GroupDetail](t, resp)
	if detail.Group.Name != "Renamed" || detail.Group.Tag != "home" || !detail.Limit.Own.Enabled || detail.Limit.Own.Bytes != 1<<20 ||
		detail.Group.Storage.LimitSource != apiv1.LimitSourceGroup || len(detail.Devices) != 1 {
		t.Errorf("group after the change = %+v", detail)
	}

	resp = f.api(key, "", http.MethodGet, "/api/v1/tags/home", nil)
	wantStatus(t, resp, http.StatusOK)
	if tag := readJSON[apiv1.Tag](t, resp); tag.Groups != 1 || tag.Devices != 1 {
		t.Errorf("tag = %+v, want 1 group with 1 device", tag)
	}

	// No store and no purger are wired in this fixture.
	wantProblem(t, f.api(key, "", http.MethodPatch, "/api/v1/groups/"+handle, map[string]any{"store": true}), http.StatusNotImplemented, "unavailable")
	wantProblem(t, f.api(key, "", http.MethodPost, "/api/v1/groups/"+handle+"/purge", nil), http.StatusNotImplemented, "unavailable")

	wantStatus(t, f.api(key, "", http.MethodDelete, "/api/v1/groups/"+handle, nil), http.StatusNoContent)
	wantProblem(t, f.api(key, "", http.MethodGet, "/api/v1/groups/"+handle, nil), http.StatusNotFound, "not_found")
	wantProblem(t, f.api(key, "", http.MethodDelete, "/api/v1/groups/"+handle, nil), http.StatusNotFound, "not_found")

	wantStatus(t, f.api(key, "", http.MethodDelete, "/api/v1/tags/home", nil), http.StatusNoContent)
	wantProblem(t, f.api(key, "", http.MethodGet, "/api/v1/tags/home", nil), http.StatusNotFound, "not_found")
}

func TestAPIAuditShowsTheActor(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "https://tpp.example.com")
	key := f.newKey("admin")
	id, _ := apikey.Parse(key)

	c := f.client()
	csrf := f.login(c)
	wantStatus(t, f.call(c, csrf, http.MethodPost, "/admin/api/tags", map[string]string{"name": "by-page"}), http.StatusCreated)
	wantStatus(t, f.api(key, "", http.MethodPost, "/api/v1/tags", map[string]string{"name": "by-key"}), http.StatusCreated)

	resp := f.api(key, "", http.MethodGet, "/api/v1/audit?action=tag.create", nil)
	wantStatus(t, resp, http.StatusOK)
	page := readJSON[apiv1.AuditPage](t, resp)
	got := map[string]apiv1.Actor{}
	for _, e := range page.Items {
		got[e.Target] = e.Actor
	}
	if a := got["by-page"]; a.Kind != apiv1.Admin || a.KeyId != nil {
		t.Errorf("by-page actor = %+v, want admin", a)
	}
	if a := got["by-key"]; a.Kind != apiv1.ApiKey || a.KeyId == nil || *a.KeyId != id {
		t.Errorf("by-key actor = %+v, want api_key %s", a, id)
	}
}

// syncBuffer is a bytes.Buffer safe for the server's goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestAPINeverLogsAKey(t *testing.T) {
	t.Parallel()
	var logs syncBuffer
	f := newFixtureWithOptions(t, "https://tpp.example.com", func(o *admin.Options) {
		o.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	key := f.newKey("read")
	wrong := key[:len(key)-1] + "q"
	if wrong == key {
		wrong = key[:len(key)-1] + "r"
	}

	f.api(key, "", http.MethodGet, "/api/v1/settings", nil)
	f.api(wrong, "", http.MethodGet, "/api/v1/settings", nil)
	f.api(key, "", http.MethodGet, "/api/v1/tokens", nil)

	secret := key[strings.LastIndex(key, "_")+1:]
	if out := logs.String(); strings.Contains(out, secret) || strings.Contains(out, wrong[strings.LastIndex(wrong, "_")+1:]) {
		t.Errorf("the log holds key material:\n%s", out)
	}
}
