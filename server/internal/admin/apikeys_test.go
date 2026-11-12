package admin_test

import (
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/apikey"
)

type apiKeyRow struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Scopes     []string `json:"scopes"`
	ExpiresAt  *string  `json:"expires_at"`
	LastUsedAt *string  `json:"last_used_at"`
	Status     string   `json:"status"`
}

func TestAPIKeysScreen(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "https://tpp.example.com")
	c := f.client()
	csrf := f.login(c)

	resp := f.call(c, csrf, http.MethodPost, "/admin/api/api-keys", map[string]any{
		"name": "backup script", "scopes": []string{"read", "tokens"}, "expiry_days": 90,
	})
	wantStatus(t, resp, http.StatusCreated)
	created := readJSON[struct {
		Key    string    `json:"key"`
		APIKey apiKeyRow `json:"api_key"`
	}](t, resp)
	id, ok := apikey.Parse(created.Key)
	if !ok || id != created.APIKey.ID || created.APIKey.Status != "active" || created.APIKey.ExpiresAt == nil {
		t.Fatalf("created = %+v", created)
	}

	// The key works on the REST API straight away.
	wantStatus(t, f.api(created.Key, "", http.MethodGet, "/api/v1/tokens", nil), http.StatusOK)

	resp = f.call(c, "", http.MethodGet, "/admin/api/api-keys", nil)
	wantStatus(t, resp, http.StatusOK)
	body := readBody(t, resp)
	rec, err := f.store.GetAPIKey(t.Context(), id)
	if err != nil {
		t.Fatalf("GetAPIKey() error = %v", err)
	}
	if strings.Contains(body, created.Key) || strings.Contains(body, hex.EncodeToString(rec.Hash)) {
		t.Error("the key list holds a key or its hash")
	}
	if !strings.Contains(body, `"last_used_at":"`) || !strings.Contains(body, `"max":50`) {
		t.Errorf("key list = %s, want the key's last use and the bound", body)
	}

	for _, tt := range []struct {
		name string
		body map[string]any
		want int
	}{
		{"no name", map[string]any{"name": "", "scopes": []string{"read"}, "expiry_days": 90}, http.StatusBadRequest},
		{"no scope", map[string]any{"name": "x", "scopes": []string{}, "expiry_days": 90}, http.StatusBadRequest},
		{"odd lifetime", map[string]any{"name": "x", "scopes": []string{"read"}, "expiry_days": 7}, http.StatusBadRequest},
	} {
		if resp := f.call(c, csrf, http.MethodPost, "/admin/api/api-keys", tt.body); resp.StatusCode != tt.want {
			t.Errorf("%s: status = %d, want %d", tt.name, resp.StatusCode, tt.want)
		}
	}
	// Changes need the CSRF token, like every other change on the page.
	wantStatus(t, f.call(c, "", http.MethodPost, "/admin/api/api-keys", map[string]any{"name": "x", "scopes": []string{"read"}, "expiry_days": 0}), http.StatusForbidden)

	wantStatus(t, f.call(c, csrf, http.MethodDelete, "/admin/api/api-keys/"+id, nil), http.StatusNoContent)
	wantStatus(t, f.call(c, csrf, http.MethodDelete, "/admin/api/api-keys/"+id, nil), http.StatusNotFound)
	wantProblem(t, f.api(created.Key, "", http.MethodGet, "/api/v1/tokens", nil), http.StatusUnauthorized, "unauthorized")

	entries, err := f.store.ListAudit(t.Context(), 10)
	if err != nil {
		t.Fatalf("ListAudit() error = %v", err)
	}
	var actions []string
	for _, e := range entries {
		if strings.HasPrefix(e.Action, "apikey.") {
			actions = append(actions, e.Action)
			if e.Target != "backup script ("+id+")" || e.Actor != "" {
				t.Errorf("%s entry = %+v, want the key named by name and id, by the admin page", e.Action, e)
			}
		}
	}
	if got := strings.Join(actions, " "); got != "apikey.revoke apikey.create" {
		t.Errorf("audit actions = %q, want %q", got, "apikey.revoke apikey.create")
	}
}

func TestAPIKeysScreenShowsExpiry(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "https://tpp.example.com")
	c := f.client()
	csrf := f.login(c)
	wantStatus(t, f.call(c, csrf, http.MethodPost, "/admin/api/api-keys", map[string]any{
		"name": "short", "scopes": []string{"read"}, "expiry_days": 30,
	}), http.StatusCreated)
	f.clk.advance(31 * 24 * time.Hour)
	f.login(c) // the session has long expired

	resp := f.call(c, "", http.MethodGet, "/admin/api/api-keys", nil)
	wantStatus(t, resp, http.StatusOK)
	page := readJSON[struct {
		Rows []apiKeyRow `json:"rows"`
	}](t, resp)
	if len(page.Rows) != 1 || page.Rows[0].Status != "expired" {
		t.Errorf("rows = %+v, want one expired key", page.Rows)
	}
}
