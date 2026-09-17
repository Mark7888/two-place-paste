/**
 * Key storage on Android: the Keystore-backed encrypted store
 * (spec/crypto.md §10).
 *
 * `react-native-keychain` on Android encrypts the value with an AES key held in
 * the Android Keystore, where this process can use it but cannot extract it,
 * and stores the ciphertext in the app's own shared preferences. That is what
 * "other local applications cannot read them" means on this platform.
 *
 * No biometric prompt is attached. The tile has to be able to sync from a
 * locked-then-unlocked device without a dialog, and a key the user must
 * authenticate to reach would make the tile's one-tap promise a two-tap
 * promise. The device lock is the boundary this relies on.
 */

import * as Keychain from 'react-native-keychain';

import type { SecureStorePort } from './types';

/** SERVICE_PREFIX namespaces this app's entries in the platform store. */
const SERVICE_PREFIX = 'com.twoplacepaste.state.';

/** The username half of the credential is unused; the value is the password half. */
const ACCOUNT = 'tpp';

const options = (name: string): Keychain.SetOptions => ({
  service: SERVICE_PREFIX + name,
  accessible: Keychain.ACCESSIBLE.WHEN_UNLOCKED_THIS_DEVICE_ONLY,
  storage: Keychain.STORAGE_TYPE.AES_GCM_NO_AUTH,
});

export const secureStore: SecureStorePort = {
  async load(name: string): Promise<string | null> {
    const stored = await Keychain.getGenericPassword({ service: SERVICE_PREFIX + name });
    return stored === false ? null : stored.password;
  },

  async save(name: string, value: string): Promise<void> {
    const result = await Keychain.setGenericPassword(ACCOUNT, value, options(name));
    if (result === false) {
      // Never include the value: it is the device private key and the group key
      // (docs/conventions.md §1).
      throw new Error('the Android Keystore refused to store this device’s keys');
    }
  },

  async clear(name: string): Promise<void> {
    await Keychain.resetGenericPassword({ service: SERVICE_PREFIX + name });
  },
};
