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

import { decodeBase64StdAsync } from '../core/bytes';
import { SHARE_EVENT, TppShare, shareEvents } from './native';
import type { PickedFile, SharePort, Shared } from './types';

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

export const share: SharePort = {
  available: TppShare !== null,

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

  async read(id: number, maxBytes: number): Promise<PickedFile> {
    if (TppShare === null) {
      throw new Error('this build cannot take shared files');
    }
    // The file comes over in chunks into one buffer: as a single base64
    // string it cost the native side several times its size at once, and a
    // large file crashed the app.
    const staged = await TppShare.stage(id, maxBytes);
    if (staged.tooLarge === true) {
      return { tooLarge: true, name: staged.name, size: staged.size };
    }
    try {
      const body = new Uint8Array(staged.size);
      for (let offset = 0; offset < staged.size; offset += READ_CHUNK_BYTES) {
        const length = Math.min(READ_CHUNK_BYTES, staged.size - offset);
        const chunk = await decodeBase64StdAsync(await TppShare.readChunk(id, offset, length));
        if (chunk.length !== length) {
          throw new Error('the shared file could not be read');
        }
        body.set(chunk, offset);
      }
      return {
        tooLarge: false,
        name: staged.name,
        contentType: staged.contentType ?? 'application/octet-stream',
        body,
      };
    } finally {
      TppShare.discard(id);
    }
  },

  finish(id: number, message: string): void {
    TppShare?.finish(id, message);
  },

  openApp(): void {
    TppShare?.openApp();
  },
};
