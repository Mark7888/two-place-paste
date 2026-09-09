/**
 * The pairing payload (SPEC §3.2): the string that travels out of band, as a
 * QR code or as copied text.
 *
 * The payload is identical in every direction — a phone must be able to read
 * what a desktop shows, and the desktop must be able to read what the phone
 * shows — so the encoding lives in the wire contract and both clients
 * reproduce it byte for byte: the protobuf bytes, base64url without padding.
 */

import { fromBase64URL, toBase64URL } from '../bytes';
import { PairingPayload } from '../../protocol/gen/tpp/v1/pairing';
import { TppError } from './errors';

/** encodePairingPayload serializes a payload to the string a QR code carries. */
export function encodePairingPayload(payload: PairingPayload): string {
  return toBase64URL(PairingPayload.encode(payload).finish());
}

/**
 * decodePairingPayload parses a scanned or pasted pairing code.
 *
 * It tolerates what a clipboard adds — surrounding whitespace and padding —
 * and rejects anything that is not a payload with a server URL and a token,
 * rather than dialling a half-decoded host.
 */
export function decodePairingPayload(text: string): PairingPayload {
  let raw: Uint8Array;
  try {
    raw = fromBase64URL(text);
  } catch (cause) {
    throw new TppError('invalid', 'the pairing code is not valid base64url', { cause });
  }
  let payload: PairingPayload;
  try {
    payload = PairingPayload.decode(raw);
  } catch (cause) {
    throw new TppError('invalid', 'the pairing code is not a pairing payload', { cause });
  }
  if (payload.serverUrl === '' || payload.pairingToken === '') {
    throw new TppError('invalid', 'the pairing code carries no server URL or token');
  }
  return payload;
}
