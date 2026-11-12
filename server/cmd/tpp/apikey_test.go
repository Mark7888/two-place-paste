package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/apikey"
	"github.com/Mark7888/two-place-paste/server/internal/store"
)

func TestAPIKeyCommand(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.NewMemoryStore()
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)

	var stdout, stderr bytes.Buffer
	err := apiKeyCommand(ctx, st, st, []string{"create", "--name", "backup", "--scope", "read, tokens", "--expires", "30d"}, &stdout, &stderr, now)
	if err != nil {
		t.Fatalf("create error = %v (stderr %q)", err, stderr.String())
	}
	key := strings.TrimSpace(stdout.String())
	id, ok := apikey.Parse(key)
	if !ok {
		t.Fatalf("stdout = %q, want the key alone", stdout.String())
	}
	if strings.Contains(stderr.String(), key) {
		t.Error("the key is on stderr too; it belongs on stdout only")
	}
	rec, err := st.GetAPIKey(ctx, id)
	if err != nil {
		t.Fatalf("GetAPIKey() error = %v", err)
	}
	if rec.CreatedBy != "cli" || !rec.ExpiresAt.Equal(now.Add(30*24*time.Hour)) || strings.Join(rec.Scopes, ",") != "read,tokens" {
		t.Errorf("stored key = %+v", rec)
	}

	stdout.Reset()
	if err := apiKeyCommand(ctx, st, st, []string{"list"}, &stdout, &stderr, now); err != nil {
		t.Fatalf("list error = %v", err)
	}
	if out := stdout.String(); !strings.Contains(out, id) || !strings.Contains(out, "backup") || strings.Contains(out, key) {
		t.Errorf("list = %q, want the id and name, never the key", out)
	}

	if err := apiKeyCommand(ctx, st, st, []string{"revoke", id}, &stdout, &stderr, now); err != nil {
		t.Fatalf("revoke error = %v", err)
	}
	if err := apiKeyCommand(ctx, st, st, []string{"revoke", id}, &stdout, &stderr, now); err == nil {
		t.Error("revoking twice succeeded, want an error")
	}

	entries, err := st.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit() error = %v", err)
	}
	var actions []string
	for _, e := range entries {
		actions = append(actions, e.Action)
		if e.Actor != "cli" {
			t.Errorf("audit entry %s actor = %q, want cli", e.Action, e.Actor)
		}
		if strings.Contains(e.Detail+e.Target, key) {
			t.Errorf("audit entry %+v holds the key", e)
		}
	}
	if got := strings.Join(actions, " "); got != "apikey.revoke apikey.create" {
		t.Errorf("audit actions = %q, want %q", got, "apikey.revoke apikey.create")
	}
}

func TestAPIKeyCommandRefuses(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		args []string
	}{
		{"no name", []string{"create", "--scope", "read"}},
		{"no scope", []string{"create", "--name", "x"}},
		{"bad scope", []string{"create", "--name", "x", "--scope", "write"}},
		{"bad expiry", []string{"create", "--name", "x", "--scope", "read", "--expires", "7"}},
		{"stray argument", []string{"create", "--name", "x", "--scope", "read", "extra"}},
		{"unknown subcommand", []string{"rotate"}},
		{"revoke without id", []string{"revoke"}},
		{"list with argument", []string{"list", "x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			st := store.NewMemoryStore()
			var stdout, stderr bytes.Buffer
			if err := apiKeyCommand(context.Background(), st, st, tt.args, &stdout, &stderr, now); err == nil {
				t.Errorf("apikey %v succeeded, want an error", tt.args)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want nothing", stdout.String())
			}
		})
	}
}
