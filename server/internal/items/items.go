// Package items is the store: a group's items that do not expire, beside its
// 24-hour history (SPEC §4.6, §6b).
//
// It holds no crypto and reads nothing it is handed. An item is a blob — an
// entry container the client sealed — plus a fixed-size record of wrapped keys
// and sealed metadata (/spec/crypto.md §7a). What the relay knows is sizes,
// times, epochs and ids.
//
// The store never evicts. A save that does not fit is refused, and the limits
// a client sees in Limits are the ones a save is checked against.
package items

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// RecordBytes is what one item's record costs besides its blob: the wrapped
// item keys and the sealed metadata, whose sizes are fixed
// (/spec/crypto.md §7a.3, §7a.5). Usage counts it, so that a store of tiny
// items still fills up.
const RecordBytes = WrappedItemKeysSize + SealedMetaSize

// WrappedItemKeysSize and SealedMetaSize are the only lengths the relay
// accepts for an item's wrapped keys and sealed metadata.
const (
	WrappedItemKeysSize = 105
	SealedMetaSize      = 2089
)

// Sentinel errors of the service. Errors of the records layer — the store
// being off, full or busy, an id in use, a stale revision — come through as
// the store package's.
var (
	// ErrNotFound is returned for an item that does not exist in the
	// caller's group.
	ErrNotFound = errors.New("items: no such item")

	// ErrTooLarge is returned for an item larger than the per-item cap, or
	// than the group's store limit on its own.
	ErrTooLarge = errors.New("items: item is too large")

	// ErrInvalid is returned for a request whose fields are out of range: an
	// id outside the alphabet, keys or metadata of the wrong length.
	ErrInvalid = errors.New("items: invalid item")
)

// Config bounds the store and the transport it uses.
type Config struct {
	// MaxTotalBytes caps every store together. Zero is unlimited.
	MaxTotalBytes int64

	// MaxGroupBytes is a group's store limit unless its tag or the group
	// itself overrides it. Zero is unlimited.
	MaxGroupBytes int64

	// MaxItemBytes is the largest blob one item may have.
	MaxItemBytes int64

	// MaxGroupItems is how many items one group may hold.
	MaxGroupItems int
}

// Registry is what the service needs of the persistent zone.
type Registry interface {
	GetGroup(ctx context.Context, groupID string) (store.Group, error)
	GetTag(ctx context.Context, name string) (store.Tag, error)
	store.Items
}

// Options configures a Service.
type Options struct {
	Registry Registry
	Config   Config
	Logger   *slog.Logger

	// Now is the clock; tests replace it. Defaults to the wall clock in UTC.
	Now func() time.Time
}

// Service is the store. It is safe for concurrent use.
type Service struct {
	reg    Registry
	cfg    Config
	logger *slog.Logger
	now    func() time.Time
}

// New returns a store service.
func New(opts Options) *Service {
	if opts.Registry == nil {
		panic("items: a registry is required")
	}
	s := &Service{
		reg:    opts.Registry,
		cfg:    opts.Config,
		logger: opts.Logger,
		now:    opts.Now,
	}
	if s.logger == nil {
		s.logger = slog.Default()
	}
	if s.now == nil {
		s.now = func() time.Time { return time.Now().UTC() }
	}
	return s
}

// Config reports the configured store limits.
func (s *Service) Config() Config { return s.cfg }

// Limit is a group's store limit and where it comes from: "group" for its
// own override, "tag" for its tag's, "default" for the server's.
type Limit struct {
	Bytes  int64
	Source string
}

// GroupLimit resolves the store limit of a group: its own override if on,
// else its tag's if on, else the server default. Zero is unlimited. The
// relay's total store limit is applied separately.
func (s *Service) GroupLimit(g store.Group, tag *store.Tag) Limit {
	if g.StoreLimit.Enabled {
		return Limit{Bytes: g.StoreLimit.Bytes, Source: "group"}
	}
	if tag != nil && tag.StoreLimit.Enabled {
		return Limit{Bytes: tag.StoreLimit.Bytes, Source: "tag"}
	}
	return Limit{Bytes: s.cfg.MaxGroupBytes, Source: "default"}
}

// Limits is the state of one group's store, as StoreLimitsResponse states it.
type Limits struct {
	Enabled bool

	// MaxBytes is the group's store limit, cut to the relay's total when that
	// is lower. Zero is unlimited.
	MaxBytes  int64
	UsedBytes int64

	// AvailableBytes is the largest blob a new item may have now: the room
	// left in the group and in the relay's total, less one item's record,
	// and never more than the per-item cap. Zero when the item limit is
	// reached.
	AvailableBytes int64

	MaxItems     int64
	UsedItems    int64
	MaxItemBytes int64
}

// Limits reports a group's store. A group whose store is off reports only
// that, and zeros.
func (s *Service) Limits(ctx context.Context, groupID string) (Limits, error) {
	g, err := s.reg.GetGroup(ctx, groupID)
	if err != nil {
		return Limits{}, fmt.Errorf("read store limits of group %s: %w", groupID, err)
	}
	if !g.StoreEnabled {
		return Limits{}, nil
	}
	limits, err := s.storeLimits(ctx, g)
	if err != nil {
		return Limits{}, fmt.Errorf("read store limits of group %s: %w", groupID, err)
	}
	used, err := s.reg.StoreUsage(ctx, groupID)
	if err != nil {
		return Limits{}, fmt.Errorf("read store limits of group %s: %w", groupID, err)
	}
	total, err := s.reg.TotalStoreUsage(ctx)
	if err != nil {
		return Limits{}, fmt.Errorf("read store limits of group %s: %w", groupID, err)
	}

	out := Limits{
		Enabled:      true,
		MaxBytes:     shown(limits.GroupBytes, limits.TotalBytes),
		UsedBytes:    used.Bytes,
		MaxItems:     limits.GroupItems,
		UsedItems:    used.Items,
		MaxItemBytes: s.cfg.MaxItemBytes,
	}
	room := s.cfg.MaxItemBytes + RecordBytes
	if limits.GroupBytes > 0 {
		room = min(room, limits.GroupBytes-used.Bytes)
	}
	if limits.TotalBytes > 0 {
		room = min(room, limits.TotalBytes-total.Bytes)
	}
	if used.Items < limits.GroupItems {
		out.AvailableBytes = max(room-RecordBytes, 0)
	}
	return out, nil
}

// Links returns the group's store links, oldest first: one per rekey, each
// leading from an epoch's store key back to the one before
// (/spec/crypto.md §7a.2). They are refused, like every store request but
// Limits, while the store is off.
func (s *Service) Links(ctx context.Context, groupID string) ([]store.ItemLink, error) {
	if _, err := s.enabledGroup(ctx, groupID); err != nil {
		return nil, err
	}
	links, err := s.reg.StoreLinks(ctx, groupID)
	if err != nil {
		return nil, fmt.Errorf("read store links of group %s: %w", groupID, err)
	}
	return links, nil
}

// enabledGroup reads a group and refuses with store.ErrStoreDisabled when its
// store is off.
func (s *Service) enabledGroup(ctx context.Context, groupID string) (store.Group, error) {
	g, err := s.reg.GetGroup(ctx, groupID)
	if err != nil {
		return store.Group{}, fmt.Errorf("read group %s: %w", groupID, err)
	}
	if !g.StoreEnabled {
		return store.Group{}, fmt.Errorf("group %s: %w", groupID, store.ErrStoreDisabled)
	}
	return g, nil
}

// storeLimits resolves what a reservation in g is checked against.
func (s *Service) storeLimits(ctx context.Context, g store.Group) (store.ItemLimits, error) {
	var tag *store.Tag
	if g.Tag != "" {
		t, err := s.reg.GetTag(ctx, g.Tag)
		switch {
		case err == nil:
			tag = &t
		case !errors.Is(err, store.ErrNotFound):
			return store.ItemLimits{}, fmt.Errorf("read tag of group %s: %w", g.ID, err)
		}
	}
	return store.ItemLimits{
		GroupBytes: s.GroupLimit(g, tag).Bytes,
		TotalBytes: s.cfg.MaxTotalBytes,
		GroupItems: int64(s.cfg.MaxGroupItems),
	}, nil
}

// shown is the cap a group actually runs into: its own limit, or the total
// when that is lower or the group has none. Zero means no cap at all.
func shown(group, total int64) int64 {
	if total > 0 && (group == 0 || total < group) {
		return total
	}
	return group
}
