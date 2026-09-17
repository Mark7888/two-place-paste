/**
 * The CSPRNG every key, nonce and identifier in this client comes from
 * (spec/crypto.md §2.2).
 *
 * On React Native `react-native-get-random-values` installs
 * `crypto.getRandomValues` over the platform CSPRNG; the app entry point
 * imports it before anything else. Under Node — the vector tests and the
 * interop script — the global is already there. Either way this module refuses
 * to invent randomness of its own: a missing CSPRNG is a fatal condition, not
 * an occasion to fall back on `Math.random`.
 */

/** randomBytes returns n bytes from the platform CSPRNG. */
export function randomBytes(n: number): Uint8Array {
  const source = (globalThis as { crypto?: Crypto }).crypto;
  if (!source || typeof source.getRandomValues !== 'function') {
    throw new Error(
      'no CSPRNG: import react-native-get-random-values before any TwoPlacePaste module',
    );
  }
  const out = new Uint8Array(n);
  // getRandomValues is capped at 65536 bytes per call by the Web Crypto spec.
  for (let offset = 0; offset < n; offset += 65536) {
    source.getRandomValues(out.subarray(offset, Math.min(offset + 65536, n)));
  }
  return out;
}
