package service

import (
	"context"
	"crypto/sha256"
	"strconv"
	"time"

	"github.com/Mark7888/two-place-paste/pkg/tppclient"
)

// fakeStore is the relay's store, in memory, for the fake relay.
type fakeStore struct {
	enabled bool
	items   []tppclient.StoreItem
	bodies  map[string]tppclient.Item
	saves   int
	failing error // returned by the next call, then cleared
}

func (f *fakeRelay) storeLocked() *fakeStore {
	if f.store == nil {
		f.store = &fakeStore{bodies: map[string]tppclient.Item{}}
	}
	return f.store
}

func (f *fakeRelay) takeStoreFailure() error {
	st := f.storeLocked()
	err := st.failing
	st.failing = nil
	return err
}

func (f *fakeRelay) StoreLimits(context.Context) (tppclient.StoreLimits, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.storeLocked()
	if !st.enabled {
		return tppclient.StoreLimits{}, nil
	}
	return tppclient.StoreLimits{Enabled: true, MaxBytes: 1 << 20, AvailableBytes: 1 << 19, MaxItems: 100,
		UsedItems: int64(len(st.items)), MaxItemBytes: 1 << 19}, nil
}

func (f *fakeRelay) ListStore(context.Context) (tppclient.StoreListing, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.storeLists++
	st := f.storeLocked()
	if err := f.takeStoreFailure(); err != nil {
		return tppclient.StoreListing{}, err
	}
	if !st.enabled {
		return tppclient.StoreListing{}, tppclient.ErrStoreDisabled
	}
	return tppclient.StoreListing{Items: append([]tppclient.StoreItem(nil), st.items...)}, nil
}

func (f *fakeRelay) SaveToStore(_ context.Context, item tppclient.Item, d tppclient.StoreDetails) (tppclient.StoreItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.storeLocked()
	if err := f.takeStoreFailure(); err != nil {
		return tppclient.StoreItem{}, err
	}
	if !st.enabled {
		return tppclient.StoreItem{}, tppclient.ErrStoreDisabled
	}
	st.saves++
	it := tppclient.StoreItem{
		ID:           "item-" + strconv.Itoa(st.saves),
		SavedAt:      time.Date(2026, 10, 1, 12, 0, st.saves, 0, time.UTC),
		StoreDetails: d,
		ContentType:  item.ContentType,
		Filename:     item.Filename,
		BodySize:     int64(len(item.Body)),
		SHA256:       sha256.Sum256(item.Body),
		BlobSize:     int64(len(item.Body)) + 100,
	}
	st.items = append(st.items, it)
	st.bodies[it.ID] = item
	return it, nil
}

func (f *fakeRelay) FetchStoreItem(_ context.Context, it tppclient.StoreItem) (tppclient.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.storeLocked()
	body, ok := st.bodies[it.ID]
	if !ok {
		return tppclient.Item{}, tppclient.ErrNotFound
	}
	return body, nil
}

func (f *fakeRelay) EditStoreItem(_ context.Context, it tppclient.StoreItem, d tppclient.StoreDetails) (tppclient.StoreItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.storeLocked()
	for i, cur := range st.items {
		if cur.ID != it.ID {
			continue
		}
		if cur.MetaRev != it.MetaRev {
			return tppclient.StoreItem{}, tppclient.ErrRevisionConflict
		}
		cur.StoreDetails, cur.MetaRev = d, cur.MetaRev+1
		st.items[i] = cur
		return cur, nil
	}
	return tppclient.StoreItem{}, tppclient.ErrNotFound
}

func (f *fakeRelay) DeleteStoreItem(_ context.Context, itemID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.storeLocked()
	for i, cur := range st.items {
		if cur.ID == itemID {
			st.items = append(st.items[:i], st.items[i+1:]...)
			delete(st.bodies, itemID)
			return nil
		}
	}
	return tppclient.ErrNotFound
}
