package admin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"

	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// A handle names a token or a group to the admin UI without being either.
//
// A creation token is a credential, and a group's id is the token that
// created it, so neither belongs in a list row, a search field, a URL or a
// log (docs/internal/plans/admin-react-rewrite/smart-table.md §12). The UI
// gets a handle instead: a digest of the value that the server can match but
// nobody can turn back into it. It is deterministic, so a group's page keeps
// its URL across restarts and bookmarks keep working. A token and the group
// it created have the same handle, which is what lets a token row link to
// its group.
//
// Finding a value by its handle reads the whole list it lives in. That is a
// handful of Redis reads at the size an admin screen deals with, and it keeps
// a second index — one more thing to keep in step — out of the store.

// handleLen is the length of a handle: 16 bytes of SHA-256 in base64url.
const handleLen = 22

// handleOf derives the handle of a token or group id.
func handleOf(id string) string {
	sum := sha256.Sum256([]byte("tpp admin handle\x00" + id))
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}

// groupByHandle finds the group a handle names. It returns store.ErrNotFound
// when none does.
func (s *Server) groupByHandle(ctx context.Context, h string) (store.Group, error) {
	if len(h) != handleLen {
		return store.Group{}, store.ErrNotFound
	}
	groups, err := s.store.ListGroups(ctx)
	if err != nil {
		return store.Group{}, fmt.Errorf("find group by handle: %w", err)
	}
	for _, g := range groups {
		if handleOf(g.ID) == h {
			return g, nil
		}
	}
	return store.Group{}, store.ErrNotFound
}

// tokenByHandle finds the token a handle names. It returns store.ErrNotFound
// when none does.
func (s *Server) tokenByHandle(ctx context.Context, h string) (store.Token, error) {
	if len(h) != handleLen {
		return store.Token{}, store.ErrNotFound
	}
	tokens, err := s.store.ListTokens(ctx)
	if err != nil {
		return store.Token{}, fmt.Errorf("find token by handle: %w", err)
	}
	for _, t := range tokens {
		if handleOf(t.Value) == h {
			return t, nil
		}
	}
	return store.Token{}, store.ErrNotFound
}
