/**
 * Where a large download goes when it should not enter JavaScript: the
 * platform's staged files, opened natively (`DownloadSink`), once the native
 * crypto has passed its self-test against `@noble/ciphers` (`nativeSeal.ts`).
 * Where there is none, or the self-test fails, downloads are read and opened
 * in JavaScript as before.
 */

import type { DownloadSink } from '../core';
import { share } from '../platform';
import { verifyNativeSeal } from './nativeSeal';

/** downloadSink is the platform's sink when it may be used, or null. */
export async function downloadSink(): Promise<DownloadSink | null> {
  const port = share.nativeSeal;
  const sink = share.downloadSink;
  if (port === null || sink === null) {
    return null;
  }
  return (await verifyNativeSeal(port)) ? sink : null;
}
