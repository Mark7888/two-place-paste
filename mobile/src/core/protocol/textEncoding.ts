/**
 * The Text Encoding API, for a runtime that does not have one.
 *
 * Every protobuf message in this client is encoded and decoded by
 * `@bufbuild/protobuf`, which converts `string` fields through the WHATWG Text
 * Encoding API and builds its encoder and decoder the first time any message is
 * touched:
 *
 *     const te = new globalThis.TextEncoder();
 *     const td = new globalThis.TextDecoder();
 *
 * Hermes has `TextEncoder` and no `TextDecoder`, so on a phone the second line
 * is `new undefined()`. Every encode and every decode threw
 * "undefined cannot be used as a constructor" before it read or wrote a byte,
 * which took both ways into a group with it:
 *
 *   - Creating a group surfaced the TypeError verbatim, because `createGroup`
 *     encodes a CreateGroupRequest.
 *   - Scanning a code from a desktop reported "that is neither a pairing code
 *     nor a creation link", because `decodePairingCode` treats a throwing
 *     `decode` as "not this kind of code" — which is the right reading of a
 *     parse failure and the wrong one of a missing constructor.
 *
 * Node has both globals, so the unit tests, the interop suite and the desktop
 * client never saw it. This is `bytes.ts`'s lesson arriving through a
 * dependency: "none of the three is guaranteed on Hermes". The UTF-8 this
 * installs is the one already written there, pinned against the Go client by
 * the crypto vectors, rather than a polyfill whose edge cases would be a wire
 * incompatibility.
 *
 * Importing this module installs it. The three modules that encode or decode a
 * protobuf message — `client`, `connection` and `pairing` — import it above
 * their generated-message imports, so no code path can reach the codec before
 * it is configured.
 */

import { configureTextEncoding } from '@bufbuild/protobuf/wire';

import { fromUTF8, utf8 } from '../bytes';

/**
 * encodeUtf8 is `utf8` under the codec's signature. `utf8` builds its result
 * with `Uint8Array.from`, so the buffer is a plain `ArrayBuffer` and is this
 * string's alone; the annotation says what it already is.
 */
function encodeUtf8(text: string): Uint8Array<ArrayBuffer> {
  return utf8(text) as Uint8Array<ArrayBuffer>;
}

/**
 * checkUtf8 reports whether a string can be encoded as UTF-8 at all, which for
 * a JavaScript string means: every surrogate in it is half of a pair. A lone
 * surrogate has no UTF-8 encoding, and `utf8` writes U+FFFD for one rather
 * than throwing — so this is the check that has to say so, and it is the only
 * place in this client that distinguishes the two.
 *
 * The library's own fallback asks `encodeURIComponent` to throw. This asks the
 * question directly.
 */
function checkUtf8(text: string): boolean {
  for (let i = 0; i < text.length; i++) {
    const unit = text.charCodeAt(i);
    if (unit < 0xd800 || unit > 0xdfff) {
      continue;
    }
    // A high surrogate must be followed by a low one; a low one must not
    // appear on its own, and the pair above has already consumed the only low
    // surrogate that is legitimate.
    if (unit > 0xdbff) {
      return false;
    }
    const next = i + 1 < text.length ? text.charCodeAt(i + 1) : 0;
    if (next < 0xdc00 || next > 0xdfff) {
      return false;
    }
    i++;
  }
  return true;
}

/**
 * installTextEncoding points the protobuf codec at this client's UTF-8. It is
 * idempotent and runs on import; it is exported so a test can reinstall it
 * after tearing the globals down.
 */
export function installTextEncoding(): void {
  configureTextEncoding({ encodeUtf8, decodeUtf8: fromUTF8, checkUtf8 });
}

installTextEncoding();
