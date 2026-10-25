package store

import (
	"context"
	"errors"
	"time"
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

// ItemLink is the previous epoch's store key sealed under the store key of
// Epoch (/spec/crypto.md §7a.2). The relay keeps it and cannot open it.
type ItemLink struct {
	Epoch uint64
	Link  []byte
}

// Item is one store item as the relay knows it: sizes, times, epochs and
// ciphertext it cannot read (/spec/crypto.md §7a).
type Item struct {
	ID      string
	GroupID string

	// Size is the length of the item's blob. Accounted is what it counts
	// against the store's limits: the blob and its fixed record.
	Size      int64
	Accounted int64

	// WrapEpoch is the epoch whose store key wraps WrappedKeys;
	// ContentID and ContentEpoch are what the blob's associated data binds.
	WrapEpoch    uint64
	ContentID    string
	ContentEpoch uint64

	WrappedKeys []byte
	SealedMeta  []byte
	MetaRev     uint64

	// Pending is true from the reservation until the commit, while the blob
	// is being uploaded. A pending item is not listed.
	Pending   bool
	StartedAt time.Time

	// SavedAt is when the commit happened: what listings are ordered by.
	SavedAt time.Time
}

// ItemReservation is room asked for an item about to be uploaded.
type ItemReservation struct {
	GroupID string
	ItemID  string

	// Epoch is the epoch the item is sealed at; it must be the group's.
	Epoch uint64

	Size      int64
	Accounted int64
	StartedAt time.Time
}

// ItemCommit completes a reserved item.
type ItemCommit struct {
	GroupID     string
	ItemID      string
	Epoch       uint64
	WrappedKeys []byte
	SealedMeta  []byte
	SavedAt     time.Time
}

// ItemRef is an item's place in its group's list.
type ItemRef struct {
	ID      string
	SavedAt time.Time
}

// Items is the persistent zone's view of the store. Implementations must be
// safe for concurrent use, and every write that checks a limit or an epoch
// must make the check and the write one atomic step.
type Items interface {
	// StoreUsage reports what one group's store holds.
	StoreUsage(ctx context.Context, groupID string) (ItemUsage, error)

	// TotalStoreUsage reports what every store holds together.
	TotalStoreUsage(ctx context.Context) (ItemUsage, error)

	// StoreLinks returns the links every rekey of the group sent, oldest
	// first. A group that was never re-keyed has none.
	StoreLinks(ctx context.Context, groupID string) ([]ItemLink, error)

	// ReserveItem claims room for an item and writes its record as pending,
	// in one step that checks the group's store is on and not being emptied,
	// that the epoch is the group's, that the id is free and that the item
	// fits every limit. It returns ErrNotFound, ErrStoreDisabled,
	// ErrStoreBusy, ErrEpochConflict, ErrItemExists or ErrStoreFull, and on
	// any error changes nothing.
	ReserveItem(ctx context.Context, r ItemReservation, limits ItemLimits) error

	// CommitItem completes a pending item: its keys and metadata are written
	// and it is listed. It checks again that the store is on, not being
	// emptied, and at the item's epoch — the same check a rekey's link
	// requirement serialises with. On ErrStoreDisabled, ErrStoreBusy or
	// ErrEpochConflict the item stays pending, for the caller to release.
	CommitItem(ctx context.Context, c ItemCommit) (Item, error)

	// ReleaseItem forgets a pending item and gives its room back. It reports
	// false for an item that is not pending in the group, and changes
	// nothing then.
	ReleaseItem(ctx context.Context, groupID, itemID string) (bool, error)

	// GetItem returns one committed item of the group, or ErrNotFound.
	GetItem(ctx context.Context, groupID, itemID string) (Item, error)

	// ItemRefs returns every committed item of the group, newest first, ties
	// broken by id, descending.
	ItemRefs(ctx context.Context, groupID string) ([]ItemRef, error)

	// GetItems returns the committed items named, in the order given,
	// leaving out any that no longer exist.
	GetItems(ctx context.Context, groupID string, ids []string) ([]Item, error)

	// UpdateItemMeta replaces a committed item's sealed metadata when its
	// revision is still expectedRev, and returns it at expectedRev+1. A
	// moved-on item is ErrRevisionConflict.
	UpdateItemMeta(ctx context.Context, groupID, itemID string, expectedRev uint64, sealedMeta []byte) (Item, error)

	// DeleteItem deletes a committed item's record and gives its room back.
	// The caller deletes the blob.
	DeleteItem(ctx context.Context, groupID, itemID string) (Item, error)

	// ReleaseAllPending forgets every pending item, everywhere, and returns
	// them. The relay calls it at startup: uploads live in its memory, so any
	// item pending then was left by the previous process.
	ReleaseAllPending(ctx context.Context) ([]Item, error)

	// PendingBefore returns the pending items started before t, oldest
	// first. The sweep releases them: no upload lives that long.
	PendingBefore(ctx context.Context, t time.Time) ([]Item, error)

	ItemReclaim
}

// BlobRetry names a store blob whose delete failed after its record was
// gone.
type BlobRetry struct {
	GroupID string
	ItemID  string
}

// ItemReclaim is the queue of groups whose store is being emptied — by a
// purge or because the group was deleted — and the retry set of blobs a
// delete left behind (SPEC §4.6). Both survive a restart.
type ItemReclaim interface {
	// QueueReclaim adds a group to the queue. From then until EndReclaim,
	// ReserveItem and CommitItem refuse the group with ErrStoreBusy.
	QueueReclaim(ctx context.Context, groupID string) error

	// Reclaiming reports whether a group is in the queue.
	Reclaiming(ctx context.Context, groupID string) (bool, error)

	// ReclaimQueue returns every queued group.
	ReclaimQueue(ctx context.Context) ([]string, error)

	// ReclaimItems deletes up to n committed item records of a queued group
	// and returns how many it deleted; zero means none is left. The usage
	// is left alone until FinishReclaim, so a batch is cheap.
	ReclaimItems(ctx context.Context, groupID string, n int) (int, error)

	// FinishReclaim deletes the group's pending records and its usage, and
	// takes that usage off the total, once ReclaimItems has left no
	// committed item. It may be run again.
	FinishReclaim(ctx context.Context, groupID string) error

	// EndReclaim takes a group off the queue, once its blobs are gone too.
	EndReclaim(ctx context.Context, groupID string) error

	// AddBlobRetry, BlobRetries and RemoveBlobRetry keep the blobs whose
	// delete failed, for the sweep to try again.
	AddBlobRetry(ctx context.Context, r BlobRetry) error
	BlobRetries(ctx context.Context) ([]BlobRetry, error)
	RemoveBlobRetry(ctx context.Context, r BlobRetry) error
}
