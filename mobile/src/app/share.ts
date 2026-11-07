/**
 * What happens to something another app shared to this one: a file, an image
 * or a piece of text, arriving through Android's share sheet.
 *
 * Nothing is uploaded on arrival. The share panel (`ShareOverlay.tsx`) asks
 * first, and the user picks one of two destinations:
 *
 *  - send it to the group as its latest entry, the same as a sync up from
 *    this device would — the other devices get it on their next sync;
 *  - or save it to the group's store, through the same sheet the Store screen
 *    uses, so it gets a name, a description and tags.
 *
 * The panel only prepares the upload: it checks the destination's limit and
 * copies a shared file into the app's own storage ([payloadFor]), because the
 * permission to read the file ends with the panel. The upload itself runs in
 * the background after the panel has closed (`jobs.ts`, `backgroundJobs.ts`),
 * so a large file does not hold the user in the panel while it is encrypted
 * and sent.
 *
 * This module imports nothing from React Native, so the rules are tested
 * directly; the platform reaches it through the functions it is given.
 */

import { hashHex, isStoreDisabled, sha256Sliced, SINGLE_FRAME_MAX_BYTES, STORE_LIMITS, utf8, type Client } from '../core';
import type { ClipboardItem, Shared, StageResult, StagedFile } from '../platform/types';
import { formatSize } from './preview';

/** TEXT_CONTENT_TYPE is what shared text is written as: the same as copied text. */
export const TEXT_CONTENT_TYPE = 'text/plain; charset=utf-8';

/** ShareClient is the part of the client a share uses. */
export type ShareClient = Pick<Client, 'connect' | 'hold' | 'limits' | 'putEntry' | 'storeLimits'>;

/** StageShared copies a shared file within `maxBytes`: the platform's `share.stage`. */
export type StageShared = (id: number, maxBytes: number) => Promise<StageResult>;

/**
 * Payload is a share made ready to upload: text, carried whole, or a file
 * staged in the app's own storage, to be read when the upload runs.
 */
export type Payload =
  | { kind: 'text'; item: ClipboardItem }
  | { kind: 'file'; file: StagedFile; contentType: string; filename: string };

/** SENT and STORED are how an upload ended, as its notification says it. */
export const SENT = 'Sent to the group.';
export const STORED = 'Saved to the store.';

/** SENDING and STORING are the toast over the sharing app as the panel closes. */
export const SENDING = 'Sending to the group in the background.';
export const STORING = 'Saving to the store in the background.';

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
 * payloadFor makes a share ready to upload, refusing it when it is over
 * `maxBytes`. `limitWhat` says what the limit is, for the message. A file is
 * staged — copied, within the limit — and refused before the copy when the
 * sending app said how large it is.
 */
export async function payloadFor(
  shared: Shared,
  maxBytes: number,
  limitWhat: string,
  stage: StageShared,
  discard: (token: string) => void,
): Promise<Payload> {
  switch (shared.kind) {
    case 'text': {
      const body = utf8(shared.text);
      if (body.length > maxBytes) {
        throw new Error(tooLarge('This text', body.length, maxBytes, limitWhat));
      }
      return { kind: 'text', item: { contentType: TEXT_CONTENT_TYPE, filename: '', body } };
    }
    case 'file': {
      if (shared.size > maxBytes) {
        throw new Error(tooLarge('This file', shared.size, maxBytes, limitWhat));
      }
      const staged = await stage(shared.id, maxBytes);
      if (staged.tooLarge) {
        throw new Error(tooLarge('This file', staged.size, maxBytes, limitWhat));
      }
      if (staged.size === 0) {
        discard(staged.token);
        throw new Error('That file is empty.');
      }
      // The name and type are the ones the panel showed, which the native
      // side settled when the share was claimed.
      const file: StagedFile = {
        token: staged.token,
        name: staged.name,
        contentType: staged.contentType,
        size: staged.size,
        sha256: staged.sha256,
      };
      return { kind: 'file', file, contentType: shared.contentType, filename: shared.name };
    }
    default:
      throw new Error(UNSUPPORTED);
  }
}

/** prepareSend checks the share against the relay's entry limit and makes it ready to send. */
export async function prepareSend(
  client: ShareClient,
  shared: Shared,
  stage: StageShared,
  discard: (token: string) => void,
): Promise<Payload> {
  await client.connect();
  const limits = await client.limits();
  // The relay's cap counts ciphertext, which is a little larger than the
  // file; a file within a few bytes of it is refused by putEntry instead,
  // before the upload starts.
  const max = limits.maxEntryBytes > 0 ? limits.maxEntryBytes : SINGLE_FRAME_MAX_BYTES;
  return payloadFor(shared, max, 'the relay takes at most', stage, discard);
}

/**
 * prepareStore checks the share against the room the store has left and
 * makes it ready to save. The details are asked by the store sheet next.
 */
export async function prepareStore(
  client: ShareClient,
  shared: Shared,
  stage: StageShared,
  discard: (token: string) => void,
): Promise<Payload> {
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
  return payloadFor(shared, room, 'the store has room for', stage, discard);
}

/** payloadHash is the payload's SHA-256 in hex, for the store's duplicate check. */
export async function payloadHash(p: Payload): Promise<string> {
  return p.kind === 'file' ? p.file.sha256 : hashHex(await sha256Sliced(p.item.body));
}

/** describePayload names a payload the way the store sheet says what it is saving. */
export function describePayload(p: Payload): string {
  if (p.kind === 'text') {
    return `text, ${formatSize(p.item.body.length)}`;
  }
  return `${p.filename} (${p.contentType.split(';')[0].trim()}, ${formatSize(p.file.size)})`;
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
