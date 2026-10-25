/**
 * The entry header (spec/crypto.md §5.5): a fixed-size, separately encrypted
 * summary of an entry — content type, filename, plaintext body length and, in
 * version 2, the SHA-256 of the body — that the relay stores beside the entry
 * and returns in history, so a client can show what an entry is without
 * fetching it. Every header of a version is the same size, so the relay learns
 * nothing from it but the version.
 *
 * The header is advisory: the frame inside the entry stays the authority.
 */

import { xchacha20poly1305 } from '@noble/ciphers/chacha.js';
import { hkdf } from '@noble/hashes/hkdf.js';
import { sha256 } from '@noble/hashes/sha2.js';

import { concat, fromUTF8, u16be, u32be, u64be, u8, utf8, view } from '../bytes';
import { NONCE_SIZE, TAG_SIZE, VERSION } from './keys';
import { randomBytes } from '../random';

/** HEADER_SIZE is the fixed length of an encoded v1 header. */
export const HEADER_SIZE = 256;
/** HEADER_CONTAINER_SIZE is the fixed length of a sealed v1 header. */
export const HEADER_CONTAINER_SIZE = 1 + NONCE_SIZE + HEADER_SIZE + TAG_SIZE;
/** HEADER_V2_SIZE is the fixed length of an encoded v2 header (§5.5.1). */
export const HEADER_V2_SIZE = 512;
/** HEADER_V2_CONTAINER_SIZE is the fixed length of a sealed v2 header. */
export const HEADER_V2_CONTAINER_SIZE = 1 + NONCE_SIZE + HEADER_V2_SIZE + TAG_SIZE;

const HEADER_VERSION = 0x01;
const HEADER_V2_VERSION = 0x02;
const V2_MAX_FILENAME = 255;
const SHA256_SIZE = 32;
const MAX_CONTENT_TYPE = 128;
const FALLBACK_CONTENT_TYPE = 'application/octet-stream';
const HEADER_FIXED = 1 + 2 + 2 + 8;

/**
 * EntryHeader is what a client may know about an entry before fetching it.
 * `sha256` is the hash of the frame's body; only a v2 header carries it.
 */
export interface EntryHeader {
  contentType: string;
  filename: string;
  bodyLen: bigint;
  sha256?: Uint8Array;
}

/** truncateUTF8 cuts b to at most n bytes without splitting a code point. */
export function truncateUTF8(b: Uint8Array, n: number): Uint8Array {
  if (b.length <= n) {
    return b;
  }
  while (n > 0 && (b[n] & 0xc0) === 0x80) {
    n--;
  }
  return b.subarray(0, n);
}

/**
 * encodeHeader serializes a header into exactly HEADER_SIZE bytes. A content
 * type over 128 bytes becomes application/octet-stream, and the filename is
 * cut, at a UTF-8 boundary, to what fits.
 */
export function encodeHeader(h: EntryHeader): Uint8Array {
  let contentType = utf8(h.contentType);
  if (contentType.length === 0) {
    throw new Error('content_type is mandatory');
  }
  if (contentType.length > MAX_CONTENT_TYPE) {
    contentType = utf8(FALLBACK_CONTENT_TYPE);
  }
  const filename = truncateUTF8(utf8(h.filename), HEADER_SIZE - HEADER_FIXED - contentType.length);
  const body = concat(
    u8(HEADER_VERSION),
    u16be(contentType.length),
    contentType,
    u16be(filename.length),
    filename,
    u64be(h.bodyLen),
  );
  return concat(body, new Uint8Array(HEADER_SIZE - body.length));
}

/**
 * encodeHeaderV2 serializes a v2 header into exactly HEADER_V2_SIZE bytes
 * (§5.5.1): the v1 fields, a filename cut to 255 bytes at a UTF-8 boundary,
 * and the body's SHA-256, which h must carry.
 */
export function encodeHeaderV2(h: EntryHeader): Uint8Array {
  let contentType = utf8(h.contentType);
  if (contentType.length === 0) {
    throw new Error('content_type is mandatory');
  }
  if (h.sha256 === undefined || h.sha256.length !== SHA256_SIZE) {
    throw new Error('a v2 header carries the 32-byte body hash');
  }
  if (contentType.length > MAX_CONTENT_TYPE) {
    contentType = utf8(FALLBACK_CONTENT_TYPE);
  }
  const filename = truncateUTF8(utf8(h.filename), V2_MAX_FILENAME);
  const body = concat(
    u8(HEADER_V2_VERSION),
    u16be(contentType.length),
    contentType,
    u16be(filename.length),
    filename,
    u64be(h.bodyLen),
    h.sha256,
  );
  return concat(body, new Uint8Array(HEADER_V2_SIZE - body.length));
}

/**
 * decodeHeader parses a header of either version, rejecting anything but its
 * one canonical encoding: each version has exactly one size.
 */
export function decodeHeader(encoded: Uint8Array): EntryHeader {
  if (encoded.length === 0) {
    throw new Error('header is empty');
  }
  const version = encoded[0];
  if (version !== HEADER_VERSION && version !== HEADER_V2_VERSION) {
    throw new Error('unsupported header version');
  }
  const size = version === HEADER_VERSION ? HEADER_SIZE : HEADER_V2_SIZE;
  if (encoded.length !== size) {
    throw new Error(`header is ${encoded.length} bytes, want ${size}`);
  }
  let offset = 1;
  const take = (n: number): Uint8Array => {
    if (encoded.length - offset < n) {
      throw new Error('length prefix exceeds the header');
    }
    const slice = encoded.subarray(offset, offset + n);
    offset += n;
    return slice;
  };
  const contentType = fromUTF8(take(view(take(2)).getUint16(0)));
  if (contentType.length === 0) {
    throw new Error('content_type is mandatory');
  }
  const filenameBytes = take(view(take(2)).getUint16(0));
  const filename = fromUTF8(filenameBytes);
  const bodyLen = view(take(8)).getBigUint64(0);
  let sha256: Uint8Array | undefined;
  if (version === HEADER_V2_VERSION) {
    if (filenameBytes.length > V2_MAX_FILENAME) {
      throw new Error(`a v2 filename is at most ${V2_MAX_FILENAME} bytes`);
    }
    sha256 = take(SHA256_SIZE).slice();
  }
  for (let i = offset; i < encoded.length; i++) {
    if (encoded[i] !== 0) {
      throw new Error('header padding is not zero');
    }
  }
  return sha256 === undefined ? { contentType, filename, bodyLen } : { contentType, filename, bodyLen, sha256 };
}

/** headerInfo is the HKDF info of a header key. */
export function headerInfo(epoch: bigint): Uint8Array {
  return concat(utf8('tpp/v1/header'), u64be(epoch));
}

/** headerAAD binds the version, the epoch and the entry id to a header. */
export function headerAAD(epoch: bigint, entryID: string): Uint8Array {
  const id = utf8(entryID);
  return concat(utf8('tpp/v1/header-aad'), u8(VERSION), u64be(epoch), u32be(id.length), id);
}

/** headerKey derives a header's one-use key from its own nonce. */
export function headerKey(groupKey: Uint8Array, nonce: Uint8Array, epoch: bigint): Uint8Array {
  return hkdf(sha256, groupKey, nonce, headerInfo(epoch), 32);
}

/** sealHeader encrypts an encoded header of either version: version || nonce || ciphertext || tag. */
export function sealHeader(
  groupKey: Uint8Array,
  nonce: Uint8Array,
  epoch: bigint,
  entryID: string,
  encoded: Uint8Array,
): Uint8Array {
  if (nonce.length !== NONCE_SIZE) {
    throw new Error(`header nonce is ${nonce.length} bytes, want ${NONCE_SIZE}`);
  }
  if (encoded.length !== HEADER_SIZE && encoded.length !== HEADER_V2_SIZE) {
    throw new Error(`header is ${encoded.length} bytes, want ${HEADER_SIZE} or ${HEADER_V2_SIZE}`);
  }
  const ciphertext = xchacha20poly1305(
    headerKey(groupKey, nonce, epoch),
    nonce,
    headerAAD(epoch, entryID),
  ).encrypt(encoded);
  return concat(u8(VERSION), nonce, ciphertext);
}

/** openHeader decrypts a sealed header of either version back to its encoding. */
export function openHeader(
  groupKey: Uint8Array,
  container: Uint8Array,
  epoch: bigint,
  entryID: string,
): Uint8Array {
  if (container.length !== HEADER_CONTAINER_SIZE && container.length !== HEADER_V2_CONTAINER_SIZE) {
    throw new Error(
      `sealed header is ${container.length} bytes, want ${HEADER_CONTAINER_SIZE} or ${HEADER_V2_CONTAINER_SIZE}`,
    );
  }
  if (container[0] !== VERSION) {
    throw new Error(`unsupported header version ${container[0]}`);
  }
  const nonce = container.subarray(1, 1 + NONCE_SIZE);
  return xchacha20poly1305(
    headerKey(groupKey, nonce, epoch),
    nonce,
    headerAAD(epoch, entryID),
  ).decrypt(container.subarray(1 + NONCE_SIZE));
}

/**
 * sealEntryHeader encodes and seals a v1 header under a fresh nonce. Writers
 * stay on v1 until the relay accepts v2 (§5.5.1).
 */
export function sealEntryHeader(
  groupKey: Uint8Array,
  epoch: bigint,
  entryID: string,
  h: EntryHeader,
): Uint8Array {
  return sealHeader(groupKey, randomBytes(NONCE_SIZE), epoch, entryID, encodeHeader(h));
}

/**
 * sealEntryHeaderV2 encodes and seals a v2 header under a fresh nonce. `h`
 * must carry the body's hash. Send it only to a relay that reports it accepts
 * v2; an older one refuses the 553 bytes.
 */
export function sealEntryHeaderV2(
  groupKey: Uint8Array,
  epoch: bigint,
  entryID: string,
  h: EntryHeader,
): Uint8Array {
  return sealHeader(groupKey, randomBytes(NONCE_SIZE), epoch, entryID, encodeHeaderV2(h));
}

/** HASH_SLICE_BYTES is how much `sha256Sliced` hashes between yields. */
const HASH_SLICE_BYTES = 256 * 1024;

/**
 * sha256Sliced hashes a body a slice at a time, letting the event loop run in
 * between: on the phone's engine, hashing a large entry in one call would stop
 * the app answering taps for seconds, as `openEntrySliced` explains.
 */
export async function sha256Sliced(
  body: Uint8Array,
  pause: () => Promise<void> = () => new Promise((resolve) => setTimeout(resolve, 0)),
): Promise<Uint8Array> {
  const h = sha256.create();
  for (let off = 0; off < body.length; off += HASH_SLICE_BYTES) {
    h.update(body.subarray(off, Math.min(off + HASH_SLICE_BYTES, body.length)));
    if (off + HASH_SLICE_BYTES < body.length) {
      await pause();
    }
  }
  return h.digest();
}

/** openEntryHeader opens and decodes a sealed header. */
export function openEntryHeader(
  groupKey: Uint8Array,
  container: Uint8Array,
  epoch: bigint,
  entryID: string,
): EntryHeader {
  return decodeHeader(openHeader(groupKey, container, epoch, entryID));
}
