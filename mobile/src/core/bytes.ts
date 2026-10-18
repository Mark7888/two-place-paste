/**
 * Byte and integer encodings for the TwoPlacePaste crypto profile
 * (spec/crypto.md §2.1). All integers are unsigned big-endian.
 *
 * These are written out rather than taken from a dependency, and hex, UTF-8
 * and base64url are implemented here rather than through `Buffer`,
 * `TextEncoder` or `atob`: none of the three is guaranteed on Hermes, and a
 * polyfill that silently differs in its handling of a lone surrogate or a
 * stray `=` would be a wire incompatibility with the Go client rather than a
 * cosmetic bug.
 */

/** concat joins byte strings in order. */
export function concat(...parts: Uint8Array[]): Uint8Array {
  let total = 0;
  for (const p of parts) {
    total += p.length;
  }
  const out = new Uint8Array(total);
  let offset = 0;
  for (const p of parts) {
    out.set(p, offset);
    offset += p.length;
  }
  return out;
}

/** equal is a length-independent value comparison. It is not constant time and must not compare secrets. */
export function equal(a: Uint8Array, b: Uint8Array): boolean {
  if (a.length !== b.length) {
    return false;
  }
  for (let i = 0; i < a.length; i++) {
    if (a[i] !== b[i]) {
      return false;
    }
  }
  return true;
}

const HEX = '0123456789abcdef';

/** toHex encodes bytes as lowercase, unprefixed hex. */
export function toHex(b: Uint8Array): string {
  let out = '';
  for (let i = 0; i < b.length; i++) {
    out += HEX[b[i] >> 4] + HEX[b[i] & 0x0f];
  }
  return out;
}

/** fromHex decodes lowercase or uppercase hex. */
export function fromHex(s: string): Uint8Array {
  if (s.length % 2 !== 0) {
    throw new Error('hex string has an odd length');
  }
  const out = new Uint8Array(s.length / 2);
  for (let i = 0; i < out.length; i++) {
    const byte = Number.parseInt(s.slice(i * 2, i * 2 + 2), 16);
    if (Number.isNaN(byte)) {
      throw new Error('hex string contains a non-hex character');
    }
    out[i] = byte;
  }
  return out;
}

/**
 * utf8 encodes a string as UTF-8. A lone surrogate is encoded as U+FFFD, which
 * is what a well-behaved TextEncoder does; throwing here would turn a
 * user-supplied filename into a crash.
 */
export function utf8(s: string): Uint8Array {
  const out: number[] = [];
  for (let i = 0; i < s.length; i++) {
    let cp = s.codePointAt(i) as number;
    if (cp > 0xffff) {
      // codePointAt combined a surrogate pair; skip its second half.
      i++;
    } else if (cp >= 0xd800 && cp <= 0xdfff) {
      cp = 0xfffd;
    }

    if (cp < 0x80) {
      out.push(cp);
    } else if (cp < 0x800) {
      out.push(0xc0 | (cp >> 6), 0x80 | (cp & 0x3f));
    } else if (cp < 0x10000) {
      out.push(0xe0 | (cp >> 12), 0x80 | ((cp >> 6) & 0x3f), 0x80 | (cp & 0x3f));
    } else {
      out.push(
        0xf0 | (cp >> 18),
        0x80 | ((cp >> 12) & 0x3f),
        0x80 | ((cp >> 6) & 0x3f),
        0x80 | (cp & 0x3f),
      );
    }
  }
  return Uint8Array.from(out);
}

/**
 * fromUTF8 decodes UTF-8. Malformed input decodes to U+FFFD rather than
 * throwing: the bytes have already been authenticated by the AEAD when this is
 * called, so the only thing left to decide is how to display them.
 */
export function fromUTF8(b: Uint8Array): string {
  let out = '';
  let i = 0;
  while (i < b.length) {
    const c = b[i];
    let cp: number;
    let size: number;
    if (c < 0x80) {
      cp = c;
      size = 1;
    } else if ((c & 0xe0) === 0xc0) {
      cp = c & 0x1f;
      size = 2;
    } else if ((c & 0xf0) === 0xe0) {
      cp = c & 0x0f;
      size = 3;
    } else if ((c & 0xf8) === 0xf0) {
      cp = c & 0x07;
      size = 4;
    } else {
      out += '�';
      i++;
      continue;
    }
    if (i + size > b.length) {
      out += '�';
      break;
    }
    let valid = true;
    for (let k = 1; k < size; k++) {
      if ((b[i + k] & 0xc0) !== 0x80) {
        valid = false;
        break;
      }
      cp = (cp << 6) | (b[i + k] & 0x3f);
    }
    if (!valid || cp > 0x10ffff || (cp >= 0xd800 && cp <= 0xdfff)) {
      out += '�';
      i++;
      continue;
    }
    out += String.fromCodePoint(cp);
    i += size;
  }
  return out;
}

const BASE64URL = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_';

/** toBase64URL encodes bytes as unpadded base64url, the alphabet the wire contract uses. */
export function toBase64URL(b: Uint8Array): string {
  let out = '';
  for (let i = 0; i < b.length; i += 3) {
    const remaining = b.length - i;
    const n = (b[i] << 16) | ((remaining > 1 ? b[i + 1] : 0) << 8) | (remaining > 2 ? b[i + 2] : 0);
    out += BASE64URL[(n >> 18) & 0x3f] + BASE64URL[(n >> 12) & 0x3f];
    if (remaining > 1) {
      out += BASE64URL[(n >> 6) & 0x3f];
    }
    if (remaining > 2) {
      out += BASE64URL[n & 0x3f];
    }
  }
  return out;
}

/**
 * fromBase64URL decodes base64url, tolerating what a user's clipboard adds:
 * surrounding whitespace, and the padding a strict encoder elsewhere might
 * have written. It accepts the standard alphabet too, because a pairing
 * payload that arrives through a chat app may have been re-encoded.
 */
export function fromBase64URL(s: string): Uint8Array {
  const cleaned = s.replace(/[\s=]/g, '').replace(/\+/g, '-').replace(/\//g, '_');
  const out = new Uint8Array(Math.floor((cleaned.length * 3) / 4));
  let bits = 0;
  let value = 0;
  let written = 0;
  for (let i = 0; i < cleaned.length; i++) {
    const digit = BASE64URL.indexOf(cleaned[i]);
    if (digit < 0) {
      throw new Error('not valid base64url');
    }
    value = (value << 6) | digit;
    bits += 6;
    if (bits >= 8) {
      bits -= 8;
      out[written++] = (value >> bits) & 0xff;
    }
  }
  return out.subarray(0, written);
}

/** u8 encodes a byte. */
export function u8(n: number): Uint8Array {
  return Uint8Array.of(n & 0xff);
}

/** u16be encodes a 16-bit unsigned big-endian integer. */
export function u16be(n: number): Uint8Array {
  const b = new Uint8Array(2);
  new DataView(b.buffer).setUint16(0, n);
  return b;
}

/** u32be encodes a 32-bit unsigned big-endian integer. */
export function u32be(n: number): Uint8Array {
  const b = new Uint8Array(4);
  new DataView(b.buffer).setUint32(0, n);
  return b;
}

/**
 * u64be encodes a 64-bit unsigned big-endian integer.
 *
 * Epochs and millisecond timestamps are `bigint` throughout this client, not
 * `number`: a u64 above 2^53 does not survive a round trip through a JavaScript
 * number, and one crypto vector deliberately uses 2^64-1 (spec/crypto.md §9.1).
 */
export function u64be(n: bigint): Uint8Array {
  const b = new Uint8Array(8);
  new DataView(b.buffer).setBigUint64(0, BigInt.asUintN(64, n));
  return b;
}

/** view returns a DataView over a slice, without copying its backing buffer's other bytes into scope. */
export function view(b: Uint8Array): DataView {
  return new DataView(b.buffer, b.byteOffset, b.byteLength);
}

const BASE64_STD = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/';
const BASE64_STD_CODES = Uint8Array.from(BASE64_STD, (c) => c.charCodeAt(0));
const BASE64_STD_VALUES = (() => {
  const t = new Int16Array(128).fill(-1);
  for (let i = 0; i < BASE64_STD.length; i++) {
    t[BASE64_STD.charCodeAt(i)] = i;
  }
  return t;
})();

/** BASE64_BLOCK is how many bytes are encoded per string piece: a multiple of 3, small enough for one `fromCharCode` call. */
const BASE64_BLOCK = 3 * 8192;

/**
 * encodeBase64Std encodes bytes as standard, padded base64 — what Android's
 * `Base64.decode` reads on the other side of the native bridge.
 *
 * It is written for size, not for the short strings `toBase64URL` handles:
 * a 50 MB entry crossing the bridge to or from the cache has to be encoded in
 * well under a second, so it works on blocks of character codes rather than
 * concatenating one character at a time.
 */
export function encodeBase64Std(b: Uint8Array): string {
  const parts: string[] = [];
  for (let start = 0; start < b.length; start += BASE64_BLOCK) {
    const end = Math.min(start + BASE64_BLOCK, b.length);
    const out = new Uint8Array(Math.ceil((end - start) / 3) * 4);
    let o = 0;
    for (let i = start; i < end; i += 3) {
      const remaining = end - i;
      const n = (b[i] << 16) | ((remaining > 1 ? b[i + 1] : 0) << 8) | (remaining > 2 ? b[i + 2] : 0);
      out[o++] = BASE64_STD_CODES[(n >> 18) & 0x3f];
      out[o++] = BASE64_STD_CODES[(n >> 12) & 0x3f];
      out[o++] = remaining > 1 ? BASE64_STD_CODES[(n >> 6) & 0x3f] : 61; // '='
      out[o++] = remaining > 2 ? BASE64_STD_CODES[n & 0x3f] : 61;
    }
    parts.push(String.fromCharCode.apply(null, out as unknown as number[]));
  }
  return parts.join('');
}

/**
 * decodeBase64Std decodes standard base64, with or without padding. It is the
 * fast counterpart of `encodeBase64Std`, for bytes coming back across the
 * native bridge; it throws on anything that is not base64.
 */
export function decodeBase64Std(s: string): Uint8Array {
  let len = s.length;
  while (len > 0 && s.charCodeAt(len - 1) === 61) {
    len--;
  }
  if (len % 4 === 1) {
    throw new Error('not valid base64');
  }
  const out = new Uint8Array(Math.floor((len * 3) / 4));
  let o = 0;
  let i = 0;
  const digit = (k: number): number => {
    const c = s.charCodeAt(k);
    const v = c < 128 ? BASE64_STD_VALUES[c] : -1;
    if (v < 0) {
      throw new Error('not valid base64');
    }
    return v;
  };
  for (; i + 4 <= len; i += 4) {
    const n = (digit(i) << 18) | (digit(i + 1) << 12) | (digit(i + 2) << 6) | digit(i + 3);
    out[o++] = (n >> 16) & 0xff;
    out[o++] = (n >> 8) & 0xff;
    out[o++] = n & 0xff;
  }
  const rest = len - i;
  if (rest === 2) {
    const n = (digit(i) << 18) | (digit(i + 1) << 12);
    out[o++] = (n >> 16) & 0xff;
  } else if (rest === 3) {
    const n = (digit(i) << 18) | (digit(i + 1) << 12) | (digit(i + 2) << 6);
    out[o++] = (n >> 16) & 0xff;
    out[o++] = (n >> 8) & 0xff;
  }
  return out;
}

/** ASYNC_BLOCKS is how many base64 blocks (24 KiB each) are done between yields: about 240 KiB. */
const ASYNC_BLOCKS = 10;

const yieldNow = (): Promise<void> => new Promise((resolve) => setTimeout(resolve, 0));

/**
 * encodeBase64StdAsync is `encodeBase64Std` for large inputs, letting the
 * event loop run every few hundred KiB so the app keeps answering taps while
 * a big entry is turned into a string for the native bridge.
 */
export async function encodeBase64StdAsync(
  b: Uint8Array,
  pause: () => Promise<void> = yieldNow,
): Promise<string> {
  const parts: string[] = [];
  const step = BASE64_BLOCK * ASYNC_BLOCKS;
  for (let start = 0; start < b.length; start += step) {
    parts.push(encodeBase64Std(b.subarray(start, Math.min(start + step, b.length))));
    if (start + step < b.length) {
      await pause();
    }
  }
  return parts.join('');
}

/** decodeBase64StdAsync is `decodeBase64Std` for large inputs, yielding the same way. */
export async function decodeBase64StdAsync(
  s: string,
  pause: () => Promise<void> = yieldNow,
): Promise<Uint8Array> {
  // Whole groups of four characters decode independently, so the string is
  // cut on a multiple of four and each piece decoded on its own.
  const step = (BASE64_BLOCK / 3) * 4 * ASYNC_BLOCKS;
  if (s.length <= step) {
    return decodeBase64Std(s);
  }
  const pieces: Uint8Array[] = [];
  let total = 0;
  for (let start = 0; start < s.length; start += step) {
    const piece = decodeBase64Std(s.slice(start, Math.min(start + step, s.length)));
    pieces.push(piece);
    total += piece.length;
    await pause();
  }
  const out = new Uint8Array(total);
  let o = 0;
  for (const piece of pieces) {
    out.set(piece, o);
    o += piece.length;
  }
  return out;
}
