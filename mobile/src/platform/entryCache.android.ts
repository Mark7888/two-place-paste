/**
 * The downloaded-entry cache on Android: the rules in `src/core` over files in
 * the app's cache directory (`CacheModule.kt`).
 *
 * Bytes cross the native bridge as base64, with the core's fast codec, done in
 * slices so the app keeps answering taps meanwhile:
 * a 50 MB entry read back from disk still beats downloading it again by far.
 */

import { EntryCache, decodeBase64StdAsync, encodeBase64StdAsync, type CacheStore } from '../core';
import { TppCache, type TppCacheModule } from './native';

function storeOver(native: TppCacheModule): CacheStore {
  return {
    async read(name) {
      const b64 = await native.read(name);
      return b64 === null ? null : decodeBase64StdAsync(b64);
    },
    write: async (name, data) => native.write(name, await encodeBase64StdAsync(data)),
    remove: (name) => native.remove(name),
    list: () => native.list(),
  };
}

const store: CacheStore | null = TppCache === null ? null : storeOver(TppCache);

/** entryCache is null in a build without the native module, which then simply keeps nothing. */
export const entryCache: EntryCache | null = store === null ? null : new EntryCache(store);
