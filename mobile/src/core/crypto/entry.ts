/**
 * Entry encryption (spec/crypto.md §5).
 */

import { xchacha20poly1305 } from '@noble/ciphers/chacha.js';
import { hkdf } from '@noble/hashes/hkdf.js';
import { sha256 } from '@noble/hashes/sha2.js';

import { concat, toBase64URL, u32be, u64be, u8, utf8 } from '../bytes';
import { NONCE_SIZE, TAG_SIZE, VERSION } from './keys';
import { randomBytes } from '../random';

/** MIN_CONTAINER_SIZE is a container holding a nonce, a tag and an empty ciphertext. */
export const MIN_CONTAINER_SIZE = 1 + NONCE_SIZE + TAG_SIZE;

/** entryInfo is the HKDF info of §5.1. */
export function entryInfo(epoch: bigint): Uint8Array {
  return concat(utf8('tpp/v1/entry'), u64be(epoch));
}

/**
 * entryAAD binds the version, the epoch and the entry id, so the relay cannot
 * relabel, duplicate or replay an entry (§5.3). The length prefix is what makes
 * the encoding unambiguous.
 */
export function entryAAD(epoch: bigint, entryID: string): Uint8Array {
  const id = utf8(entryID);
  return concat(utf8('tpp/v1/entry-aad'), u8(VERSION), u64be(epoch), u32be(id.length), id);
}

/**
 * contentKey derives an entry's one-use key (§5.1). The nonce is both the HKDF
 * salt and the AEAD nonce, which is what makes the key unique per entry.
 */
export function contentKey(groupKey: Uint8Array, nonce: Uint8Array, epoch: bigint): Uint8Array {
  return hkdf(sha256, groupKey, nonce, entryInfo(epoch), 32);
}

/** sealEntry encrypts an encoded frame into the container the relay stores (§5.2). */
export function sealEntry(
  groupKey: Uint8Array,
  nonce: Uint8Array,
  epoch: bigint,
  entryID: string,
  frame: Uint8Array,
): Uint8Array {
  if (nonce.length !== NONCE_SIZE) {
    throw new Error(`entry nonce is ${nonce.length} bytes, want ${NONCE_SIZE}`);
  }
  const ciphertext = xchacha20poly1305(
    contentKey(groupKey, nonce, epoch),
    nonce,
    entryAAD(epoch, entryID),
  ).encrypt(frame);
  return concat(u8(VERSION), nonce, ciphertext);
}

/**
 * openEntry decrypts a container back to its encoded frame (§5.4).
 *
 * The epoch and the entry id are the ones the relay reports for the entry. A
 * client must never try other epochs or other ids to make an entry decrypt: at
 * its own epoch, a failure is corruption or tampering.
 */
export function openEntry(
  groupKey: Uint8Array,
  container: Uint8Array,
  epoch: bigint,
  entryID: string,
): Uint8Array {
  if (container.length < MIN_CONTAINER_SIZE) {
    throw new Error('container is too short to hold a nonce and a tag');
  }
  if (container[0] !== VERSION) {
    throw new Error(`unsupported entry version ${container[0]}`);
  }
  const nonce = container.subarray(1, 1 + NONCE_SIZE);
  return xchacha20poly1305(
    contentKey(groupKey, nonce, epoch),
    nonce,
    entryAAD(epoch, entryID),
  ).decrypt(container.subarray(1 + NONCE_SIZE));
}

/**
 * newEntryID returns the identifier this client binds into an entry's
 * associated data and sends with it (§5.3).
 *
 * The writing client chooses it, because the AAD has to be built before the
 * ciphertext exists: 128 bits from the CSPRNG, in base64url, which is the
 * alphabet the relay accepts.
 */
export function newEntryID(): string {
  return toBase64URL(randomBytes(16));
}
