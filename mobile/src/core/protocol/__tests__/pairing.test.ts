/**
 * The two codes that travel out of band, and the fingerprint a user compares
 * across two screens.
 *
 * The fingerprint vectors here are the same ones `TestFingerprintFormat` pins
 * in the Go client. If the two ever disagree the confirmation dialog of
 * docs/plans/joiner-emitted-pairing.md §5 is worthless, because the user is
 * being asked to compare two renderings of one key that do not look alike.
 */

import { PairingPayload } from '../../../protocol/gen/tpp/v1/pairing';
import { toBase64URL } from '../../bytes';
import {
  classifyCode,
  decodePairingCode,
  decodePairingOffer,
  decodePairingPayload,
  encodePairingOffer,
  encodePairingPayload,
  fingerprint,
} from '../pairing';

const invite = {
  serverUrl: 'https://tpp.example.com',
  pairingToken: 'AbCd-1234_x',
  inviterEphemeralPublicKey: new Uint8Array(32).fill(0x2a),
};

const offer = {
  serverUrl: 'https://tpp.example.com',
  offerCode: 'Zz99-offer_x',
  devicePublicKey: new Uint8Array(32).fill(0x7f),
  deviceName: 'Anna — phone',
};

describe('fingerprint', () => {
  test('matches the Go client, byte for byte', () => {
    expect(fingerprint(new Uint8Array(32).fill(0x00))).toBe('6668 7AAD F862 BD77');
    expect(fingerprint(new Uint8Array(32).fill(0xff))).toBe('AF96 1376 0F72 635F');
    expect(fingerprint(new Uint8Array([0x74, 0x70, 0x70]))).toBe('1B3C EA64 57B6 9A77');
  });
});

describe('pairing codes', () => {
  test('an invitation and an offer are told apart, not half-understood', () => {
    const encodedInvite = encodePairingPayload(invite);
    const encodedOffer = encodePairingOffer(offer);
    expect(encodedInvite).not.toBe(encodedOffer);
    expect(encodedOffer).not.toMatch(/[+/=]/);

    expect(classifyCode(encodedInvite)).toBe('pairing-code');
    expect(classifyCode(encodedOffer)).toBe('offer-code');

    expect(decodePairingCode(encodedInvite).invite?.pairingToken).toBe(invite.pairingToken);
    const decoded = decodePairingOffer(encodedOffer);
    expect(decoded.offerCode).toBe(offer.offerCode);
    expect(decoded.deviceName).toBe(offer.deviceName);
    expect(decoded.devicePublicKey).toEqual(offer.devicePublicKey);

    // The typed entry points refuse the other kind rather than decoding it
    // into nonsense, which is exactly what a bare PairingOffer would do.
    expect(() => decodePairingPayload(encodedOffer)).toThrow();
    expect(() => decodePairingOffer(encodedInvite)).toThrow();
  });

  test('what a clipboard adds is tolerated, and what is not a code is refused', () => {
    const encodedOffer = encodePairingOffer(offer);
    for (const variant of [encodedOffer, ` ${encodedOffer}\n`, `${encodedOffer}==`]) {
      expect(decodePairingOffer(variant).offerCode).toBe(offer.offerCode);
    }
    for (const bad of ['', 'not base64!!', 'AAAA']) {
      expect(() => decodePairingCode(bad)).toThrow();
      expect(classifyCode(bad)).toBe('unknown');
    }
  });

  test('an invitation stays readable by builds that predate PairingCode', () => {
    // Emitted bare, so an older decoder — which knows only PairingPayload —
    // reads what this build shows.
    const encoded = encodePairingPayload(invite);
    const asOldBuildSeesIt = PairingPayload.decode(
      Uint8Array.from(atobLike(encoded)),
    );
    expect(asOldBuildSeesIt.serverUrl).toBe(invite.serverUrl);
    expect(asOldBuildSeesIt.pairingToken).toBe(invite.pairingToken);

    // And the other direction: what such a build emits is the fallback in
    // decodePairingCode.
    const theirs = toBase64URL(PairingPayload.encode(invite).finish());
    expect(decodePairingCode(theirs).invite?.pairingToken).toBe(invite.pairingToken);
  });
});

/** atobLike decodes unpadded base64url without relying on a platform global. */
function atobLike(s: string): number[] {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_';
  const out: number[] = [];
  let bits = 0;
  let value = 0;
  for (const ch of s) {
    const index = alphabet.indexOf(ch);
    if (index < 0) {
      continue;
    }
    value = (value << 6) | index;
    bits += 6;
    if (bits >= 8) {
      bits -= 8;
      out.push((value >> bits) & 0xff);
    }
  }
  return out;
}
