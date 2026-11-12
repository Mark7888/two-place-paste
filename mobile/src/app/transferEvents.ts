/**
 * Every chunked transfer's progress, for whoever wants to hear it: the
 * screens' list (through `sessionStore`), and the background notification
 * (`backgroundJobs.ts`, `backgroundHold.ts`).
 *
 * It is its own module, with nothing imported, so all three can depend on it
 * without depending on each other.
 */

import type { Progress } from '../core';

const listeners = new Set<(p: Progress) => void>();

/** emitProgress tells every listener how a transfer is going. */
export function emitProgress(p: Progress): void {
  for (const listener of listeners) {
    listener(p);
  }
}

/** onTransferProgress subscribes to every transfer's progress. The returned function unsubscribes. */
export function onTransferProgress(listener: (p: Progress) => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}
