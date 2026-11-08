/**
 * The one `KeepAlive` (`holds.ts`), wired to the platform: large downloads
 * the user started carry on, with a notification, when they switch apps.
 *
 * It imports nothing from the session, so the session, the screens and the
 * background queue can all use it.
 */

import { AppState } from 'react-native';

import { describe as describeError } from '../core';
import { background } from '../platform';
import { KeepAlive } from './holds';
import { onTransferProgress } from './transferEvents';

export const keepAlive = new KeepAlive({
  start: () => (background.available ? background.start() : Promise.reject(new Error('no background runner'))),
  progress: (title, done, total) => background.progress(title, '', done, total),
  result: (id, ok, message) => background.result(id, ok, message),
  foreground: () => AppState.currentState === 'active',
  onProgress: onTransferProgress,
  describeError,
  now: () => Date.now(),
});

/** keepAliveDuring runs `work` so a large download in it survives the user leaving the app. */
export function keepAliveDuring<T>(work: () => Promise<T>): Promise<T> {
  return keepAlive.during(work);
}
