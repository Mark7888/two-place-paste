/**
 * Key storage on iOS: the Keychain, with the same service naming as Android
 * (spec/crypto.md §10). iOS is deferred (SPEC §7.3); this file exists so the
 * platform surface is complete on both targets.
 */

import * as Keychain from 'react-native-keychain';

import type { SecureStorePort } from './types';

const SERVICE_PREFIX = 'com.twoplacepaste.state.';
const ACCOUNT = 'tpp';

export const secureStore: SecureStorePort = {
  async load(name: string): Promise<string | null> {
    const stored = await Keychain.getGenericPassword({ service: SERVICE_PREFIX + name });
    return stored === false ? null : stored.password;
  },

  async save(name: string, value: string): Promise<void> {
    const result = await Keychain.setGenericPassword(ACCOUNT, value, {
      service: SERVICE_PREFIX + name,
      accessible: Keychain.ACCESSIBLE.WHEN_UNLOCKED_THIS_DEVICE_ONLY,
    });
    if (result === false) {
      throw new Error('the Keychain refused to store this device’s keys');
    }
  },

  async clear(name: string): Promise<void> {
    await Keychain.resetGenericPassword({ service: SERVICE_PREFIX + name });
  },
};
