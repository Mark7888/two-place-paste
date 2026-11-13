/**
 * The out-of-space banners and popup (SPEC §7), as the desktop app has them.
 *
 * Two banners are advice — the store is nearly full, history entries were
 * removed early to make room — and the user can close them, or hide them for
 * good in Settings. A save or upload the relay refused for lack of room is a
 * failure, shown in a popup whatever that setting says.
 *
 * Like `storeState` it is a module, not React state, so an eviction or a
 * refused background upload is kept while no screen is mounted. All of it is
 * the group in use's: `resetSpace` drops it when that group changes. The
 * store's limits themselves live in `storeState`; this module is handed them.
 */

import {
  STORE_META_CONTAINER_SIZE,
  WRAPPED_ITEM_KEYS_SIZE,
  isQuotaExceeded,
  type StoreLimits,
} from '../core';

/** A refusal for lack of room, for the popup. */
export type SpaceFailure = 'store_full' | 'relay_store_full' | 'relay_full';

/** One of the banners, or the popup, as dismissSpace names it. */
export type SpaceBanner = 'store' | 'evicted' | 'failure';

/** STORE_RECORD_BYTES is what the relay counts per store item besides its blob. */
export const STORE_RECORD_BYTES = WRAPPED_ITEM_KEYS_SIZE + STORE_META_CONTAINER_SIZE;

interface Usage {
  bytes: number;
  items: number;
}

export interface SpaceSnapshot {
  /** evicted counts entries removed early since the banner was closed. */
  evicted: number;
  /** closedAt is the store's usage when its banner was closed, or null. */
  closedAt: Usage | null;
  /** failure is the last refusal for lack of room, until the popup is closed. */
  failure: SpaceFailure | null;
  /** failureSeq changes with every refusal, so the popup opens for each one. */
  failureSeq: number;
  /** hideBanners is the "Hide out-of-space banners" setting. */
  hideBanners: boolean;
}

/** SpaceView is what the screens draw: a true or non-zero field is a banner to show. */
export interface SpaceView {
  storeNearlyFull: boolean;
  storeUsedBytes: number;
  storeMaxBytes: number;
  storeUsedItems: number;
  storeMaxItems: number;
  evicted: number;
  failure: SpaceFailure | null;
  failureSeq: number;
}

let state: SpaceSnapshot = { evicted: 0, closedAt: null, failure: null, failureSeq: 0, hideBanners: false };
const listeners = new Set<() => void>();

function set(next: Partial<SpaceSnapshot>): void {
  state = { ...state, ...next };
  for (const listener of listeners) {
    listener();
  }
}

export function subscribeSpace(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function getSpaceSnapshot(): SpaceSnapshot {
  return state;
}

/** nearlyFull reports whether a store is on and at least 90% full, by bytes or items. */
export function nearlyFull(l: StoreLimits): boolean {
  if (!l.enabled) {
    return false;
  }
  return (l.maxBytes > 0 && l.usedBytes * 10 >= l.maxBytes * 9) || (l.maxItems > 0 && l.usedItems * 10 >= l.maxItems * 9);
}

/**
 * storeFullCause says why a save was refused for lack of room, from the limits
 * read right after: the group's own store, or the relay's total store limit,
 * which only the operator can raise. The relay states availableBytes as the
 * smallest of the per-item cap, the group's room and the total's room, less
 * one record; when it is below the group's room less one record, the total
 * bound it. The desktop app applies the same rule.
 */
export function storeFullCause(l: StoreLimits): SpaceFailure {
  if (l.maxItems > 0 && l.usedItems >= l.maxItems) {
    return 'store_full';
  }
  if (l.maxBytes === 0) {
    return 'relay_store_full';
  }
  const groupRoom = l.maxBytes - l.usedBytes - STORE_RECORD_BYTES;
  if (l.availableBytes < groupRoom && l.availableBytes < l.maxItemBytes) {
    return 'relay_store_full';
  }
  return 'store_full';
}

/** spaceView is what the screens draw, from this module's state and the store's limits. */
export function spaceView(s: SpaceSnapshot, limits: StoreLimits | null): SpaceView {
  const on = limits !== null && limits.enabled;
  const view: SpaceView = {
    storeNearlyFull: false,
    storeUsedBytes: on ? limits.usedBytes : 0,
    storeMaxBytes: on ? limits.maxBytes : 0,
    storeUsedItems: on ? limits.usedItems : 0,
    storeMaxItems: on ? limits.maxItems : 0,
    evicted: 0,
    failure: s.failure,
    failureSeq: s.failureSeq,
  };
  if (s.hideBanners) {
    return view;
  }
  const closed =
    s.closedAt !== null && on && s.closedAt.bytes === limits.usedBytes && s.closedAt.items === limits.usedItems;
  view.storeNearlyFull = on && nearlyFull(limits) && !closed;
  view.evicted = s.evicted;
  return view;
}

/** noteEvicted counts an entry the relay removed early to make room. */
export function noteEvicted(): void {
  set({ evicted: state.evicted + 1 });
}

/** noteFailure records a refusal for the popup. */
export function noteFailure(kind: SpaceFailure): void {
  set({ failure: kind, failureSeq: state.failureSeq + 1 });
}

/**
 * noteStoreFailure records a store save refused for lack of room, given the
 * limits read after it (null when they could not be read, which blames the
 * group's store: the common case, and the one the user can act on).
 */
export function noteStoreFailure(err: unknown, limits: StoreLimits | null): void {
  if (isQuotaExceeded(err)) {
    noteFailure(limits !== null ? storeFullCause(limits) : 'store_full');
  }
}

/**
 * noteUploadFailure records a history upload refused for lack of room.
 * History evicts old entries for a new one, so this is the relay's own
 * trouble, not the group's.
 */
export function noteUploadFailure(err: unknown): void {
  if (isQuotaExceeded(err)) {
    noteFailure('relay_full');
  }
}

/** dismissSpace closes a banner, or the popup, until it is next triggered. */
export function dismissSpace(banner: SpaceBanner, limits: StoreLimits | null): void {
  switch (banner) {
    case 'store':
      set({ closedAt: limits !== null ? { bytes: limits.usedBytes, items: limits.usedItems } : null });
      return;
    case 'evicted':
      set({ evicted: 0 });
      return;
    case 'failure':
      set({ failure: null });
      return;
  }
}

/** setHideBanners applies the "Hide out-of-space banners" setting. */
export function setHideBanners(on: boolean): void {
  set({ hideBanners: on });
}

/** resetSpace drops everything of the group in use: it is going away. */
export function resetSpace(): void {
  set({ evicted: 0, closedAt: null, failure: null });
}
