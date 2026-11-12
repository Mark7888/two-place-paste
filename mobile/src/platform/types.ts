import type { DownloadSink, StagedItem } from '../core';

/**
 * The platform surface, as interfaces.
 *
 * Every OS-specific capability the app uses is declared here and implemented
 * in a per-OS file next to it (`clipboard.android.ts`, `clipboard.ios.ts`, and
 * so on). Metro picks the file for the platform it is bundling, so adding iOS
 * is adding files rather than restructuring the app (SPEC §7.3), and it is why
 * `src/core` can stay free of React Native imports.
 */

/** ClipboardItem is what the OS clipboard currently holds, in the shape an entry is written from. */
export interface ClipboardItem {
  /** contentType is an IANA media type; an unknown one is "application/octet-stream". */
  contentType: string;
  /** filename is a bare filename for file entries, empty otherwise. */
  filename: string;
  body: Uint8Array;
}

/** ClipboardPort is the OS clipboard. */
export interface ClipboardPort {
  /**
   * read returns what the clipboard holds, or null when it holds nothing this
   * client can carry.
   *
   * On Android 10+ this only works while the app is focused: the quick-settings
   * tile foregrounds the app precisely to make this call legal (SPEC §7.1).
   */
  read(): Promise<ClipboardItem | null>;

  /**
   * canWrite reports whether `write` can put this item on the clipboard. An
   * item it cannot hold — a file, most often — goes to `files` instead.
   */
  canWrite(item: ClipboardItem): boolean;

  /** write puts an item on the clipboard, or reports why it could not. */
  write(item: ClipboardItem): Promise<void>;

  /**
   * writeText puts a plain string on the clipboard.
   *
   * It is separate from `write` because what it carries is not an entry: a
   * pairing code the user is handing to another device is this app's own UI
   * text, not something that came from or is going to the group. Keeping the
   * two apart is what stops a "copy this code" button from looking like a
   * clipboard entry to anything that inspects the write path.
   */
  writeText(text: string): Promise<void>;
}

/** SavedFile is a file `FilesPort.save` wrote, and what it takes to open it again. */
export interface SavedFile {
  /** name is what it was saved as, which can differ from the filename asked for. */
  name: string;
  /** uri addresses the saved file for `FilesPort.openSaved`. */
  uri: string;
  contentType: string;
  filename: string;
}

/**
 * FilesPort is what happens to a received item the user does not paste: they
 * keep it in Downloads, open it in another app, or share it with one.
 */
export interface FilesPort {
  /** available reports whether this platform can save, open or share a file at all. */
  readonly available: boolean;

  /** save keeps a copy in Downloads/TwoPlacePaste and says where it went. */
  save(item: ClipboardItem): Promise<SavedFile>;

  /** openSaved opens a file `save` wrote, in an app that opens its type. */
  openSaved(file: SavedFile): Promise<void>;

  /**
   * open shows the item in an app that opens its type, without saving it to
   * Downloads: it is staged in this app's cache, and the next open replaces it.
   */
  open(item: ClipboardItem): Promise<void>;

  /**
   * share hands the item to the system's share sheet, staged the same way
   * `open` stages it: nothing is saved to Downloads.
   */
  share(item: ClipboardItem): Promise<void>;

  /**
   * pick lets the user choose a file with the system's picker, for the store.
   * It resolves with the file, with `tooLarge` when it is over `maxBytes`
   * (nothing is read then), or with null when the user backed out.
   */
  pick(maxBytes: number): Promise<PickedFile | null>;

  /**
   * pickToStage lets the user choose a file the same way and stages it — a
   * copy in the app's own storage, hashed — instead of reading it, so the
   * store can save it sealed and sent natively, like a shared file. Null when
   * the user backed out; it rejects where the platform cannot stage.
   */
  pickToStage(maxBytes: number): Promise<StageResult | null>;

  /** saveStaged keeps a staged item's body in Downloads/TwoPlacePaste, as `save` does a body in memory. */
  saveStaged(item: StagedItem): Promise<SavedFile>;
  /** openStaged shows a staged item's body in an app that opens its type, as `open` does. */
  openStaged(item: StagedItem): Promise<void>;
  /** shareStaged hands a staged item's body to the share sheet, as `share` does. */
  shareStaged(item: StagedItem): Promise<void>;
}

/** PickedFile is a file the user chose to save to the store. */
export type PickedFile =
  | { tooLarge: false; name: string; contentType: string; body: Uint8Array }
  | { tooLarge: true; name: string; size: number };

/** SecureStorePort is where the device private key and the group key live at rest (spec/crypto.md §10). */
export interface SecureStorePort {
  load(name: string): Promise<string | null>;
  save(name: string, value: string): Promise<void>;
  clear(name: string): Promise<void>;
}

/** DeviceInfoPort names this device in the revocation dialog on every other device (SPEC §3.3 step 2). */
export interface DeviceInfoPort {
  /** defaultName is a name the user will recognise. Not a secret, and never clipboard content. */
  defaultName(): string;
}

/**
 * NoticesPort reads the third-party notices this build ships with
 * (LICENSING.md): the license text of every library the app is built from.
 */
export interface NoticesPort {
  /** read resolves with the notices text, or null when this build carries none. */
  read(): Promise<string | null>;
}

/** AppVersion is which build of the app is running. */
export interface AppVersion {
  /** version is the build's version, as scripts/version.sh computed it ("0.0.0-local" for a local build). */
  version: string;
  /**
   * stamp is the build's stamp: whole seconds since 2025-01-01T00:00:00Z when
   * CI built it, or 0 when it was built locally or the platform cannot say.
   */
  stamp: number;
}

/** AppInfoPort says which build of the app this is, for Settings. */
export interface AppInfoPort {
  /** version resolves with the running build's version and stamp. */
  version(): Promise<AppVersion>;
}

/** TilePort is the quick-settings tile (SPEC §7.1). It exists on Android and is a no-op elsewhere. */
export interface TilePort {
  /** available reports whether this platform has a quick-settings tile at all. */
  readonly available: boolean;

  /**
   * requests subscribes to tile taps that reached the app. The returned
   * function unsubscribes.
   */
  requests(handler: () => void): () => void;

  /** report tells the tile how the sync it asked for went, so the tile can render it and show a toast. */
  report(ok: boolean, message: string): void;

  /** pending returns a tile request that arrived while the app was starting, if there was one. */
  pending(): Promise<boolean>;

  /**
   * closeOverlay dismisses the panel a tile tap opened, once the sync it asked
   * for has finished or the user has declined it.
   *
   * The panel is a window over whatever the user was doing, so closing it is
   * the app's job the moment it has nothing left to say. It is a no-op where
   * there is no overlay — on another platform, or when the sync was started
   * from inside the app rather than from the tile.
   */
  closeOverlay(): void;
}

/**
 * Shared is what another app shared to this one through the system's share
 * sheet: one file, one piece of text, or something this app will not read.
 * `id` names the share for `SharePort.read` and `SharePort.finish`; a newer
 * share replaces an older one.
 */
export type Shared =
  | {
      id: number;
      kind: 'file';
      /** name is the file's display name, made safe; one is made up when the sender gave none. */
      name: string;
      contentType: string;
      /** size is in bytes, or -1 when the sending app does not say. */
      size: number;
      /** previewUri can be shown as an image, or is "" for anything but an image. */
      previewUri: string;
    }
  | { id: number; kind: 'text'; text: string; subject: string }
  | { id: number; kind: 'unsupported' };

/**
 * SharePort is the receiving end of the system's share sheet. It exists on
 * Android; elsewhere it reports that nothing can be shared to the app.
 */
export interface SharePort {
  /** available reports whether this platform can share to the app at all. */
  readonly available: boolean;

  /**
   * requests subscribes to shares that reached the running app; the handler
   * then claims them with `pending`. The returned function unsubscribes.
   */
  requests(handler: () => void): () => void;

  /** pending claims the share waiting, once, or resolves with null. */
  pending(): Promise<Shared | null>;

  /**
   * stage copies a shared file into the app's own storage, within
   * `maxBytes`, so it can be uploaded after the share panel has closed. It
   * rejects when share `id` is no longer the one waiting.
   */
  stage(id: number, maxBytes: number): Promise<StageResult>;

  /** readStaged reads a staged copy whole, reporting how far it has got. */
  readStaged(file: StagedFile, onProgress?: (done: number, total: number) => void): Promise<Uint8Array>;

  /** readSlice reads `length` bytes of a staged copy from `offset`. */
  readSlice(token: string, offset: number, length: number): Promise<Uint8Array>;

  /** discard deletes a staged copy. Deleting one already gone is fine. */
  discard(token: string): void;

  /**
   * nativeSeal is the platform's XChaCha20-Poly1305 for staged files, or null
   * where there is none. The app uses it only after `sealBytes` has matched
   * `@noble/ciphers` byte for byte (`src/app/nativeSeal.ts`).
   */
  readonly nativeSeal: NativeSealPort | null;

  /**
   * downloadSink is where a large download goes without entering JavaScript
   * — staged files, opened natively — or null where there is none. Like
   * `nativeSeal`, the app uses it only after the self-test.
   */
  readonly downloadSink: DownloadSink | null;

  /**
   * finish closes the share panel for share `id`, with `message` as a toast
   * when it is not empty. It is a status, never what was shared: a toast is
   * visible over other apps.
   */
  finish(id: number, message: string): void;

  /** openApp brings the app forward and closes the share panel. */
  openApp(): void;
}

/** StagedFile is a shared file copied into the app's own storage, waiting to be uploaded. */
export interface StagedFile {
  /** token names the copy for `readStaged` and `discard`. */
  token: string;
  name: string;
  contentType: string;
  size: number;
  /** sha256 is the file's SHA-256 in lowercase hex, for the store's duplicate check. */
  sha256: string;
}

/** StageResult is a staged copy, or the size of a file that was over the limit. */
export type StageResult = ({ tooLarge: false } & StagedFile) | { tooLarge: true; name: string; size: number };

/**
 * BackgroundPort runs uploads after the window that started them has closed,
 * with a notification showing their progress. On Android that is a
 * foreground service; elsewhere `available` is false and an upload runs only
 * while the app does.
 */
export interface BackgroundPort {
  readonly available: boolean;

  /**
   * askToNotify asks for permission to show notifications, where the OS asks
   * for that, and resolves whatever the answer: an upload runs either way.
   */
  askToNotify(): Promise<void>;

  /**
   * start starts the background runner, which calls the registered task. It
   * must be called while a window of the app is in the foreground.
   */
  start(): Promise<void>;

  /**
   * progress shows `title` with a bar at `done`/`total` (`total` 0 is a moving
   * bar), and `detail` — "2 of 3" — beside it when it is not empty. A status,
   * never content.
   */
  progress(title: string, detail: string, done: number, total: number): void;

  /** result says how upload `id` ended, in a notification of its own. A status, never content. */
  result(id: number, ok: boolean, message: string): void;
}

/** NativeSealPort seals a staged file without it entering JavaScript. */
export interface NativeSealPort {
  /**
   * sealStaged seals staged copy `token` into a new staged copy: `head`, then
   * the sealing under `key`, `nonce` and `aad` of `frameHead` followed by the
   * file. The file's copy is left for the caller to discard.
   */
  sealStaged(
    token: string,
    params: { key: Uint8Array; nonce: Uint8Array; aad: Uint8Array; head: Uint8Array; frameHead: Uint8Array },
  ): Promise<{ token: string; size: number; head: Uint8Array }>;

  /** sealBytes seals `plain` in memory the same way, for the self-test: ciphertext and tag. */
  sealBytes(key: Uint8Array, nonce: Uint8Array, aad: Uint8Array, plain: Uint8Array): Promise<Uint8Array>;

  /** openBytes opens ciphertext-with-tag in memory the way downloads are opened, for the self-test; it rejects what does not verify. */
  openBytes(key: Uint8Array, nonce: Uint8Array, aad: Uint8Array, sealed: Uint8Array): Promise<Uint8Array>;
}
