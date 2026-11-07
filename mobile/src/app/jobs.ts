/**
 * The queue of uploads the share panel hands off, and how one is run.
 *
 * The panel closes as soon as an upload is handed over, so the user can go on
 * with what they were doing; this queue then runs the uploads one at a time,
 * in the background (`backgroundJobs.ts` wires it to the platform). One at a
 * time, because each holds a whole file in memory while it is encrypted.
 *
 * Like `share.ts` it imports nothing from React Native, so it is tested
 * directly.
 */

import { fromHex, type StoreDetails } from '../core';
import type { ClipboardItem, StagedFile } from '../platform/types';
import { SENT, STORED, type Payload, type ShareClient } from './share';

/** Job is one upload: a payload, and where it goes. */
export type Job = { id: number; payload: Payload } & (
  | { target: 'send' }
  | { target: 'store'; details: StoreDetails }
);

/**
 * UploadItem is an item to upload, with its body's SHA-256 when it is already
 * known: a staged file is hashed natively while it is copied, far faster than
 * JavaScript hashes it again.
 */
export type UploadItem = ClipboardItem & { sha256?: Uint8Array };

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
  return client.hold(async () => {
    let item: UploadItem;
    if (p.kind === 'file') {
      try {
        deps.progress('Preparing…', 0, p.file.size);
        const body = await deps.readStaged(p.file, (done, total) => deps.progress('Preparing…', done, total));
        item = { contentType: p.contentType, filename: p.filename, body, sha256: knownHash(p.file.sha256) };
      } finally {
        deps.discard(p.file.token);
      }
    } else {
      item = p.item;
    }
    // A bar of its own until the upload reports progress: the encryption
    // moves in slices too, but has nothing to count that the user cares about.
    deps.progress('Encrypting…', 0, 0);
    if (job.target === 'send') {
      await client.putEntry(item);
      return SENT;
    }
    await deps.saveToStore(item, job.details);
    return STORED;
  });
}

/**
 * JobQueue runs jobs one at a time, in the order they were added, and reports
 * each outcome. `drain` runs until the queue is empty; calling it while it
 * runs joins the run in progress, and a job added meanwhile is picked up by it.
 */
export class JobQueue {
  private readonly jobs: Job[] = [];
  private draining: Promise<void> | null = null;
  /** finished counts the jobs this drain has done, for "2 of 3". */
  private finished = 0;
  /** current is true while a job runs. */
  private current = false;

  constructor(
    private readonly run: (job: Job) => Promise<string>,
    private readonly report: (job: Job, ok: boolean, message: string) => void,
    private readonly describeError: (err: unknown) => string,
  ) {}

  /** size is the number of jobs waiting, not counting the one running. */
  get size(): number {
    return this.jobs.length;
  }

  /** running reports whether a drain is under way. */
  get running(): boolean {
    return this.draining !== null;
  }

  /**
   * position is where the running job stands among this drain's jobs: the
   * second of three, say. Before a drain it counts what is waiting.
   */
  get position(): { index: number; total: number } {
    const running = this.draining !== null && this.current ? 1 : 0;
    return { index: this.finished + running, total: this.finished + running + this.jobs.length };
  }

  add(job: Job): void {
    this.jobs.push(job);
  }

  drain(): Promise<void> {
    if (this.draining === null) {
      this.draining = (async () => {
        // One turn first, so `draining` is set before the loop can find the
        // queue empty and clear it.
        await Promise.resolve();
        await this.loop();
      })();
    }
    return this.draining;
  }

  private async loop(): Promise<void> {
    for (;;) {
      const job = this.jobs.shift();
      if (job === undefined) {
        // Cleared in the same turn the queue is found empty, so a job added
        // after this point starts a new drain rather than waiting on this one.
        this.draining = null;
        this.finished = 0;
        return;
      }
      this.current = true;
      try {
        const message = await this.run(job);
        this.current = false;
        this.finished++;
        this.report(job, true, message);
      } catch (err) {
        this.current = false;
        this.finished++;
        this.report(job, false, this.describeError(err));
      }
    }
  }
}
