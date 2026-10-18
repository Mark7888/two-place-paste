import { EntryCache, type CacheStore } from '../entryCache';
import type { EntryMeta } from '../client';

/** MemoryStore is a CacheStore over a Map, standing in for the app's cache directory. */
class MemoryStore implements CacheStore {
  readonly files = new Map<string, Uint8Array>();
  async read(name: string) {
    return this.files.get(name) ?? null;
  }
  async write(name: string, data: Uint8Array) {
    this.files.set(name, data.slice());
  }
  async remove(name: string) {
    this.files.delete(name);
  }
  async list() {
    return [...this.files].map(([name, data]) => ({ name, size: data.length }));
  }
}

const HOUR = 60 * 60 * 1000;

function meta(id: string, size: number, expiresAt = 24 * HOUR): EntryMeta {
  return {
    id,
    epoch: 3n,
    size,
    createdAt: new Date(1000),
    expiresAt: new Date(expiresAt),
    inline: false,
    header: { contentType: 'application/pdf', filename: 'a.pdf', size: size - 40 },
  };
}

function bytes(n: number, seed = 1): Uint8Array {
  return Uint8Array.from({ length: n }, (_, i) => (i * seed + 3) & 0xff);
}

describe('EntryCache', () => {
  let clock = 0;
  const now = () => clock;
  beforeEach(() => {
    clock = 0;
  });

  it('gives back what was put, metadata and bytes, for an hour', async () => {
    const cache = new EntryCache(new MemoryStore(), { now });
    const m = meta('entry-1', 100);
    await cache.put({ meta: m, ciphertext: bytes(100) });

    clock = HOUR - 1;
    const got = await cache.get('entry-1');
    expect(got?.ciphertext).toEqual(bytes(100));
    expect(got?.meta).toEqual(m);

    clock = HOUR;
    expect(await cache.get('entry-1')).toBeNull();
  });

  it('never keeps an entry past the relay’s own expiry for it', async () => {
    const cache = new EntryCache(new MemoryStore(), { now });
    await cache.put({ meta: meta('soon', 10, 1000), ciphertext: bytes(10) });
    clock = 999;
    expect(await cache.get('soon')).not.toBeNull();
    clock = 1000;
    expect(await cache.get('soon')).toBeNull();
  });

  it('drops the oldest entries to stay under its bound', async () => {
    const store = new MemoryStore();
    const cache = new EntryCache(store, { now, maxBytes: 250 });
    for (const [i, id] of ['a', 'b', 'c'].entries()) {
      clock = i;
      await cache.put({ meta: meta(id, 100), ciphertext: bytes(100) });
    }
    expect(await cache.get('a')).toBeNull();
    expect(await cache.get('b')).not.toBeNull();
    expect(await cache.get('c')).not.toBeNull();
  });

  it('refuses a file whose size does not match its index, and forgets it', async () => {
    const store = new MemoryStore();
    const cache = new EntryCache(store, { now });
    await cache.put({ meta: meta('cut', 100), ciphertext: bytes(100) });
    store.files.set('cut.bin', bytes(60));
    expect(await cache.get('cut')).toBeNull();
    expect(store.files.size).toBe(0);
  });

  it('ignores ids that could not be file names, and clears leftovers', async () => {
    const store = new MemoryStore();
    const cache = new EntryCache(store, { now });
    await cache.put({ meta: meta('../escape', 10), ciphertext: bytes(10) });
    expect(store.files.size).toBe(0);
    store.files.set('orphan.bin', bytes(5));
    store.files.set('stray.tmp', bytes(5));
    await cache.prune();
    expect(store.files.size).toBe(0);
  });

  it('removes one entry, or all of them', async () => {
    const store = new MemoryStore();
    const cache = new EntryCache(store, { now });
    await cache.put({ meta: meta('x', 10), ciphertext: bytes(10) });
    await cache.put({ meta: meta('y', 10), ciphertext: bytes(10) });
    await cache.remove('x');
    expect(await cache.get('x')).toBeNull();
    expect(await cache.get('y')).not.toBeNull();
    await cache.clear();
    expect(store.files.size).toBe(0);
  });
});
