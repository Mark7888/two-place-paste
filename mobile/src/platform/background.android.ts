/**
 * Background uploads on Android: a foreground service (`TransferService`)
 * running a headless JS task, with a notification showing the progress.
 *
 * Both halves are needed. Without the service, Android may freeze or kill the
 * process once no window of the app is on screen; without the headless task,
 * React Native pauses JavaScript timers then, and the encryption and the
 * upload yield through timers.
 */

import { PermissionsAndroid, Platform } from 'react-native';

import { TppTransfer } from './native';
import type { BackgroundPort } from './types';

export const background: BackgroundPort = {
  available: TppTransfer !== null,

  async askToNotify(): Promise<void> {
    // Android 13 made notifications a runtime permission. Asked once; a "no"
    // is not asked again by the OS, and the upload runs without one.
    if (typeof Platform.Version !== 'number' || Platform.Version < 33) {
      return;
    }
    try {
      await PermissionsAndroid.request(PermissionsAndroid.PERMISSIONS.POST_NOTIFICATIONS);
    } catch {
      // No answer is an answer: go on without notifications.
    }
  },

  async start(): Promise<void> {
    if (TppTransfer === null) {
      throw new Error('this build cannot upload in the background');
    }
    await TppTransfer.start();
  },

  progress(title: string, done: number, total: number): void {
    TppTransfer?.progress(title, done, total);
  },

  result(ok: boolean, message: string): void {
    TppTransfer?.result(ok, message);
  },
};
