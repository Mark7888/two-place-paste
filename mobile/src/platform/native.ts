/**
 * The two native modules this app ships, declared once.
 *
 * `TppTile` is the bridge to the quick-settings tile (SPEC §7.1); `TppClipboard`
 * fills the one gap the clipboard library leaves on Android — it can read an
 * image from the clipboard but only write one on iOS, and an image that can be
 * received but not pasted would be half a feature.
 *
 * Both are optional at runtime. A JS bundle running in a host that does not
 * carry them — a test, a future platform — sees `null` and the app degrades
 * rather than crashing on import.
 */

import { NativeEventEmitter, NativeModules } from 'react-native';

/** TppTileModule is implemented by `android/app/src/main/java/.../TileModule.kt`. */
export interface TppTileModule {
  /** report tells the tile how a sync went; it renders the state and shows a toast. */
  report(ok: boolean, message: string): void;
  /** consumePendingRequest returns true once if the app was launched by a tile tap. */
  consumePendingRequest(): Promise<boolean>;
}

/** TppClipboardModule is implemented by `android/app/src/main/java/.../ClipboardModule.kt`. */
export interface TppClipboardModule {
  /** setImagePNG puts a PNG on the clipboard as a content URI the pasting app may read. */
  setImagePNG(base64: string): Promise<void>;

  /**
   * primaryClipTimestamp is when the clipboard was last set, in UTC
   * milliseconds, or 0 when the platform will not say (SPEC §6).
   */
  primaryClipTimestamp(): Promise<number>;
}

const modules = NativeModules as {
  TppTile?: TppTileModule;
  TppClipboard?: TppClipboardModule;
};

export const TppTile: TppTileModule | null = modules.TppTile ?? null;
export const TppClipboard: TppClipboardModule | null = modules.TppClipboard ?? null;

/** TILE_REQUEST_EVENT is emitted by the native tile bridge when a tap reaches a running app. */
export const TILE_REQUEST_EVENT = 'TppTileSyncRequested';

/** tileEvents is the emitter tile requests arrive on, or null where there is no tile. */
/**
 * React Native does not export the structural type its event emitter wants, so
 * it is written out: the two methods a native module must have for JavaScript
 * to subscribe to it.
 */
type EmitterModule = {
  addListener(eventType: string): void;
  removeListeners(count: number): void;
};

export const tileEvents: NativeEventEmitter | null =
  TppTile === null ? null : new NativeEventEmitter(TppTile as unknown as EmitterModule);
