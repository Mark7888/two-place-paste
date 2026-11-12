/**
 * Downloads whose bytes never enter JavaScript (`getEntryStaged`,
 * `fetchStoreItemStaged`), driven against the in-process relay with stand-ins
 * for the platform: a socket that captures range replies the way
 * `CaptureHandler.kt` does, and a sink of staged files that opens with
 * `@noble/ciphers` the way `XChaCha20Poly1305Opener.java` must.
 */

import { xchacha20poly1305 } from '@noble/ciphers/chacha.js';
import { sha256 } from '@noble/hashes/sha2.js';

import { EntryFetchChunkResponse } from '../../../protocol/gen/tpp/v1/entry';
import { Envelope, MessageType } from '../../../protocol/gen/tpp/v1/envelope';
import { StoreFetchChunkResponse } from '../../../protocol/gen/tpp/v1/store';
import { concat, fromUTF8, utf8 } from '../../bytes';
import { decodeFrameHead, encodeFrame } from '../../crypto';
import { FakeRelay, MemoryStore } from '../../demo';
import { Client, isStaged, type DownloadSink, type StagedItem } from '../client';
import type { SocketFactory } from '../connection';

const MiB = 1 << 20;

/** files holds the staged files, by token, as the platform's cache does. */
function platform(now: () => number = () => Date.now()) {
  const files = new Map<string, Uint8Array>();
  /** cache is the sealed cache: key to a kept container's token, what came with it, and until when. */
  const cache = new Map<string, { token: string; info: string; until: number }>();
  const kept = () => new Set([...cache.values()].map((c) => c.token));
  const calls = { captured: 0, wholeRanges: 0, opened: 0 };
  let next = 1;
  const writeAt = (token: string, offset: number, data: Uint8Array) => {
    const old = files.get(token);
    if (old === undefined) {
      throw new Error('no such staged file');
    }
    const grown = new Uint8Array(Math.max(old.length, offset + data.length));
    grown.set(old);
    grown.set(data, offset);
    files.set(token, grown);
  };

  const sink: DownloadSink = {
    create: async () => {
      const token = `rx-${next++}`;
      files.set(token, new Uint8Array(0));
      return token;
    },
    write: async (token, offset, data) => {
      calls.wholeRanges++;
      writeAt(token, offset, data);
    },
    read: async (token, offset, length) => (files.get(token) ?? new Uint8Array(0)).slice(offset, offset + length),
    open: async (token, { key, nonce, aad }) => {
      calls.opened++;
      const container = files.get(token);
      if (container === undefined || !nonce.every((b, i) => container[1 + i] === b)) {
        throw new Error('no such container, or another nonce');
      }
      // Throws "invalid tag" on a container that does not open, before anything is written.
      const plain = xchacha20poly1305(key, nonce, aad).decrypt(container.subarray(25));
      const out = `plain-${next++}`;
      files.set(out, plain);
      return { token: out, size: plain.length, head: plain.slice(0, 131091) };
    },
    digest: async (token, offset) => sha256((files.get(token) ?? new Uint8Array(0)).subarray(offset)),
    discard: (token) => {
      // A container moved into the cache is not the caller's to delete.
      if (!kept().has(token)) {
        files.delete(token);
      }
    },
    cached: async (key) => {
      const c = cache.get(key);
      if (c === undefined || now() >= c.until || !files.has(c.token)) {
        return null;
      }
      return { token: c.token, size: files.get(c.token)!.length, info: c.info };
    },
    keep: async (key, token, info, until) => {
      cache.set(key, { token, info, until });
    },
    forget: (key) => {
      const c = cache.get(key);
      cache.delete(key);
      if (c !== undefined) {
        files.delete(c.token);
      }
    },
  };

  /** capturing adds `capture` to a socket factory, splitting armed replies as CaptureHandler.kt does. */
  const capturing =
    (inner: SocketFactory): SocketFactory =>
    (url) => {
      const socket = inner(url);
      const arms = new Map<string, { type: number; token: string; offset: number; resolve: (n: number | null) => void }>();
      let handler: (frame: Uint8Array) => void = () => {};
      socket.onMessage((frame) => {
        const env = Envelope.decode(frame);
        const arm = arms.get(env.id);
        if (arm !== undefined) {
          arms.delete(env.id);
          if (Number(env.type) === arm.type) {
            const codec =
              arm.type === Number(MessageType.MESSAGE_TYPE_ENTRY_FETCH_CHUNK_RESPONSE) ? EntryFetchChunkResponse : StoreFetchChunkResponse;
            // Both replies have their data in field 3; the codecs agree on everything else.
            const reply = (codec as typeof EntryFetchChunkResponse).decode(env.payload);
            writeAt(arm.token, arm.offset, reply.data);
            calls.captured++;
            const stripped = (codec as typeof EntryFetchChunkResponse).encode({ ...reply, data: new Uint8Array(0) }).finish();
            arm.resolve(reply.data.length);
            handler(Envelope.encode({ ...env, payload: stripped }).finish());
            return;
          }
          arm.resolve(null);
        }
        handler(frame);
      });
      return {
        ...socket,
        onMessage: (h) => {
          handler = h;
        },
        capture: (id, type, target) =>
          new Promise<number | null>((resolve) => {
            arms.set(id, { type, ...target, resolve });
          }),
        releaseCapture: (id) => {
          arms.get(id)?.resolve(null);
          arms.delete(id);
        },
      };
    };
  return { files, calls, sink, capturing, cache };
}

function chunkingRelay(): FakeRelay {
  return new FakeRelay({
    limits: {
      maxEntryBytes: (32 * MiB).toString(),
      singleFrameMaxBytes: (10 * MiB).toString(),
      chunkBytes: MiB.toString(),
      inlineMaxBytes: (64 * 1024).toString(),
      headerV2: true,
    },
    store: { maxBytes: 64 * MiB, maxItems: 10, maxItemBytes: 32 * MiB },
  });
}

const opened: Client[] = [];
afterEach(() => {
  while (opened.length > 0) {
    opened.pop()?.disconnect();
  }
});

async function open(relay: FakeRelay, name: string, socketFactory: SocketFactory = relay.factory): Promise<Client> {
  const client = await Client.open({
    store: new MemoryStore(),
    deviceName: name,
    socketFactory,
    healthCheck: async () => false,
    deviceStatus: relay.deviceStatus,
  });
  opened.push(client);
  return client;
}

async function pair(inviter: Client, joiner: Client): Promise<void> {
  const invitation = await inviter.startPairing();
  await joiner.joinPairing(invitation.payload);
  await invitation.joined;
}

/** body is larger than one frame and not a whole number of chunks. */
const body = Uint8Array.from({ length: 11 * MiB + 4321 }, (_, i) => (i * 11 + 1) & 0xff);

/** bodyOf reads a staged item's body out of the fake platform. */
const bodyOf = (files: Map<string, Uint8Array>, item: StagedItem) =>
  (files.get(item.staged.token) ?? new Uint8Array(0)).subarray(item.staged.offset, item.staged.offset + item.staged.size);

describe('decodeFrameHead', () => {
  test.each([
    ['a file', { contentType: 'application/pdf', filename: 'a.pdf' }],
    ['text', { contentType: 'text/plain; charset=utf-8', filename: '' }],
    ['a long name', { contentType: 'image/png', filename: 'é'.repeat(3000) }],
  ])('reads the head of %s exactly as encodeFrame wrote it', (_name, f) => {
    const frame = encodeFrame({ ...f, createdAt: 1234567n, body: new Uint8Array(500).fill(3) });
    const head = decodeFrameHead(frame);
    expect(head).toEqual({ ...f, createdAt: 1234567n, bodyLength: 500, headLength: frame.length - 500 });
  });

  test('refuses a prefix too short to hold the head', () => {
    const frame = encodeFrame({ contentType: 'a/b', filename: 'x', createdAt: 0n, body: new Uint8Array(0) });
    expect(() => decodeFrameHead(frame.subarray(0, frame.length - 1))).toThrow('length prefix exceeds');
  });
});

describe('a staged entry download', () => {
  test('captures every range into the staged file and opens it there', async () => {
    const relay = chunkingRelay();
    const p = platform();
    const laptop = await open(relay, 'laptop');
    await laptop.createGroup(relay.creationURL);
    const phone = await open(relay, 'phone', p.capturing(relay.factory));
    await pair(laptop, phone);
    const meta = await laptop.putEntry({ contentType: 'application/octet-stream', filename: 'big.bin', body });

    const got = await phone.getEntryStaged(meta.id, meta, p.sink);
    expect(isStaged(got)).toBe(true);
    const item = got as StagedItem;
    expect(item.filename).toBe('big.bin');
    expect(item.contentType).toBe('application/octet-stream');
    expect(Buffer.from(bodyOf(p.files, item)).equals(Buffer.from(body))).toBe(true);
    // Twelve 1 MiB ranges, all captured; none came through JavaScript.
    expect(p.calls.captured).toBe(12);
    expect(p.calls.wholeRanges).toBe(0);
    // The opened frame is left until the caller releases it, and the sealed
    // container is kept in the cache.
    expect(p.files.has(item.staged.token)).toBe(true);
    expect(p.cache.get(`entry:${meta.id}`)).toBeDefined();
    item.staged.release();
    expect([...p.files.keys()]).toEqual([p.cache.get(`entry:${meta.id}`)!.token]);
  });

  test('writes the ranges itself when the socket cannot capture', async () => {
    const relay = chunkingRelay();
    const p = platform();
    const laptop = await open(relay, 'laptop');
    await laptop.createGroup(relay.creationURL);
    const phone = await open(relay, 'phone');
    await pair(laptop, phone);
    const meta = await laptop.putEntry({ contentType: 'application/octet-stream', filename: 'big.bin', body });

    const item = (await phone.getEntryStaged(meta.id, meta, p.sink)) as StagedItem;
    expect(Buffer.from(bodyOf(p.files, item)).equals(Buffer.from(body))).toBe(true);
    expect(p.calls.wholeRanges).toBe(12);
    expect(p.calls.captured).toBe(0);
  });

  test('refuses a container that does not open, and leaves nothing behind', async () => {
    const relay = chunkingRelay();
    const p = platform();
    const laptop = await open(relay, 'laptop');
    await laptop.createGroup(relay.creationURL);
    const phone = await open(relay, 'phone', p.capturing(relay.factory));
    await pair(laptop, phone);
    const meta = await laptop.putEntry({ contentType: 'text/plain', filename: 'f.txt', body: utf8('x'.repeat(3 * MiB)) });
    // The platform flips one byte of the container as it opens it.
    const sink: DownloadSink = {
      ...p.sink,
      open: async (token, params) => {
        const c = p.files.get(token)!;
        c[100] ^= 1;
        return p.sink.open(token, params);
      },
    };
    await expect(phone.getEntryStaged(meta.id, meta, sink)).rejects.toThrow('invalid tag');
    expect(p.files.size).toBe(0);
  });

  test('applies the epoch rules before opening anything', async () => {
    const relay = chunkingRelay();
    const p = platform();
    const laptop = await open(relay, 'laptop', p.capturing(relay.factory));
    await laptop.createGroup(relay.creationURL);
    const phone = await open(relay, 'phone');
    await pair(laptop, phone);
    const meta = await laptop.putEntry({ contentType: 'text/plain', filename: 'f.txt', body: utf8('y'.repeat(2 * MiB)) });
    // The laptop re-keys past the entry, which is now from an older epoch.
    await (await laptop.prepareRevoke(phone.deviceId)).confirm();
    expect(laptop.epoch).toBe(2n);
    await expect(laptop.getEntryStaged(meta.id, meta, p.sink)).rejects.toMatchObject({ kind: 'stale-entry' });
    expect(p.calls.opened).toBe(0);
    expect(p.files.size).toBe(0);
  });
});

describe('a staged store item download', () => {
  test('opens and checks the item where it is staged', async () => {
    const relay = chunkingRelay();
    const p = platform();
    const laptop = await open(relay, 'laptop', p.capturing(relay.factory));
    await laptop.createGroup(relay.creationURL);
    const saved = await laptop.saveToStore(
      { contentType: 'application/octet-stream', filename: 'kept.bin', body },
      { name: 'Kept', description: '', tags: [] },
    );
    const item = await laptop.fetchStoreItemStaged(saved, p.sink);
    expect(item.filename).toBe('kept.bin');
    expect(Buffer.from(bodyOf(p.files, item)).equals(Buffer.from(body))).toBe(true);
    // Every range the relay returned was captured; none came through JavaScript.
    expect(p.calls.captured).toBeGreaterThan(1);
    expect(p.calls.wholeRanges).toBe(0);
    item.staged.release();
    // Only the sealed container is left, in the cache.
    expect([...p.files.keys()]).toEqual([p.cache.get(`store:${saved.id}`)!.token]);
  });

  test('refuses an item whose body is not the one its metadata names', async () => {
    const relay = chunkingRelay();
    const p = platform();
    const laptop = await open(relay, 'laptop', p.capturing(relay.factory));
    await laptop.createGroup(relay.creationURL);
    const saved = await laptop.saveToStore(
      { contentType: 'text/plain', filename: 'k.txt', body: utf8('kept'), sha256: new Uint8Array(32).fill(7) },
      { name: 'K', description: '', tags: [] },
    );
    await expect(laptop.fetchStoreItemStaged(saved, p.sink)).rejects.toMatchObject({ kind: 'store-unreadable' });
    expect(p.files.size).toBe(0);
  });

  test('reads the same body as the in-memory fetch', async () => {
    const relay = chunkingRelay();
    const p = platform();
    const laptop = await open(relay, 'laptop', p.capturing(relay.factory));
    await laptop.createGroup(relay.creationURL);
    const saved = await laptop.saveToStore(
      { contentType: 'text/plain', filename: 'n.txt', body: utf8('note') },
      { name: 'N', description: '', tags: [] },
    );
    const staged = await laptop.fetchStoreItemStaged(saved, p.sink);
    const memory = await laptop.fetchStoreItem(saved);
    expect(fromUTF8(bodyOf(p.files, staged))).toBe(fromUTF8(memory.body));
    expect(concat(utf8('note')).length).toBe(staged.staged.size);
  });
});

describe('the sealed cache', () => {
  test('a second read of an entry opens the kept container, without the relay', async () => {
    const relay = chunkingRelay();
    const p = platform();
    const laptop = await open(relay, 'laptop');
    await laptop.createGroup(relay.creationURL);
    const phone = await open(relay, 'phone', p.capturing(relay.factory));
    await pair(laptop, phone);
    const meta = await laptop.putEntry({ contentType: 'application/octet-stream', filename: 'big.bin', body });

    const first = (await phone.getEntryStaged(meta.id, meta, p.sink)) as StagedItem;
    first.staged.release();
    const ranges = p.calls.captured;
    // No hint this time: what the cache kept says what the entry is.
    const second = (await phone.getEntryStaged(meta.id, undefined, p.sink)) as StagedItem;
    expect(p.calls.captured).toBe(ranges);
    expect(p.calls.opened).toBe(2);
    expect(second.filename).toBe('big.bin');
    expect(Buffer.from(bodyOf(p.files, second)).equals(Buffer.from(body))).toBe(true);
    second.staged.release();
  });

  test('a store item is kept the same way', async () => {
    const relay = chunkingRelay();
    const p = platform();
    const laptop = await open(relay, 'laptop', p.capturing(relay.factory));
    await laptop.createGroup(relay.creationURL);
    const saved = await laptop.saveToStore(
      { contentType: 'application/octet-stream', filename: 'kept.bin', body },
      { name: 'Kept', description: '', tags: [] },
    );
    (await laptop.fetchStoreItemStaged(saved, p.sink)).staged.release();
    const ranges = p.calls.captured;
    const again = await laptop.fetchStoreItemStaged(saved, p.sink);
    expect(p.calls.captured).toBe(ranges);
    expect(Buffer.from(bodyOf(p.files, again)).equals(Buffer.from(body))).toBe(true);
    again.staged.release();
  });

  test('a kept container that does not open is dropped and the entry read again', async () => {
    const relay = chunkingRelay();
    const p = platform();
    const laptop = await open(relay, 'laptop');
    await laptop.createGroup(relay.creationURL);
    const phone = await open(relay, 'phone', p.capturing(relay.factory));
    await pair(laptop, phone);
    const meta = await laptop.putEntry({ contentType: 'application/octet-stream', filename: 'big.bin', body });
    (await phone.getEntryStaged(meta.id, meta, p.sink) as StagedItem).staged.release();
    const keptToken = p.cache.get(`entry:${meta.id}`)!.token;
    p.files.get(keptToken)![5000] ^= 1;
    const ranges = p.calls.captured;

    const item = (await phone.getEntryStaged(meta.id, meta, p.sink)) as StagedItem;
    expect(Buffer.from(bodyOf(p.files, item)).equals(Buffer.from(body))).toBe(true);
    expect(p.calls.captured).toBeGreaterThan(ranges);
    expect(p.files.has(keptToken)).toBe(false);
    item.staged.release();
  });

  test('an expired container is not used', async () => {
    let clock = Date.now();
    const relay = chunkingRelay();
    const p = platform(() => clock);
    const laptop = await open(relay, 'laptop');
    await laptop.createGroup(relay.creationURL);
    const phone = await open(relay, 'phone', p.capturing(relay.factory));
    await pair(laptop, phone);
    const meta = await laptop.putEntry({ contentType: 'application/octet-stream', filename: 'big.bin', body });
    (await phone.getEntryStaged(meta.id, meta, p.sink) as StagedItem).staged.release();
    expect(p.cache.get(`entry:${meta.id}`)!.until).toBeLessThanOrEqual(Date.now() + 60 * 60 * 1000 + 1000);
    clock += 61 * 60 * 1000;
    const ranges = p.calls.captured;
    (await phone.getEntryStaged(meta.id, meta, p.sink) as StagedItem).staged.release();
    expect(p.calls.captured).toBeGreaterThan(ranges);
  });

  test('a kept entry from before a re-key is refused, not read again', async () => {
    const relay = chunkingRelay();
    const p = platform();
    const laptop = await open(relay, 'laptop', p.capturing(relay.factory));
    await laptop.createGroup(relay.creationURL);
    const phone = await open(relay, 'phone');
    await pair(laptop, phone);
    const meta = await laptop.putEntry({ contentType: 'application/octet-stream', filename: 'big.bin', body });
    (await laptop.getEntryStaged(meta.id, meta, p.sink) as StagedItem).staged.release();
    await (await laptop.prepareRevoke(phone.deviceId)).confirm();
    const ranges = p.calls.captured;
    await expect(laptop.getEntryStaged(meta.id, meta, p.sink)).rejects.toMatchObject({ kind: 'stale-entry' });
    expect(p.calls.captured).toBe(ranges);
  });
});
