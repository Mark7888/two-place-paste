/**
 * Sealing a shared file natively, and making sure that is safe to do.
 *
 * The client core is pure JavaScript, which on the phone runs with no JIT:
 * sealing a large file there took most of an upload's time. A file shared to
 * the app is already staged on the platform's side, so it can be sealed there
 * too, by `XChaCha20Poly1305Sealer.java`, without ever entering JavaScript to
 * be encrypted. The core still draws the nonce, derives the key, builds the
 * associated data and the frame's head, and checks the container's shape
 * (`ExternalBody` in `src/core/protocol/client.ts`).
 *
 * The native sealer is not trusted blind. Before the first use in a run of
 * the app, [verifyNativeSeal] seals inputs of many sizes both natively and
 * with `@noble/ciphers` — the AEAD held to spec/vectors — and the native one
 * is used only when every byte agrees. Otherwise, or where there is none, the
 * upload falls back to sealing in JavaScript, which is slower but the same.
 *
 * This module imports nothing from React Native, so it is tested directly.
 */

import { xchacha20poly1305 } from '@noble/ciphers/chacha.js';

import { randomBytes, type ExternalBody } from '../core';
import type { NativeSealPort, StagedFile } from '../platform/types';
import { knownHash } from './jobs';

/** SELF_TEST_SIZES straddle the ChaCha block, the Poly1305 block and the native buffer's edges. */
export const SELF_TEST_SIZES = [0, 1, 15, 16, 17, 63, 64, 65, 127, 128, 129, 1000, 4099, 65535, 65536, 65537, 70001];

/**
 * selfTest reports whether `port` seals exactly as `@noble/ciphers` does, on
 * random inputs of every size in SELF_TEST_SIZES. A failure to seal at all is
 * a "no", not an error: the caller falls back to JavaScript.
 */
export async function selfTest(port: NativeSealPort): Promise<boolean> {
  try {
    for (const n of SELF_TEST_SIZES) {
      const key = randomBytes(32);
      const nonce = randomBytes(24);
      const aad = randomBytes(n % 41);
      const plain = randomBytes(n);
      const want = xchacha20poly1305(key, nonce, aad).encrypt(plain);
      const got = await port.sealBytes(key, nonce, aad, plain);
      if (got.length !== want.length || !got.every((b, i) => b === want[i])) {
        return false;
      }
    }
    return true;
  } catch {
    return false;
  }
}

let verified: { port: NativeSealPort; ok: Promise<boolean> } | null = null;

/** verifyNativeSeal runs the self-test once per port and remembers the answer. */
export function verifyNativeSeal(port: NativeSealPort): Promise<boolean> {
  if (verified === null || verified.port !== port) {
    verified = { port, ok: selfTest(port) };
  }
  return verified.ok;
}

/** NativeSealDeps is what sealing a staged file natively needs from the platform. */
export interface NativeSealDeps {
  port: NativeSealPort;
  readStaged(file: StagedFile, onProgress: (done: number, total: number) => void): Promise<Uint8Array>;
  discard(token: string): void;
  progress(title: string, done: number, total: number): void;
}

/**
 * externalBody makes a staged file an `ExternalBody` the client seals
 * natively: the platform seals it into a second staged copy, which is read
 * back as the container and then deleted. It is null when the file's hash is
 * not known, which the client needs for the header: the caller then reads
 * the file and seals it in JavaScript.
 */
export function externalBody(file: StagedFile, deps: NativeSealDeps): ExternalBody | null {
  const sha256 = knownHash(file.sha256);
  if (sha256 === undefined) {
    return null;
  }
  return {
    size: file.size,
    sha256,
    seal: async (params) => {
      deps.progress('Encrypting…', 0, 0);
      const sealed = await deps.port.sealStaged(file.token, params);
      try {
        const container: StagedFile = { token: sealed.token, name: '', contentType: '', size: sealed.size, sha256: '' };
        deps.progress('Preparing upload…', 0, sealed.size);
        return await deps.readStaged(container, (done, total) => deps.progress('Preparing upload…', done, total));
      } finally {
        deps.discard(sealed.token);
      }
    },
  };
}
