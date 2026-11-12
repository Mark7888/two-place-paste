/**
 * The one transfer queue (`transferQueue.ts`), wired to the platform: the
 * background runner that keeps it going when the user switches apps, the
 * notification that shows it there, and the panel that shows it in the app
 * (`TransferPanel.tsx`).
 *
 * Large uploads and large downloads both go through [queueTransfer], so one
 * file moves at a time, in the order asked for. A transfer that fits in one
 * frame is not queued ([queueDownload]): copying a line of text should not
 * wait behind an 80 MB upload, and it is over before the user could see it.
 *
 * It imports nothing from the session, so the session, the screens and the
 * background jobs can all use it.
 */

import { AppState } from 'react-native';

import { describe as describeError } from '../core';
import { background } from '../platform';
import { onTransferProgress } from './transferEvents';
import { TransferQueue, type TransferControl, type TransferInfo, type Transfer } from './transferQueue';

/** QUEUE_MIN_BYTES is the size from which a download waits its turn: more than one frame. */
export const QUEUE_MIN_BYTES = 1 << 20;

/** DOWNLOAD_DONE is how a download ends, said where the name cannot be: the notification. */
export const DOWNLOAD_DONE = 'Download finished.';

/**
 * PROGRESS_INTERVAL_MS is how often the notification's bar may move. Android
 * drops updates that come faster than a few a second, and a dropped one can
 * leave the notification showing a step that is over; a new step, or a new
 * transfer, is always shown at once.
 */
const PROGRESS_INTERVAL_MS = 500;

const listeners = new Set<() => void>();
let shown = { id: 0, phase: '', position: '', at: 0 };
let runnerStarted = false;

export const transferQueue = new TransferQueue(() => {
  for (const listener of listeners) {
    listener();
  }
  notify();
  startRunner();
}, describeError);

/** subscribeTransfers and getTransfers are the queue as React reads it (`useSyncExternalStore`). */
export function subscribeTransfers(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function getTransfers(): readonly Transfer[] {
  return transferQueue.transfers;
}

/** positionText is "2 of 3" while the queue holds more than one transfer in this run, else "". */
export function positionText(): string {
  const { index, total } = transferQueue.position;
  return total > 1 ? `${Math.max(index, 1)} of ${total}` : '';
}

/**
 * queueTransfer runs `work` as a transfer in the queue. The client's chunk
 * progress moves the transfer along while it runs; `work` reports its other
 * steps itself (`TransferControl`). An upload's outcome is always posted as
 * a notification; a download's only when the user is elsewhere when it ends.
 */
export function queueTransfer<T>(
  info: TransferInfo,
  work: (control: TransferControl) => Promise<T>,
  outcome: (result: T) => string,
): Promise<T> {
  let id = 0;
  const run = transferQueue.run(
    info,
    async (control) => {
      id = transferQueue.current?.id ?? 0;
      const stop = onTransferProgress((p) => {
        if (p.direction !== info.direction || p.total <= 0) {
          return;
        }
        if (p.direction === 'download' && p.done >= p.total) {
          // Downloaded: what is left is decrypting and saving.
          control.progress('Decrypting…', 0, 0);
          return;
        }
        control.progress(p.direction === 'upload' ? 'Uploading…' : 'Downloading…', p.done, p.total);
      });
      try {
        return await work(control);
      } finally {
        stop();
      }
    },
    outcome,
  );
  const tell = (ok: boolean, message: string) => {
    if (info.direction === 'upload' || AppState.currentState !== 'active') {
      background.result(id, ok, message);
    }
  };
  run.then(
    (result) => tell(true, info.direction === 'upload' ? outcome(result) : DOWNLOAD_DONE),
    (err: unknown) => tell(false, describeError(err)),
  );
  return run;
}

/**
 * queueDownload is `queueTransfer` for a download, which waits its turn only
 * when it is large — or of a size not known yet.
 */
export function queueDownload<T>(
  info: Omit<TransferInfo, 'direction'>,
  work: (control: TransferControl) => Promise<T>,
): Promise<T> {
  if (info.size > 0 && info.size <= QUEUE_MIN_BYTES) {
    return work({ progress: () => {} });
  }
  return queueTransfer({ ...info, direction: 'download' }, work, () => 'Downloaded.');
}

/**
 * startRunner starts the background runner once there is something in the
 * queue, so the queue keeps going when the user switches apps. It can only
 * be started while the app is on screen — Android refuses a foreground
 * service from the background — which is where a transfer is asked for.
 */
function startRunner(): void {
  if (runnerStarted || !transferQueue.busy || !background.available || AppState.currentState !== 'active') {
    return;
  }
  runnerStarted = true;
  background.start().catch(() => {
    // Not allowed just now: the queue runs while the app does.
    runnerStarted = false;
  });
}

/** runnerStopped is called by the background task as it ends: the next transfer starts it again. */
export function runnerStopped(): void {
  runnerStarted = false;
}

/** notify keeps the notification on the running transfer: its step, its progress, and where it is in the run. */
function notify(): void {
  const current = transferQueue.current;
  if (current === null) {
    return;
  }
  const phase = current.phase !== '' ? current.phase : current.direction === 'upload' ? 'Preparing…' : 'Downloading…';
  const position = positionText();
  const now = Date.now();
  const same = shown.id === current.id && shown.phase === phase && shown.position === position;
  const end = current.total > 0 && current.done >= current.total;
  if (same && !end && now - shown.at < PROGRESS_INTERVAL_MS) {
    return;
  }
  shown = { id: current.id, phase, position, at: now };
  background.progress(phase, position, current.done, current.total);
}
