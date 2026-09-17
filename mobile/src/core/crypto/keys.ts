/**
 * Device and group key material (spec/crypto.md §3, §4.1).
 */

import { x25519 } from '@noble/curves/ed25519.js';

import { randomBytes } from '../random';

/** KEY_SIZE is the size of an X25519 private or public key. */
export const KEY_SIZE = 32;

/** GROUP_KEY_SIZE is the size of a group key. */
export const GROUP_KEY_SIZE = 32;

/** NONCE_SIZE is the size of an XChaCha20-Poly1305 nonce, and of an entry nonce. */
export const NONCE_SIZE = 24;

/** TAG_SIZE is the size of a Poly1305 tag. */
export const TAG_SIZE = 16;

/** VERSION is the profile's version byte, bound into every container's associated data (§2.4). */
export const VERSION = 0x01;

/**
 * generateDeviceKey returns a device private key: 32 raw bytes from the
 * CSPRNG, unclamped, because clamping happens inside the scalar
 * multiplication and the stored key must stay as generated (§3).
 */
export function generateDeviceKey(): Uint8Array {
  return randomBytes(KEY_SIZE);
}

/** publicKey derives the X25519 public key of a private key. */
export function publicKey(privateKey: Uint8Array): Uint8Array {
  if (privateKey.length !== KEY_SIZE) {
    throw new Error(`private key is ${privateKey.length} bytes, want ${KEY_SIZE}`);
  }
  return x25519.getPublicKey(privateKey);
}

/**
 * sharedSecret is the raw X25519 agreement. `@noble/curves` rejects an
 * all-zero output, which is the check RFC 7748 §6.1 requires.
 */
export function sharedSecret(privateKey: Uint8Array, peerPublicKey: Uint8Array): Uint8Array {
  if (peerPublicKey.length !== KEY_SIZE) {
    throw new Error(`peer public key is ${peerPublicKey.length} bytes, want ${KEY_SIZE}`);
  }
  return x25519.getSharedSecret(privateKey, peerPublicKey);
}

/**
 * generateGroupKey returns a fresh group key (§4.1). A rekey calls this: a new
 * epoch's key is new randomness, never a ratchet from the old one, because
 * revocation has to be a hard break.
 */
export function generateGroupKey(): Uint8Array {
  return randomBytes(GROUP_KEY_SIZE);
}

/** generateNonce returns an entry nonce: 24 CSPRNG bytes, fresh for every entry (§5.1). */
export function generateNonce(): Uint8Array {
  return randomBytes(NONCE_SIZE);
}
