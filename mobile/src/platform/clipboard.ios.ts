/**
 * The iOS clipboard.
 *
 * iOS is deferred (SPEC §7.3): this file exists so that the platform surface
 * is complete on both targets and the app builds if someone adds an iOS
 * project, and it is deliberately the minimum that is honest. Every iOS read
 * raises the system's "pasted from" banner, so a future iOS client is
 * manual-only by construction — which is what this implementation is.
 */

import Clipboard from '@react-native-clipboard/clipboard';

import { fromUTF8, utf8 } from '../core/bytes';
import type { ClipboardItem, ClipboardPort } from './types';

const TEXT_CONTENT_TYPE = 'text/plain; charset=utf-8';

export const clipboard: ClipboardPort = {
  async read(): Promise<ClipboardItem | null> {
    const text = await Clipboard.getString();
    if (text === '') {
      return null;
    }
    return { contentType: TEXT_CONTENT_TYPE, filename: '', body: utf8(text) };
  },

  async write(item: ClipboardItem): Promise<void> {
    if (!item.contentType.startsWith('text/')) {
      throw new Error(`${item.contentType} cannot be placed on the iOS clipboard`);
    }
    Clipboard.setString(fromUTF8(item.body));
  },
};
