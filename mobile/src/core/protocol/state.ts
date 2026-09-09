/**
 * Everything an installation must survive a restart with, and where it lives
 * at rest (spec/crypto.md §10).
 *
 * It is one blob because all of it is secret-bearing: the device private key
 * and the group key obviously, and the identifiers because they are bearer
 * credentials on this wire. It is written through a SecureStore — on Android,
 * Keystore-backed encrypted storage — and nowhere else. Never to a log, never
 * to AsyncStorage.
 */

import { fromBase64URL, toBase64URL } from '../bytes';
import { GROUP_KEY_SIZE, KEY_SIZE, generateDeviceKey, publicKey } from '../crypto';
import { TppError } from './errors';

/** DEFAULT_STATE_NAME is the secure-store entry this client keeps its state under. */
export const DEFAULT_STATE_NAME = 'session';

/**
 * SecureStore is the port to platform key storage. `src/platform` supplies the
 * Android implementation; the tests and the interop script supply their own,
 * which is the only reason this is an interface rather than a direct import.
 */
export interface SecureStore {
  /** load returns the stored value, or null when nothing is stored under the name. */
  load(name: string): Promise<string | null>;
  /** save writes a value, replacing any previous one. */
  save(name: string, value: string): Promise<void>;
  /** clear removes a value. Removing what is not there is not an error. */
  clear(name: string): Promise<void>;
}

/** ClientState is the persisted identity of this installation. */
export interface ClientState {
  /** serverUrl is the relay's base URL, e.g. "https://tpp.example.com". */
  serverUrl: string;
  groupId: string;
  deviceId: string;
  deviceName: string;
  /** epoch is the group key generation this client holds. It holds exactly one (epoch, group key) pair. */
  epoch: bigint;
  /** devicePrivateKey is the X25519 private key generated on first launch. */
  devicePrivateKey: Uint8Array;
  /** groupKey is the current group key, unwrapped. Empty until this device joins a group. */
  groupKey: Uint8Array;
}

/** newState generates the state of a first launch: a device keypair and nothing else. */
export function newState(deviceName: string): ClientState {
  return {
    serverUrl: '',
    groupId: '',
    deviceId: '',
    deviceName,
    epoch: 0n,
    devicePrivateKey: generateDeviceKey(),
    groupKey: new Uint8Array(0),
  };
}

/** inGroup reports whether this state can talk to a group. */
export function inGroup(state: ClientState): boolean {
  return (
    state.groupId !== '' && state.deviceId !== '' && state.groupKey.length === GROUP_KEY_SIZE
  );
}

/** devicePublicKey derives this device's public key. */
export function devicePublicKey(state: ClientState): Uint8Array {
  if (state.devicePrivateKey.length !== KEY_SIZE) {
    throw new TppError(
      'invalid',
      `device private key is ${state.devicePrivateKey.length} bytes, want ${KEY_SIZE}`,
    );
  }
  return publicKey(state.devicePrivateKey);
}

/**
 * The stored shape. Keys are base64url strings and the epoch is a decimal
 * string: JSON.parse would round a u64 epoch, and this file is the one that
 * has to survive a rekey at any epoch.
 */
interface StoredState {
  server_url: string;
  group_id: string;
  device_id: string;
  device_name: string;
  epoch: string;
  device_private_key: string;
  group_key: string;
}

/** encodeState serializes state for the secure store. */
export function encodeState(state: ClientState): string {
  const stored: StoredState = {
    server_url: state.serverUrl,
    group_id: state.groupId,
    device_id: state.deviceId,
    device_name: state.deviceName,
    epoch: state.epoch.toString(),
    device_private_key: toBase64URL(state.devicePrivateKey),
    group_key: toBase64URL(state.groupKey),
  };
  return JSON.stringify(stored);
}

/** decodeState parses what encodeState wrote. */
export function decodeState(raw: string): ClientState {
  let stored: StoredState;
  try {
    stored = JSON.parse(raw) as StoredState;
  } catch (cause) {
    throw new TppError('invalid', 'stored state could not be decoded', { cause });
  }
  return {
    serverUrl: stored.server_url ?? '',
    groupId: stored.group_id ?? '',
    deviceId: stored.device_id ?? '',
    deviceName: stored.device_name ?? '',
    epoch: BigInt(stored.epoch ?? '0'),
    devicePrivateKey: fromBase64URL(stored.device_private_key ?? ''),
    groupKey: fromBase64URL(stored.group_key ?? ''),
  };
}

/**
 * loadState reads state from the secure store, generating a first-launch
 * identity when there is none. A device keypair is created once and then
 * never again: it is what this device is.
 */
export async function loadState(
  store: SecureStore,
  deviceName: string,
  name: string = DEFAULT_STATE_NAME,
): Promise<ClientState> {
  const raw = await store.load(name);
  if (raw === null) {
    const state = newState(deviceName);
    await saveState(store, state, name);
    return state;
  }
  const state = decodeState(raw);
  if (state.devicePrivateKey.length !== KEY_SIZE) {
    throw new TppError('invalid', 'stored state carries no usable device key');
  }
  return state;
}

/** saveState writes state through to the secure store. */
export async function saveState(
  store: SecureStore,
  state: ClientState,
  name: string = DEFAULT_STATE_NAME,
): Promise<void> {
  await store.save(name, encodeState(state));
}
