package store

import (
	"context"
	"errors"
)

// The store's records (SPEC §4.6): one record per item, a per-group index of
// committed items, usage counters, and the queues the sweep works through.
// The items' blobs are the blob backend's; what is here is ciphertext the
// relay cannot read and sizes it can.

// Sentinel errors of the store.
var (
	// ErrStoreDisabled is returned when the group's store is off.
	ErrStoreDisabled = errors.New("store: the group's store is off")

	// ErrStoreBusy is returned while the group's store is being emptied by
	// the admin: nothing is saved until that has finished.
	ErrStoreBusy = errors.New("store: the group's store is being emptied")

	// ErrStoreFull is returned when an item does not fit the group's store,
	// the relay's total store limit, or the group's item limit. The store
	// never evicts.
	ErrStoreFull = errors.New("store: the store is full")

	// ErrItemExists is returned for an item id already in use.
	ErrItemExists = errors.New("store: item id is already in use")

	// ErrRevisionConflict is returned when an edit names a revision the item
	// has moved past.
	ErrRevisionConflict = errors.New("store: item revision conflict")

	// ErrStoreLinkRequired is returned for a rekey without a store link while
	// the group has store items: applying it would leave them unreadable.
	ErrStoreLinkRequired = errors.New("store: rekey needs a store link while the group has store items")
)

// ItemUsage is how much of a store is held: committed items, and the room
// reserved for items still being uploaded.
type ItemUsage struct {
	Bytes int64
	Items int64
}

// ItemLimits are what a reservation is checked against. Zero bytes means
// no limit of that kind; Items is always a limit.
type ItemLimits struct {
	GroupBytes int64
	TotalBytes int64
	GroupItems int64
}

// Items is the persistent zone's view of the store. Implementations must be
// safe for concurrent use, and every write that checks a limit or an epoch
// must make the check and the write one atomic step.
type Items interface {
	// StoreUsage reports what one group's store holds.
	StoreUsage(ctx context.Context, groupID string) (ItemUsage, error)

	// TotalStoreUsage reports what every store holds together.
	TotalStoreUsage(ctx context.Context) (ItemUsage, error)
}
