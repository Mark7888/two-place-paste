/**
 * Keeping the app running through a large download the user started, after
 * they switch to another app.
 *
 * Two things stopped one. Android freezes an app's process soon after its last
 * window leaves the screen, so the socket died with it; and React Native
 * pauses JavaScript timers then, which the decryption yields through. The
 * background runner that carries uploads (`TransferService`) answers both, so
 * a download borrows it: [KeepAlive.during] wraps whatever the user asked for
 * — fetch, decrypt, save — and once it turns out to be a large download (the
 * client reports progress only for those), holds the runner until it is done,
 * with the download's progress in the notification.
 *
 * A small read never starts the runner. Nor does one that only becomes large
 * after the app has left the screen: Android refuses to start a foreground
 * service from the background.
 *
 * This module imports nothing from React Native, so it is tested directly.
 */

import type { Progress } from '../core';

/** KeepAliveDeps is what holding the runner needs from the app and the platform. */
export interface KeepAliveDeps {
  /** start starts the background runner; it then waits until every hold is released. */
  start(): Promise<void>;
  /** progress moves the notification along: a status, never content. */
  progress(title: string, done: number, total: number): void;
  /** result posts how a held download ended, when the user is elsewhere. */
  result(id: number, ok: boolean, message: string): void;
  /** foreground reports whether a window of the app is on screen. */
  foreground(): boolean;
  onProgress(listener: (p: Progress) => void): () => void;
  describeError(err: unknown): string;
  /** now is the clock the notification is throttled by. */
  now(): number;
}

/** DOWNLOAD_DONE is what a held download's notification says when it ends off screen. */
export const DOWNLOAD_DONE = 'Download finished.';

/** PROGRESS_INTERVAL_MS is how often a download moves the notification's bar. */
const PROGRESS_INTERVAL_MS = 500;

export class KeepAlive {
  private holds = 0;
  private waiters: (() => void)[] = [];
  /** Ids for result notifications, apart from the upload jobs' small ones. */
  private nextId = 1_000_000;

  constructor(private readonly deps: KeepAliveDeps) {}

  /** held reports whether anything holds the runner now. */
  get held(): boolean {
    return this.holds > 0;
  }

  /** whenReleased resolves once nothing holds the runner. */
  whenReleased(): Promise<void> {
    if (this.holds === 0) {
      return Promise.resolve();
    }
    return new Promise((resolve) => this.waiters.push(resolve));
  }

  /**
   * during runs `work`, holding the runner from the moment a download within
   * it reports progress until `work` settles. When it settles with the user
   * elsewhere, the outcome is posted as a notification — a status, never the
   * file's name.
   */
  async during<T>(work: () => Promise<T>): Promise<T> {
    const id = this.nextId++;
    let held = false;
    let shownAt = Number.NEGATIVE_INFINITY;
    let decrypting = false;
    const stop = this.deps.onProgress((p) => {
      if (p.direction !== 'download') {
        return;
      }
      if (!held) {
        if (!this.deps.foreground()) {
          return;
        }
        held = true;
        this.holds++;
        this.deps.start().catch(() => {
          // Not allowed to start just now: the download runs while the app
          // does, as it always did.
        });
      }
      if (p.total > 0 && p.done >= p.total) {
        // Downloaded: decrypting and saving have nothing to count.
        if (!decrypting) {
          decrypting = true;
          this.deps.progress('Decrypting…', 0, 0);
        }
        return;
      }
      const now = this.deps.now();
      if (now - shownAt >= PROGRESS_INTERVAL_MS) {
        shownAt = now;
        this.deps.progress('Downloading…', p.done, p.total);
      }
    });
    try {
      const result = await work();
      if (held && !this.deps.foreground()) {
        this.deps.result(id, true, DOWNLOAD_DONE);
      }
      return result;
    } catch (err) {
      if (held && !this.deps.foreground()) {
        this.deps.result(id, false, this.deps.describeError(err));
      }
      throw err;
    } finally {
      stop();
      if (held) {
        this.holds--;
        if (this.holds === 0) {
          const waiters = this.waiters;
          this.waiters = [];
          for (const wake of waiters) {
            wake();
          }
        }
      }
    }
  }
}
