import { xchacha20poly1305 } from '@noble/ciphers/chacha.js';
import { sha256 } from '@noble/hashes/sha2.js';

import { Client, concat, FakeRelay, fromUTF8, MemoryStore, toHex, utf8, type SocketFactory } from '../../core';
import type { NativeSealPort, StagedFile } from '../../platform/types';
import { externalBody, selfTest, SELF_TEST_SIZES } from '../nativeSeal';

/**
 * fakePlatform stands in for the native side: staged copies by token, and a
 * sealer that is @noble/ciphers, optionally broken in a given way.
 */
function fakePlatform(broken?: 'flip' | 'throw') {
  const files = new Map<string, Uint8Array>();
  const discarded: string[] = [];
  let next = 1;
  const seal = (key: Uint8Array, nonce: Uint8Array, aad: Uint8Array, plain: Uint8Array): Uint8Array => {
    if (broken === 'throw') {
      throw new Error('no sealer');
    }
    const out = xchacha20poly1305(key, nonce, aad).encrypt(plain);
    if (broken === 'flip' && out.length > 20) {
      out[out.length - 20] ^= 1;
    }
    return out;
  };
  const port: NativeSealPort = {
    sealStaged: async (token, p) => {
      const file = files.get(token);
      if (file === undefined) {
        throw new Error('no such copy');
      }
      const sealed = concat(p.head, seal(p.key, p.nonce, p.aad, concat(p.frameHead, file)));
      const t = `sealed-${next++}`;
      files.set(t, sealed);
      return { token: t, size: sealed.length, head: sealed.slice(0, p.head.length) };
    },
    sealBytes: async (key, nonce, aad, plain) => seal(key, nonce, aad, plain),
  };
  const stage = (body: Uint8Array): StagedFile => {
    const token = `file-${next++}`;
    files.set(token, body);
    return { token, name: 'f.bin', contentType: 'application/octet-stream', size: body.length, sha256: toHex(sha256(body)) };
  };
  const calls = { readStaged: 0, readSlice: 0, spliced: 0 };
  const deps = {
    port,
    readSlice: async (token: string, offset: number, length: number) => {
      calls.readSlice++;
      const b = files.get(token);
      if (b === undefined) {
        throw new Error('no such copy');
      }
      return b.slice(offset, offset + length);
    },
    readStaged: async (f: StagedFile, onProgress: (done: number, total: number) => void) => {
      calls.readStaged++;
      const b = files.get(f.token);
      if (b === undefined) {
        throw new Error('no such copy');
      }
      onProgress(b.length, b.length);
      return b;
    },
    discard: (token: string) => {
      discarded.push(token);
      files.delete(token);
    },
    progress: () => {},
  };
  /** splicing wraps a socket factory with `sendSpliced`, reading the slice from the staged copies as the native side does. */
  const splicing =
    (inner: SocketFactory): SocketFactory =>
    (url) => {
      const socket = inner(url);
      return {
        ...socket,
        sendSpliced: async (head, slice) => {
          calls.spliced++;
          const b = files.get(slice.token);
          if (b === undefined || slice.offset + slice.length > b.length) {
            throw new Error('no such slice');
          }
          socket.send(concat(head, b.subarray(slice.offset, slice.offset + slice.length)));
        },
      };
    };
  return { port, stage, deps, files, discarded, calls, splicing };
}

const MiB = 1 << 20;

/** chunkingRelay takes large entries in 1 MiB chunks, as a real relay does. */
function chunkingRelay(): FakeRelay {
  return new FakeRelay({
    limits: {
      maxEntryBytes: (32 * MiB).toString(),
      singleFrameMaxBytes: (10 * MiB).toString(),
      chunkBytes: MiB.toString(),
      inlineMaxBytes: (64 * 1024).toString(),
      headerV2: true,
    },
    store: { maxBytes: 32 * MiB, maxItems: 10, maxItemBytes: 32 * MiB },
  });
}

describe('selfTest', () => {
  it('passes a sealer that agrees with @noble/ciphers at every size', async () => {
    expect(SELF_TEST_SIZES).toContain(65537);
    expect(await selfTest(fakePlatform().port)).toBe(true);
  });

  it.each([
    ['gets a byte wrong', 'flip' as const],
    ['cannot seal at all', 'throw' as const],
  ])('fails a sealer that %s, so JavaScript seals instead', async (_name, broken) => {
    expect(await selfTest(fakePlatform(broken).port)).toBe(false);
  });
});

describe('externalBody', () => {
  it('is null when the copy’s hash is not known, so the file is sealed in JavaScript', () => {
    const p = fakePlatform();
    const file = { ...p.stage(utf8('x')), sha256: '' };
    expect(externalBody(file, p.deps)).toBeNull();
  });

  it('writes an entry another device opens, and deletes the sealed copy', async () => {
    const relay = new FakeRelay();
    const opened: Client[] = [];
    const open = async (name: string) => {
      const c = await Client.open({
        store: new MemoryStore(),
        deviceName: name,
        socketFactory: relay.factory,
        healthCheck: async () => false,
        deviceStatus: relay.deviceStatus,
      });
      opened.push(c);
      return c;
    };
    try {
      const laptop = await open('laptop');
      await laptop.createGroup(relay.creationURL);

      const p = fakePlatform();
      const body = utf8('sealed natively, opened anywhere');
      const file = p.stage(body);
      const external = externalBody(file, p.deps);
      expect(external).not.toBeNull();
      await laptop.putEntry({ contentType: 'text/plain', filename: 'n.txt', external: external! });

      const got = await laptop.getLatest();
      expect(fromUTF8(got.body)).toBe('sealed natively, opened anywhere');
      expect(got.filename).toBe('n.txt');
      // A small entry is read in whole and sent in one frame. The sealed copy
      // is gone; the file's own copy is the job's to delete.
      expect(p.calls.readStaged).toBe(1);
      expect(p.discarded).toEqual(['sealed-2']);
      expect(p.files.has(file.token)).toBe(true);
    } finally {
      for (const c of opened) {
        c.disconnect();
      }
    }
  });
});

describe('a large staged upload', () => {
  const opened: Client[] = [];
  afterEach(() => {
    while (opened.length > 0) {
      opened.pop()?.disconnect();
    }
  });
  const open = async (relay: FakeRelay, name: string, socketFactory: SocketFactory) => {
    const c = await Client.open({
      store: new MemoryStore(),
      deviceName: name,
      socketFactory,
      healthCheck: async () => false,
      deviceStatus: relay.deviceStatus,
    });
    opened.push(c);
    return c;
  };
  /** body is larger than one frame, so it goes in chunks, and not a whole number of them. */
  const body = Uint8Array.from({ length: 11 * MiB + 12345 }, (_, i) => (i * 7 + 3) & 0xff);

  it.each([
    ['an entry', 'send' as const],
    ['a store item', 'store' as const],
  ])('goes as %s chunk by chunk from the staged copy, never read into JavaScript', async (_name, target) => {
    const relay = chunkingRelay();
    const p = fakePlatform();
    const laptop = await open(relay, 'laptop', p.splicing(relay.factory));
    await laptop.createGroup(relay.creationURL);
    const file = p.stage(body);
    const external = externalBody(file, p.deps)!;

    let got: Uint8Array;
    if (target === 'send') {
      await laptop.putEntry({ contentType: 'application/octet-stream', filename: 'big.bin', external });
      got = (await laptop.getLatest()).body;
    } else {
      const saved = await laptop.saveToStore(
        { contentType: 'application/octet-stream', filename: 'big.bin', external },
        { name: 'Big', description: '', tags: [] },
      );
      got = (await laptop.fetchStoreItem(saved)).body;
    }
    expect(got.length).toBe(body.length);
    expect(Buffer.from(got).equals(Buffer.from(body))).toBe(true);
    // 11 MiB and a bit, in 1 MiB chunks: twelve, all spliced.
    expect(p.calls.spliced).toBe(12);
    expect(p.calls.readSlice).toBe(0);
    expect(p.calls.readStaged).toBe(0);
    expect(p.discarded).toEqual(['sealed-2']);
  });

  it('reads each chunk in instead when the socket cannot splice', async () => {
    const relay = chunkingRelay();
    const p = fakePlatform();
    const laptop = await open(relay, 'laptop', relay.factory);
    await laptop.createGroup(relay.creationURL);
    const external = externalBody(p.stage(body), p.deps)!;
    await laptop.putEntry({ contentType: 'application/octet-stream', filename: 'big.bin', external });
    expect(Buffer.from((await laptop.getLatest()).body).equals(Buffer.from(body))).toBe(true);
    expect(p.calls.readSlice).toBe(12);
    expect(p.calls.spliced).toBe(0);
    expect(p.calls.readStaged).toBe(0);
  });

  it('deletes the sealed copy when the upload fails', async () => {
    const relay = chunkingRelay();
    const p = fakePlatform();
    const failing: SocketFactory = (url) => {
      const socket = p.splicing(relay.factory)(url);
      return { ...socket, sendSpliced: async () => Promise.reject(new Error('socket gone')) };
    };
    const laptop = await open(relay, 'laptop', failing);
    await laptop.createGroup(relay.creationURL);
    const external = externalBody(p.stage(body), p.deps)!;
    await expect(
      laptop.putEntry({ contentType: 'application/octet-stream', filename: 'big.bin', external }),
    ).rejects.toThrow('sending');
    expect(p.discarded).toEqual(['sealed-2']);
  });
});
