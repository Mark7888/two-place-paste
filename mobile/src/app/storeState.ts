/**
 * The group's store on this phone (SPEC §6b): the opened items, kept in a
 * StoreIndex for the Store screen to search, and the History screen's
 * "saved" marks.
 *
 * Like `sessionStore` it is a module, not React state, so the client's store
 * events reach it whether or not the Store screen is mounted. It is listed the
 * first time a screen needs it and again after anything that may have made it
 * stale — a reconnect, which may have missed events, or the admin turning the
 * store on. It is never written to disk: the items carry their keys.
 */

import {
  StoreIndex,
  hashHex,
  isCode,
  isStoreDisabled,
  sha256Sliced,
  type Client,
  type StoreDetails,
  type StoreItem,
  type StoreLimits,
  type Unreadable,
} from '../core';
import { ErrorCode } from '../protocol/gen/tpp/v1/error';
import type { ClipboardItem } from '../platform';
import { failureMessage } from './session';

/** StoreStatus is where the store is: not asked yet, being listed, off, listed, or failed. */
export type StoreStatus = 'idle' | 'loading' | 'off' | 'ready' | 'error';

export interface StoreSnapshot {
  status: StoreStatus;
  limits: StoreLimits | null;
  unreadable: readonly Unreadable[];
  error: string;
  /** version changes whenever the index does, so a screen re-queries it. */
  version: number;
}

/** index is the opened store. Screens query it; only this module changes it. */
export const index = new StoreIndex();

let status: StoreStatus = 'idle';
let limits: StoreLimits | null = null;
let unreadable: Unreadable[] = [];
let error = '';
let version = 0;
let stale = true;
let loading: Promise<void> | null = null;
const listeners = new Set<() => void>();
let snapshot: StoreSnapshot = { status, limits, unreadable, error, version };

function emit(): void {
  version++;
  snapshot = { status, limits, unreadable, error, version };
  for (const listener of listeners) {
    listener();
  }
}

export function subscribeStore(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function getStoreSnapshot(): StoreSnapshot {
  return snapshot;
}

/** invalidate makes the next load list the store again. */
export function invalidate(): void {
  stale = true;
  emit();
}

/** clearStore forgets everything: this device left its group. */
export function clearStore(): void {
  index.reset([]);
  unreadable = [];
  limits = null;
  status = 'idle';
  stale = true;
  emit();
}

/** The client's store events, wired in `sessionStore`. */
export const storeEvents = {
  put: (item: StoreItem): void => {
    if (status === 'ready') {
      index.put(item);
      emit();
    }
  },
  remove: (itemId: string): void => {
    index.remove(itemId);
    unreadable = unreadable.filter((u) => u.id !== itemId);
    emit();
  },
  state: (): void => {
    invalidate();
  },
};

/**
 * loadStore asks for the limits — cheap, and what says whether the store is on
 * — and lists the store when it is stale. Concurrent calls share one listing.
 */
export function loadStore(client: Client, force = false): Promise<void> {
  if (force) {
    stale = true;
  }
  if (loading !== null) {
    return loading;
  }
  loading = (async () => {
    status = status === 'ready' ? 'ready' : 'loading';
    emit();
    try {
      await client.connect();
      limits = await client.storeLimits();
      if (!limits.enabled) {
        index.reset([]);
        unreadable = [];
        status = 'off';
        stale = true;
        return;
      }
      if (stale || status !== 'ready') {
        const listing = await client.listStore();
        index.reset(listing.items);
        unreadable = listing.unreadable;
        stale = false;
      }
      status = 'ready';
      error = '';
    } catch (err) {
      if (isStoreDisabled(err)) {
        status = 'off';
      } else {
        status = 'error';
        error = failureMessage(err);
      }
    } finally {
      loading = null;
      emit();
    }
  })();
  return loading;
}

/** DuplicateError refuses a save whose body the store already holds, naming the items. */
export class DuplicateError extends Error {
  readonly duplicates: StoreItem[];

  constructor(duplicates: StoreItem[]) {
    super('The store already holds this content.');
    this.name = 'DuplicateError';
    this.duplicates = duplicates;
  }
}

/**
 * saveItem saves something to the store, refusing a body it already holds
 * unless the user said "Save anyway".
 */
export async function saveItem(
  client: Client,
  item: ClipboardItem,
  details: StoreDetails,
  allowDuplicate: boolean,
): Promise<StoreItem> {
  await loadStore(client);
  if (!allowDuplicate) {
    const dups = index.duplicates(hashHex(await sha256Sliced(item.body)));
    if (dups.length > 0) {
      throw new DuplicateError(dups);
    }
  }
  const saved = await client.saveToStore(item, details);
  index.put(saved);
  limits = await client.storeLimits().catch(() => limits);
  emit();
  return saved;
}

/** editItem edits an item; an edit from a stale version refreshes the list and says so. */
export async function editItem(client: Client, item: StoreItem, details: StoreDetails): Promise<StoreItem> {
  try {
    const edited = await client.editStoreItem(item, details);
    index.put(edited);
    emit();
    return edited;
  } catch (err) {
    if (isCode(err, ErrorCode.ERROR_CODE_REVISION_CONFLICT)) {
      void loadStore(client, true);
      throw new Error('Another device edited this item first. The list now shows its version.', { cause: err });
    }
    throw err;
  }
}

/** deleteItem deletes an item for every device; one already gone is simply gone. */
export async function deleteItem(client: Client, itemId: string): Promise<void> {
  try {
    await client.deleteStoreItem(itemId);
  } catch (err) {
    if (!isCode(err, ErrorCode.ERROR_CODE_NOT_FOUND)) {
      throw err;
    }
  }
  storeEvents.remove(itemId);
  limits = await client.storeLimits().catch(() => limits);
  emit();
}

/** savedHash reports whether the store holds a body with this hash — the History's "saved" mark. */
export function savedHash(sha256Hex: string | undefined): boolean {
  return sha256Hex !== undefined && status === 'ready' && index.duplicates(sha256Hex).length > 0;
}
