/**
 * Where a large download goes when it should not enter JavaScript: the
 * platform's staged files, opened natively (`DownloadSink`), once the native
 * crypto has passed its self-test against `@noble/ciphers` (`nativeSeal.ts`).
 * Where there is none, or the self-test fails, downloads are read and opened
 * in JavaScript as before.
 */

import { isStaged, type Client, type DownloadSink, type EntryHint, type Item, type StagedItem, type StoreItem } from '../core';
import { share } from '../platform';
import { verifyNativeSeal } from './nativeSeal';

/** downloadSink is the platform's sink when it may be used, or null. */
export async function downloadSink(): Promise<DownloadSink | null> {
  const port = share.nativeSeal;
  const sink = share.downloadSink;
  if (port === null || sink === null) {
    return null;
  }
  return (await verifyNativeSeal(port)) ? sink : null;
}

/** READ_BYTES is how much of a staged body crosses into JavaScript at once. */
const READ_BYTES = 4 << 20;

/**
 * readStaged brings a staged item's body into memory — for a preview or a
 * copy, which need it here — and lets the staged copy go.
 */
async function readStaged(item: StagedItem, sink: DownloadSink): Promise<Item> {
  try {
    const { token, offset, size } = item.staged;
    const body = new Uint8Array(size);
    for (let at = 0; at < size; at += READ_BYTES) {
      const part = await sink.read(token, offset + at, Math.min(READ_BYTES, size - at));
      body.set(part, at);
    }
    return { contentType: item.contentType, filename: item.filename, body, createdAt: item.createdAt, meta: item.meta };
  } finally {
    item.staged.release();
  }
}

/**
 * entryBody reads an entry into memory through the same download as a save
 * or an open — natively, and from the sealed cache when it was read within
 * the hour — so previewing, copying and saving a file are one download.
 * Without a sink it is the ordinary read.
 */
export async function entryBody(client: Client, entryId: string, hint?: EntryHint): Promise<Item> {
  const sink = await downloadSink();
  if (sink === null) {
    return client.getEntry(entryId, hint);
  }
  const got = await client.getEntryStaged(entryId, hint, sink);
  return isStaged(got) ? readStaged(got, sink) : got;
}

/** storeBody is entryBody for a store item. */
export async function storeBody(client: Client, it: StoreItem): Promise<Item> {
  const sink = await downloadSink();
  if (sink === null) {
    return client.fetchStoreItem(it);
  }
  return readStaged(await client.fetchStoreItemStaged(it, sink), sink);
}
