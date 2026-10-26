/**
 * The store (SPEC §6b, spec/crypto.md §7a): what a group keeps on purpose,
 * beside its 24-hour history. This module holds the types the client returns
 * and the StoreIndex a screen searches; the calls themselves are on Client,
 * mirroring `pkg/tppclient/store.go`.
 *
 * Searching, tag filters, sorting and duplicate checks all happen here, on the
 * device: the relay cannot search what it cannot read.
 */

import { toHex } from '../bytes';
import {
  STORE_MAX_DESCRIPTION,
  STORE_MAX_NAME,
  STORE_MAX_TAG,
  STORE_MAX_TAGS,
} from '../crypto';

/** The limits on what a user types, in bytes of UTF-8 (spec/crypto.md §7a.5). A UI shows them as counters. */
export const STORE_LIMITS = {
  name: STORE_MAX_NAME,
  description: STORE_MAX_DESCRIPTION,
  tags: STORE_MAX_TAGS,
  tag: STORE_MAX_TAG,
} as const;

/** StoreLimits is the group's store as the relay reports it, in bytes it counts. */
export interface StoreLimits {
  /** enabled is false while the admin has the store off or it is being emptied; everything else is then zero. */
  enabled: boolean;
  /** maxBytes is the cap the group runs into; zero means none. */
  maxBytes: number;
  usedBytes: number;
  /** availableBytes is the largest blob a save can still have. */
  availableBytes: number;
  maxItems: number;
  usedItems: number;
  maxItemBytes: number;
}

/** StoreDetails is what a user types about an item. */
export interface StoreDetails {
  name: string;
  description: string;
  /** tags is a set: none empty, none twice. */
  tags: string[];
}

/**
 * ItemSecrets are an item's keys and the values its blob is bound to. They
 * stay in memory with the item, for a fetch or an edit; they are never
 * persisted, logged or shown.
 */
export interface ItemSecrets {
  contentKey: Uint8Array;
  metaKey: Uint8Array;
  contentId: string;
  contentEpoch: bigint;
}

/** StoreItem is one item of the store, with its metadata opened. */
export interface StoreItem extends StoreDetails {
  id: string;
  savedAt: Date;
  contentType: string;
  filename: string;
  /** bodySize is the plaintext length. */
  bodySize: number;
  /** sha256Hex is the plaintext's hash, for spotting duplicates without fetching anything. */
  sha256Hex: string;
  /** blobSize is the ciphertext length, what the item costs on the relay. */
  blobSize: number;
  /** metaRev is the revision an edit starts from. */
  metaRev: bigint;
  /** wrapEpoch is the epoch the item's keys are wrapped at. */
  wrapEpoch: bigint;
  secrets: ItemSecrets;
}

/** Unreadable is an item this client listed but could not open, and why. A UI shows it with Delete. */
export interface Unreadable {
  id: string;
  savedAt: Date;
  reason: string;
}

/** StoreListing is the whole store, as far as this client can read it. */
export interface StoreListing {
  items: StoreItem[];
  unreadable: Unreadable[];
}

export type StoreSort = 'newest' | 'oldest' | 'name' | 'name-desc' | 'largest' | 'smallest';

/** StoreQuery selects and orders items. */
export interface StoreQuery {
  /** text matches items whose name or description contains every word of it, ignoring case. */
  text?: string;
  /** tags matches items carrying every one of them. */
  tags?: string[];
  sort?: StoreSort;
}

/** TagCount is a tag and how many items carry it. */
export interface TagCount {
  tag: string;
  count: number;
}

/** hashHex renders a SHA-256 the way StoreItem.sha256Hex holds it. */
export function hashHex(sum: Uint8Array): string {
  return toHex(sum);
}

const newestFirst = (a: StoreItem, b: StoreItem): number => {
  const d = b.savedAt.getTime() - a.savedAt.getTime();
  if (d !== 0) {
    return d;
  }
  return a.id < b.id ? 1 : a.id > b.id ? -1 : 0;
};

const byName = (a: StoreItem, b: StoreItem): number => {
  const x = a.name.toLowerCase();
  const y = b.name.toLowerCase();
  if (x !== y) {
    return x < y ? -1 : 1;
  }
  return newestFirst(a, b);
};

/**
 * StoreIndex is a group's store as one device sees it, kept in memory for the
 * Store screen. A screen fills it from listStore and keeps it current with the
 * store handlers and the results of its own saves, edits and deletes. It is
 * the TypeScript twin of `tppclient.StoreIndex`, and behaves the same.
 */
export class StoreIndex {
  private items = new Map<string, StoreItem>();

  /** reset replaces the whole index with a listing. */
  reset(items: StoreItem[]): void {
    this.items = new Map(items.map((it) => [it.id, it]));
  }

  /** put adds an item, or replaces it with a newer revision; an older one — a late event — is ignored. */
  put(it: StoreItem): void {
    const old = this.items.get(it.id);
    if (old !== undefined && old.metaRev > it.metaRev) {
      return;
    }
    this.items.set(it.id, it);
  }

  remove(itemId: string): void {
    this.items.delete(itemId);
  }

  get(itemId: string): StoreItem | undefined {
    return this.items.get(itemId);
  }

  get size(): number {
    return this.items.size;
  }

  /** query returns the items that match, in the order asked for. */
  query(q: StoreQuery = {}): StoreItem[] {
    const words = (q.text ?? '').toLowerCase().split(/\s+/).filter((w) => w !== '');
    const tags = q.tags ?? [];
    const out = [...this.items.values()].filter((it) => {
      const hay = `${it.name}\n${it.description}`.toLowerCase();
      return words.every((w) => hay.includes(w)) && tags.every((t) => it.tags.includes(t));
    });
    switch (q.sort ?? 'newest') {
      case 'oldest':
        return out.sort((a, b) => newestFirst(b, a));
      case 'name':
        return out.sort(byName);
      case 'name-desc':
        return out.sort((a, b) => byName(b, a));
      // By plaintext size; items of the same size newest first.
      case 'largest':
        return out.sort((a, b) => b.bodySize - a.bodySize || newestFirst(a, b));
      case 'smallest':
        return out.sort((a, b) => a.bodySize - b.bodySize || newestFirst(a, b));
      default:
        return out.sort(newestFirst);
    }
  }

  /** tags lists every tag in use with how many items carry it, by name. */
  tags(): TagCount[] {
    const counts = new Map<string, number>();
    for (const it of this.items.values()) {
      for (const t of it.tags) {
        counts.set(t, (counts.get(t) ?? 0) + 1);
      }
    }
    return [...counts.entries()]
      .map(([tag, count]) => ({ tag, count }))
      .sort((a, b) => (a.tag < b.tag ? -1 : a.tag > b.tag ? 1 : 0));
  }

  /** duplicates returns the items whose body has this hash, newest first. */
  duplicates(sha256Hex: string): StoreItem[] {
    return [...this.items.values()].filter((it) => it.sha256Hex === sha256Hex).sort(newestFirst);
  }
}
