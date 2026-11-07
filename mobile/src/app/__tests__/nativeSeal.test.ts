import { xchacha20poly1305 } from '@noble/ciphers/chacha.js';
import { sha256 } from '@noble/hashes/sha2.js';

import { Client, concat, FakeRelay, fromUTF8, MemoryStore, toHex, utf8 } from '../../core';
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
      return { token: t, size: sealed.length };
    },
    sealBytes: async (key, nonce, aad, plain) => seal(key, nonce, aad, plain),
  };
  const stage = (body: Uint8Array): StagedFile => {
    const token = `file-${next++}`;
    files.set(token, body);
    return { token, name: 'f.bin', contentType: 'application/octet-stream', size: body.length, sha256: toHex(sha256(body)) };
  };
  const deps = {
    port,
    readStaged: async (f: StagedFile, onProgress: (done: number, total: number) => void) => {
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
  return { port, stage, deps, files, discarded };
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
      // The sealed copy is gone; the file's own copy is the job's to delete.
      expect(p.discarded).toEqual(['sealed-2']);
      expect(p.files.has(file.token)).toBe(true);
    } finally {
      for (const c of opened) {
        c.disconnect();
      }
    }
  });
});
