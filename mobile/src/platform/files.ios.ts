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
  openSaved: async () => {
    throw new Error('opening files is not supported on iOS yet');
  },
  open: async () => {
    throw new Error('opening files is not supported on iOS yet');
  },
  share: async () => {
    throw new Error('sharing files is not supported on iOS yet');
  },
  pick: async () => {
    throw new Error('picking files is not supported on iOS yet');
  },
  pickToStage: async () => {
    throw new Error('picking files is not supported on iOS yet');
  },
  saveStaged: async () => {
    throw new Error('saving files is not supported on iOS yet');
  },
  openStaged: async () => {
    throw new Error('opening files is not supported on iOS yet');
  },
  shareStaged: async () => {
    throw new Error('sharing files is not supported on iOS yet');
  },
};
