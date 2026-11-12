/**
 * Received files on Android: saved to Downloads, opened in another app, or
 * shared through the share sheet.
 *
 * The Android clipboard has no way to carry a file, so an entry with a filename
 * (or a type the clipboard cannot hold) ends up here, after the user has
 * picked what they want. An image can too, when the user chose not to paste it.
 *
 * Opening and sharing do not save anything to Downloads: the native module
 * stages the bytes in this app's cache and hands the receiving app a
 * read-only content URI.
 */

import { PermissionsAndroid, Platform } from 'react-native';

import { decodeBase64StdAsync } from '../core/bytes';
import { toStandardBase64 } from './clipboard.android';
import { TppFiles } from './native';
import type { StagedItem } from '../core';
import type { ClipboardItem, FilesPort, PickedFile, SavedFile, StageResult } from './types';

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

  async save(item: ClipboardItem): Promise<SavedFile> {
    if (TppFiles === null) {
      throw new Error('this build cannot save files');
    }
    await ensureLegacyWrite();
    const saved = await TppFiles.saveToDownloads(
      await toStandardBase64(item.body),
      item.filename,
      item.contentType,
    );
    return { ...saved, contentType: item.contentType, filename: item.filename };
  },

  async openSaved(file: SavedFile): Promise<void> {
    if (TppFiles === null) {
      throw new Error('this build cannot open files');
    }
    await TppFiles.openSaved(file.uri, file.contentType, file.filename);
  },

  async open(item: ClipboardItem): Promise<void> {
    if (TppFiles === null) {
      throw new Error('this build cannot open files');
    }
    await TppFiles.open(await toStandardBase64(item.body), item.filename, item.contentType);
  },

  async share(item: ClipboardItem): Promise<void> {
    if (TppFiles === null) {
      throw new Error('this build cannot share files');
    }
    await TppFiles.share(await toStandardBase64(item.body), item.filename, item.contentType);
  },

  async pick(maxBytes: number): Promise<PickedFile | null> {
    if (TppFiles === null) {
      throw new Error('this build cannot pick files');
    }
    const picked = await TppFiles.pickFile(maxBytes);
    if (picked === null) {
      return null;
    }
    if (picked.tooLarge === true || picked.base64 === undefined) {
      return { tooLarge: true, name: picked.name, size: picked.size };
    }
    return {
      tooLarge: false,
      name: picked.name,
      contentType: picked.contentType ?? 'application/octet-stream',
      body: await decodeBase64StdAsync(picked.base64),
    };
  },

  async saveStaged(item: StagedItem): Promise<SavedFile> {
    if (TppFiles === null) {
      throw new Error('this build cannot save files');
    }
    await ensureLegacyWrite();
    const { token, offset, size } = item.staged;
    const saved = await TppFiles.saveStagedToDownloads(token, offset, size, item.filename, item.contentType);
    return { ...saved, contentType: item.contentType, filename: item.filename };
  },

  async openStaged(item: StagedItem): Promise<void> {
    if (TppFiles === null) {
      throw new Error('this build cannot open files');
    }
    const { token, offset, size } = item.staged;
    await TppFiles.openStaged(token, offset, size, item.filename, item.contentType);
  },

  async shareStaged(item: StagedItem): Promise<void> {
    if (TppFiles === null) {
      throw new Error('this build cannot share files');
    }
    const { token, offset, size } = item.staged;
    await TppFiles.shareStaged(token, offset, size, item.filename, item.contentType);
  },

  async pickToStage(maxBytes: number): Promise<StageResult | null> {
    if (TppFiles === null) {
      throw new Error('this build cannot pick files');
    }
    const picked = await TppFiles.pickToStage(maxBytes);
    if (picked === null) {
      return null;
    }
    if (picked.tooLarge === true || picked.token === undefined) {
      return { tooLarge: true, name: picked.name, size: picked.size };
    }
    return {
      tooLarge: false,
      token: picked.token,
      name: picked.name,
      contentType: picked.contentType ?? 'application/octet-stream',
      size: picked.size,
      sha256: picked.sha256 ?? '',
    };
  },
};
