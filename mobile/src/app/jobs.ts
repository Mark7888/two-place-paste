/**
 * How one upload handed off to the background is carried out: read or seal
 * the staged file, then send it to the group or save it to the store. The
 * uploads wait their turn in the one transfer queue (`transferQueue.ts`),
 * which `backgroundJobs.ts` hands them to.
 *
 * Like `share.ts` it imports nothing from React Native, so it is tested
 * directly.
 */

import { fromHex, type ExternalBody, type StoreDetails, type UploadBody } from '../core';
import type { StagedFile } from '../platform/types';
import { SENT, STORED, type Payload, type ShareClient } from './share';
import { noteUploadFailure } from './spaceState';

/** Job is one upload: a payload, and where it goes. */
export type Job = { id: number; payload: Payload } & (
  | { target: 'send' }
  | { target: 'store'; details: StoreDetails }
);

/**
 * UploadItem is an item to upload: bytes in memory, with their SHA-256 when it
 * is already known — a staged file is hashed natively while it is copied —
 * or an `ExternalBody` the platform seals.
 */
export type UploadItem = { contentType: string; filename: string } & UploadBody;

/** knownHash turns a staged file's hex hash into bytes, or undefined when it is not one. */
export function knownHash(hex: string): Uint8Array | undefined {
  return /^[0-9a-f]{64}$/.test(hex) ? fromHex(hex) : undefined;
}

/** JobDeps is what running a job needs from the app and the platform. */
export interface JobDeps {
  /** client is the client in use when the job runs, or null when there is none. */
  client(): ShareClient | null;
  /** readStaged reads a staged file whole. */
  readStaged(file: StagedFile, onProgress: (done: number, total: number) => void): Promise<Uint8Array>;
  discard(token: string): void;
  /**
   * external makes a staged file an `ExternalBody` sealed natively, or
   * resolves with null when that cannot be used here; the file is then read
   * and sealed in JavaScript.
   */
  external?(file: StagedFile): Promise<ExternalBody | null>;
  /** saveToStore saves an item to the store with the details the user gave. */
  saveToStore(item: UploadItem, details: StoreDetails): Promise<void>;
  /** progress moves the notification along: a status, never content. */
  progress(title: string, done: number, total: number): void;
}

/**
 * runJob carries out one upload and resolves with how it ended, in the words
 * its notification uses. A staged file is deleted once it has been read,
 * whatever happens after.
 *
 * The whole job runs under `client.hold`, so the socket stays up while the
 * app is off screen — through the encryption, before the upload itself has
 * started — and closes once the job is done.
 */
export async function runJob(job: Job, deps: JobDeps): Promise<string> {
  const p = job.payload;
  const client = deps.client();
  if (client === null) {
    if (p.kind === 'file') {
      deps.discard(p.file.token);
    }
    throw new Error('TwoPlacePaste is not paired.');
  }
  const upload = async (item: UploadItem): Promise<string> => {
    // A bar of its own until the upload reports progress.
    deps.progress('Encrypting…', 0, 0);
    if (job.target === 'send') {
      await client.putEntry(item).catch((err: unknown) => {
        noteUploadFailure(err);
        throw err;
      });
      return SENT;
    }
    await deps.saveToStore(item, job.details);
    return STORED;
  };
  return client.hold(async () => {
    if (p.kind !== 'file') {
      return upload(p.item);
    }
    const named = { contentType: p.contentType, filename: p.filename };
    try {
      // Sealed natively when it can be: the file never enters JavaScript.
      const external = deps.external === undefined ? null : await deps.external(p.file);
      if (external !== null) {
        return await upload({ ...named, external });
      }
      deps.progress('Preparing…', 0, p.file.size);
      const body = await deps.readStaged(p.file, (done, total) => deps.progress('Preparing…', done, total));
      return await upload({ ...named, body, sha256: knownHash(p.file.sha256) });
    } finally {
      // After the upload: a native seal reads the copy while it uploads.
      deps.discard(p.file.token);
    }
  });
}
