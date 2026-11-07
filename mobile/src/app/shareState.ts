/**
 * The share waiting on the user, as a module rather than React state.
 *
 * A share can arrive before any window is on screen — it is what started the
 * process — so, like the session and the tile handler, it is listened for
 * from the moment the bundle loads (`index.js` calls `initShare`), and the
 * share panel renders whatever is here when it mounts.
 */

import { share, type Shared } from '../platform';

let current: Shared | null = null;
let started = false;
const listeners = new Set<() => void>();

function emit(): void {
  for (const listener of listeners) {
    listener();
  }
}

export function subscribeShare(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/** getShared is the share the panel is about, or null. */
export function getShared(): Shared | null {
  return current;
}

/**
 * initShare starts listening for shares. It is called once, from `index.js`;
 * a second call does nothing.
 */
export function initShare(): void {
  if (started || !share.available) {
    return;
  }
  started = true;
  // Subscribed before the claim below: a share that arrives while this
  // process is starting is either already recorded (the claim finds it) or
  // arrives as an event (the listener finds it). It is claimed once either way.
  share.requests(() => void claim());
  void claim();
}

async function claim(): Promise<void> {
  const shared = await share.pending().catch(() => null);
  if (shared !== null) {
    // A newer share replaces the one on screen, as it does on the native side.
    current = shared;
    emit();
  }
}

/**
 * finishShare closes the panel for share `id`, with `message` as a toast over
 * the app the user returns to ("" for none). A share that has since been
 * replaced is left alone: the panel is asking about the new one.
 */
export function finishShare(id: number, message: string): void {
  if (current?.id === id) {
    current = null;
    emit();
  }
  share.finish(id, message);
}

/** openAppFromShare drops the share and brings the app forward — to pair, say. */
export function openAppFromShare(): void {
  current = null;
  emit();
  share.openApp();
}
