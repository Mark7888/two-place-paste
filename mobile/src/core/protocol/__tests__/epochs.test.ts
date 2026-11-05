/**
 * spec/crypto.md §7.1: a new epoch's key is accepted only when the store links
 * lead from it to the key this device holds.
 */

import { generateGroupKey, sealStoreLink, storeKey } from '../../crypto';
import { chainsBack } from '../epochs';

/** keys[e] is the group key of epoch e. */
const keys = [new Uint8Array(0), generateGroupKey(), generateGroupKey(), generateGroupKey(), generateGroupKey()];
const link = (older: Uint8Array, newer: Uint8Array, epoch: bigint) =>
  sealStoreLink(storeKey(newer, epoch), storeKey(older, epoch - 1n), epoch);

const honest = new Map([
  [2n, link(keys[1], keys[2], 2n)],
  [3n, link(keys[2], keys[3], 3n)],
  [4n, link(keys[3], keys[4], 4n)],
]);

// What a relay can make on its own: a key of its choosing, and a link from it
// to a store key it also chose, since it never held keys[1].
const forged = generateGroupKey();
const withForgery = new Map([[2n, link(generateGroupKey(), forged, 2n)]]);

describe('chainsBack', () => {
  test.each([
    ['one epoch', honest, 1n, 2n, keys[2]],
    ['three epochs at once', honest, 1n, 4n, keys[4]],
  ])('accepts %s', (_name, links, current, epoch, candidate) => {
    expect(() => chainsBack(links, current, keys[Number(current)], epoch, candidate)).not.toThrow();
  });

  test.each([
    ["the relay's own key and link", withForgery, 1n, 2n, forged],
    ["the relay's own key, honest links", honest, 1n, 2n, forged],
    ['no links at all', new Map<bigint, Uint8Array>(), 1n, 2n, keys[2]],
    ['a link missing in the middle', new Map([[2n, honest.get(2n)!], [4n, honest.get(4n)!]]), 1n, 4n, keys[4]],
    ['a link presented for another epoch', new Map([[3n, honest.get(2n)!]]), 2n, 3n, keys[3]],
  ])('refuses %s', (_name, links, current, epoch, candidate) => {
    let thrown: unknown;
    try {
      chainsBack(links, current, keys[Number(current)], epoch, candidate);
    } catch (err) {
      thrown = err;
    }
    expect(thrown).toMatchObject({ kind: 'unverified-key' });
  });
});
