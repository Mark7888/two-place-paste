/**
 * iOS is deferred (SPEC §7.3): there is no iOS build yet, so there are no
 * notices to show. An iOS build will need its own, generated the same way.
 */

import type { NoticesPort } from './types';

export const notices: NoticesPort = {
  read: async () => null,
};
