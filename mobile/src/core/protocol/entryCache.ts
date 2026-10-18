/**
 * A short-lived cache of downloaded entries, so opening the same large entry
 * twice does not download it twice.
 *
 * What is cached is the entry as the relay serves it — the sealed container,
 * still encrypted under the group key — never the plaintext. Reading it back
 * goes through the same decryption and epoch checks as a fresh download, so a
 * cached entry from before a rekey is refused exactly as the relay's copy
 * would be, and a damaged file fails to decrypt rather than being trusted.
 *
 * An entry is kept for at most `ttlMs` (an hour by default) and never past
 * the relay's own expiry for it; the whole cache is kept under `maxBytes`,
 * oldest first out. The storage underneath is a `CacheStore` — on Android,
 * files in the app's cache directory, which the OS may also clear at will.
 */

import { fromUTF8, utf8 } from '../bytes';
import type { EntryHeaderView, EntryMeta } from './client';

/** CacheStore is the storage a cache sits on: named files of bytes. */
export interface CacheStore {
  /** read returns a file's bytes, or null when there is no such file. */
  read(name: string): Promise<Uint8Array | null>;
  write(name: string, data: Uint8Array): Promise<void>;
  /** remove deletes a file; removing one that is not there is not an error. */
  remove(name: string): Promise<void>;
  /** list names every file, with its size in bytes. */
  list(): Promise<{ name: string; size: number }[]>;
}

/** CachedEntry is what a download produced: the relay's metadata and the sealed container. */
export interface CachedEntry {
  meta: EntryMeta;
  ciphertext: Uint8Array;
}

/** DEFAULT_TTL_MS is how long a downloaded entry is kept: an hour. */
export const DEFAULT_TTL_MS = 60 * 60 * 1000;

/** DEFAULT_MAX_BYTES bounds the whole cache: four entries at the relay's default 50 MiB cap. */
export const DEFAULT_MAX_BYTES = 200 << 20;

/** Index is the record kept beside each entry's bytes. */
interface Index {
  savedAt: number;
  keepUntil: number;
  size: number;
  meta: {
    id: string;
    epoch: string;
    size: number;
    createdAt: number;
    expiresAt: number;
    inline: boolean;
    header: EntryHeaderView | null;
  };
}

/** SAFE_ID is what an entry id must look like to become part of a file name. */
const SAFE_ID = /^[A-Za-z0-9_-]{1,128}$/;

export class EntryCache {
  private readonly ttlMs: number;
  private readonly maxBytes: number;
  private readonly now: () => number;

  constructor(
    private readonly store: CacheStore,
    options: { ttlMs?: number; maxBytes?: number; now?: () => number } = {},
  ) {
    this.ttlMs = options.ttlMs ?? DEFAULT_TTL_MS;
    this.maxBytes = options.maxBytes ?? DEFAULT_MAX_BYTES;
    this.now = options.now ?? (() => Date.now());
  }

  /** get returns a cached entry that is still fresh, or null. */
  async get(entryId: string): Promise<CachedEntry | null> {
    if (!SAFE_ID.test(entryId)) {
      return null;
    }
    const index = await this.readIndex(entryId);
    if (index === null) {
      return null;
    }
    if (this.now() >= index.keepUntil) {
      await this.remove(entryId);
      return null;
    }
    const ciphertext = await this.store.read(dataName(entryId));
    if (ciphertext === null || ciphertext.length !== index.size) {
      // Half a pair, or a file the OS truncated: neither is worth trusting.
      await this.remove(entryId);
      return null;
    }
    const m = index.meta;
    return {
      meta: {
        id: m.id,
        epoch: BigInt(m.epoch),
        size: m.size,
        createdAt: new Date(m.createdAt),
        expiresAt: new Date(m.expiresAt),
        inline: m.inline,
        header: m.header,
      },
      ciphertext,
    };
  }

  /**
   * put keeps an entry. The bytes are written before the index, so a crash in
   * between leaves bytes with no index — which `prune` clears — and never an
   * index pointing at nothing.
   */
  async put(entry: CachedEntry): Promise<void> {
    const id = entry.meta.id;
    if (!SAFE_ID.test(id) || entry.ciphertext.length > this.maxBytes) {
      return;
    }
    const savedAt = this.now();
    const expires = entry.meta.expiresAt.getTime();
    const keepUntil = Math.min(savedAt + this.ttlMs, expires > 0 ? expires : Infinity);
    if (keepUntil <= savedAt) {
      return;
    }
    const index: Index = {
      savedAt,
      keepUntil,
      size: entry.ciphertext.length,
      meta: {
        id,
        epoch: entry.meta.epoch.toString(),
        size: entry.meta.size,
        createdAt: entry.meta.createdAt.getTime(),
        expiresAt: entry.meta.expiresAt.getTime(),
        inline: entry.meta.inline,
        header: entry.meta.header,
      },
    };
    // Room first, so the cache never sits over its bound with this entry in it.
    await this.prune(entry.ciphertext.length, id);
    await this.store.write(dataName(id), entry.ciphertext);
    await this.store.write(indexName(id), utf8(JSON.stringify(index)));
  }

  /** remove drops one entry: the relay evicted it, or it failed to open. */
  async remove(entryId: string): Promise<void> {
    if (!SAFE_ID.test(entryId)) {
      return;
    }
    await this.store.remove(indexName(entryId));
    await this.store.remove(dataName(entryId));
  }

  /** clear drops everything: the device left its group. */
  async clear(): Promise<void> {
    for (const file of await this.store.list()) {
      await this.store.remove(file.name);
    }
  }

  /**
   * prune drops what has expired, what is incomplete, and then the oldest
   * entries until `incoming` more bytes fit under the bound. `replacing` is an
   * entry about to be rewritten, whose current bytes do not count.
   */
  async prune(incoming = 0, replacing = ''): Promise<void> {
    const files = await this.store.list();
    const sizes = new Map(files.map((f) => [f.name, f.size]));
    const now = this.now();
    const live: { id: string; savedAt: number; bytes: number }[] = [];
    for (const file of files) {
      const id = idOf(file.name);
      if (id === null) {
        // Not this cache's: nothing else writes here, so it is left over.
        await this.store.remove(file.name);
        continue;
      }
      if (!file.name.endsWith(INDEX_SUFFIX)) {
        if (!sizes.has(indexName(id))) {
          await this.store.remove(file.name);
        }
        continue;
      }
      const index = await this.readIndex(id);
      if (index === null || now >= index.keepUntil || id === replacing || !sizes.has(dataName(id))) {
        await this.remove(id);
        continue;
      }
      live.push({ id, savedAt: index.savedAt, bytes: sizes.get(dataName(id)) ?? 0 });
    }
    live.sort((a, b) => a.savedAt - b.savedAt);
    let total = live.reduce((sum, e) => sum + e.bytes, 0);
    for (const e of live) {
      if (total + incoming <= this.maxBytes) {
        break;
      }
      await this.remove(e.id);
      total -= e.bytes;
    }
  }

  private async readIndex(entryId: string): Promise<Index | null> {
    const raw = await this.store.read(indexName(entryId));
    if (raw === null) {
      return null;
    }
    try {
      const index = JSON.parse(fromUTF8(raw)) as Index;
      if (
        typeof index.keepUntil !== 'number' ||
        typeof index.size !== 'number' ||
        index.meta?.id !== entryId
      ) {
        return null;
      }
      return index;
    } catch {
      return null;
    }
  }
}

const DATA_SUFFIX = '.bin';
const INDEX_SUFFIX = '.json';

function dataName(id: string): string {
  return id + DATA_SUFFIX;
}

function indexName(id: string): string {
  return id + INDEX_SUFFIX;
}

/** idOf returns the entry id a cache file belongs to, or null for a file that is not the cache's. */
function idOf(name: string): string | null {
  const suffix = name.endsWith(DATA_SUFFIX) ? DATA_SUFFIX : name.endsWith(INDEX_SUFFIX) ? INDEX_SUFFIX : null;
  if (suffix === null) {
    return null;
  }
  const id = name.slice(0, -suffix.length);
  return SAFE_ID.test(id) ? id : null;
}
