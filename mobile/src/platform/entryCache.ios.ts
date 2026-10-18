/** iOS is deferred (SPEC §7.3): no cache, so every read goes to the relay. */

import type { EntryCache } from '../core';

export const entryCache: EntryCache | null = null;
