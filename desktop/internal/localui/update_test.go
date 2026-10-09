package localui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// The stub is also the updater.

func (s *stubAPI) UpdateStatus(context.Context) UpdateView {
	s.record("update-status")
	return UpdateView{Channel: "stable", State: UpdateStateIdle}
}

func (s *stubAPI) CheckUpdate(context.Context) (UpdateView, error) {
	s.record("update-check")
	return UpdateView{Channel: "stable", State: UpdateStateAvailable,
		Available: &UpdateCandidate{BuildView: BuildView{Version: "1.0.0", Stamp: 2}, Signed: true}}, s.nextErr
}

func (s *stubAPI) InstallUpdate(_ context.Context, req InstallRequest) (UpdateView, error) {
	s.record("update-install")
	if !req.Confirm {
		return UpdateView{}, Errorf(http.StatusConflict, nil, "confirm it")
	}
	return UpdateView{State: UpdateStateRestarting}, nil
}

func (s *stubAPI) RollbackUpdate(context.Context) (UpdateView, error) {
	s.record("update-rollback")
	return UpdateView{State: UpdateStateRestarting}, nil
}

func (s *stubAPI) SetUpdateToken(_ context.Context, token string) (UpdateView, error) {
	s.record("update-token-set:" + token)
	return UpdateView{Token: TokenView{Set: true, Source: "keystore"}}, nil
}

func (s *stubAPI) DeleteUpdateToken(context.Context) (UpdateView, error) {
	s.record("update-token-delete")
	return UpdateView{}, nil
}

func TestTheTokenRoutesTakeTheTokenInTheBody(t *testing.T) {
	t.Parallel()
	srv, api := newTestServer(t)
	headers := map[string]string{"Origin": fmt.Sprintf("http://127.0.0.1:%d", srv.Port()), tokenHeader: "test-token"}

	rec := do(t, srv, "PUT", "/api/update/token", `{"token":"github_pat_x"}`, headers)
	if rec.Code != http.StatusOK || !api.called("update-token-set:github_pat_x") {
		t.Fatalf("PUT /api/update/token = %d", rec.Code)
	}
	if rec := do(t, srv, "DELETE", "/api/update/token", "", headers); rec.Code != http.StatusOK {
		t.Fatalf("DELETE /api/update/token = %d", rec.Code)
	}
	// A page on another origin can neither set nor remove it.
	evil := map[string]string{"Origin": "https://evil.example", tokenHeader: "test-token"}
	if rec := do(t, srv, "PUT", "/api/update/token", `{"token":"github_pat_y"}`, evil); rec.Code != http.StatusForbidden {
		t.Fatalf("PUT from a foreign origin = %d, want 403", rec.Code)
	}
}

func TestUpdateRoutesReachTheUpdater(t *testing.T) {
	t.Parallel()
	srv, api := newTestServer(t)
	headers := map[string]string{"Origin": fmt.Sprintf("http://127.0.0.1:%d", srv.Port()), tokenHeader: "test-token"}

	rec := do(t, srv, "POST", "/api/update/check", `{}`, headers)
	if rec.Code != http.StatusOK || !api.called("update-check") {
		t.Fatalf("POST /api/update/check = %d, reached %v", rec.Code, api.called("update-check"))
	}
	var v UpdateView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Available == nil || v.Available.Version != "1.0.0" {
		t.Fatalf("the view lost its candidate: %+v", v)
	}

	// A refusal from the updater keeps its status and its words.
	rec = do(t, srv, "POST", "/api/update/install", `{}`, headers)
	if rec.Code != http.StatusConflict {
		t.Fatalf("POST /api/update/install without confirm = %d, want 409", rec.Code)
	}

	// An unknown field is a client bug, not something to ignore.
	rec = do(t, srv, "POST", "/api/update/install", `{"force":true}`, headers)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/update/install with an unknown field = %d, want 400", rec.Code)
	}
}

func TestUpdateRoutesWithoutAnUpdater(t *testing.T) {
	t.Parallel()
	srv, err := Listen(context.Background(), Options{API: newStubAPI(), Port: 0, Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	headers := map[string]string{"Origin": fmt.Sprintf("http://127.0.0.1:%d", srv.Port()), tokenHeader: "test-token"}
	if rec := do(t, srv, "GET", "/api/update", "", headers); rec.Code != http.StatusNotImplemented {
		t.Fatalf("GET /api/update with no updater = %d, want 501", rec.Code)
	}
}
