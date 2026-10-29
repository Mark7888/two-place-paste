/**
 * iOS is deferred (SPEC §7.3): there is no iOS build yet, so there is no
 * version to report. An iOS build will read its bundle's version here.
 */

import type { AppInfoPort } from './types';

export const appInfo: AppInfoPort = {
  version: async () => ({ version: '', stamp: 0 }),
};
