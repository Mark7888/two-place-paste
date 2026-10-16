/**
 * Received files on Android: saved to Downloads, or opened in another app.
 *
 * The Android clipboard has no way to carry a file, so an entry with a filename
 * (or a type the clipboard cannot hold) ends up here, after the user has
 * picked which of the two they want.
 *
 * Opening does not save anything to Downloads: the native module stages the
 * bytes in this app's cache and hands the opening app a read-only content URI.
 */

import { PermissionsAndroid, Platform } from 'react-native';

import { toStandardBase64 } from './clipboard.android';
import { TppFiles } from './native';
import type { ClipboardItem, FilesPort } from './types';

/**
 * ensureLegacyWrite asks for storage access on Android 9 and older, the only
 * versions where saving to Downloads needs it. Android 10+ saves through
 * MediaStore with no permission.
 */
async function ensureLegacyWrite(): Promise<void> {
  if (typeof Platform.Version !== 'number' || Platform.Version >= 29) {
    return;
  }
  const result = await PermissionsAndroid.request(
    PermissionsAndroid.PERMISSIONS.WRITE_EXTERNAL_STORAGE,
  );
  if (result !== PermissionsAndroid.RESULTS.GRANTED) {
    throw new Error('Saving to Downloads needs storage access, which was not granted.');
  }
}

export const files: FilesPort = {
  available: TppFiles !== null,

  async save(item: ClipboardItem): Promise<string> {
    if (TppFiles === null) {
      throw new Error('this build cannot save files');
    }
    await ensureLegacyWrite();
    return TppFiles.saveToDownloads(toStandardBase64(item.body), item.filename, item.contentType);
  },

  async open(item: ClipboardItem): Promise<void> {
    if (TppFiles === null) {
      throw new Error('this build cannot open files');
    }
    await TppFiles.open(toStandardBase64(item.body), item.filename, item.contentType);
  },
};
