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

import type { EntryHeaderView, Item, Progress } from '../core';
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

/**
 * headerPreviewable says whether the history offers a preview for an entry,
 * from its sealed header and without fetching it — the same rule the desktop
 * applies: only text/* and image/* without a filename. An entry whose header
 * is missing or unreadable has an unknown type, and no preview.
 */
export function headerPreviewable(header: EntryHeaderView | null): boolean {
  if (header === null || header.filename !== '') {
    return false;
  }
  return header.contentType.startsWith('text/') || header.contentType.startsWith('image/');
}

const SIZE_UNITS = ['KB', 'MB', 'GB'];

/**
 * formatSize renders a byte count for people: "512 B", "3.4 KB", "12 MB".
 *
 * Units are binary (1 KB = 1024 bytes), like the desktop's. One decimal below
 * 10 of a unit and whole numbers above, so a list of sizes stays short and
 * still tells 1.2 MB from 1.8 MB.
 */
export function formatSize(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 1024) {
    return `${Math.max(0, Math.round(bytes) || 0)} B`;
  }
  let value = bytes;
  let unit = -1;
  while (value >= 1024 && unit < SIZE_UNITS.length - 1) {
    value /= 1024;
    unit++;
  }
  // 1023.96 KB rounds to "1024 KB"; say "1.0 MB" instead.
  if (value >= 1023.5 && unit < SIZE_UNITS.length - 1) {
    value /= 1024;
    unit++;
  }
  const text = value < 10 ? value.toFixed(1) : Math.round(value).toString();
  return `${text} ${SIZE_UNITS[unit]}`;
}

/**
 * transferText is what the progress bar says: what is moving, which way, and
 * how far — or that it is queued behind another transfer — "Downloading report.pdf" over "4.0 MB of 20 MB · 20%". An entry
 * with no filename is named by its kind, and one whose header could not be
 * opened is just "a large entry".
 */
export function transferText(p: Progress): { title: string; detail: string; percent: number } {
  const percent = p.total > 0 ? Math.min(100, Math.floor((p.done / p.total) * 100)) : 0;
  const what =
    p.filename !== ''
      ? p.filename
      : p.contentType.startsWith('image/')
        ? 'an image'
        : p.contentType.startsWith('text/')
          ? 'text'
          : p.contentType !== ''
            ? p.contentType.split(';')[0].trim()
            : 'a large entry';
  const verb = p.direction === 'upload' ? 'upload' : 'download';
  if (p.waiting) {
    return {
      title: `Waiting to ${verb} ${what}`,
      detail:
        p.done > 0
          ? `Queued behind another transfer · ${formatSize(p.done)} of ${formatSize(p.total)}`
          : `Queued behind another transfer · ${formatSize(p.total)}`,
      percent,
    };
  }
  return {
    title: `${verb === 'upload' ? 'Uploading' : 'Downloading'} ${what}`,
    detail: `${formatSize(p.done)} of ${formatSize(p.total)} · ${percent}%`,
    percent,
  };
}

/** describeHeader names an entry from its header: its filename, else its type. */
export function describeHeader(header: EntryHeaderView | null): string {
  if (header === null) {
    return 'type unknown';
  }
  return header.filename !== '' ? header.filename : header.contentType;
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
