package items

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// Emptying a store (SPEC §4.6). Purge store and group deletion only queue the
// group; the reclaimer empties it in the background, so neither the admin
// request nor the group delete waits on a store of any size. The queue lives
// in Redis, so a restart picks up where the last process stopped, and the
// sweep runs the same work as a backstop.

// reclaimBatch is how many item records one step deletes.
const reclaimBatch = 500

// stalePendingAfter is when the sweep treats a pending item as abandoned: no
// upload lives longer than maxUploadLifetime, so one older than this is left
// over from a crash the startup release did not see.
const stalePendingAfter = maxUploadLifetime + 10*time.Minute

// Purge empties a group's store: its items, blobs and usage. The store closes
// at once — every request is refused as disabled until it is empty — and is
// emptied in the background. The switch and the link chain are kept.
func (s *Service) Purge(ctx context.Context, groupID string) error {
	if _, err := s.reg.GetGroup(ctx, groupID); err != nil {
		return fmt.Errorf("purge store of group %s: %w", groupID, err)
	}
	if err := s.reg.QueueReclaim(ctx, groupID); err != nil {
		return fmt.Errorf("purge store of group %s: %w", groupID, err)
	}
	s.abortGroupUploads(context.WithoutCancel(ctx), groupID)
	if s.hooks.OnStoreState != nil {
		s.hooks.OnStoreState(ctx, groupID, false)
	}
	s.logger.InfoContext(ctx, "store purge queued", slog.String("group_id", groupID))
	s.Kick()
	return nil
}

// Reclaiming reports whether a group's store is being emptied.
func (s *Service) Reclaiming(ctx context.Context, groupID string) (bool, error) {
	ok, err := s.reg.Reclaiming(ctx, groupID)
	if err != nil {
		return false, fmt.Errorf("read reclaim state of group %s: %w", groupID, err)
	}
	return ok, nil
}

// Kick wakes the reclaimer: a group was queued. It never blocks.
func (s *Service) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// RunReclaimer empties queued stores now and whenever Kick is called, until
// ctx is done. It is the goroutine's whole life: the caller owns it and stops
// it by cancelling ctx.
func (s *Service) RunReclaimer(ctx context.Context) {
	for {
		if err := s.ReclaimQueued(ctx); err != nil && ctx.Err() == nil {
			s.logger.WarnContext(ctx, "store reclaim failed; the sweep will retry", slog.Any("error", err))
		}
		select {
		case <-ctx.Done():
			return
		case <-s.kick:
		}
	}
}

// ReclaimQueued empties every queued store.
func (s *Service) ReclaimQueued(ctx context.Context) error {
	s.reclaimMu.Lock()
	defer s.reclaimMu.Unlock()

	queue, err := s.reg.ReclaimQueue(ctx)
	if err != nil {
		return fmt.Errorf("reclaim stores: %w", err)
	}
	var errs []error
	for _, groupID := range queue {
		if ctx.Err() != nil {
			break
		}
		if err := s.reclaim(ctx, groupID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// reclaim empties one queued store: records in batches, then the pending
// records and the usage, then the blobs as one prefix, and only then takes
// the group off the queue. Every step may run again after a failure.
func (s *Service) reclaim(ctx context.Context, groupID string) error {
	s.abortGroupUploads(ctx, groupID)
	removed := 0
	for {
		n, err := s.reg.ReclaimItems(ctx, groupID, reclaimBatch)
		if err != nil {
			return fmt.Errorf("reclaim store of group %s: %w", groupID, err)
		}
		removed += n
		if n == 0 {
			break
		}
		if ctx.Err() != nil {
			return fmt.Errorf("reclaim store of group %s: %w", groupID, ctx.Err())
		}
	}
	if err := s.reg.FinishReclaim(ctx, groupID); err != nil {
		return fmt.Errorf("reclaim store of group %s: %w", groupID, err)
	}
	if s.blobs != nil {
		if err := s.blobs.DeleteItemGroup(ctx, groupID); err != nil {
			return fmt.Errorf("reclaim store of group %s: %w", groupID, err)
		}
	}
	if err := s.reg.EndReclaim(ctx, groupID); err != nil {
		return fmt.Errorf("reclaim store of group %s: %w", groupID, err)
	}
	s.logger.InfoContext(ctx, "store emptied", slog.String("group_id", groupID), slog.Int("items", removed))

	// A purged group's store opens again, empty, as its switch says.
	g, err := s.reg.GetGroup(ctx, groupID)
	if err == nil && s.hooks.OnStoreState != nil {
		s.hooks.OnStoreState(ctx, groupID, g.StoreEnabled)
	}
	return nil
}

// Sweep is the store's part of the blob sweep: it abandons idle uploads,
// releases items left pending by a crash, retries the blob deletes that
// failed, queues the store of any group that is gone but left blobs, and
// empties the queue. Unlike the bucket sweep it reads Redis, but never per
// item: once per stale upload, failed delete and stored group.
func (s *Service) Sweep(ctx context.Context, now time.Time) error {
	s.ReapUploads(ctx, now)
	errs := []error{
		s.releaseStale(ctx, now),
		s.retryBlobDeletes(ctx),
		s.queueOrphanGroups(ctx),
		s.ReclaimQueued(ctx),
	}
	return errors.Join(errs...)
}

func (s *Service) releaseStale(ctx context.Context, now time.Time) error {
	stale, err := s.reg.PendingBefore(ctx, now.Add(-stalePendingAfter))
	if err != nil {
		return fmt.Errorf("release stale store uploads: %w", err)
	}
	for _, it := range stale {
		released, err := s.reg.ReleaseItem(ctx, it.GroupID, it.ID)
		if err != nil {
			return fmt.Errorf("release stale store upload %s: %w", it.ID, err)
		}
		if released {
			s.deleteBlob(ctx, it.GroupID, it.ID)
			s.logger.InfoContext(ctx, "stale store upload released",
				slog.String("group_id", it.GroupID), slog.String("item_id", it.ID))
		}
	}
	return nil
}

func (s *Service) retryBlobDeletes(ctx context.Context) error {
	retries, err := s.reg.BlobRetries(ctx)
	if err != nil {
		return fmt.Errorf("retry store blob deletes: %w", err)
	}
	var errs []error
	for _, r := range retries {
		// An item listed under the id again owns the blob; leave it.
		_, err := s.reg.GetItem(ctx, r.GroupID, r.ItemID)
		switch {
		case err == nil:
		case !errors.Is(err, store.ErrNotFound):
			errs = append(errs, fmt.Errorf("retry delete of store blob %s: %w", r.ItemID, err))
			continue
		default:
			if err := s.blobs.DeleteItem(ctx, r.GroupID, r.ItemID); err != nil {
				errs = append(errs, fmt.Errorf("retry delete of store blob %s: %w", r.ItemID, err))
				continue
			}
		}
		if err := s.reg.RemoveBlobRetry(ctx, r); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// queueOrphanGroups queues the store of every group that has blobs but was
// deleted: the backstop for a reclaim the queue lost. A group counts as
// deleted only when its creation token says so, never merely because it is
// missing, so a relay pointed at the wrong or an empty Redis deletes nothing.
func (s *Service) queueOrphanGroups(ctx context.Context) error {
	groups, err := s.blobs.ItemGroups(ctx)
	if err != nil {
		return fmt.Errorf("find orphaned stores: %w", err)
	}
	var errs []error
	for _, groupID := range groups {
		deleted, err := s.groupDeleted(ctx, groupID)
		if err != nil {
			errs = append(errs, fmt.Errorf("find orphaned stores: %w", err))
			continue
		}
		if !deleted {
			continue
		}
		if err := s.reg.QueueReclaim(ctx, groupID); err != nil {
			errs = append(errs, err)
			continue
		}
		s.logger.InfoContext(ctx, "orphaned store queued for reclaim", slog.String("group_id", groupID))
	}
	return errors.Join(errs...)
}

// groupDeleted reports whether a group is gone and its token marked deleted.
func (s *Service) groupDeleted(ctx context.Context, groupID string) (bool, error) {
	_, err := s.reg.GetGroup(ctx, groupID)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return false, fmt.Errorf("read group %s: %w", groupID, err)
	}
	tok, err := s.reg.GetToken(ctx, groupID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read token of group %s: %w", groupID, err)
	}
	return tok.GroupDeleted, nil
}
