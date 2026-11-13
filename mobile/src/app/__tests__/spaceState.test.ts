/**
 * The out-of-space banners and popup: when each shows, how ✕ closes it until
 * next triggered, what the setting hides, and whose room a refusal blames.
 * The desktop app (desktop/internal/service/space.go) follows the same rules.
 */

import { ProtocolError, type StoreLimits } from '../../core';
import { ErrorCode } from '../../protocol/gen/tpp/v1/error';
import {
  STORE_RECORD_BYTES,
  dismissSpace,
  getSpaceSnapshot,
  nearlyFull,
  noteEvicted,
  noteStoreFailure,
  noteUploadFailure,
  resetSpace,
  setHideBanners,
  spaceView,
  storeFullCause,
} from '../spaceState';

const MIB = 1 << 20;

function limits(over: Partial<StoreLimits>): StoreLimits {
  return {
    enabled: true,
    maxBytes: 100,
    usedBytes: 0,
    availableBytes: 50,
    maxItems: 10,
    usedItems: 0,
    maxItemBytes: 50,
    ...over,
  };
}

const quota = new ProtocolError(ErrorCode.ERROR_CODE_QUOTA_EXCEEDED, 'full');

beforeEach(() => {
  resetSpace();
  setHideBanners(false);
});

describe('nearlyFull', () => {
  test.each([
    ['off', limits({ enabled: false, usedBytes: 100 }), false],
    ['empty', limits({}), false],
    ['89% of bytes', limits({ usedBytes: 89 }), false],
    ['90% of bytes', limits({ usedBytes: 90 }), true],
    ['90% of items', limits({ usedItems: 9 }), true],
    ['no limits at all', limits({ maxBytes: 0, maxItems: 0, usedBytes: 1e9 }), false],
  ])('%s', (_name, l, want) => {
    expect(nearlyFull(l)).toBe(want);
  });
});

describe('storeFullCause', () => {
  const base = { maxBytes: 100 * MIB, maxItems: 10, usedItems: 3, maxItemBytes: 50 * MIB };
  test.each([
    ['out of items', limits({ ...base, usedItems: 10, usedBytes: MIB, availableBytes: 0 }), 'store_full'],
    ["out of the group's bytes", limits({ ...base, usedBytes: 99 * MIB, availableBytes: MIB - STORE_RECORD_BYTES }), 'store_full'],
    ["the group has room, the relay's total does not", limits({ ...base, usedBytes: 10 * MIB, availableBytes: MIB }), 'relay_store_full'],
    ['no group limit', limits({ ...base, maxBytes: 0, usedBytes: 10 * MIB, availableBytes: 0 }), 'relay_store_full'],
  ])('%s', (_name, l, want) => {
    expect(storeFullCause(l)).toBe(want);
  });
});

describe('banners', () => {
  test('the store banner closes until the store changes while still that full', () => {
    const full = limits({ usedBytes: 95, usedItems: 4 });
    expect(spaceView(getSpaceSnapshot(), full).storeNearlyFull).toBe(true);
    dismissSpace('store', full);
    expect(spaceView(getSpaceSnapshot(), full).storeNearlyFull).toBe(false);
    expect(spaceView(getSpaceSnapshot(), { ...full, usedBytes: 97, usedItems: 5 }).storeNearlyFull).toBe(true);
  });

  test('evictions count up until the banner is closed', () => {
    noteEvicted();
    noteEvicted();
    expect(spaceView(getSpaceSnapshot(), null).evicted).toBe(2);
    dismissSpace('evicted', null);
    expect(spaceView(getSpaceSnapshot(), null).evicted).toBe(0);
    noteEvicted();
    expect(spaceView(getSpaceSnapshot(), null).evicted).toBe(1);
  });

  test('the setting hides both banners and never the popup', () => {
    noteEvicted();
    noteUploadFailure(quota);
    setHideBanners(true);
    const view = spaceView(getSpaceSnapshot(), limits({ usedBytes: 99 }));
    expect(view.storeNearlyFull).toBe(false);
    expect(view.evicted).toBe(0);
    expect(view.failure).toBe('relay_full');
  });
});

describe('popup', () => {
  test('a refused history upload blames the relay, and each one is a new popup', () => {
    noteUploadFailure(quota);
    const first = getSpaceSnapshot().failureSeq;
    expect(getSpaceSnapshot().failure).toBe('relay_full');
    dismissSpace('failure', null);
    expect(getSpaceSnapshot().failure).toBeNull();
    noteUploadFailure(quota);
    expect(getSpaceSnapshot().failureSeq).toBe(first + 1);
  });

  test('a refused save says whose store is full', () => {
    noteStoreFailure(quota, limits({ maxBytes: 100 * MIB, usedBytes: 10 * MIB, availableBytes: MIB, maxItemBytes: 50 * MIB }));
    expect(getSpaceSnapshot().failure).toBe('relay_store_full');
    noteStoreFailure(quota, null);
    expect(getSpaceSnapshot().failure).toBe('store_full');
  });

  test('other failures are not about room', () => {
    noteUploadFailure(new ProtocolError(ErrorCode.ERROR_CODE_TOO_LARGE, 'too large'));
    noteStoreFailure(new Error('offline'), null);
    expect(getSpaceSnapshot().failure).toBeNull();
  });

  test('a group change drops everything of the old group', () => {
    noteEvicted();
    noteUploadFailure(quota);
    dismissSpace('store', limits({ usedBytes: 95 }));
    resetSpace();
    const s = getSpaceSnapshot();
    expect([s.evicted, s.failure, s.closedAt]).toEqual([0, null, null]);
  });
});
