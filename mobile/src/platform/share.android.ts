/**
 * The receiving end of Android's share sheet: "Share → TwoPlacePaste".
 *
 * The native side (`ShareActivity`, `ShareModule`) records what was shared and
 * keeps the file's URI to itself; this side is told the name, type and size,
 * and reads the bytes only once the user has picked where the share goes.
 *
 * A share reaches JavaScript the way a tile tap does: a running app gets an
 * event and claims it, and a cold start claims it once during startup.
 */

import type { DownloadSink } from '../core';
import { decodeBase64Std, decodeBase64StdAsync, encodeBase64Std, encodeBase64StdAsync, fromHex } from '../core/bytes';
import { SHARE_EVENT, TppShare, shareEvents, type TppShareModule } from './native';
import type { NativeSealPort, SharePort, Shared, StageResult, StagedFile } from './types';

/** READ_CHUNK_BYTES is how much of a shared file crosses to JavaScript at once. */
const READ_CHUNK_BYTES = 4 << 20;

/** parse checks what the native side sent, so a malformed share is refused rather than half-shown. */
function parse(raw: unknown): Shared | null {
  if (raw === null || typeof raw !== 'object') {
    return null;
  }
  const r = raw as Record<string, unknown>;
  const id = r.id;
  if (typeof id !== 'number') {
    return null;
  }
  const str = (v: unknown): string => (typeof v === 'string' ? v : '');
  switch (r.kind) {
    case 'file':
      return {
        id,
        kind: 'file',
        name: str(r.name),
        contentType: str(r.contentType) || 'application/octet-stream',
        size: typeof r.size === 'number' ? r.size : -1,
        previewUri: str(r.previewUri),
      };
    case 'text':
      return { id, kind: 'text', text: str(r.text), subject: str(r.subject) };
    default:
      return { id, kind: 'unsupported' };
  }
}

/**
 * nativeSeal is the native XChaCha20-Poly1305 (`XChaCha20Poly1305Sealer.java`).
 * Only small values cross as base64: the key, nonce, associated data and the
 * frame's head. The file itself never enters JavaScript to be sealed.
 */
const nativeSeal: NativeSealPort | null =
  TppShare === null
    ? null
    : {
        async sealStaged(token, p) {
          const native = TppShare;
          if (native === null) {
            throw new Error('this build cannot seal files natively');
          }
          const sealed = await native.sealStaged(
            token,
            encodeBase64Std(p.key),
            encodeBase64Std(p.nonce),
            encodeBase64Std(p.aad),
            encodeBase64Std(p.head),
            encodeBase64Std(p.frameHead),
          );
          return { token: sealed.token, size: sealed.size, head: decodeBase64Std(sealed.head) };
        },
        async openBytes(key, nonce, aad, sealed) {
          const native = TppShare;
          if (native === null) {
            throw new Error('this build cannot open natively');
          }
          return decodeBase64Std(
            await native.openBytes(encodeBase64Std(key), encodeBase64Std(nonce), encodeBase64Std(aad), encodeBase64Std(sealed)),
          );
        },
        async sealBytes(key, nonce, aad, plain) {
          const native = TppShare;
          if (native === null) {
            throw new Error('this build cannot seal natively');
          }
          return decodeBase64Std(
            await native.sealBytes(encodeBase64Std(key), encodeBase64Std(nonce), encodeBase64Std(aad), encodeBase64Std(plain)),
          );
        },
      };

/**
 * downloadSink keeps a large download on the native side: ranges captured or
 * written into a staged file, opened there by `XChaCha20Poly1305Opener`, and
 * hashed there. Only the container's first bytes and the frame's head come
 * back to JavaScript. The sealed container is then kept for up to an hour
 * (`SealedCache.kt`), so the next action on the same file needs no download.
 */
const downloadSink: DownloadSink | null =
  TppShare === null
    ? null
    : {
        create: () => (TppShare as TppShareModule).createStaged(),
        write: async (token, offset, data) =>
          (TppShare as TppShareModule).writeStaged(token, offset, await encodeBase64StdAsync(data)),
        read: async (token, offset, length) =>
          decodeBase64StdAsync(await (TppShare as TppShareModule).readChunk(token, offset, length)),
        open: async (token, p) => {
          const opened = await (TppShare as TppShareModule).openStaged(
            token,
            encodeBase64Std(p.key),
            encodeBase64Std(p.nonce),
            encodeBase64Std(p.aad),
          );
          return { token: opened.token, size: opened.size, head: decodeBase64Std(opened.head) };
        },
        digest: async (token, offset) => fromHex(await (TppShare as TppShareModule).digestStaged(token, offset)),
        discard: (token) => TppShare?.discard(token),
        cached: (key) => (TppShare as TppShareModule).cacheLookup(key),
        keep: (key, token, info, until) => (TppShare as TppShareModule).cacheKeep(key, token, info, until),
        forget: (key) => TppShare?.cacheForget(key),
        clear: () => TppShare?.cacheClear(),
      };

export const share: SharePort = {
  available: TppShare !== null,
  nativeSeal,
  downloadSink,

  requests(handler: () => void): () => void {
    const subscription = shareEvents?.addListener(SHARE_EVENT, handler);
    return () => subscription?.remove();
  },

  async pending(): Promise<Shared | null> {
    if (TppShare === null) {
      return null;
    }
    return parse(await TppShare.consumePending());
  },

  async stage(id: number, maxBytes: number): Promise<StageResult> {
    if (TppShare === null) {
      throw new Error('this build cannot take shared files');
    }
    const staged = await TppShare.stage(id, maxBytes);
    if (staged.tooLarge === true || staged.token === undefined) {
      return { tooLarge: true, name: staged.name, size: staged.size };
    }
    return {
      tooLarge: false,
      token: staged.token,
      name: staged.name,
      contentType: staged.contentType ?? 'application/octet-stream',
      size: staged.size,
      sha256: staged.sha256 ?? '',
    };
  },

  async readStaged(file: StagedFile, onProgress?: (done: number, total: number) => void): Promise<Uint8Array> {
    if (TppShare === null) {
      throw new Error('this build cannot take shared files');
    }
    // The file comes over in chunks into one buffer: as a single base64
    // string it cost the native side several times its size at once, and a
    // large file crashed the app.
    const body = new Uint8Array(file.size);
    for (let offset = 0; offset < file.size; offset += READ_CHUNK_BYTES) {
      const length = Math.min(READ_CHUNK_BYTES, file.size - offset);
      const chunk = await decodeBase64StdAsync(await TppShare.readChunk(file.token, offset, length));
      if (chunk.length !== length) {
        throw new Error('the shared file could not be read');
      }
      body.set(chunk, offset);
      onProgress?.(offset + length, file.size);
    }
    return body;
  },

  async readSlice(token: string, offset: number, length: number): Promise<Uint8Array> {
    if (TppShare === null) {
      throw new Error('this build cannot take shared files');
    }
    return decodeBase64StdAsync(await TppShare.readChunk(token, offset, length));
  },

  discard(token: string): void {
    TppShare?.discard(token);
  },

  finish(id: number, message: string): void {
    TppShare?.finish(id, message);
  },

  openApp(): void {
    TppShare?.openApp();
  },
};
