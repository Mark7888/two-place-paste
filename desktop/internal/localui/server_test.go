package localui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubAPI is a service that does nothing but record that it was reached. A
// request that gets this far has passed both guards, which is what these tests
// are about.
type stubAPI struct {
	mu      sync.Mutex
	calls   []string
	events  chan Event
	plans   map[string]RevokePlan
	nextErr error
}

func newStubAPI() *stubAPI {
	return &stubAPI{events: make(chan Event, 4), plans: map[string]RevokePlan{}}
}

func (s *stubAPI) record(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, name)
}

func (s *stubAPI) called(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.calls {
		if c == name {
			return true
		}
	}
	return false
}

func (s *stubAPI) Status(context.Context) Status {
	s.record("status")
	return Status{InGroup: true, DeviceName: "laptop"}
}

func (s *stubAPI) Sync(context.Context, Direction) (SyncResult, error) {
	s.record("sync")
	return SyncResult{Direction: DirectionUpload, Changed: true, Message: "uploaded"}, s.nextErr
}

func (s *stubAPI) History(context.Context, HistoryQuery) (HistoryPage, error) {
	s.record("history")
	return HistoryPage{Entries: []EntryView{{ID: "e1"}}}, nil
}

func (s *stubAPI) CopyEntry(context.Context, string) (SyncResult, error) {
	s.record("copy")
	return SyncResult{Direction: DirectionDownload, Changed: true}, nil
}

func (s *stubAPI) Devices(context.Context) (RosterView, error) {
	s.record("devices")
	return RosterView{Epoch: 3, Devices: []DeviceView{{ID: "d1", Name: "laptop", This: true}, {ID: "d2", Name: "phone"}}}, nil
}

func (s *stubAPI) PrepareRevoke(_ context.Context, deviceID string) (RevokePlan, error) {
	s.record("prepare")
	plan := RevokePlan{
		ID:        "plan-1",
		Target:    DeviceView{ID: deviceID, Name: "phone"},
		Remaining: []DeviceView{{ID: "d1", Name: "laptop", This: true}},
		Epoch:     3,
	}
	s.mu.Lock()
	s.plans[plan.ID] = plan
	s.mu.Unlock()
	return plan, nil
}

func (s *stubAPI) ConfirmRevoke(_ context.Context, planID string) (RosterView, error) {
	s.record("confirm")
	s.mu.Lock()
	_, ok := s.plans[planID]
	s.mu.Unlock()
	if !ok {
		return RosterView{}, Errorf(http.StatusConflict, nil, "this revocation is no longer pending")
	}
	return RosterView{Epoch: 4}, nil
}

func (s *stubAPI) StartPairing(context.Context) (Invite, error) {
	s.record("pair-start")
	return Invite{Payload: "tpp1:abc"}, nil
}

func (s *stubAPI) JoinPairing(context.Context, string) error {
	s.record("pair-join")
	return s.nextErr
}

func (s *stubAPI) CreateGroup(context.Context, string) error {
	s.record("create")
	return s.nextErr
}

func (s *stubAPI) Settings(context.Context) SettingsView {
	s.record("settings")
	return SettingsView{Port: 0, ListenPort: 47821}
}

func (s *stubAPI) UpdateSettings(context.Context, SettingsPatch) (SettingsView, error) {
	s.record("settings-update")
	return SettingsView{AutoWatch: true}, nil
}

func (s *stubAPI) Subscribe() (<-chan Event, func()) {
	s.record("subscribe")
	return s.events, func() {}
}

func newTestServer(t *testing.T) (*Server, *stubAPI) {
	t.Helper()
	api := newStubAPI()
	srv, err := Listen(context.Background(), Options{API: api, Port: 0, Token: "test-token"})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv, api
}

// TestOriginIsValidatedOnEveryRequest is the acceptance criterion of ROADMAP
// P6: a page the user happens to be browsing can reach 127.0.0.1, and the
// Origin check is the whole of the defence against it driving this service.
func TestOriginIsValidatedOnEveryRequest(t *testing.T) {
	t.Parallel()

	srv, api := newTestServer(t)
	good := fmt.Sprintf("http://127.0.0.1:%d", srv.Port())

	routes := []struct {
		method, path, body string
	}{
		{"GET", "/api/status", ""},
		{"POST", "/api/sync", `{"direction":"upload"}`},
		{"GET", "/api/history", ""},
		{"POST", "/api/entries/copy", `{"entry_id":"e1"}`},
		{"GET", "/api/devices", ""},
		{"POST", "/api/devices/revoke/prepare", `{"device_id":"d2"}`},
		{"POST", "/api/devices/revoke/confirm", `{"plan_id":"plan-1"}`},
		{"POST", "/api/pairing/start", `{}`},
		{"POST", "/api/pairing/join", `{"payload":"tpp1:abc"}`},
		{"POST", "/api/group/create", `{"creation_url":"https://relay.example/t"}`},
		{"GET", "/api/settings", ""},
		{"POST", "/api/settings", `{"auto_watch":true}`},
		{"GET", "/api/events", ""},
		{"GET", "/app", ""},
		{"GET", "/index.html", ""},
	}

	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			t.Parallel()

			// A foreign Origin is refused everywhere, with the token present:
			// the token is not what is being tested here.
			rec := do(t, srv, rt.method, rt.path, rt.body, map[string]string{
				"Origin":    "https://evil.example",
				tokenHeader: "test-token",
			})
			if rec.Code != http.StatusForbidden {
				t.Errorf("a request from https://evil.example got %d, want %d", rec.Code, http.StatusForbidden)
			}

			// The app's own Origin is accepted.
			rec = do(t, srv, rt.method, rt.path, rt.body, map[string]string{
				"Origin":    good,
				tokenHeader: "test-token",
			})
			if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
				t.Errorf("a request from the app itself got %d, want it served", rec.Code)
			}
		})
	}

	if api.called("sync") && !api.called("status") {
		t.Error("the stub was reached inconsistently; the guards let something through")
	}
}

func TestMissingOriginIsRefusedOnTheAPI(t *testing.T) {
	t.Parallel()

	srv, _ := newTestServer(t)

	// The API and the event stream are scripted: a browser always attaches an
	// Origin to them, so its absence means the caller is not the app.
	for _, path := range []string{"/api/status", "/api/events"} {
		rec := do(t, srv, "GET", path, "", map[string]string{tokenHeader: "test-token"})
		if rec.Code != http.StatusForbidden {
			t.Errorf("GET %s with no Origin = %d, want %d", path, rec.Code, http.StatusForbidden)
		}
	}

	// A navigation carries no Origin, and refusing it would mean the tray
	// could not open the UI at all.
	rec := do(t, srv, "GET", "/app", "", map[string]string{tokenHeader: "test-token"})
	if rec.Code != http.StatusOK {
		t.Errorf("GET /app with no Origin = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestTokenIsRequiredOnEveryRequest(t *testing.T) {
	t.Parallel()

	srv, api := newTestServer(t)
	origin := fmt.Sprintf("http://127.0.0.1:%d", srv.Port())

	tests := []struct {
		name    string
		headers map[string]string
		query   string
		want    int
	}{
		{name: "no token at all", headers: map[string]string{"Origin": origin}, want: http.StatusUnauthorized},
		{name: "a wrong token", headers: map[string]string{"Origin": origin, tokenHeader: "not-it"}, want: http.StatusUnauthorized},
		{name: "an empty token", headers: map[string]string{"Origin": origin, tokenHeader: ""}, want: http.StatusUnauthorized},
		{name: "the token as a header", headers: map[string]string{"Origin": origin, tokenHeader: "test-token"}, want: http.StatusOK},
		{name: "the token as a bearer", headers: map[string]string{"Origin": origin, "Authorization": "Bearer test-token"}, want: http.StatusOK},
		{name: "the token in the query", headers: map[string]string{"Origin": origin}, query: "?token=test-token", want: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := do(t, srv, "GET", "/api/status"+tt.query, "", tt.headers)
			if rec.Code != tt.want {
				t.Errorf("GET /api/status = %d, want %d", rec.Code, tt.want)
			}
		})
	}

	// A request that never passed the guards must not have reached the
	// service: an unauthenticated caller learns nothing, not even the shape of
	// an answer.
	rec := do(t, srv, "POST", "/api/group/create", `{"creation_url":"https://relay.example/t"}`,
		map[string]string{"Origin": origin})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("POST /api/group/create with no token = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if api.called("create") {
		t.Error("an unauthenticated request reached the service")
	}
}

// TestPortInUseIsVisible covers the SPEC §7.2 requirement that the service
// never fails silently when its fixed port is taken: the condition has its own
// error so the tray can show it and point at the port override.
func TestPortInUseIsVisible(t *testing.T) {
	t.Parallel()

	first, _ := newTestServer(t)
	_, err := Listen(context.Background(), Options{API: newStubAPI(), Port: first.Port(), Token: "test-token"})
	if !errors.Is(err, ErrPortInUse) {
		t.Fatalf("Listen() on a taken port = %v, want ErrPortInUse", err)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(first.Port())) {
		t.Errorf("the error does not name the port: %v", err)
	}
}

func TestURLCarriesTheTokenAndNothingElse(t *testing.T) {
	t.Parallel()

	srv, _ := newTestServer(t)
	want := fmt.Sprintf("http://127.0.0.1:%d/app?token=test-token", srv.Port())
	if got := srv.URL(); got != want {
		t.Errorf("URL() = %q, want %q", got, want)
	}
}

// TestListenerIsLoopbackOnly is the other half of SPEC §7.2: a service bound to
// 0.0.0.0 would publish the user's clipboard to the network.
func TestListenerIsLoopbackOnly(t *testing.T) {
	t.Parallel()

	srv, _ := newTestServer(t)
	addr, ok := srv.listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address is %T, want *net.TCPAddr", srv.listener.Addr())
	}
	if !addr.IP.IsLoopback() {
		t.Errorf("the service is listening on %s, want a loopback address", addr.IP)
	}
}

func TestRevocationNeedsAPlan(t *testing.T) {
	t.Parallel()

	srv, api := newTestServer(t)
	origin := fmt.Sprintf("http://127.0.0.1:%d", srv.Port())
	headers := map[string]string{"Origin": origin, tokenHeader: "test-token"}

	// Confirming without a plan id is refused: the plan is what proves the
	// roster was fetched and shown (SPEC §3.3 step 2).
	rec := do(t, srv, "POST", "/api/devices/revoke/confirm", `{"plan_id":""}`, headers)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("confirming with no plan = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if api.called("confirm") {
		t.Fatal("a revocation with no plan reached the service")
	}

	// An unknown plan is refused by the service itself.
	rec = do(t, srv, "POST", "/api/devices/revoke/confirm", `{"plan_id":"made-up"}`, headers)
	if rec.Code != http.StatusConflict {
		t.Fatalf("confirming an unknown plan = %d, want %d", rec.Code, http.StatusConflict)
	}

	rec = do(t, srv, "POST", "/api/devices/revoke/prepare", `{"device_id":"d2"}`, headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("preparing a revocation = %d, want %d", rec.Code, http.StatusOK)
	}
	var plan RevokePlan
	if err := json.NewDecoder(rec.Body).Decode(&plan); err != nil {
		t.Fatalf("decoding the plan: %v", err)
	}
	if len(plan.Remaining) == 0 || plan.Target.Name == "" {
		t.Fatalf("the plan does not carry a roster to show: %+v", plan)
	}

	rec = do(t, srv, "POST", "/api/devices/revoke/confirm", `{"plan_id":"`+plan.ID+`"}`, headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("confirming a prepared revocation = %d, want %d", rec.Code, http.StatusOK)
	}
}

// TestEventStreamRejectsAForeignOrigin drives a real WebSocket upgrade over a
// real listener: the recorder-based test above cannot prove the handshake
// itself is guarded.
func TestEventStreamRejectsAForeignOrigin(t *testing.T) {
	t.Parallel()

	srv, _ := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go func() { _ = srv.Serve(ctx) }()

	base := fmt.Sprintf("http://127.0.0.1:%d/api/events?token=test-token", srv.Port())
	for _, tc := range []struct {
		name, origin string
		want         int
	}{
		{name: "foreign", origin: "https://evil.example", want: http.StatusForbidden},
		{name: "own", origin: fmt.Sprintf("http://127.0.0.1:%d", srv.Port()), want: http.StatusSwitchingProtocols},
	} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base, nil)
		if err != nil {
			t.Fatalf("building the upgrade request: %v", err)
		}
		req.Header.Set("Origin", tc.origin)
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Sec-WebSocket-Version", "13")
		req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")

		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			t.Fatalf("%s origin: upgrade request: %v", tc.name, err)
		}
		// A 101 hands back a hijacked connection: reading it would block
		// until the service shuts down, so it is only closed.
		if resp.StatusCode != http.StatusSwitchingProtocols {
			_, _ = io.Copy(io.Discard, resp.Body)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s origin: upgrade = %d, want %d", tc.name, resp.StatusCode, tc.want)
		}
	}
}

func do(t *testing.T, srv *Server, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}
