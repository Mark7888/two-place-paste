/**
 * The app's one client, and the sync direction of SPEC §6.
 *
 * This module is where the protocol client meets the platform: it is the only
 * place that both imports `src/core` and touches the clipboard. The client
 * itself has no clipboard and must never gain one — that is what structurally
 * keeps a rekey from touching what the user has copied (SPEC §3.3).
 */

import {
  Client,
  Handlers,
  Item,
  TppError,
  classifyCode,
  describe as describeError,
  type EntryHeaderView,
  type EntryMeta,
} from '../core';
import { formatSize } from './preview';
import {
  clipboard,
  clipboardTimestamp,
  deviceInfo,
  entryCache,
  files,
  secureStore,
  type ClipboardItem,
  type SavedFile,
} from '../platform';

/**
 * Direction is what one sync did, which is what the UI reports back.
 *
 * `ambiguous` is not a failure and not "nothing happened": it is the one
 * outcome that needs the user. It means the comparison SPEC §6 defines could
 * not be made, so the caller must ask which way to go rather than report a
 * dead end — which is what the screen and the quick-settings tile both now do.
 *
 * `file` needs the user too: the entry is one the clipboard cannot hold, so the
 * caller asks whether to save it to Downloads or open it. When the entry's
 * sealed header says so, that is asked *before* anything is downloaded.
 */
export type Direction = 'uploaded' | 'downloaded' | 'nothing' | 'ambiguous' | 'file';

/** SyncResult is the outcome of one sync, in the words a screen or a toast shows. */
export interface SyncResult {
  direction: Direction;
  message: string;
  /** file is the entry waiting for the user's save-or-open choice, when `direction` is `file`. */
  file?: PendingFile;
  /** saved is the file just saved to Downloads, so the notice can offer to open it. */
  saved?: SavedFile;
}

/** FileChoice is what the user does with a received file the clipboard cannot hold. */
export type FileChoice = 'save' | 'open';

/**
 * PendingFile is a file entry waiting for the user to pick save or open. It is
 * either not downloaded yet (`entry`: its sealed header said what it is) or
 * already downloaded (`item`: the header could not be read, so the download
 * came first). Either way it carries what the question shows, so the user
 * can tell which entry they are being asked about.
 */
export interface PendingFile {
  /** name is the filename, or the entry's type when it has none. */
  name: string;
  /** detail is "type · size · time", the line under the name. */
  detail: string;
  entry?: EntryMeta;
  item?: ClipboardItem;
}

/**
 * needsFileChoice says, from an entry's sealed header and without fetching
 * it, whether it is something the clipboard cannot hold — and so whether the
 * save-or-open question comes before the download.
 */
export function needsFileChoice(header: EntryHeaderView | null): boolean {
  if (header === null || !files.available) {
    return false;
  }
  return !clipboard.canWrite({
    contentType: header.contentType,
    filename: header.filename,
    body: new Uint8Array(0),
  });
}

/** pendingFromEntry is the question for an entry that has not been downloaded. */
export function pendingFromEntry(meta: EntryMeta): PendingFile {
  const h = meta.header;
  const type = h === null ? 'type unknown' : h.contentType.split(';')[0].trim();
  return {
    name: h !== null && h.filename !== '' ? h.filename : type,
    detail: `${type} · ${formatSize(h?.size ?? meta.size)} · ${meta.createdAt.toLocaleString()}`,
    entry: meta,
  };
}

/** pendingFromItem is the question for an entry already downloaded. */
function pendingFromItem(item: ClipboardItem): PendingFile {
  const type = item.contentType.split(';')[0].trim();
  return {
    name: item.filename !== '' ? item.filename : type,
    detail: `${type} · ${formatSize(item.body.length)}`,
    item,
  };
}

/** openSession builds the app's client over the platform's secure store. */
export async function openSession(handlers: Handlers): Promise<Client> {
  return Client.open({
    store: secureStore,
    deviceName: deviceInfo.defaultName(),
    handlers,
    cache: entryCache ?? undefined,
  });
}

/** toClipboardItem narrows a decrypted entry to what the clipboard can hold. */
export function toClipboardItem(item: Item): ClipboardItem {
  return { contentType: item.contentType, filename: item.filename, body: item.body };
}

/**
 * download copies the group's latest entry onto this device's clipboard.
 *
 * A file the clipboard cannot hold is not downloaded here: its header says what
 * it is, so the save-or-open question is returned first and the download
 * happens once it is answered (`handleFile`). `latest` is the newest entry's
 * metadata when the caller already has it.
 *
 * An entry from an older epoch is not an error: it predates a rekey, this
 * device no longer holds the key it was written under, and it expires within
 * 24 hours (spec/crypto.md §7). The user is told there is nothing to copy.
 */
export async function download(client: Client, latest?: EntryMeta): Promise<SyncResult> {
  const newest = latest ?? (await client.getHistory({ limit: 1 })).entries[0];
  if (newest !== undefined && newest.epoch === client.epoch && needsFileChoice(newest.header)) {
    const file = pendingFromEntry(newest);
    return { direction: 'file', message: `${file.name} is a file.`, file };
  }
  let item: Item;
  try {
    item = await client.getLatest();
  } catch (err) {
    if (err instanceof TppError && err.kind === 'no-entry') {
      return { direction: 'nothing', message: 'The group has no entry yet.' };
    }
    if (err instanceof TppError && err.kind === 'stale-entry') {
      return {
        direction: 'nothing',
        message: 'The latest entry predates the last re-key, so it cannot be read.',
      };
    }
    if (err instanceof TppError && err.kind === 'epoch-ahead') {
      return {
        direction: 'nothing',
        message: 'This device is behind a re-key. Try again in a moment.',
      };
    }
    throw err;
  }
  return deliver(toClipboardItem(item));
}

/**
 * deliver puts a received item on the clipboard — or, when the clipboard
 * cannot hold it, hands it back as `file` so the caller can ask whether to save
 * it or open it. Nothing is written to storage until the user has chosen.
 */
export async function deliver(item: ClipboardItem): Promise<SyncResult> {
  if (!clipboard.canWrite(item) && files.available) {
    return {
      direction: 'file',
      message: `${describeItem(item)} cannot go on the clipboard.`,
      file: pendingFromItem(item),
    };
  }
  await clipboard.write(item);
  return { direction: 'downloaded', message: `Copied ${describeItem(item)} to this device.` };
}

/**
 * handleFile carries out the user's choice for a file: downloading it first if
 * the question came before the download, then saving or opening it.
 */
export async function handleFile(
  client: Client,
  file: PendingFile,
  choice: FileChoice,
): Promise<SyncResult> {
  let item = file.item;
  if (item === undefined) {
    if (file.entry === undefined) {
      throw new Error('a pending file has neither an entry nor its bytes');
    }
    item = toClipboardItem(await client.getEntry(file.entry.id, file.entry));
  }
  if (choice === 'save') {
    const saved = await files.save(item);
    return {
      direction: 'downloaded',
      message: `Saved ${saved.name} to Downloads/TwoPlacePaste.`,
      saved,
    };
  }
  await files.open(item);
  return { direction: 'downloaded', message: `Opened ${describeItem(item)}.` };
}

/** upload writes this device's clipboard to the group as the latest entry. */
export async function upload(client: Client): Promise<SyncResult> {
  const item = await clipboard.read();
  if (item === null) {
    return { direction: 'nothing', message: 'This device’s clipboard is empty.' };
  }
  const meta = await client.putEntry({
    contentType: item.contentType,
    filename: item.filename,
    body: item.body,
  });
  return {
    direction: 'uploaded',
    message: `Sent ${formatSize(item.body.length)} to the group (${meta.epoch} epoch).`,
  };
}

/**
 * sync performs the one sync SPEC §6 defines, in the direction the timestamps
 * imply: if this device's clipboard is newer than the group's latest entry,
 * upload; otherwise download.
 *
 * Android is the one platform in this system where that comparison is
 * available: `ClipDescription.getTimestamp()` records when the clip was placed
 * on the clipboard (API 26+). Where it is not — an OS that returns nothing, a
 * clipboard set before the app was installed — this returns `ambiguous` rather
 * than guessing, because guessing silently destroys whichever side it
 * overwrites. The caller then puts the question to the user: on the Sync
 * screen as a dialog, and on a tile tap as the overlay's two buttons.
 *
 * It compares against the newest entry's *metadata*, not the entry: deciding a
 * direction should not cost a 50 MB download. That listing is one entry deep
 * and is not the History tab's fetch, which stays something the user asks for.
 */
export async function sync(client: Client): Promise<SyncResult> {
  const localMs = await clipboardTimestamp();
  const page = await client.getHistory({ limit: 1 });
  const latest = page.entries[0];

  if (latest === undefined) {
    return upload(client);
  }
  if (localMs === null) {
    return {
      direction: 'ambiguous',
      message:
        'This device cannot say when its clipboard was last set, so the direction is yours to choose.',
    };
  }
  return localMs > latest.createdAt.getTime() ? upload(client) : download(client, latest);
}

/** describeItem renders an entry for a status line. It never renders the entry's body. */
export function describeItem(item: ClipboardItem): string {
  const size = formatSize(item.body.length);
  if (item.filename !== '') {
    // A filename from another device is display text, never a path.
    return `${item.filename} (${item.contentType}, ${size})`;
  }
  return `${item.contentType}, ${size}`;
}

/** failureMessage renders an error for a status line or a toast, with no cause chain and no secrets. */
export const failureMessage = describeError;

/**
 * enterGroup acts on a code the user scanned or pasted, whichever of the two
 * this system has it turns out to be: a creation link from a relay's admin
 * page creates the group, and a pairing payload from a device already in one
 * joins it (SPEC §3.1, §3.2).
 *
 * One entry point rather than two buttons the user has to choose between: the
 * code says what it is, and being wrong about it is a decodable fact rather
 * than a guess.
 */
export async function enterGroup(client: Client, code: string): Promise<string> {
  switch (classifyCode(code)) {
    case 'creation-url':
      await client.createGroup(code.trim());
      return 'The group was created and this device holds its first key.';
    case 'pairing-code':
      await client.joinPairing(code);
      return 'This device is now in the group. It starts empty by design.';
    default:
      throw new TppError(
        'invalid',
        'That is neither a pairing code nor a creation link from a relay’s admin page.',
      );
  }
}
