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
  isStaged,
  type StagedItem,
  type StoreItem,
  classifyCode,
  describe as describeError,
  type EntryHeaderView,
  type EntryMeta,
} from '../core';
import { choiceRule, type ChoiceRule } from './choices';
import { downloadSink } from './downloads';
import { keepAliveDuring } from './keepAlive';
import { formatSize, kindOf } from './preview';
import {
  clipboard,
  clipboardTimestamp,
  deviceInfo,
  entryCache,
  files,
  secureStore,
  socketFactory,
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
 * `file` needs the user too: the entry is one the clipboard cannot hold — or an
 * image, where the caller asked for images to be asked about — so the caller
 * asks what to do with it: copy it (images only), save it to Downloads, open it
 * or share it. When the entry's sealed header says so, that is asked *before*
 * anything is downloaded.
 */
export type Direction = 'uploaded' | 'downloaded' | 'nothing' | 'ambiguous' | 'file';

/** SyncResult is the outcome of one sync, in the words a screen or a toast shows. */
export interface SyncResult {
  direction: Direction;
  message: string;
  /** file is the entry waiting for the user's choice, when `direction` is `file`. */
  file?: PendingFile;
  /** saved is the file just saved to Downloads, so the notice can offer to open it. */
  saved?: SavedFile;
}

/**
 * FileChoice is what the user does with a received entry they were asked
 * about. `copy` is only offered when the entry's `canCopy` says so.
 */
export type FileChoice = 'copy' | 'save' | 'open' | 'share';

/** isFileChoice narrows an answer that came through an untyped path, such as the tile panel. */
export function isFileChoice(value: unknown): value is FileChoice {
  return value === 'copy' || value === 'save' || value === 'open' || value === 'share';
}

/**
 * PendingFile is an entry waiting for the user to pick what happens to it. It is
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
  /** canCopy is true when Copy is one of the answers: an image the clipboard can take. */
  canCopy: boolean;
  entry?: EntryMeta;
  item?: ClipboardItem;
  /** storeItem is a store item not downloaded yet: its opened metadata said what it is. */
  storeItem?: StoreItem;
}

/**
 * choiceFor applies the rule in `choices.ts` on this device: whether an entry
 * of this type asks what to do with it, and whether Copy is among the answers.
 * `askForImages` is true where the user is browsing — the history and the
 * store — and false on the one-tap paths, Sync down and the tile.
 */
export function choiceFor(contentType: string, filename: string, askForImages = false): ChoiceRule {
  return choiceRule(kindOf(contentType, filename), {
    clipboardCanHold: clipboard.canWrite({ contentType, filename, body: new Uint8Array(0) }),
    filesAvailable: files.available,
    askForImages,
  });
}

/**
 * needsFileChoice says, from an entry's sealed header and without fetching
 * it, whether the question comes before the download: always for something
 * the clipboard cannot hold, and for an image when `askForImages` is set.
 */
export function needsFileChoice(header: EntryHeaderView | null, askForImages = false): boolean {
  if (header === null) {
    return false;
  }
  return choiceFor(header.contentType, header.filename, askForImages).ask;
}

/** pendingFromEntry is the question for an entry that has not been downloaded. */
export function pendingFromEntry(meta: EntryMeta, askForImages = false): PendingFile {
  const h = meta.header;
  const type = h === null ? 'type unknown' : h.contentType.split(';')[0].trim();
  return {
    name: h !== null && h.filename !== '' ? h.filename : type,
    detail: `${type} · ${formatSize(h?.size ?? meta.size)} · ${meta.createdAt.toLocaleString()}`,
    canCopy: h !== null && choiceFor(h.contentType, h.filename, askForImages).canCopy,
    entry: meta,
  };
}

/** pendingFromStore is the question for a store item that has not been downloaded. */
export function pendingFromStore(it: StoreItem, canCopy: boolean): PendingFile {
  const type = it.contentType.split(';')[0].trim();
  return {
    name: it.filename !== '' ? it.filename : type,
    detail: `${type} · ${formatSize(it.bodySize)} · saved ${it.savedAt.toLocaleString()}`,
    canCopy,
    storeItem: it,
  };
}

/** pendingFromItem is the question for an entry already downloaded. */
function pendingFromItem(item: ClipboardItem, canCopy: boolean): PendingFile {
  const type = item.contentType.split(';')[0].trim();
  return {
    name: item.filename !== '' ? item.filename : type,
    detail: `${type} · ${formatSize(item.body.length)}`,
    canCopy,
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
    socketFactory,
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
 * it is, so the question of what to do with it is returned first and the
 * download happens once it is answered (`handleFile`). `latest` is the newest entry's
 * metadata when the caller already has it.
 *
 * An entry from an older epoch is not an error: it predates a rekey, this
 * device no longer holds the key it was written under, and it expires within
 * 24 hours (spec/crypto.md §7). The user is told there is nothing to copy.
 */
export async function download(client: Client, latest?: EntryMeta): Promise<SyncResult> {
  // A large latest entry carries on if the user switches apps meanwhile.
  return keepAliveDuring(() => downloadLatest(client, latest));
}

async function downloadLatest(client: Client, latest?: EntryMeta): Promise<SyncResult> {
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
 * cannot hold it, or it is an image and `askForImages` is set, hands it back as
 * `file` so the caller can ask what to do with it. Nothing is written to the
 * clipboard or to storage until the user has chosen.
 */
export async function deliver(item: ClipboardItem, askForImages = false): Promise<SyncResult> {
  const rule = choiceFor(item.contentType, item.filename, askForImages);
  if (rule.ask) {
    return {
      direction: 'file',
      message: rule.canCopy
        ? `${describeItem(item)} is waiting for a choice.`
        : `${describeItem(item)} cannot go on the clipboard.`,
      file: pendingFromItem(item, rule.canCopy),
    };
  }
  return copyToClipboard(item);
}

/** copyToClipboard writes an item to this device's clipboard and says so. */
async function copyToClipboard(item: ClipboardItem): Promise<SyncResult> {
  await clipboard.write(item);
  return { direction: 'downloaded', message: `Copied ${describeItem(item)} to this device.` };
}

/**
 * handleFile carries out the user's choice for an entry they were asked about:
 * downloading it first if the question came before the download, then copying,
 * saving, opening or sharing it.
 */
export async function handleFile(
  client: Client,
  file: PendingFile,
  choice: FileChoice,
): Promise<SyncResult> {
  // A large file's download, and the save after it, carry on if the user
  // switches apps meanwhile.
  return keepAliveDuring(() => carryOut(client, file, choice));
}

async function carryOut(client: Client, file: PendingFile, choice: FileChoice): Promise<SyncResult> {
  let item = file.item;
  if (item === undefined && file.storeItem !== undefined) {
    // A store item, downloaded now that the user has chosen: natively when
    // it is saved, opened or shared, into memory when it is copied.
    const sink = choice === 'copy' ? null : await downloadSink();
    if (sink !== null) {
      return deliverStaged(await client.fetchStoreItemStaged(file.storeItem, sink), choice);
    }
    item = toClipboardItem(await client.fetchStoreItem(file.storeItem));
  }
  if (item === undefined) {
    if (file.entry === undefined) {
      throw new Error('a pending file has neither an entry nor its bytes');
    }
    // Saved, opened or shared, the body need never enter JavaScript: it is
    // downloaded and opened natively where it can be. Copy needs it here.
    const sink = choice === 'copy' ? null : await downloadSink();
    if (sink !== null) {
      const got = await client.getEntryStaged(file.entry.id, file.entry, sink);
      if (isStaged(got)) {
        return deliverStaged(got, choice);
      }
      item = toClipboardItem(got);
    } else {
      item = toClipboardItem(await client.getEntry(file.entry.id, file.entry));
    }
  }
  switch (choice) {
    case 'copy':
      return copyToClipboard(item);
    case 'save': {
      const saved = await files.save(item);
      return {
        direction: 'downloaded',
        message: `Saved ${saved.name} to Downloads/TwoPlacePaste.`,
        saved,
      };
    }
    case 'open':
      await files.open(item);
      return { direction: 'downloaded', message: `Opened ${describeItem(item)}.` };
    case 'share':
      // The share sheet is up; whether the user then picks an app is theirs
      // to finish, and Android does not report it back.
      await files.share(item);
      return { direction: 'downloaded', message: `Opened the share sheet for ${describeItem(item)}.` };
  }
}

/**
 * deliverStaged saves, opens or shares an item whose body stayed on the
 * platform, then lets the staged copy go. (A saved one is in Downloads by
 * then, and an opened or shared one has been copied for the other app.)
 */
export async function deliverStaged(item: StagedItem, choice: FileChoice): Promise<SyncResult> {
  try {
    switch (choice) {
      case 'save': {
        const saved = await files.saveStaged(item);
        return { direction: 'downloaded', message: `Saved ${saved.name} to Downloads/TwoPlacePaste.`, saved };
      }
      case 'open':
        await files.openStaged(item);
        return { direction: 'downloaded', message: `Opened ${describeStaged(item)}.` };
      case 'share':
        await files.shareStaged(item);
        return { direction: 'downloaded', message: `Opened the share sheet for ${describeStaged(item)}.` };
      default:
        throw new Error('a staged item cannot be copied');
    }
  } finally {
    item.staged.release();
  }
}

/** describeStaged names a staged item the way `describeItem` names one in memory. */
export function describeStaged(item: StagedItem): string {
  const size = formatSize(item.staged.size);
  return item.filename !== '' ? `${item.filename} (${item.contentType}, ${size})` : `${item.contentType}, ${size}`;
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
