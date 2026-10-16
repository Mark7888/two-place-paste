/**
 * The entry header (spec/crypto.md §5.5): a fixed-size, separately encrypted
 * summary of an entry — content type, filename, plaintext body length — that
 * the relay stores beside the entry and returns in history, so a client can
 * show what an entry is without fetching it. Every header is the same size, so
 * the relay learns nothing from it.
 *
 * The header is advisory: the frame inside the entry stays the authority.
 */

import { xchacha20poly1305 } from '@noble/ciphers/chacha.js';
import { hkdf } from '@noble/hashes/hkdf.js';
import { sha256 } from '@noble/hashes/sha2.js';

import { concat, fromUTF8, u16be, u32be, u64be, u8, utf8, view } from '../bytes';
import { NONCE_SIZE, TAG_SIZE, VERSION } from './keys';
import { randomBytes } from '../random';

/** HEADER_SIZE is the fixed length of an encoded header. */
export const HEADER_SIZE = 256;
/** HEADER_CONTAINER_SIZE is the fixed length of a sealed header. */
export const HEADER_CONTAINER_SIZE = 1 + NONCE_SIZE + HEADER_SIZE + TAG_SIZE;

const HEADER_VERSION = 0x01;
const MAX_CONTENT_TYPE = 128;
const FALLBACK_CONTENT_TYPE = 'application/octet-stream';
const HEADER_FIXED = 1 + 2 + 2 + 8;

/** EntryHeader is what a client may know about an entry before fetching it. */
export interface EntryHeader {
  contentType: string;
  filename: string;
  bodyLen: bigint;
}

/** truncateUTF8 cuts b to at most n bytes without splitting a code point. */
function truncateUTF8(b: Uint8Array, n: number): Uint8Array {
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

/** decodeHeader parses a header, rejecting anything but its one canonical encoding. */
export function decodeHeader(encoded: Uint8Array): EntryHeader {
  if (encoded.length !== HEADER_SIZE) {
    throw new Error(`header is ${encoded.length} bytes, want ${HEADER_SIZE}`);
  }
  let offset = 0;
  const take = (n: number): Uint8Array => {
    if (encoded.length - offset < n) {
      throw new Error('length prefix exceeds the header');
    }
    const slice = encoded.subarray(offset, offset + n);
    offset += n;
    return slice;
  };
  if (take(1)[0] !== HEADER_VERSION) {
    throw new Error('unsupported header version');
  }
  const contentType = fromUTF8(take(view(take(2)).getUint16(0)));
  if (contentType.length === 0) {
    throw new Error('content_type is mandatory');
  }
  const filename = fromUTF8(take(view(take(2)).getUint16(0)));
  const bodyLen = view(take(8)).getBigUint64(0);
  for (let i = offset; i < encoded.length; i++) {
    if (encoded[i] !== 0) {
      throw new Error('header padding is not zero');
    }
  }
  return { contentType, filename, bodyLen };
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

/** sealHeader encrypts an encoded header: version || nonce || ciphertext || tag. */
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
  if (encoded.length !== HEADER_SIZE) {
    throw new Error(`header is ${encoded.length} bytes, want ${HEADER_SIZE}`);
  }
  const ciphertext = xchacha20poly1305(
    headerKey(groupKey, nonce, epoch),
    nonce,
    headerAAD(epoch, entryID),
  ).encrypt(encoded);
  return concat(u8(VERSION), nonce, ciphertext);
}

/** openHeader decrypts a sealed header back to its encoding. */
export function openHeader(
  groupKey: Uint8Array,
  container: Uint8Array,
  epoch: bigint,
  entryID: string,
): Uint8Array {
  if (container.length !== HEADER_CONTAINER_SIZE) {
    throw new Error(`sealed header is ${container.length} bytes, want ${HEADER_CONTAINER_SIZE}`);
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

/** sealEntryHeader encodes and seals a header under a fresh nonce. */
export function sealEntryHeader(
  groupKey: Uint8Array,
  epoch: bigint,
  entryID: string,
  h: EntryHeader,
): Uint8Array {
  return sealHeader(groupKey, randomBytes(NONCE_SIZE), epoch, entryID, encodeHeader(h));
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
