/**
 * Checking a new epoch's key before it is installed (spec/crypto.md §7.1).
 *
 * A wrapped key proves only that it was made for this device, and a device's
 * public key is no secret: anyone, the relay included, can wrap a key of their
 * own choosing to it. What the relay cannot make is a store link, because
 * sealing one takes the previous epoch's key. So a key for epoch E is accepted
 * only when the links lead from it, one epoch at a time, down to the store key
 * of the epoch this device already holds.
 */

import { openStoreLink, storeKey } from '../crypto';
import { TppError } from './errors';

/**
 * chainsBack throws an 'unverified-key' TppError unless `links` lead from
 * `candidate`, the group key of `epoch`, down to the store key of `current`,
 * the epoch whose group key (`currentKey`) this device holds.
 */
export function chainsBack(
  links: ReadonlyMap<bigint, Uint8Array>,
  current: bigint,
  currentKey: Uint8Array,
  epoch: bigint,
  candidate: Uint8Array,
): void {
  let key = storeKey(candidate, epoch);
  for (let e = epoch; e > current; e--) {
    const link = links.get(e);
    if (link === undefined) {
      throw new TppError('unverified-key', `the relay has no store link into epoch ${e}`);
    }
    try {
      key = openStoreLink(key, link, e);
    } catch (err) {
      throw new TppError('unverified-key', `the store link into epoch ${e} does not open with that key`, {
        cause: err,
      });
    }
  }
  if (!constantTimeEqual(key, storeKey(currentKey, current))) {
    throw new TppError('unverified-key', 'its store links lead to a key this device never held');
  }
}

/** constantTimeEqual compares two keys without stopping at the first difference. */
function constantTimeEqual(a: Uint8Array, b: Uint8Array): boolean {
  if (a.length !== b.length) {
    return false;
  }
  let diff = 0;
  for (let i = 0; i < a.length; i++) {
    diff |= a[i] ^ b[i];
  }
  return diff === 0;
}
