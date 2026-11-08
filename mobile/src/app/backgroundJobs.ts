/**
 * Background uploads, wired to the platform: the queue in `jobs.ts`, run by
 * a headless task inside a foreground service with a progress notification
 * (`TransferService` on Android).
 *
 * The share panel prepares an upload and hands it here with [enqueue], then
 * closes. The service is started while the panel is still on screen —
 * Android refuses to start one from the background — and its task drains the
 * queue ([backgroundTask], registered in `index.js`). When the queue is empty
 * the task ends and the service stops with it.
 */

import { AppState } from 'react-native';

import type { StoreDetails } from '../core';
import { background, share } from '../platform';
import { JobQueue, runJob, type Job, type UploadItem } from './jobs';
import { keepAlive } from './keepAlive';
import { externalBody, verifyNativeSeal } from './nativeSeal';
import { failureMessage } from './session';
import { getSnapshot, refresh, setNotice } from './sessionStore';
import { onTransferProgress } from './transferEvents';
import type { Payload } from './share';
import { saveItem } from './storeState';

let nextJobId = 1;

/**
 * PROGRESS_INTERVAL_MS is how often the notification's bar may move. Android
 * drops updates that come faster than a few a second, and a dropped one can
 * leave the notification showing a step that is over; a new step is always
 * shown at once.
 */
const PROGRESS_INTERVAL_MS = 500;

/** last is what the notification shows now, so a change in the queue can redraw it. */
let last = { title: '', done: 0, total: 0, at: 0 };

/** queueDetail is "2 of 3" while more than one upload is in this run, else "". */
function queueDetail(): string {
  const { index, total } = queue.position;
  return total > 1 ? `${Math.max(index, 1)} of ${total}` : '';
}

function showProgress(title: string, done: number, total: number, force = false): void {
  const now = Date.now();
  const end = total > 0 && done >= total;
  if (!force && title === last.title && !end && now - last.at < PROGRESS_INTERVAL_MS) {
    return;
  }
  last = { title, done, total, at: now };
  background.progress(title, queueDetail(), done, total);
}

const queue = new JobQueue(
  async (job) => {
    // The relay's chunk progress moves the bar once the upload starts.
    last = { title: '', done: 0, total: 0, at: 0 };
    const stop = onTransferProgress((p) => {
      if (p.direction === 'upload' && p.total > 0) {
        showProgress('Uploading…', p.done, p.total);
      }
    });
    try {
      return await runJob(job, {
        client: () => getSnapshot().client,
        readStaged: (file, onProgress) => share.readStaged(file, onProgress),
        external: async (file) => {
          const port = share.nativeSeal;
          if (port === null || !(await verifyNativeSeal(port))) {
            return null;
          }
          return externalBody(file, {
            port,
            readStaged: (f, onProgress) => share.readStaged(f, onProgress),
            readSlice: (token, offset, length) => share.readSlice(token, offset, length),
            discard: (token) => share.discard(token),
            progress: showProgress,
          });
        },
        discard: (token) => share.discard(token),
        saveToStore: async (item: UploadItem, details: StoreDetails) => {
          const client = getSnapshot().client;
          if (client === null) {
            throw new Error('TwoPlacePaste is not paired.');
          }
          // The duplicate check was made in the panel, before it closed.
          await saveItem(client, item, details, true);
        },
        progress: showProgress,
      });
    } finally {
      stop();
    }
  },
  (job, ok, message) => {
    background.result(job.id, ok, message);
    // The app shows the last outcome too, for whoever opens it next.
    setNotice(message);
  },
  failureMessage,
);

/**
 * enqueue hands an upload to the background and starts the runner. It is
 * called from the share panel while it is on screen. Where nothing can run in
 * the background, the upload runs here, for as long as the app does.
 */
export async function enqueue(payload: Payload, target: Job['target'], details?: StoreDetails): Promise<void> {
  const id = nextJobId++;
  if (target === 'store') {
    if (details === undefined) {
      throw new Error('a save to the store needs its details');
    }
    queue.add({ id, payload, target, details });
  } else {
    queue.add({ id, payload, target });
  }
  if (queue.running && last.title !== '') {
    // Already uploading: the notification now says how many are in the run.
    showProgress(last.title, last.done, last.total, true);
  }
  if (background.available) {
    try {
      await background.start();
      return;
    } catch {
      // Not allowed to start a service just now: run it in the app instead.
    }
  }
  void backgroundTask();
}

/**
 * backgroundTask is the headless task: it runs the queue until it is empty and
 * nothing else holds the runner — a large download the user started
 * (`keepAlive.ts`). The socket is closed afterwards if the app is still not
 * on screen, as it would have been without the work (SPEC §5.2).
 */
export async function backgroundTask(): Promise<void> {
  try {
    do {
      await queue.drain();
      await keepAlive.whenReleased();
    } while (queue.running || queue.size > 0);
  } finally {
    if (AppState.currentState !== 'active' && !queue.running) {
      getSnapshot().client?.disconnect();
    }
    refresh();
  }
}
