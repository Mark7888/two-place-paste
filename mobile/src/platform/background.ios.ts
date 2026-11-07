/**
 * No background uploads on iOS yet: an upload runs while the app does. The
 * port exists so the share flow is one path on both platforms.
 */

import type { BackgroundPort } from './types';

export const background: BackgroundPort = {
  available: false,
  askToNotify: async () => {},
  start: async () => {
    throw new Error('this platform cannot upload in the background');
  },
  progress: () => {},
  result: () => {},
};
