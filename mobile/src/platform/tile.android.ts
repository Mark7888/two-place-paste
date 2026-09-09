/**
 * The quick-settings tile, from the JavaScript side (SPEC §7.1).
 *
 * The tile itself is a `TileService` in Kotlin. Tapping it collapses the shade
 * and starts the app — the only legal moment to read the clipboard on
 * Android 10+ — and the app then performs exactly one sync and reports back
 * here, so the tile can render the outcome and show a toast.
 *
 * Two paths reach this module, because a tap can find the app in either state:
 * a running app receives an event, and a cold-started one asks
 * `pending()` once during startup.
 */

import type { TilePort } from './types';
import { TILE_REQUEST_EVENT, TppTile, tileEvents } from './native';

export const tile: TilePort = {
  available: TppTile !== null,

  requests(handler: () => void): () => void {
    const subscription = tileEvents?.addListener(TILE_REQUEST_EVENT, handler);
    return () => subscription?.remove();
  },

  report(ok: boolean, message: string): void {
    // The message is a status, never clipboard content: a toast is visible on
    // a locked screen's shade and above other apps.
    TppTile?.report(ok, message);
  },

  async pending(): Promise<boolean> {
    return (await TppTile?.consumePendingRequest()) ?? false;
  },
};
