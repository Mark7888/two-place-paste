/**
 * Group key wrapping and unwrapping (spec/crypto.md §4).
 *
 * A wrap is an anonymous public-key encryption of the group key to one device:
 * DHKEM in the shape of RFC 9180 base mode, built from the three primitives
 * §2.2 pins and nothing else.
 */

import { xchacha20poly1305 } from '@noble/ciphers/chacha.js';
import { hkdf } from '@noble/hashes/hkdf.js';
import { sha256 } from '@noble/hashes/sha2.js';

import { concat, u64be, u8, utf8 } from '../bytes';
import { GROUP_KEY_SIZE, KEY_SIZE, VERSION, publicKey, sharedSecret } from './keys';
import { randomBytes } from '../random';

/** WRAPPED_KEY_SIZE is the only valid length of a wrapped key: version + ephemeral public key + ciphertext + tag. */
export const WRAPPED_KEY_SIZE = 1 + KEY_SIZE + GROUP_KEY_SIZE + 16;

/** The all-zero nonce is safe because a wrap key encrypts exactly one message (§4.3). */
const ZERO_NONCE = new Uint8Array(24);

/** wrapInfo is the HKDF info of §4.2: both public keys, so a wrap is tied to its recipient. */
export function wrapInfo(ephemeralPublic: Uint8Array, recipientPublic: Uint8Array): Uint8Array {
  return concat(utf8('tpp/v1/wrap'), ephemeralPublic, recipientPublic);
}

/** wrapAAD binds the version byte and the epoch, so a wrapped key cannot be replayed from an earlier epoch (§4.2). */
export function wrapAAD(epoch: bigint): Uint8Array {
  return concat(utf8('tpp/v1/wrap-aad'), u8(VERSION), u64be(epoch));
}

/** wrapKey derives the one-message key a wrap is sealed under (§4.2). */
export function wrapKey(
  shared: Uint8Array,
  ephemeralPublic: Uint8Array,
  recipientPublic: Uint8Array,
): Uint8Array {
  return hkdf(sha256, shared, undefined, wrapInfo(ephemeralPublic, recipientPublic), 32);
}

/**
 * wrap seals a group key to one recipient at one epoch (§4.2, §4.3).
 *
 * `ephemeralPrivate` exists so the vectors can be reproduced. Product code
 * omits it and gets a fresh ephemeral keypair, which is mandatory: a rekey to
 * three devices performs three wraps with three ephemeral keypairs, and
 * reusing one across recipients would reuse a key under the all-zero nonce.
 */
export function wrap(
  groupKey: Uint8Array,
  recipientPublic: Uint8Array,
  epoch: bigint,
  ephemeralPrivate: Uint8Array = randomBytes(KEY_SIZE),
): Uint8Array {
  if (groupKey.length !== GROUP_KEY_SIZE) {
    throw new Error(`group key is ${groupKey.length} bytes, want ${GROUP_KEY_SIZE}`);
  }
  const ephemeralPublic = publicKey(ephemeralPrivate);
  const key = wrapKey(
    sharedSecret(ephemeralPrivate, recipientPublic),
    ephemeralPublic,
    recipientPublic,
  );
  const ciphertext = xchacha20poly1305(key, ZERO_NONCE, wrapAAD(epoch)).encrypt(groupKey);
  return concat(u8(VERSION), ephemeralPublic, ciphertext);
}

/**
 * unwrap opens a wrapped group key with this device's private key (§4.4).
 *
 * The recipient public key is recomputed from the private key rather than read
 * from the wire: a relay that substituted one would produce a different HKDF
 * info and the tag would not verify.
 */
export function unwrap(
  wrapped: Uint8Array,
  recipientPrivate: Uint8Array,
  epoch: bigint,
): Uint8Array {
  if (wrapped.length !== WRAPPED_KEY_SIZE) {
    throw new Error(`a wrapped key is ${WRAPPED_KEY_SIZE} bytes, got ${wrapped.length}`);
  }
  if (wrapped[0] !== VERSION) {
    throw new Error(`unsupported wrapped key version ${wrapped[0]}`);
  }
  const ephemeralPublic = wrapped.subarray(1, 1 + KEY_SIZE);
  const recipientPublic = publicKey(recipientPrivate);
  const key = wrapKey(
    sharedSecret(recipientPrivate, ephemeralPublic),
    ephemeralPublic,
    recipientPublic,
  );
  return xchacha20poly1305(key, ZERO_NONCE, wrapAAD(epoch)).decrypt(
    wrapped.subarray(1 + KEY_SIZE),
  );
}
