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

  /** write puts an item on the clipboard, or reports why it could not. */
  write(item: ClipboardItem): Promise<void>;
}

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
