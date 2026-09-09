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
  });
});
