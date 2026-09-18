/**
 * Turning a decrypted entry into something a screen can show.
 *
 * The classification here deliberately matches the desktop service's
 * `clipboard.Content.Kind` and its preview endpoint, so the two clients
 * describe the same entry the same way: a filename makes it a file, an
 * `image/*` media type makes it an image, and everything else is text — of
 * which only a `text/*` body is actually rendered.
 *
 * Nothing in here is cached or stored. A preview exists for as long as the
 * screen showing it does, and building one costs exactly the same decryption
 * that copying the entry would.
 */

import type { Item } from '../core';
import { fromUTF8 } from '../core';

/** PREVIEW_CHARS bounds a text preview. The desktop uses the same number. */
export const PREVIEW_CHARS = 240;

/**
 * MAX_IMAGE_BYTES bounds an image rendered inline. Base64 costs a third on top
 * and Hermes has to hold the whole string, so past this the screen says how
 * big it is instead of trying to draw it.
 */
export const MAX_IMAGE_BYTES = 4 << 20;

export type PreviewKind = 'text' | 'image' | 'file';

/** Preview is a decrypted entry as a screen shows it. */
export interface Preview {
  kind: PreviewKind;
  contentType: string;
  filename: string;
  bytes: number;
  /** text is a bounded rendering of a text entry; truncated says there was more. */
  text: string;
  truncated: boolean;
  /** imageUri is a data URI for an image small enough to draw, or ''. */
  imageUri: string;
  imageTooLarge: boolean;
}

/** kindOf classifies an entry exactly as the desktop's clipboard does. */
export function kindOf(contentType: string, filename: string): PreviewKind {
  if (filename !== '') {
    return 'file';
  }
  if (contentType.startsWith('image/')) {
    return 'image';
  }
  return 'text';
}

/** previewOf renders one decrypted entry for display. */
export function previewOf(item: Item): Preview {
  const kind = kindOf(item.contentType, item.filename);
  const out: Preview = {
    kind,
    contentType: item.contentType,
    filename: item.filename,
    bytes: item.body.length,
    text: '',
    truncated: false,
    imageUri: '',
    imageTooLarge: false,
  };

  if (kind === 'text' && item.contentType.startsWith('text/')) {
    const text = fromUTF8(item.body);
    // Counted in code points rather than UTF-16 units, so an emoji is one
    // character and a surrogate pair is never cut in half.
    const chars = Array.from(text);
    out.truncated = chars.length > PREVIEW_CHARS;
    out.text = out.truncated ? chars.slice(0, PREVIEW_CHARS).join('') + '…' : text;
    return out;
  }

  if (kind === 'image') {
    if (item.body.length > MAX_IMAGE_BYTES) {
      out.imageTooLarge = true;
      return out;
    }
    out.imageUri = `data:${item.contentType};base64,${toBase64(item.body)}`;
  }
  return out;
}

const BASE64 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/';

/**
 * toBase64 encodes bytes as standard, padded base64 — the alphabet a data URI
 * needs.
 *
 * `src/core` has base64**url** without padding, because that is what the wire
 * contract uses; feeding that to an <Image> gives a picture that silently
 * fails to decode. The two are deliberately separate: this one is a display
 * concern and must never be used for anything the other end parses.
 */
export function toBase64(b: Uint8Array): string {
  let out = '';
  for (let i = 0; i < b.length; i += 3) {
    const remaining = b.length - i;
    const n =
      (b[i] << 16) | ((remaining > 1 ? b[i + 1] : 0) << 8) | (remaining > 2 ? b[i + 2] : 0);
    out += BASE64[(n >> 18) & 0x3f] + BASE64[(n >> 12) & 0x3f];
    out += remaining > 1 ? BASE64[(n >> 6) & 0x3f] : '=';
    out += remaining > 2 ? BASE64[n & 0x3f] : '=';
  }
  return out;
}
