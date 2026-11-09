/**
 * Uploads handed off to run in the background — from the share panel, or a
 * file added to the store — wired to the platform: each runs as a transfer
 * in the one transfer queue (`transfers.ts`), with `jobs.ts` carrying it out.
 *
 * The share panel prepares an upload and hands it here with [enqueue], then
 * closes. The queue starts the background runner — a foreground service with
 * a headless task, `TransferService` on Android — while the panel is still on
 * screen, as Android requires, and the task ([backgroundTask], registered in
 * `index.js`) lasts until the queue is empty.
 */

import { AppState } from 'react-native';

import type { StoreDetails } from '../core';
import { share } from '../platform';
import { runJob, type Job, type UploadItem } from './jobs';
import { externalBody, verifyNativeSeal } from './nativeSeal';
import { failureMessage } from './session';
import { getSnapshot, refresh, setNotice } from './sessionStore';
import type { Payload } from './share';
import { saveItem } from './storeState';
import type { TransferControl, TransferInfo } from './transferQueue';
import { queueTransfer, runnerStopped, transferQueue } from './transfers';

let nextJobId = 1;

/** infoOf says what an upload is, for the Transfers panel: its name, where it goes, its size. */
function infoOf(payload: Payload, target: Job['target'], details?: StoreDetails): TransferInfo {
  const fileName = payload.kind === 'file' ? payload.filename || 'File' : 'Text';
  return {
    direction: 'upload',
    target: target === 'send' ? 'group' : 'store',
    // In the store an item goes by the name the user gave it.
    name: details?.name ?? fileName,
    size: payload.kind === 'file' ? payload.file.size : payload.item.body.length,
  };
}

/** run carries out one upload, its steps reported to the transfer it runs as. */
function run(job: Job, control: TransferControl): Promise<string> {
  return runJob(job, {
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
        progress: control.progress,
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
    progress: control.progress,
  });
}

/**
 * enqueue hands an upload to the transfer queue, where it waits its turn
 * behind any other upload or download, and runs on in the background when
 * the user leaves (`transfers.ts`). It is called while the app is on screen —
 * from the share panel or the store — and returns once the upload is queued.
 */
export function enqueue(payload: Payload, target: Job['target'], details?: StoreDetails): Promise<void> {
  const id = nextJobId++;
  let job: Job;
  if (target === 'store') {
    if (details === undefined) {
      return Promise.reject(new Error('a save to the store needs its details'));
    }
    job = { id, payload, target, details };
  } else {
    job = { id, payload, target };
  }
  queueTransfer(infoOf(payload, target, details), (control) => run(job, control), (message) => message).then(
    // The app shows the last outcome too, for whoever opens it next.
    (message) => setNotice(message),
    (err: unknown) => setNotice(failureMessage(err)),
  );
  return Promise.resolve();
}

/**
 * backgroundTask is the headless task: it runs until the transfer queue is
 * empty. The socket is closed afterwards if the app is still not on screen,
 * as it would have been without the work (SPEC §5.2).
 */
export async function backgroundTask(): Promise<void> {
  try {
    await transferQueue.whenIdle();
  } finally {
    runnerStopped();
    if (AppState.currentState !== 'active' && !transferQueue.busy) {
      getSnapshot().client?.disconnect();
    }
    refresh();
  }
}
