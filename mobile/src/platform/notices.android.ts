/**
 * The third-party notices, read from the APK (LICENSING.md).
 *
 * The Android build writes them into the app's assets — app/notices.gradle
 * lists the Android libraries and scripts/notices adds the npm packages — and
 * `NoticesModule.kt` reads them back.
 */

import type { NoticesPort } from './types';
import { TppNotices } from './native';

export const notices: NoticesPort = {
  async read(): Promise<string | null> {
    return (await TppNotices?.read()) ?? null;
  },
};
