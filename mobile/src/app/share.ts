/**
 * What happens to something another app shared to this one: a file, an image
 * or a piece of text, arriving through Android's share sheet.
 *
 * Nothing is uploaded on arrival. The share panel (`ShareOverlay.tsx`) asks
 * first, and the user picks one of two destinations this module carries out:
 *
 *  - send it to the group as its latest entry, the same as a sync up from
 *    this device would — the other devices get it on their next sync;
 *  - or save it to the group's store, through the same sheet the Store screen
 *    uses, so it gets a name, a description and tags.
 *
 * The bytes of a shared file are read only once the destination is chosen,
 * because the destination sets the limit: a file over it is refused before a
 * byte of it is read, when the sending app says how large it is, and at the
 * limit otherwise.
 *
 * This module imports nothing from React Native, so the rules are tested
 * directly; the platform reaches it through the `read` function it is given.
 */

import { isStoreDisabled, SINGLE_FRAME_MAX_BYTES, STORE_LIMITS, utf8, type Client } from '../core';
import type { ClipboardItem, PickedFile, Shared } from '../platform/types';
import { formatSize } from './preview';

/** TEXT_CONTENT_TYPE is what shared text is written as: the same as copied text. */
export const TEXT_CONTENT_TYPE = 'text/plain; charset=utf-8';

/** ShareClient is the part of the client a share uses. */
export type ShareClient = Pick<Client, 'connect' | 'limits' | 'putEntry' | 'storeLimits'>;

/** ReadShared reads the shared file's bytes, up to `maxBytes`: the platform's `share.read`. */
export type ReadShared = (id: number, maxBytes: number) => Promise<PickedFile>;

/** SENT and STORED are the outcomes, as the toast over the sharing app says them. */
export const SENT = 'Sent to the group.';
export const STORED = 'Saved to the store.';

/** UNSUPPORTED is what the panel says about a share this app will not read. */
export const UNSUPPORTED =
  'TwoPlacePaste cannot take this. Share a single file, image or piece of text from another app.';

/**
 * describeShared is how the panel names what was shared: a title and the line
 * under it. Text is shown as its own first line — it is the user's own text,
 * on their own screen — and a file by its name, type and size.
 */
export function describeShared(shared: Shared): { title: string; detail: string } {
  switch (shared.kind) {
    case 'file': {
      const type = shared.contentType.split(';')[0].trim();
      const size = shared.size >= 0 ? ` · ${formatSize(shared.size)}` : '';
      return { title: shared.name, detail: `${type}${size}` };
    }
    case 'text': {
      const bytes = utf8(shared.text).length;
      return { title: firstLine(shared.text, 80) || 'Text', detail: `Text · ${formatSize(bytes)}` };
    }
    default:
      return { title: 'Something this app cannot take', detail: '' };
  }
}

/**
 * suggestedName is what the store sheet's Name starts as: a file's name, or
 * for text the subject the sending app gave, else the text's first line. It is
 * cut to fit the store's name limit, so the sheet opens ready to save.
 */
export function suggestedName(shared: Shared): string {
  switch (shared.kind) {
    case 'file':
      return fitBytes(shared.name, STORE_LIMITS.name);
    case 'text':
      return fitBytes(shared.subject.trim() !== '' ? shared.subject.trim() : firstLine(shared.text, 60), STORE_LIMITS.name);
    default:
      return '';
  }
}

/**
 * itemFor turns a share into an entry's shape, refusing it when it is over
 * `maxBytes`. `limitWhat` says what the limit is, for the message.
 */
export async function itemFor(
  shared: Shared,
  maxBytes: number,
  limitWhat: string,
  read: ReadShared,
): Promise<ClipboardItem> {
  switch (shared.kind) {
    case 'text': {
      const body = utf8(shared.text);
      if (body.length > maxBytes) {
        throw new Error(tooLarge('This text', body.length, maxBytes, limitWhat));
      }
      return { contentType: TEXT_CONTENT_TYPE, filename: '', body };
    }
    case 'file': {
      // Refused before the read when the sending app said how large it is.
      if (shared.size > maxBytes) {
        throw new Error(tooLarge('This file', shared.size, maxBytes, limitWhat));
      }
      const picked = await read(shared.id, maxBytes);
      if (picked.tooLarge) {
        throw new Error(tooLarge('This file', picked.size, maxBytes, limitWhat));
      }
      if (picked.body.length === 0) {
        throw new Error('That file is empty.');
      }
      // The name and type are the ones the panel showed, which the native
      // side settled when the share was claimed.
      return { contentType: shared.contentType, filename: shared.name, body: picked.body };
    }
    default:
      throw new Error(UNSUPPORTED);
  }
}

/**
 * sendShared puts the share up as the group's latest entry — last write wins,
 * as for any sync up (SPEC §6) — and resolves with the outcome to show.
 */
export async function sendShared(client: ShareClient, shared: Shared, read: ReadShared): Promise<string> {
  await client.connect();
  const limits = await client.limits();
  // The relay's cap counts ciphertext, which is a little larger than the
  // file; a file within a few bytes of it is refused by putEntry instead,
  // before the upload starts.
  const max = limits.maxEntryBytes > 0 ? limits.maxEntryBytes : SINGLE_FRAME_MAX_BYTES;
  const item = await itemFor(shared, max, 'the relay takes at most', read);
  await client.putEntry(item);
  return SENT;
}

/**
 * storeItemFor reads the share for a save to the store, within the room the
 * store has left. The save itself goes through the store sheet, which asks
 * for the name, description and tags and refuses a duplicate.
 */
export async function storeItemFor(client: ShareClient, shared: Shared, read: ReadShared): Promise<ClipboardItem> {
  await client.connect();
  let room: number;
  try {
    const limits = await client.storeLimits();
    if (!limits.enabled) {
      throw new Error(STORE_OFF);
    }
    room = limits.availableBytes;
  } catch (err) {
    if (isStoreDisabled(err)) {
      throw new Error(STORE_OFF, { cause: err });
    }
    throw err;
  }
  return itemFor(shared, room, 'the store has room for', read);
}

/** STORE_OFF is why a save to the store cannot happen at all. */
export const STORE_OFF = 'The store is turned off for this group.';

function tooLarge(what: string, size: number, max: number, limitWhat: string): string {
  return `${what} is ${formatSize(size)}; ${limitWhat} ${formatSize(max)}.`;
}

/** firstLine is the text's first non-empty line, cut to `max` characters. */
function firstLine(text: string, max: number): string {
  const line = text.split(/\r?\n/).find((l) => l.trim() !== '')?.trim() ?? '';
  const chars = Array.from(line);
  return chars.length > max ? `${chars.slice(0, max - 1).join('')}…` : line;
}

/** fitBytes cuts a string, by whole characters, to at most `max` UTF-8 bytes. */
function fitBytes(s: string, max: number): string {
  const chars = Array.from(s);
  while (chars.length > 0 && utf8(chars.join('')).length > max) {
    chars.pop();
  }
  return chars.join('');
}
