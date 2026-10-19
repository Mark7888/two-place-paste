/**
 * The two native modules this app ships, declared once.
 *
 * `TppTile` is the bridge to the quick-settings tile (SPEC §7.1); `TppClipboard`
 * fills the gap the clipboard library leaves on Android — its image methods are
 * iOS-only in both directions, so on this platform an image can be neither read
 * from the clipboard nor put back on it without native code of our own.
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

  /** closeOverlay finishes the overlay activity a tile tap opened, if one is on screen. */
  closeOverlay(): void;
}

/** TppClipboardModule is implemented by `android/app/src/main/java/.../ClipboardModule.kt`. */
export interface TppClipboardModule {
  /**
   * readImagePNG returns the clipboard's image as base64 PNG, or "" when the
   * clipboard holds no image. It rejects only when a clip that says it is an
   * image cannot be read as one.
   */
  readImagePNG(): Promise<string>;

  /** setImagePNG puts a PNG on the clipboard as a content URI the pasting app may read. */
  setImagePNG(base64: string): Promise<void>;

  /**
   * primaryClipTimestamp is when the clipboard was last set, in UTC
   * milliseconds, or 0 when the platform will not say (SPEC §6).
   */
  primaryClipTimestamp(): Promise<number>;
}

/** TppFilesModule is implemented by `android/app/src/main/java/.../FileModule.kt`. */
export interface TppFilesModule {
  /**
   * saveToDownloads writes the file to Downloads/TwoPlacePaste and resolves
   * with the name it was saved under and a content URI to open it by.
   */
  saveToDownloads(
    base64: string,
    filename: string,
    contentType: string,
  ): Promise<{ name: string; uri: string }>;

  /** openSaved opens a file saveToDownloads saved, by the URI it returned. */
  openSaved(uri: string, contentType: string, filename: string): Promise<void>;

  /**
   * open hands the file to an app that opens its type, staged in this app's
   * cache rather than saved anywhere. It rejects when no app opens the type.
   */
  open(base64: string, filename: string, contentType: string): Promise<void>;
}

/** TppCacheModule is implemented by `android/app/src/main/java/.../CacheModule.kt`. */
export interface TppCacheModule {
  /** read resolves with a cache file as base64, or null when there is none. */
  read(name: string): Promise<string | null>;
  write(name: string, base64: string): Promise<void>;
  remove(name: string): Promise<void>;
  list(): Promise<{ name: string; size: number }[]>;
}

/** TppNoticesModule is implemented by `android/app/src/main/java/.../NoticesModule.kt`. */
export interface TppNoticesModule {
  /** read resolves with the notices the build put in the APK, or null when there are none. */
  read(): Promise<string | null>;
}

const modules = NativeModules as {
  TppCache?: TppCacheModule;
  TppNotices?: TppNoticesModule;
  TppTile?: TppTileModule;
  TppClipboard?: TppClipboardModule;
  TppFiles?: TppFilesModule;
};

export const TppTile: TppTileModule | null = modules.TppTile ?? null;
export const TppClipboard: TppClipboardModule | null = modules.TppClipboard ?? null;
export const TppFiles: TppFilesModule | null = modules.TppFiles ?? null;
export const TppCache: TppCacheModule | null = modules.TppCache ?? null;
export const TppNotices: TppNoticesModule | null = modules.TppNotices ?? null;

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
