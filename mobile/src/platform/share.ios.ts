/**
 * Sharing to the app is Android-only for now: iOS would need a share
 * extension, a separate target with its own process. The port exists so the
 * app's startup path is one path on both platforms.
 */

import type { SharePort } from './types';

export const share: SharePort = {
  available: false,
  nativeSeal: null,
  requests: () => () => {},
  pending: async () => null,
  stage: async () => {
    throw new Error('this platform cannot take shared files');
  },
  readStaged: async () => {
    throw new Error('this platform cannot take shared files');
  },
  discard: () => {},
  finish: () => {},
  openApp: () => {},
};
