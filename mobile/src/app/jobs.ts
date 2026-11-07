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

import type { StoreDetails } from '../core';
import type { ClipboardItem, StagedFile } from '../platform/types';
import { SENT, STORED, type Payload, type ShareClient } from './share';

/** Job is one upload: a payload, and where it goes. */
export type Job = { id: number; payload: Payload } & (
  | { target: 'send' }
  | { target: 'store'; details: StoreDetails }
);

/** JobDeps is what running a job needs from the app and the platform. */
export interface JobDeps {
  /** client is the client in use when the job runs, or null when there is none. */
  client(): ShareClient | null;
  /** readStaged reads a staged file whole. */
  readStaged(file: StagedFile, onProgress: (done: number, total: number) => void): Promise<Uint8Array>;
  discard(token: string): void;
  /** saveToStore saves an item to the store with the details the user gave. */
  saveToStore(item: ClipboardItem, details: StoreDetails): Promise<void>;
  /** progress moves the notification along: a status, never content. */
  progress(title: string, done: number, total: number): void;
}

/**
 * runJob carries out one upload and resolves with how it ended, in the words
 * its notification uses. A staged file is deleted once it has been read,
 * whatever happens after.
 */
export async function runJob(job: Job, deps: JobDeps): Promise<string> {
  let item: ClipboardItem;
  const p = job.payload;
  if (p.kind === 'file') {
    try {
      deps.progress('Preparing…', 0, p.file.size);
      const body = await deps.readStaged(p.file, (done, total) => deps.progress('Preparing…', done, total));
      item = { contentType: p.contentType, filename: p.filename, body };
    } finally {
      deps.discard(p.file.token);
    }
  } else {
    item = p.item;
  }

  const client = deps.client();
  if (client === null) {
    throw new Error('TwoPlacePaste is not paired.');
  }
  // A moving bar until the upload reports its own progress: encrypting a
  // large file takes a while and has nothing to count.
  deps.progress('Encrypting…', 0, 0);
  if (job.target === 'send') {
    await client.putEntry(item);
    return SENT;
  }
  await deps.saveToStore(item, job.details);
  return STORED;
}

/**
 * JobQueue runs jobs one at a time, in the order they were added, and reports
 * each outcome. `drain` runs until the queue is empty; calling it while it
 * runs joins the run in progress, and a job added meanwhile is picked up by it.
 */
export class JobQueue {
  private readonly jobs: Job[] = [];
  private draining: Promise<void> | null = null;

  constructor(
    private readonly run: (job: Job) => Promise<string>,
    private readonly report: (ok: boolean, message: string) => void,
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
        return;
      }
      try {
        this.report(true, await this.run(job));
      } catch (err) {
        this.report(false, this.describeError(err));
      }
    }
  }
}
