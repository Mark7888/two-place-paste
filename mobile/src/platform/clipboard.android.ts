/**
 * The Android clipboard (SPEC §7.1).
 *
 * **The platform constraint this whole app is shaped by:** Android 10+ refuses
 * a clipboard read to an app that is not focused and is not the default IME.
 * Every call here therefore happens while the app is in the foreground — from a
 * screen the user is looking at, or from the moment the quick-settings tile
 * foregrounds the app. There is no background polling and no accessibility
 * service; if that ever proves impossible, auto-sync is dropped rather than
 * worked around (SPEC §7.1).
 *
 * Text and images are supported. A file reference is not: Android hands a
 * `content://` URI whose lifetime is a permission grant to the copying app, and
 * reading it from here would need the SAF permissions the desktop client does
 * not need either. A URI on the clipboard is carried as the text it is.
 *
 * Images go through this app's own native module in **both** directions. The
 * clipboard library's `hasImage`, `getImagePNG` and `setImage` are iOS-only and
 * reject on Android with "not supported on Android" — which is what every sync
 * hit, because `read` asked `hasImage` first and the rejection took the text
 * case down with it.
 */

import Clipboard from '@react-native-clipboard/clipboard';

import { TppClipboard } from './native';

import { fromBase64URL, toBase64URL, utf8, fromUTF8 } from '../core/bytes';
import type { ClipboardItem, ClipboardPort } from './types';

/** TEXT_CONTENT_TYPE is what a text entry is written as. */
export const TEXT_CONTENT_TYPE = 'text/plain; charset=utf-8';

/** PNG_CONTENT_TYPE is what an image from the clipboard is written as. */
export const PNG_CONTENT_TYPE = 'image/png';

/** base64 (standard alphabet) is what the native module speaks; the core speaks bytes. */
const fromStandardBase64 = (s: string): Uint8Array => fromBase64URL(s);
const toStandardBase64 = (b: Uint8Array): string => {
  const url = toBase64URL(b);
  const padding = url.length % 4 === 0 ? '' : '='.repeat(4 - (url.length % 4));
  return url.replace(/-/g, '+').replace(/_/g, '/') + padding;
};

export const clipboard: ClipboardPort = {
  async read(): Promise<ClipboardItem | null> {
    // Images first: a screenshot copied from another app puts a content URI on
    // the clipboard and an empty string on the text side, and reading the text
    // side first would upload nothing.
    //
    // A build with no native module cannot see an image at all and falls
    // through to the text side, which is the same way `write` and the clipboard
    // timestamp degrade. It is never the app on a phone.
    if (TppClipboard !== null) {
      const png = await TppClipboard.readImagePNG();
      if (png !== '') {
        return {
          contentType: PNG_CONTENT_TYPE,
          filename: '',
          body: fromStandardBase64(png),
        };
      }
    }
    const text = await Clipboard.getString();
    if (text === '') {
      return null;
    }
    return { contentType: TEXT_CONTENT_TYPE, filename: '', body: utf8(text) };
  },

  async write(item: ClipboardItem): Promise<void> {
    if (item.contentType.startsWith('image/')) {
      // The clipboard library writes images on iOS only. An image that can be
      // received but not pasted is half a feature, so this goes through the
      // app's own native module, which stages the PNG in the app's cache and
      // puts a FileProvider URI on the clipboard with a read grant attached.
      if (TppClipboard === null) {
        throw new Error('this build cannot place an image on the clipboard');
      }
      await TppClipboard.setImagePNG(toStandardBase64(item.body));
      return;
    }
    if (item.contentType.startsWith('text/') || item.contentType.startsWith('application/json')) {
      Clipboard.setString(fromUTF8(item.body));
      return;
    }
    // Anything else — a PDF, an archive, a file entry from a desktop — has no
    // representation this client can put on an Android clipboard. The history
    // screen says so rather than writing bytes that would paste as mojibake.
    throw new Error(`${item.contentType} cannot be placed on the Android clipboard`);
  },
};
