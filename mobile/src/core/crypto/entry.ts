/**
 * Entry encryption (spec/crypto.md §5).
 */

import { poly1305 } from '@noble/ciphers/_poly1305.js';
import { xchacha20, xchacha20poly1305 } from '@noble/ciphers/chacha.js';
import { equalBytes } from '@noble/ciphers/utils.js';
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
  return openEntryWithKey(contentKey(groupKey, container.subarray(1, 1 + NONCE_SIZE), epoch), container, epoch, entryID);
}

/**
 * openEntryWithKey opens a container with its content key instead of the group
 * key (§7a.4). A store item is read this way: its content key is unwrapped from
 * the store, by a client that may no longer hold the group key of the epoch
 * the item was sealed at.
 */
export function openEntryWithKey(
  key: Uint8Array,
  container: Uint8Array,
  epoch: bigint,
  entryID: string,
): Uint8Array {
  if (key.length !== 32) {
    throw new Error(`content key is ${key.length} bytes, want 32`);
  }
  if (container.length < MIN_CONTAINER_SIZE) {
    throw new Error('container is too short to hold a nonce and a tag');
  }
  if (container[0] !== VERSION) {
    throw new Error(`unsupported entry version ${container[0]}`);
  }
  const nonce = container.subarray(1, 1 + NONCE_SIZE);
  return xchacha20poly1305(key, nonce, entryAAD(epoch, entryID)).decrypt(container.subarray(1 + NONCE_SIZE));
}

/** SLICE_BYTES is how much `openEntrySliced` works on between yields: a multiple of the 64-byte ChaCha block. */
const SLICE_BYTES = 256 * 1024;

/** yieldToUI lets the event loop run — a tap, a render — between slices of work. */
export const yieldToUI = (): Promise<void> => new Promise((resolve) => setTimeout(resolve, 0));

/**
 * openEntrySliced is `openEntry` for large containers, done a slice at a time
 * with the event loop let run in between.
 *
 * The phone's JavaScript engine has no JIT: a 50 MB entry takes many seconds
 * to authenticate and decrypt, and done in one call that is many seconds in
 * which the app cannot answer a tap. This is the same XChaCha20-Poly1305 of
 * spec/crypto.md §5.4, composed from its parts (draft-irtf-cfrg-xchacha,
 * RFC 8439 §2.8) so it can pause: the Poly1305 tag is computed over the
 * ciphertext slice by slice and checked *before* anything is decrypted, then
 * the ChaCha20 keystream is applied slice by slice from the right counter.
 * The result is byte for byte what `openEntry` returns, and a test holds the
 * two to that.
 */
export async function openEntrySliced(
  groupKey: Uint8Array,
  container: Uint8Array,
  epoch: bigint,
  entryID: string,
  pause: () => Promise<void> = yieldToUI,
): Promise<Uint8Array> {
  if (container.length < MIN_CONTAINER_SIZE) {
    throw new Error('container is too short to hold a nonce and a tag');
  }
  const key = contentKey(groupKey, container.subarray(1, 1 + NONCE_SIZE), epoch);
  return openEntrySlicedWithKey(key, container, epoch, entryID, pause);
}

/** openEntrySlicedWithKey is `openEntryWithKey` done a slice at a time, as `openEntrySliced` is. */
export async function openEntrySlicedWithKey(
  key: Uint8Array,
  container: Uint8Array,
  epoch: bigint,
  entryID: string,
  pause: () => Promise<void> = yieldToUI,
): Promise<Uint8Array> {
  if (key.length !== 32) {
    throw new Error(`content key is ${key.length} bytes, want 32`);
  }
  if (container.length < MIN_CONTAINER_SIZE) {
    throw new Error('container is too short to hold a nonce and a tag');
  }
  if (container[0] !== VERSION) {
    throw new Error(`unsupported entry version ${container[0]}`);
  }
  const nonce = container.subarray(1, 1 + NONCE_SIZE);
  const sealed = container.subarray(1 + NONCE_SIZE);
  const data = sealed.subarray(0, sealed.length - TAG_SIZE);
  const passedTag = sealed.subarray(sealed.length - TAG_SIZE);
  const aad = entryAAD(epoch, entryID);

  // The one-time Poly1305 key is the first 32 bytes of keystream block 0.
  const authKey = xchacha20(key, nonce, new Uint8Array(32), undefined, 0);
  const mac = poly1305.create(authKey);
  const pad = (n: number) => (n % 16 === 0 ? null : new Uint8Array(16 - (n % 16)));
  mac.update(aad);
  const aadPad = pad(aad.length);
  if (aadPad !== null) {
    mac.update(aadPad);
  }
  for (let off = 0; off < data.length; off += SLICE_BYTES) {
    mac.update(data.subarray(off, Math.min(off + SLICE_BYTES, data.length)));
    await pause();
  }
  const dataPad = pad(data.length);
  if (dataPad !== null) {
    mac.update(dataPad);
  }
  // Two little-endian 64-bit lengths, written by hand: both fit in 2^53, and
  // this avoids depending on BigInt DataView support in the engine.
  const lengths = new Uint8Array(16);
  const le64 = (at: number, n: number) => {
    for (let i = 0; i < 8; i++) {
      lengths[at + i] = Math.floor(n / 2 ** (8 * i)) & 0xff;
    }
  };
  le64(0, aad.length);
  le64(8, data.length);
  mac.update(lengths);
  if (!equalBytes(mac.digest(), passedTag)) {
    throw new Error('invalid tag');
  }

  // Authenticated: now decrypt, block counter 1 onwards.
  const out = new Uint8Array(data.length);
  for (let off = 0; off < data.length; off += SLICE_BYTES) {
    const end = Math.min(off + SLICE_BYTES, data.length);
    xchacha20(key, nonce, data.subarray(off, end), out.subarray(off, end), 1 + off / 64);
    await pause();
  }
  return out;
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
