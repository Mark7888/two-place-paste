/**
 * The codes that travel out of band, as a QR code or as copied text: the
 * pairing payload a member shows (SPEC §3.2) and the offer a device with no
 * group shows (docs/plans/joiner-emitted-pairing.md).
 *
 * Both are identical in every direction — a phone must be able to read what a
 * desktop shows, and the desktop must be able to read what the phone shows —
 * so the encoding lives in the wire contract and both clients reproduce it
 * byte for byte: the protobuf bytes, base64url without padding.
 */

// Installs the UTF-8 the protobuf codec reaches for. It must be imported
// above the generated messages below: on Hermes they cannot encode or
// decode a single field without it.
import './textEncoding';

import { sha256 } from '@noble/hashes/sha2.js';

import { fromBase64URL, toBase64URL, toHex } from '../bytes';
import { PairingCode, PairingOffer, PairingPayload } from '../../protocol/gen/tpp/v1/pairing';
import { TppError } from './errors';
import { splitCreationURL } from './urls';

/**
 * encodePairingPayload serializes an invitation to the string a QR code
 * carries.
 *
 * An invitation is emitted bare, not wrapped in a PairingCode, even though the
 * wrapper can carry one. Wrapping it would buy nothing and would stop every
 * build that predates the wrapper from reading a code this one shows — the
 * compatibility problem the wrapper exists to avoid, pointed the other way.
 * Only an offer, which no such build understands at all, is wrapped.
 */
export function encodePairingPayload(payload: PairingPayload): string {
  return toBase64URL(PairingPayload.encode(payload).finish());
}

/**
 * encodePairingOffer serializes a joiner-emitted offer: a PairingCode wrapping
 * it, base64url without padding
 * (docs/plans/joiner-emitted-pairing.md).
 *
 * The wrapper is not optional. Protobuf decodes by field number, so a bare
 * PairingOffer decodes as a PairingPayload without complaint — an offer shown
 * unwrapped would be read by the other side as an invitation with a nonsense
 * token.
 */
export function encodePairingOffer(offer: PairingOffer): string {
  return toBase64URL(PairingCode.encode({ offer }).finish());
}

/**
 * decodePairingOffer parses a scanned or pasted offer, and rejects an
 * invitation rather than half-understanding it.
 */
export function decodePairingOffer(text: string): PairingOffer {
  const code = decodePairingCode(text);
  if (code.offer === undefined) {
    throw new TppError('invalid', 'that is a pairing invitation, not an offer');
  }
  return code.offer;
}

/**
 * decodePairingCode parses a scanned or pasted code and reports which of the
 * two kinds it is.
 *
 * Protobuf decodes by field number, not by name, so neither form can simply be
 * tried and trusted: a bare PairingPayload is a syntactically fine PairingCode
 * and the reverse holds too. The order below is what makes it unambiguous in
 * practice, and it mirrors the Go client's `DecodeCode` exactly.
 *
 *  1. An offer is only ever emitted wrapped, so a wrapper carrying a usable
 *     offer is an offer, full stop.
 *  2. An invitation is only ever emitted bare, so that is tried next. This is
 *     also the fallback for a code shown by a build that predates PairingCode.
 *  3. A wrapper carrying a usable invitation is accepted last, for an
 *     implementation that chose to wrap one. Reaching here means the bare
 *     reading produced nothing usable, so there is nothing to be ambiguous
 *     with.
 */
export function decodePairingCode(text: string): PairingCode {
  let raw: Uint8Array;
  try {
    raw = fromBase64URL(text);
  } catch (cause) {
    throw new TppError('invalid', 'the pairing code is not valid base64url', { cause });
  }

  let wrapped: PairingCode | null = null;
  try {
    wrapped = PairingCode.decode(raw);
  } catch {
    // Not a wrapper; the bare reading below is the only one left.
  }
  if (wrapped?.offer !== undefined && validOffer(wrapped.offer)) {
    return { offer: wrapped.offer };
  }

  try {
    const bare = PairingPayload.decode(raw);
    if (validInvite(bare)) {
      return { invite: bare };
    }
  } catch {
    // Not a bare invitation either.
  }

  if (wrapped?.invite !== undefined && validInvite(wrapped.invite)) {
    return { invite: wrapped.invite };
  }
  throw new TppError('invalid', 'the pairing code is neither an invitation nor an offer');
}

function validInvite(p: PairingPayload): boolean {
  return p.serverUrl !== '' && p.pairingToken !== '';
}

function validOffer(o: PairingOffer): boolean {
  return o.serverUrl !== '' && o.offerCode !== '' && o.devicePublicKey.length > 0;
}

/**
 * fingerprint renders a device public key for a person to read aloud or
 * compare across two screens.
 *
 * It is the first 8 bytes of SHA-256 over the raw key, uppercase hex, in
 * groups of four: "A1B2 C3D4 E5F6 0718". This must match the Go client's
 * `tppclient.Fingerprint` byte for byte — the whole point is that the same key
 * looks the same on the phone showing an offer and on the desktop about to
 * accept it — so both have a test pinning the same vectors.
 *
 * It is not a crypto primitive and spec/crypto.md derives nothing from it: the
 * bytes that matter are compared byte for byte by the relay and bound into the
 * wrap by spec/crypto.md §4.2. This is the part a human checks, and 64 bits is
 * what a human will actually compare.
 */
export function fingerprint(publicKey: Uint8Array): string {
  const hexed = toHex(sha256(publicKey).subarray(0, 8)).toUpperCase();
  const groups: string[] = [];
  for (let i = 0; i < hexed.length; i += 4) {
    groups.push(hexed.slice(i, i + 4));
  }
  return groups.join(' ');
}

/**
 * decodePairingPayload parses a scanned or pasted pairing code.
 *
 * It tolerates what a clipboard adds — surrounding whitespace and padding —
 * and rejects anything that is not a payload with a server URL and a token,
 * rather than dialling a half-decoded host.
 */
export function decodePairingPayload(text: string): PairingPayload {
  const code = decodePairingCode(text);
  if (code.invite === undefined) {
    throw new TppError('invalid', 'that is a pairing offer, not an invitation');
  }
  return code.invite;
}

/**
 * CodeKind is what a scanned or pasted string turns out to be.
 *
 * There are three: the creation link on a relay's admin page (SPEC §3.1), the
 * pairing payload a device that is already in a group displays (SPEC §3.2),
 * and the offer a device with no group shows for a member to accept
 * (docs/plans/joiner-emitted-pairing.md).
 *
 * They are told apart by shape rather than by asking the user which one they
 * are holding. A creation link is an http(s) URL ending in one token segment,
 * so it cannot be mistaken for either of the others. The two pairing codes are
 * both base64url protobuf, which is exactly why an offer is wrapped in a
 * PairingCode: without that discriminator protobuf would decode one as the
 * other without complaining.
 *
 * Which kinds a screen can act on depends on where it is. An unpaired device
 * can use a creation link or an invitation; a member can use an offer. Telling
 * the user they are holding the wrong one is better than a failure further in.
 */
export type CodeKind = 'creation-url' | 'pairing-code' | 'offer-code' | 'unknown';

/** classifyCode reports what a scanned or pasted code is, without acting on it. */
export function classifyCode(text: string): CodeKind {
  const trimmed = text.trim();
  if (trimmed === '') {
    return 'unknown';
  }
  try {
    splitCreationURL(trimmed);
    return 'creation-url';
  } catch {
    // Not a creation link; fall through to the two pairing codes.
  }
  try {
    return decodePairingCode(trimmed).offer !== undefined ? 'offer-code' : 'pairing-code';
  } catch {
    return 'unknown';
  }
}
