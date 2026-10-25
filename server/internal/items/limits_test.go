package items_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/Mark7888/two-place-paste/server/internal/items"
	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// newGroup makes a group in reg and returns its id.
func newGroup(t *testing.T, reg *store.MemoryStore, tok store.NewToken) string {
	t.Helper()
	ctx := context.Background()
	if tok.Name == "" {
		tok.Name = "g"
	}
	created, err := reg.CreateTokenWith(ctx, tok)
	if err != nil {
		t.Fatalf("CreateTokenWith() error = %v", err)
	}
	if _, err := reg.CreateGroup(ctx, created.Value, store.NewDevice{Name: "laptop", PublicKey: []byte("pk"), WrappedGroupKey: []byte("wk")}); err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}
	return created.Value
}

// TestLimits: a store that is off says only that; one that is on reports
// its limit from the group, its tag or the default, cut to the relay's total,
// and the room a new item has (SPEC §4.6).
func TestLimits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := items.Config{MaxTotalBytes: 0, MaxGroupBytes: 100 << 20, MaxItemBytes: 50 << 20, MaxGroupItems: 1000}

	tests := []struct {
		name       string
		cfg        items.Config
		tagLimit   *store.LimitOverride
		ownLimit   *store.LimitOverride
		want       items.Limits
		wantSource string
	}{
		{
			name:       "the default",
			cfg:        cfg,
			want:       items.Limits{Enabled: true, MaxBytes: 100 << 20, AvailableBytes: 50 << 20, MaxItems: 1000, MaxItemBytes: 50 << 20},
			wantSource: "default",
		},
		{
			name:       "the tag's",
			cfg:        cfg,
			tagLimit:   &store.LimitOverride{Bytes: 10 << 20, Enabled: true},
			want:       items.Limits{Enabled: true, MaxBytes: 10 << 20, AvailableBytes: 10<<20 - items.RecordBytes, MaxItems: 1000, MaxItemBytes: 50 << 20},
			wantSource: "tag",
		},
		{
			name:       "the group's own, over its tag's",
			cfg:        cfg,
			tagLimit:   &store.LimitOverride{Bytes: 10 << 20, Enabled: true},
			ownLimit:   &store.LimitOverride{Bytes: 20 << 20, Enabled: true},
			want:       items.Limits{Enabled: true, MaxBytes: 20 << 20, AvailableBytes: 20<<20 - items.RecordBytes, MaxItems: 1000, MaxItemBytes: 50 << 20},
			wantSource: "group",
		},
		{
			name:       "an override that is off",
			cfg:        cfg,
			ownLimit:   &store.LimitOverride{Bytes: 20 << 20},
			want:       items.Limits{Enabled: true, MaxBytes: 100 << 20, AvailableBytes: 50 << 20, MaxItems: 1000, MaxItemBytes: 50 << 20},
			wantSource: "default",
		},
		{
			name:       "cut to the total",
			cfg:        items.Config{MaxTotalBytes: 30 << 20, MaxGroupBytes: 100 << 20, MaxItemBytes: 50 << 20, MaxGroupItems: 5},
			want:       items.Limits{Enabled: true, MaxBytes: 30 << 20, AvailableBytes: 30<<20 - items.RecordBytes, MaxItems: 5, MaxItemBytes: 50 << 20},
			wantSource: "default",
		},
		{
			name:       "no limit at all",
			cfg:        items.Config{MaxItemBytes: 50 << 20, MaxGroupItems: 5},
			want:       items.Limits{Enabled: true, AvailableBytes: 50 << 20, MaxItems: 5, MaxItemBytes: 50 << 20},
			wantSource: "default",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reg := store.NewMemoryStore()
			svc := items.New(items.Options{Registry: reg, Config: tt.cfg, Logger: slog.New(slog.DiscardHandler)})
			tag := ""
			if tt.tagLimit != nil {
				tag = "work"
				if _, err := reg.CreateTag(ctx, tag); err != nil {
					t.Fatalf("CreateTag() error = %v", err)
				}
				if err := reg.SetTagStoreLimit(ctx, tag, *tt.tagLimit); err != nil {
					t.Fatalf("SetTagStoreLimit() error = %v", err)
				}
			}
			id := newGroup(t, reg, store.NewToken{Tag: tag})
			if tt.ownLimit != nil {
				if err := reg.SetGroupStoreLimit(ctx, id, *tt.ownLimit); err != nil {
					t.Fatalf("SetGroupStoreLimit() error = %v", err)
				}
			}

			off, err := svc.Limits(ctx, id)
			if err != nil {
				t.Fatalf("Limits() error = %v", err)
			}
			if off != (items.Limits{}) {
				t.Errorf("Limits() with the store off = %+v, want zeros", off)
			}

			if err := reg.SetGroupStore(ctx, id, true); err != nil {
				t.Fatalf("SetGroupStore() error = %v", err)
			}
			got, err := svc.Limits(ctx, id)
			if err != nil {
				t.Fatalf("Limits() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("Limits() = %+v, want %+v", got, tt.want)
			}
			g, _ := reg.GetGroup(ctx, id)
			var tg *store.Tag
			if tag != "" {
				v, _ := reg.GetTag(ctx, tag)
				tg = &v
			}
			if src := svc.GroupLimit(g, tg).Source; src != tt.wantSource {
				t.Errorf("GroupLimit().Source = %q, want %q", src, tt.wantSource)
			}
		})
	}
}
