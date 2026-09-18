/**
 * The app's one client, and the sync direction of SPEC §6.
 *
 * This module is where the protocol client meets the platform: it is the only
 * place that both imports `src/core` and touches the clipboard. The client
 * itself has no clipboard and must never gain one — that is what structurally
 * keeps a rekey from touching what the user has copied (SPEC §3.3).
 */

import { Client, Handlers, Item, TppError, classifyCode, describe as describeError } from '../core';
import {
  clipboard,
  clipboardTimestamp,
  deviceInfo,
  secureStore,
  type ClipboardItem,
} from '../platform';

/**
 * Direction is what one sync did, which is what the UI reports back.
 *
 * `ambiguous` is not a failure and not "nothing happened": it is the one
 * outcome that needs the user. It means the comparison SPEC §6 defines could
 * not be made, so the caller must ask which way to go rather than report a
 * dead end — which is what the screen and the quick-settings tile both now do.
 */
export type Direction = 'uploaded' | 'downloaded' | 'nothing' | 'ambiguous';

/** SyncResult is the outcome of one sync, in the words a screen or a toast shows. */
export interface SyncResult {
  direction: Direction;
  message: string;
}

/** openSession builds the app's client over the platform's secure store. */
export async function openSession(handlers: Handlers): Promise<Client> {
  return Client.open({
    store: secureStore,
    deviceName: deviceInfo.defaultName(),
    handlers,
  });
}

/** toClipboardItem narrows a decrypted entry to what the clipboard can hold. */
export function toClipboardItem(item: Item): ClipboardItem {
  return { contentType: item.contentType, filename: item.filename, body: item.body };
}

/**
 * download copies the group's latest entry onto this device's clipboard.
 *
 * An entry from an older epoch is not an error: it predates a rekey, this
 * device no longer holds the key it was written under, and it expires within
 * 24 hours (spec/crypto.md §7). The user is told there is nothing to copy.
 */
export async function download(client: Client): Promise<SyncResult> {
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
  await clipboard.write(toClipboardItem(item));
  return { direction: 'downloaded', message: `Copied ${describeItem(item)} to this device.` };
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
    message: `Sent ${item.body.length} bytes to the group (${meta.epoch} epoch).`,
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
 * direction should not cost a 10 MB download. That listing is one entry deep
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
  return localMs > latest.createdAt.getTime() ? upload(client) : download(client);
}

/** describeItem renders an entry for a status line. It never renders the entry's body. */
export function describeItem(item: Item): string {
  const size = `${item.body.length} bytes`;
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
