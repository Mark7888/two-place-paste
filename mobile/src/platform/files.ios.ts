/**
 * iOS is deferred (SPEC §7.3). The port exists so the app has one code path on
 * both platforms; here it reports that it cannot save or open files.
 */

import type { FilesPort } from './types';

export const files: FilesPort = {
  available: false,
  save: async () => {
    throw new Error('saving files is not supported on iOS yet');
  },
  open: async () => {
    throw new Error('opening files is not supported on iOS yet');
  },
};
