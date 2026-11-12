package admin

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/apikey"
	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// The API keys screen (docs/internal/plans/admin-rest-api.md §4.4): the
// admin page creates, lists and revokes the REST API's keys. These endpoints
// are the page's, behind its session and CSRF check like every other; the
// REST API itself has none, so a key can never mint or reveal another (D7).

// apiKeyJSON is one key as the screen lists it: never the key, never its
// hash.
type apiKeyJSON struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	LastUsedIP string     `json:"last_used_ip"`
	CreatedBy  string     `json:"created_by"`

	// Status is "active" or "expired".
	Status string `json:"status"`
}

// apiKeysJSON is the screen: one page of keys, and what a new key can be.
type apiKeysJSON struct {
	tablePage[apiKeyJSON]
	Max               int      `json:"max"`
	Count             int      `json:"count"`
	Scopes            []string `json:"scopes"`
	ExpiryDays        []int    `json:"expiry_days"`
	DefaultExpiryDays int      `json:"default_expiry_days"`
}

// createdAPIKeyJSON is a key just created: the one response that carries the
// key itself.
type createdAPIKeyJSON struct {
	Key    string     `json:"key"`
	APIKey apiKeyJSON `json:"api_key"`
}

// apiKeyTable is the key list: searched by name, id and scope, newest first.
var apiKeyTable = table[apiKeyJSON]{
	sorts: map[string]sortKey[apiKeyJSON]{
		"name":      {cmp: func(a, b apiKeyJSON) int { return byText(a.Name, b.Name) }},
		"created":   {cmp: func(a, b apiKeyJSON) int { return a.CreatedAt.Compare(b.CreatedAt) }, desc: true},
		"expires":   {cmp: func(a, b apiKeyJSON) int { return byTime(a.ExpiresAt, b.ExpiresAt) }, desc: true},
		"last_used": {cmp: func(a, b apiKeyJSON) int { return byTime(a.LastUsedAt, b.LastUsedAt) }, desc: true},
	},
	defaultSort: "created",
	fields: func(k apiKeyJSON) []string {
		return append([]string{k.Name, k.ID, k.Status}, k.Scopes...)
	},
	tie: func(a, b apiKeyJSON) int { return strings.Compare(a.ID, b.ID) },
}

func (s *Server) apiKeyView(k store.APIKey, now time.Time) apiKeyJSON {
	v := apiKeyJSON{
		ID:         k.ID,
		Name:       k.Name,
		Scopes:     k.Scopes,
		CreatedAt:  k.CreatedAt.UTC(),
		ExpiresAt:  timeOrNil(k.ExpiresAt),
		LastUsedAt: timeOrNil(k.LastUsedAt),
		LastUsedIP: k.LastUsedIP,
		CreatedBy:  k.CreatedBy,
		Status:     "active",
	}
	if v.Scopes == nil {
		v.Scopes = []string{}
	}
	if k.Expired(now) {
		v.Status = "expired"
	}
	return v
}

// handleAPIKeys returns one page of the API keys.
func (s *Server) handleAPIKeys(w http.ResponseWriter, r *http.Request, _ session) {
	ctx := r.Context()
	out := apiKeysJSON{
		Max:               store.MaxAPIKeys,
		Scopes:            apikey.Scopes,
		ExpiryDays:        apikey.ExpiryDays,
		DefaultExpiryDays: apikey.DefaultExpiryDays,
	}
	var rows []apiKeyJSON
	if s.apiKeys != nil {
		keys, err := s.apiKeys.ListAPIKeys(ctx)
		if err != nil {
			s.logger.ErrorContext(ctx, "list API keys", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "The API keys could not be loaded.")
			return
		}
		now := s.now()
		rows = make([]apiKeyJSON, 0, len(keys))
		for _, k := range keys {
			rows = append(rows, s.apiKeyView(k, now))
		}
	}
	out.Count = len(rows)
	out.tablePage = apiKeyTable.run(rows, r.URL.Query())
	writeJSON(w, http.StatusOK, out)
}

// handleCreateAPIKey creates an API key and returns it, once.
func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request, _ session) {
	var req struct {
		Name       string   `json:"name"`
		Scopes     []string `json:"scopes"`
		ExpiryDays int      `json:"expiry_days"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if s.apiKeys == nil {
		writeJSONError(w, http.StatusNotImplemented, "This server cannot keep API keys.")
		return
	}
	ctx := r.Context()
	key, rec, err := apikey.Create(ctx, s.apiKeys, apikey.New{
		Name: req.Name, Scopes: req.Scopes, ExpiryDays: req.ExpiryDays, CreatedBy: "admin",
	}, s.now())
	switch {
	case errors.Is(err, apikey.ErrName):
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("Give the key a name of at most %d characters, so you can tell it apart later.", apikey.MaxNameLen))
		return
	case errors.Is(err, apikey.ErrScope):
		writeJSONError(w, http.StatusBadRequest, "Choose at least one scope.")
		return
	case errors.Is(err, apikey.ErrExpiry):
		writeJSONError(w, http.StatusBadRequest, "Choose when the key expires from the list.")
		return
	case errors.Is(err, store.ErrAPIKeyLimit):
		writeJSONError(w, http.StatusConflict, fmt.Sprintf("There are already %d API keys. Revoke one you no longer use first.", store.MaxAPIKeys))
		return
	case err != nil:
		s.logger.ErrorContext(ctx, "create API key", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "The API key could not be created.")
		return
	}
	// Named by id only: the key is a credential (docs/internal/conventions.md §2).
	s.logger.InfoContext(ctx, "API key created", slog.String("key_id", rec.ID))
	s.audit(r, "apikey.create", apiKeyLabel(rec), strings.Join(rec.Scopes, ", ")+"; "+expiryText(rec.ExpiresAt))
	writeJSON(w, http.StatusCreated, createdAPIKeyJSON{Key: key, APIKey: s.apiKeyView(rec, s.now())})
}

// handleRevokeAPIKey deletes an API key, so it stops working on its next
// request.
func (s *Server) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request, _ session) {
	ctx := r.Context()
	id := r.PathValue("id")
	if s.apiKeys == nil {
		writeJSONError(w, http.StatusNotFound, "That API key no longer exists.")
		return
	}
	rec, err := s.apiKeys.GetAPIKey(ctx, id)
	if err == nil {
		err = s.apiKeys.DeleteAPIKey(ctx, id)
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeJSONError(w, http.StatusNotFound, "That API key no longer exists.")
		return
	case err != nil:
		s.logger.ErrorContext(ctx, "revoke API key", "key_id", id, "error", err)
		writeJSONError(w, http.StatusInternalServerError, "The API key could not be revoked.")
		return
	}
	s.logger.InfoContext(ctx, "API key revoked", slog.String("key_id", rec.ID))
	s.audit(r, "apikey.revoke", apiKeyLabel(rec), "")
	w.WriteHeader(http.StatusNoContent)
}

// apiKeyLabel is how a key is named in the audit log.
func apiKeyLabel(k store.APIKey) string {
	return k.Name + " (" + k.ID + ")"
}
