/**
 * The native modules this app ships, declared once.
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

/** TppShareModule is implemented by `android/app/src/main/java/.../ShareModule.kt`. */
export interface TppShareModule {
  /** consumePending resolves once with the share waiting, as untyped native data, or null. */
  consumePending(): Promise<unknown>;
  /**
   * stage copies the shared file into the app's cache, within `maxBytes`, and
   * resolves with its token, name, type, size and SHA-256, or with `tooLarge`
   * and no copy.
   */
  stage(
    id: number,
    maxBytes: number,
  ): Promise<{ token?: string; name: string; size: number; contentType?: string; sha256?: string; tooLarge?: boolean }>;
  /** readChunk resolves with `length` bytes of staged copy `token` from `offset`, as base64. */
  readChunk(token: string, offset: number, length: number): Promise<string>;
  /** discard deletes staged copy `token`. */
  discard(token: string): void;
  /** sealStaged seals a staged copy into a new one, as base64 arguments; see `NativeSealPort`. */
  sealStaged(
    token: string,
    key: string,
    nonce: string,
    aad: string,
    head: string,
    frameHead: string,
  ): Promise<{ token: string; size: number; head: string }>;
  /**
   * sendSpliced sends one binary frame on React Native WebSocket `socketId`:
   * base64 `head`, then `length` bytes of staged copy `token` from `offset`.
   */
  sendSpliced(socketId: number, head: string, token: string, offset: number, length: number): Promise<void>;
  /** createStaged makes an empty staged file for a download and resolves with its token. */
  createStaged(): Promise<string>;
  /** writeStaged writes base64 `data` into staged file `token` at `offset`. */
  writeStaged(token: string, offset: number, data: string): Promise<void>;
  /**
   * openStaged opens staged container `token` into a new staged file of the
   * frame, refusing with "invalid tag" when it does not verify. All base64.
   */
  openStaged(token: string, key: string, nonce: string, aad: string): Promise<{ token: string; size: number; head: string }>;
  /** openBytes opens ciphertext-with-tag in memory, for the self-test; base64 in and out. */
  openBytes(key: string, nonce: string, aad: string, sealed: string): Promise<string>;
  /** digestStaged resolves with the SHA-256, in hex, of staged file `token` from `offset`. */
  digestStaged(token: string, offset: number): Promise<string>;
  /**
   * armCapture arms the capture of the reply to request `envelopeId` on React
   * Native WebSocket `socketId` into staged file `token` at `offset`. It
   * resolves with the bytes captured, or null when the reply was not captured.
   */
  armCapture(socketId: number, envelopeId: string, responseType: number, token: string, offset: number): Promise<number | null>;
  /** releaseCapture withdraws a capture whose reply will not come. */
  releaseCapture(socketId: number, envelopeId: string): void;
  /** sealBytes seals base64 `plain` in memory and resolves with ciphertext and tag as base64. */
  sealBytes(key: string, nonce: string, aad: string, plain: string): Promise<string>;
  /** finish closes the share panel and forgets the share, with an optional toast. */
  finish(id: number, message: string): void;
  /** openApp brings the app forward and closes the share panel. */
  openApp(): void;
}

/** TppTransferModule is implemented by `android/app/src/main/java/.../TransferModule.kt`. */
export interface TppTransferModule {
  /** start starts the foreground service that runs background uploads. */
  start(): Promise<void>;
  progress(title: string, detail: string, done: number, total: number): void;
  result(id: number, ok: boolean, message: string): void;
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

  /**
   * share hands the file to Android's share sheet, staged in this app's cache
   * the same way `open` stages it. It resolves once the sheet is up, not once
   * the user has picked an app.
   */
  share(base64: string, filename: string, contentType: string): Promise<void>;

  /**
   * pickFile shows the system's file picker. It resolves with the file, with
   * `tooLarge` and no bytes when it is over `maxBytes`, or with null when the
   * user backed out.
   */
  pickFile(
    maxBytes: number,
  ): Promise<{ name: string; size: number; contentType?: string; base64?: string; tooLarge?: boolean } | null>;

  /** saveStagedToDownloads saves `length` bytes of staged file `token` from `offset` to Downloads/TwoPlacePaste. */
  saveStagedToDownloads(
    token: string,
    offset: number,
    length: number,
    filename: string,
    contentType: string,
  ): Promise<{ name: string; uri: string }>;
  /** openStaged opens `length` bytes of staged file `token` from `offset` in an app that opens its type. */
  openStaged(token: string, offset: number, length: number, filename: string, contentType: string): Promise<void>;
  /** shareStaged hands `length` bytes of staged file `token` from `offset` to the share sheet. */
  shareStaged(token: string, offset: number, length: number, filename: string, contentType: string): Promise<void>;

  /**
   * pickToStage shows the system's file picker and stages the chosen file in
   * the app's cache, hashed, without reading it into JavaScript. It resolves
   * like `TppShareModule.stage`, or with null when the user backed out.
   */
  pickToStage(
    maxBytes: number,
  ): Promise<{ token?: string; name: string; size: number; contentType?: string; sha256?: string; tooLarge?: boolean } | null>;
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

/** TppAppInfoModule is implemented by `android/app/src/main/java/.../AppInfoModule.kt`. */
export interface TppAppInfoModule {
  /** read resolves with the APK's version name and version code (the build's stamp). */
  read(): Promise<{ version: string; stamp: number }>;
}

const modules = NativeModules as {
  TppAppInfo?: TppAppInfoModule;
  TppCache?: TppCacheModule;
  TppNotices?: TppNoticesModule;
  TppTile?: TppTileModule;
  TppClipboard?: TppClipboardModule;
  TppFiles?: TppFilesModule;
  TppShare?: TppShareModule;
  TppTransfer?: TppTransferModule;
};

export const TppTile: TppTileModule | null = modules.TppTile ?? null;
export const TppClipboard: TppClipboardModule | null = modules.TppClipboard ?? null;
export const TppFiles: TppFilesModule | null = modules.TppFiles ?? null;
export const TppShare: TppShareModule | null = modules.TppShare ?? null;
export const TppTransfer: TppTransferModule | null = modules.TppTransfer ?? null;
export const TppCache: TppCacheModule | null = modules.TppCache ?? null;
export const TppNotices: TppNoticesModule | null = modules.TppNotices ?? null;
export const TppAppInfo: TppAppInfoModule | null = modules.TppAppInfo ?? null;

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

/** SHARE_EVENT is emitted by the native share bridge when a share reaches a running app. */
export const SHARE_EVENT = 'TppShareReceived';

/** shareEvents is the emitter shares arrive on, or null where nothing can be shared to the app. */
export const shareEvents: NativeEventEmitter | null =
  TppShare === null ? null : new NativeEventEmitter(TppShare as unknown as EmitterModule);
