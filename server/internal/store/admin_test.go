package store_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// uniqueName returns a name no other test uses. The Redis half of the suite
// shares one database, and tags, settings and the audit log are global keys
// there, so every test works on names of its own.
func uniqueName(t *testing.T, prefix string) string {
	t.Helper()
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}

func mustGroup(ctx context.Context, t *testing.T, s store.Store, tok store.NewToken) (store.Group, store.Device) {
	t.Helper()
	created, err := s.CreateTokenWith(ctx, tok)
	if err != nil {
		t.Fatalf("CreateTokenWith() error = %v", err)
	}
	g, err := s.CreateGroup(ctx, created.Value, store.NewDevice{
		Name: "laptop", PublicKey: []byte("pk"), WrappedGroupKey: []byte("wk"),
	})
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}
	return g.Group, g.Device
}

func TestTokenExtras(t *testing.T) {
	t.Parallel()

	forEachStore(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		tag := uniqueName(t, "family")
		if _, err := s.CreateTag(ctx, tag); err != nil {
			t.Fatalf("CreateTag() error = %v", err)
		}
		expires := time.Now().UTC().Add(time.Hour).Truncate(time.Millisecond)

		tok, err := s.CreateTokenWith(ctx, store.NewToken{Name: "Anna", Note: "her phone", Tag: tag, ExpiresAt: expires})
		if err != nil {
			t.Fatalf("CreateTokenWith() error = %v", err)
		}
		got, err := s.GetToken(ctx, tok.Value)
		if err != nil {
			t.Fatalf("GetToken() error = %v", err)
		}
		if got.Note != "her phone" || got.Tag != tag || !got.ExpiresAt.Equal(expires) {
			t.Errorf("GetToken() = note %q, tag %q, expires %v; want %q, %q, %v",
				got.Note, got.Tag, got.ExpiresAt, "her phone", tag, expires)
		}

		g, err := s.ConsumeToken(ctx, tok.Value)
		if err != nil {
			t.Fatalf("ConsumeToken() error = %v", err)
		}
		if g.Name != "Anna" || g.Tag != tag {
			t.Errorf("ConsumeToken() = name %q, tag %q; want %q, %q", g.Name, g.Tag, "Anna", tag)
		}
	})
}

func TestExpiredTokenCreatesNothing(t *testing.T) {
	t.Parallel()

	forEachStore(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		past := time.Now().UTC().Add(-time.Minute)
		tok, err := s.CreateTokenWith(ctx, store.NewToken{Name: "late", ExpiresAt: past})
		if err != nil {
			t.Fatalf("CreateTokenWith() error = %v", err)
		}
		if !tok.Expired(time.Now()) {
			t.Errorf("Expired() = false, want true")
		}
		_, err = s.CreateGroup(ctx, tok.Value, store.NewDevice{Name: "x", PublicKey: []byte("pk"), WrappedGroupKey: []byte("wk")})
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("CreateGroup(expired) error = %v, want %v", err, store.ErrNotFound)
		}
		// Expired is still revocable: it was never used.
		if err := s.RevokeToken(ctx, tok.Value); err != nil {
			t.Errorf("RevokeToken(expired) error = %v, want nil", err)
		}
	})
}

func TestTokenTagThatNoLongerExists(t *testing.T) {
	t.Parallel()

	forEachStore(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		g, _ := mustGroup(ctx, t, s, store.NewToken{Name: "n", Tag: uniqueName(t, "gone")})
		if g.Tag != "" {
			t.Errorf("group tag = %q, want none for a tag that does not exist", g.Tag)
		}
	})
}

func TestRenameAndTagGroup(t *testing.T) {
	t.Parallel()

	forEachStore(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		g, _ := mustGroup(ctx, t, s, store.NewToken{Name: "first"})

		if err := s.RenameGroup(ctx, g.ID, "Family laptop"); err != nil {
			t.Fatalf("RenameGroup() error = %v", err)
		}
		tag := uniqueName(t, "friends")
		if err := s.SetGroupTag(ctx, g.ID, tag); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("SetGroupTag(missing tag) error = %v, want %v", err, store.ErrNotFound)
		}
		if _, err := s.CreateTag(ctx, tag); err != nil {
			t.Fatalf("CreateTag() error = %v", err)
		}
		if _, err := s.CreateTag(ctx, tag); !errors.Is(err, store.ErrTagExists) {
			t.Errorf("CreateTag(again) error = %v, want %v", err, store.ErrTagExists)
		}
		if err := s.SetGroupTag(ctx, g.ID, tag); err != nil {
			t.Fatalf("SetGroupTag() error = %v", err)
		}
		limit := store.LimitOverride{Bytes: 5 << 30, Enabled: true}
		if err := s.SetGroupLimit(ctx, g.ID, limit); err != nil {
			t.Fatalf("SetGroupLimit() error = %v", err)
		}

		got, err := s.GetGroup(ctx, g.ID)
		if err != nil {
			t.Fatalf("GetGroup() error = %v", err)
		}
		if got.Name != "Family laptop" || got.Tag != tag || got.Limit != limit {
			t.Errorf("GetGroup() = %q, %q, %+v; want %q, %q, %+v", got.Name, got.Tag, got.Limit, "Family laptop", tag, limit)
		}

		// Unticking keeps the bytes.
		if err := s.SetGroupLimit(ctx, g.ID, store.LimitOverride{Bytes: limit.Bytes}); err != nil {
			t.Fatalf("SetGroupLimit(off) error = %v", err)
		}
		got, _ = s.GetGroup(ctx, g.ID)
		if got.Limit.Enabled || got.Limit.Bytes != limit.Bytes {
			t.Errorf("GetGroup().Limit = %+v, want disabled with %d bytes kept", got.Limit, limit.Bytes)
		}

		for name, err := range map[string]error{
			"rename":    s.RenameGroup(ctx, "missing", "x"),
			"set tag":   s.SetGroupTag(ctx, "missing", ""),
			"set limit": s.SetGroupLimit(ctx, "missing", limit),
		} {
			if !errors.Is(err, store.ErrNotFound) {
				t.Errorf("%s on a missing group error = %v, want %v", name, err, store.ErrNotFound)
			}
		}
		if _, err := s.GetGroup(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("an edit of a missing group created it: GetGroup() error = %v", err)
		}
	})
}

func TestDeleteTagUntags(t *testing.T) {
	t.Parallel()

	forEachStore(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		tag := uniqueName(t, "gone")
		if _, err := s.CreateTag(ctx, tag); err != nil {
			t.Fatalf("CreateTag() error = %v", err)
		}
		if err := s.SetTagLimit(ctx, tag, store.LimitOverride{Bytes: 1 << 30, Enabled: true}); err != nil {
			t.Fatalf("SetTagLimit() error = %v", err)
		}
		g, _ := mustGroup(ctx, t, s, store.NewToken{Name: "g", Tag: tag})
		spare, err := s.CreateTokenWith(ctx, store.NewToken{Name: "spare", Tag: tag})
		if err != nil {
			t.Fatalf("CreateTokenWith() error = %v", err)
		}

		tags, err := s.ListTags(ctx)
		if err != nil {
			t.Fatalf("ListTags() error = %v", err)
		}
		if !slices.ContainsFunc(tags, func(x store.Tag) bool { return x.Name == tag && x.Limit.Enabled }) {
			t.Errorf("ListTags() = %+v, want %q with its limit", tags, tag)
		}

		if err := s.DeleteTag(ctx, tag); err != nil {
			t.Fatalf("DeleteTag() error = %v", err)
		}
		if err := s.DeleteTag(ctx, tag); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("DeleteTag(again) error = %v, want %v", err, store.ErrNotFound)
		}
		if got, _ := s.GetGroup(ctx, g.ID); got.Tag != "" {
			t.Errorf("group tag after DeleteTag = %q, want none", got.Tag)
		}
		if got, _ := s.GetToken(ctx, spare.Value); got.Tag != "" {
			t.Errorf("token tag after DeleteTag = %q, want none", got.Tag)
		}
	})
}

func TestDeleteGroup(t *testing.T) {
	t.Parallel()

	forEachStore(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		g, first := mustGroup(ctx, t, s, store.NewToken{Name: "doomed"})
		second, err := s.AddDevice(ctx, g.ID, store.NewDevice{Name: "phone", PublicKey: []byte("pk2"), WrappedGroupKey: []byte("wk2")})
		if err != nil {
			t.Fatalf("AddDevice() error = %v", err)
		}

		deleted, err := s.DeleteGroup(ctx, g.ID)
		if err != nil {
			t.Fatalf("DeleteGroup() error = %v", err)
		}
		want := []string{first.ID, second.ID}
		slices.Sort(want)
		got := slices.Clone(deleted.DeviceIDs)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("DeleteGroup().DeviceIDs = %v, want %v", got, want)
		}
		if deleted.Group.Name != "doomed" {
			t.Errorf("DeleteGroup().Group.Name = %q, want %q", deleted.Group.Name, "doomed")
		}

		if _, err := s.GetGroup(ctx, g.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetGroup() after delete error = %v, want %v", err, store.ErrNotFound)
		}
		for _, id := range want {
			if _, err := s.GetDevice(ctx, id); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("GetDevice(%s) after delete error = %v, want %v", id, err, store.ErrNotFound)
			}
			if _, err := s.GetWrappedKey(ctx, id); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("GetWrappedKey(%s) after delete error = %v, want %v", id, err, store.ErrNotFound)
			}
			dead, err := s.DeviceTombstoned(ctx, id)
			if err != nil || !dead {
				t.Errorf("DeviceTombstoned(%s) = %v, %v; want true, nil", id, dead, err)
			}
		}
		if alive, _ := s.DeviceTombstoned(ctx, "never-existed"); alive {
			t.Errorf("DeviceTombstoned(unknown) = true, want false")
		}

		tok, err := s.GetToken(ctx, g.ID)
		if err != nil {
			t.Fatalf("GetToken() after delete error = %v", err)
		}
		if !tok.GroupDeleted {
			t.Errorf("GetToken().GroupDeleted = false, want true")
		}
		groups, err := s.ListGroups(ctx)
		if err != nil {
			t.Fatalf("ListGroups() error = %v", err)
		}
		if slices.ContainsFunc(groups, func(x store.Group) bool { return x.ID == g.ID }) {
			t.Errorf("ListGroups() still lists the deleted group")
		}
		if _, err := s.DeleteGroup(ctx, g.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("DeleteGroup(again) error = %v, want %v", err, store.ErrNotFound)
		}
	})
}

func TestSettingsAndAudit(t *testing.T) {
	t.Parallel()

	forEachStore(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		st, err := s.GetSettings(ctx)
		if err != nil {
			t.Fatalf("GetSettings() error = %v", err)
		}
		if st.StaleAfter <= 0 {
			t.Errorf("GetSettings().StaleAfter = %v, want a positive default", st.StaleAfter)
		}
		if err := s.PutSettings(ctx, store.Settings{StaleAfter: 30 * 24 * time.Hour}); err != nil {
			t.Fatalf("PutSettings() error = %v", err)
		}
		if st, _ := s.GetSettings(ctx); st.StaleAfter != 30*24*time.Hour {
			t.Errorf("GetSettings().StaleAfter = %v, want %v", st.StaleAfter, 30*24*time.Hour)
		}

		target := uniqueName(t, "audit")
		if err := s.AppendAudit(ctx, store.AuditEntry{Action: "group.rename", Target: target, IP: "192.0.2.1"}); err != nil {
			t.Fatalf("AppendAudit() error = %v", err)
		}
		entries, err := s.ListAudit(ctx, 0)
		if err != nil {
			t.Fatalf("ListAudit() error = %v", err)
		}
		if !slices.ContainsFunc(entries, func(e store.AuditEntry) bool {
			return e.Target == target && e.Action == "group.rename" && !e.At.IsZero()
		}) {
			t.Errorf("ListAudit() does not hold the appended entry")
		}
		if len(entries) > store.AuditLimit {
			t.Errorf("len(ListAudit()) = %d, want at most %d", len(entries), store.AuditLimit)
		}
	})
}
