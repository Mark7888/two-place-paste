/**
 * What this device calls itself in every other device's revocation dialog
 * (SPEC §3.3 step 2).
 */

import { Platform } from 'react-native';

import type { DeviceInfoPort } from './types';

export const deviceInfo: DeviceInfoPort = {
  defaultName(): string {
    // A name the user recognises, from information the OS already gives every
    // app. Nothing here is a secret and nothing here is clipboard content.
    // Platform.constants is a union across platforms; this file is only ever
    // bundled for Android, where `Release` is the OS version string.
    const { Release } = Platform.constants as { Release?: string };
    const release = Release ?? '';
    return release === '' ? 'Android phone' : `Android ${release} phone`;
  },
};
