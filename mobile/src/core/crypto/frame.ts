/**
 * The plaintext frame (spec/crypto.md §6): the structure the AEAD protects,
 * carrying the content type and filename so that neither reaches the relay.
 *
 * The encoding is canonical — one frame has exactly one valid serialization —
 * so the decoder rejects trailing bytes and any length prefix that exceeds the
 * remaining input.
 */

import { concat, fromUTF8, u16be, u32be, u64be, u8, utf8, view } from '../bytes';

/** FRAME_VERSION is the frame's own version byte. */
export const FRAME_VERSION = 0x01;

/** Frame is one clipboard payload in plaintext. */
export interface Frame {
  /** contentType is an IANA media type; an unknown one is "application/octet-stream". It must not be empty. */
  contentType: string;

  /**
   * filename is a bare filename for file entries and empty otherwise. It is
   * untrusted display text, never a path to write to.
   */
  filename: string;

  /** createdAt is the writing client's clock in UTC milliseconds, and is advisory: expiry and ordering come from the relay. */
  createdAt: bigint;

  /** body is the payload. */
  body: Uint8Array;
}

/** encodeFrame serializes a frame (§6). */
export function encodeFrame(frame: Frame): Uint8Array {
  const contentType = utf8(frame.contentType);
  if (contentType.length === 0) {
    throw new Error('content_type is mandatory');
  }
  if (contentType.length > 0xffff) {
    throw new Error('content_type is longer than its length prefix can express');
  }
  const filename = utf8(frame.filename);
  if (filename.length > 0xffff) {
    throw new Error('filename is longer than its length prefix can express');
  }
  return concat(
    u8(FRAME_VERSION),
    u16be(contentType.length),
    contentType,
    u16be(filename.length),
    filename,
    u64be(frame.createdAt),
    // pad_len is reserved for the plaintext padding SPEC §9 leaves open. In v1
    // an encoder writes 0 and a decoder rejects anything else, so padding can
    // arrive as tpp-crypto-v2 without ambiguity.
    u32be(0),
    u32be(frame.body.length),
    frame.body,
  );
}

/** decodeFrame parses a frame, rejecting anything that is not its one canonical encoding (§6). */
export function decodeFrame(encoded: Uint8Array): Frame {
  let offset = 0;
  const take = (n: number): Uint8Array => {
    if (n < 0 || encoded.length - offset < n) {
      throw new Error('length prefix exceeds the remaining input');
    }
    const slice = encoded.subarray(offset, offset + n);
    offset += n;
    return slice;
  };

  if (take(1)[0] !== FRAME_VERSION) {
    throw new Error('unsupported frame version');
  }
  const contentType = fromUTF8(take(view(take(2)).getUint16(0)));
  if (contentType.length === 0) {
    throw new Error('content_type is mandatory');
  }
  const filename = fromUTF8(take(view(take(2)).getUint16(0)));
  const createdAt = view(take(8)).getBigUint64(0);
  if (view(take(4)).getUint32(0) !== 0) {
    throw new Error('pad_len must be zero in v1');
  }
  const body = take(view(take(4)).getUint32(0));
  if (offset !== encoded.length) {
    throw new Error('trailing bytes after the frame');
  }
  return { contentType, filename, createdAt, body };
}
