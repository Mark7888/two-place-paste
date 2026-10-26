/**
 * Turning a decrypted entry into something a screen can show.
 *
 * The classification here deliberately matches the desktop service's
 * `clipboard.Content.Kind` and its preview endpoint, so the two clients
 * describe the same entry the same way: a filename makes it a file, an
 * `image/*` media type makes it an image, and everything else is text — of
 * which only a `text/*` body is actually rendered.
 *
 * A file is previewed when its extension names a format in FILE_FORMATS — a
 * shorter list than the desktop's, of what this app can draw — and its bytes
 * are what the extension claims, the same check the desktop makes.
 *
 * Nothing in here is cached or stored. A preview exists for as long as the
 * screen showing it does, and building one costs exactly the same decryption
 * that copying the entry would.
 */

import type { EntryHeaderView, Item, Progress, StoreLimits } from '../core';
import { fromUTF8 } from '../core';

/** PREVIEW_CHARS bounds a text preview. The desktop uses the same number. */
export const PREVIEW_CHARS = 240;

/**
 * MAX_IMAGE_BYTES bounds an image rendered inline. Base64 costs a third on top
 * and Hermes has to hold the whole string, so past this the screen says how
 * big it is instead of trying to draw it.
 */
export const MAX_IMAGE_BYTES = 4 << 20;

/**
 * MAX_FILE_PREVIEW_BYTES bounds a file the history offers to preview. A
 * preview downloads the whole file, so past this the row offers Save or open
 * only, as it does for a type with no preview.
 */
export const MAX_FILE_PREVIEW_BYTES = MAX_IMAGE_BYTES;

/** FILE_PREVIEW_CHARS bounds a text file's preview, which is worth more of a
 *  look than a copied snippet. */
export const FILE_PREVIEW_CHARS = 2000;

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
  /** format names what is shown: "PNG image" for a file, else the media type. */
  format: string;
  /** mismatch says a file's bytes are not what its name claims, so it is not shown. */
  mismatch: boolean;
}

/** FileFormat is a file type the history previews, known by its extension. */
interface FileFormat {
  name: string;
  render: 'text' | 'image';
  /** mime is the type it is drawn as: the table's, never the sender's. */
  mime: string;
  /** check says whether the bytes are what the extension claims. */
  check: (b: Uint8Array) => boolean;
}

const startsWith = (b: Uint8Array, ...sig: number[]) => sig.every((v, i) => b[i] === v);
const ascii = (s: string) => Array.from(s, (c) => c.charCodeAt(0));

function image(name: string, mime: string, check: (b: Uint8Array) => boolean): FileFormat {
  return { name, render: 'image', mime, check };
}

// The images Android's <Image> draws. SVG and AVIF, which the desktop shows,
// are not among them.
const IMAGES: [string[], FileFormat][] = [
  [['.png'], image('PNG image', 'image/png', (b) => startsWith(b, 0x89, ...ascii('PNG\r\n'), 0x1a, 0x0a))],
  [['.jpg', '.jpeg', '.jpe', '.jfif'], image('JPEG image', 'image/jpeg', (b) => startsWith(b, 0xff, 0xd8, 0xff))],
  [['.gif'], image('GIF image', 'image/gif', (b) => startsWith(b, ...ascii('GIF87a')) || startsWith(b, ...ascii('GIF89a')))],
  [['.webp'], image('WebP image', 'image/webp', (b) => startsWith(b, ...ascii('RIFF')) && ascii('WEBP').every((v, i) => b[8 + i] === v))],
  [['.bmp'], image('BMP image', 'image/bmp', (b) => startsWith(b, ...ascii('BM')))],
];

// The text files the desktop previews (desktop/internal/preview), shown as
// text here too: an .html file is its source, never a page.
const TEXTS: [string, string[]][] = [
  ['Text', ['.txt', '.text', '.log', '.csv', '.tsv', '.srt', '.vtt', '.diff', '.patch']],
  ['Markdown', ['.md', '.markdown']],
  ['Data', ['.json', '.jsonl', '.ndjson', '.xml', '.yaml', '.yml', '.toml']],
  ['Config', ['.ini', '.cfg', '.conf', '.env', '.properties']],
  ['Web source', ['.html', '.htm', '.css', '.scss', '.less']],
  [
    'Source code',
    [
      '.go', '.py', '.rb', '.rs', '.java', '.kt', '.kts', '.scala', '.groovy', '.gradle',
      '.js', '.mjs', '.cjs', '.jsx', '.ts', '.tsx', '.c', '.h', '.cc', '.cpp', '.hpp',
      '.cs', '.swift', '.m', '.php', '.pl', '.lua', '.r', '.dart', '.sql', '.proto',
      '.sh', '.bash', '.zsh', '.fish', '.ps1', '.bat', '.cmd', '.tex',
    ],
  ],
];

const FILE_FORMATS: ReadonlyMap<string, FileFormat> = new Map([
  ...IMAGES.flatMap(([exts, f]) => exts.map((e): [string, FileFormat] => [e, f])),
  ...TEXTS.flatMap(([name, exts]) =>
    exts.map((e): [string, FileFormat] => [e, { name, render: 'text', mime: 'text/plain', check: isText }]),
  ),
]);

/** fileFormat finds a file's format from its name, or undefined. */
export function fileFormat(filename: string): FileFormat | undefined {
  const dot = filename.lastIndexOf('.');
  return dot < 0 ? undefined : FILE_FORMATS.get(filename.slice(dot).toLowerCase());
}

/**
 * isText says whether bytes read as text: valid UTF-8 with no NUL, the
 * desktop's check. A binary file renamed .txt is not drawn as mojibake.
 */
export function isText(b: Uint8Array): boolean {
  let i = 0;
  while (i < b.length) {
    const c = b[i];
    if (c === 0) {
      return false;
    }
    const size = c < 0x80 ? 1 : (c & 0xe0) === 0xc0 && c >= 0xc2 ? 2 : (c & 0xf0) === 0xe0 ? 3 : (c & 0xf8) === 0xf0 && c <= 0xf4 ? 4 : 0;
    if (size === 0 || i + size > b.length) {
      return false;
    }
    for (let k = 1; k < size; k++) {
      if ((b[i + k] & 0xc0) !== 0x80) {
        return false;
      }
    }
    i += size;
  }
  return true;
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
 * from its sealed header and without fetching it: copied text and images,
 * and a file whose name is a format in FILE_FORMATS, up to
 * MAX_FILE_PREVIEW_BYTES. An entry whose header is missing or unreadable has
 * an unknown type, and no preview.
 */
export function headerPreviewable(header: EntryHeaderView | null): boolean {
  if (header === null) {
    return false;
  }
  if (header.filename !== '') {
    return fileFormat(header.filename) !== undefined && header.size <= MAX_FILE_PREVIEW_BYTES;
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
    format: item.contentType,
    mismatch: false,
  };

  if (kind === 'file') {
    const format = fileFormat(item.filename);
    if (format === undefined) {
      return out;
    }
    out.format = format.name;
    if (!format.check(item.body)) {
      out.mismatch = true;
      return out;
    }
    out.kind = format.render;
    if (format.render === 'text') {
      textOf(out, item.body, FILE_PREVIEW_CHARS);
    } else {
      imageOf(out, item.body, format.mime);
    }
    return out;
  }

  if (kind === 'text' && item.contentType.startsWith('text/')) {
    textOf(out, item.body, PREVIEW_CHARS);
    return out;
  }

  if (kind === 'image') {
    imageOf(out, item.body, item.contentType);
  }
  return out;
}

/** textOf fills in a text preview of at most limit characters. */
function textOf(out: Preview, body: Uint8Array, limit: number): void {
  const text = fromUTF8(body).replace(/^\uFEFF/, '');
  // Counted in code points rather than UTF-16 units, so an emoji is one
  // character and a surrogate pair is never cut in half.
  const chars = Array.from(text);
  out.truncated = chars.length > limit;
  out.text = out.truncated ? chars.slice(0, limit).join('') + '…' : text;
}

/** imageOf fills in an image preview, drawn as mime, unless it is too large. */
function imageOf(out: Preview, body: Uint8Array, mime: string): void {
  if (body.length > MAX_IMAGE_BYTES) {
    out.imageTooLarge = true;
    return;
  }
  out.imageUri = `data:${mime};base64,${toBase64(body)}`;
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

/**
 * storeRoom is how much the store has left, for the save sheet to say.
 *
 * availableBytes is not it: that is the largest item a save may still be,
 * which the relay cuts to the per-item limit. Below that limit it is the
 * room left; at it, the room is the store limit less what is used, and a
 * store with no limit has nothing to say. Whether one file fits is still
 * availableBytes's to answer.
 */
export function storeRoom(limits: StoreLimits | null | undefined): number | undefined {
  if (!limits) {
    return undefined;
  }
  if (limits.availableBytes < limits.maxItemBytes) {
    return limits.availableBytes;
  }
  return limits.maxBytes > 0 ? Math.max(limits.maxBytes - limits.usedBytes, 0) : undefined;
}
