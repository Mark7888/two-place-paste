/**
 * What survives a restart, and what the URL helpers refuse.
 */

import { KEY_SIZE } from '../../crypto';
import {
  decodeState,
  encodeState,
  inGroup,
  loadState,
  newState,
  saveState,
} from '../state';
import { classifyCode, encodePairingPayload } from '../pairing';
import { normalizeServerURL, splitCreationURL, websocketURL } from '../urls';
import { MemoryStore } from './fakeRelay';

describe('stored state', () => {
  test('round trips, including an epoch no JSON number could hold', () => {
    const state = {
      ...newState('phone'),
      serverUrl: 'https://relay.example.com',
      groupId: 'group-1',
      deviceId: 'device-1',
      epoch: 2n ** 64n - 1n,
      groupKey: new Uint8Array(32).fill(7),
    };
    const back = decodeState(encodeState(state));
    expect(back).toEqual(state);
    expect(back.epoch).toBe(2n ** 64n - 1n);
  });

  test('a first launch generates one device keypair and keeps it', async () => {
    const store = new MemoryStore();
    const first = await loadState(store, 'phone');
    expect(first.devicePrivateKey).toHaveLength(KEY_SIZE);
    expect(inGroup(first)).toBe(false);

    const second = await loadState(store, 'phone');
    expect(second.devicePrivateKey).toEqual(first.devicePrivateKey);
  });

  test('a state with no group key is not in a group, whatever else it carries', async () => {
    const store = new MemoryStore();
    const state = { ...newState('phone'), groupId: 'g', deviceId: 'd' };
    expect(inGroup(state)).toBe(false);
    await saveState(store, state);
    expect(inGroup(await loadState(store, 'phone'))).toBe(false);
  });

  test('unreadable state is a typed failure, not a silent new identity', () => {
    expect(() => decodeState('{not json')).toThrow(/could not be decoded/);
  });
});

describe('relay URLs', () => {
  test('a base URL keeps scheme, host and port and loses everything else', () => {
    expect(normalizeServerURL(' https://relay.example.com:8443/some/path?x=1 ')).toBe(
      'https://relay.example.com:8443',
    );
  });

  test.each(['ftp://relay.example.com', 'relay.example.com', 'https://'])(
    'refuses %s',
    (raw) => {
      expect(() => normalizeServerURL(raw)).toThrow();
    },
  );

  test('a creation URL is a host and one token segment', () => {
    expect(splitCreationURL('https://relay.example.com/abc123')).toEqual({
      base: 'https://relay.example.com',
      token: 'abc123',
    });
    expect(() => splitCreationURL('https://relay.example.com/')).toThrow(/single-segment/);
    expect(() => splitCreationURL('https://relay.example.com/a/b')).toThrow(/single-segment/);
  });

  test('the transport endpoint carries the device credential, and the unauthenticated flows carry none', () => {
    expect(websocketURL('https://relay.example.com', 'device-1')).toBe(
      'wss://relay.example.com/ws?device_id=device-1',
    );
    expect(websocketURL('http://127.0.0.1:8080', '')).toBe('ws://127.0.0.1:8080/ws');
    expect(websocketURL('https://Relay.Example.COM:8443', 'a b+c')).toBe(
      'wss://relay.example.com:8443/ws?device_id=a%20b%2Bc',
    );
    expect(websocketURL('http://[::1]:8080', '')).toBe('ws://[::1]:8080/ws');
  });

  test.each(['https://relay.example.com:0', 'https://relay.example.com:99999', 'https://u:p@relay.example.com'])(
    'refuses %s',
    (raw) => {
      expect(() => normalizeServerURL(raw)).toThrow();
    },
  );

  /**
   * These helpers must not touch the platform's `URL`. Node's is
   * WHATWG-compliant and React Native's is a shim with getters and no setters,
   * so a helper that used it passed here and, on a phone, silently produced
   * the base URL back — `https://relay/` dialled as a WebSocket, which fails
   * with nothing but "socket error". Taking `URL` away is the only way this
   * suite can tell the difference.
   */
  test('the URL helpers do not depend on the platform’s URL class', () => {
    const real = globalThis.URL;
    Object.defineProperty(globalThis, 'URL', {
      configurable: true,
      value: function forbidden(): never {
        throw new Error('the URL helpers must not use the platform URL class');
      },
    });
    try {
      expect(normalizeServerURL('https://relay.example.com:8443/some/path?x=1')).toBe(
        'https://relay.example.com:8443',
      );
      expect(splitCreationURL('https://relay.example.com/abc123').token).toBe('abc123');
      expect(websocketURL('https://relay.example.com', 'device-1')).toBe(
        'wss://relay.example.com/ws?device_id=device-1',
      );
      expect(classifyCode('https://relay.example.com/abc123')).toBe('creation-url');
    } finally {
      Object.defineProperty(globalThis, 'URL', { configurable: true, value: real });
    }
  });
});

describe('telling the system’s two QR codes apart', () => {
  test('a creation link from the relay’s admin page', () => {
    expect(classifyCode('https://relay.example.com/abc123')).toBe('creation-url');
    expect(classifyCode('  http://127.0.0.1:8080/tok_1  ')).toBe('creation-url');
  });

  test('a pairing payload from a device that is already in a group', () => {
    const payload = encodePairingPayload({
      serverUrl: 'https://relay.example.com',
      pairingToken: 'pair-1',
      inviterEphemeralPublicKey: new Uint8Array(32).fill(3),
    });
    expect(classifyCode(payload)).toBe('pairing-code');
    // What a clipboard adds must not change the answer.
    expect(classifyCode(` ${payload}==\n`)).toBe('pairing-code');
  });

  test('anything else is unknown, and is never guessed at', () => {
    for (const junk of ['', '   ', 'hello world', 'https://relay.example.com/', 'ftp://x/y']) {
      expect(classifyCode(junk)).toBe('unknown');
    }
  });
});
