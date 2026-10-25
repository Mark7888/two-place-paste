/**
 * The store and header v2 suites of spec/vectors (spec/crypto.md §5.5.1,
 * §7a), run against this client. Like vectors.test.ts, the fixtures are read
 * from the repository so this client and the Go one share one contract.
 */

import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import { fromHex, toHex, view } from '../../bytes';
import {
  contentKey,
  decodeHeader,
  decodeStoreMeta,
  encodeFrame,
  encodeHeaderV2,
  encodeStoreMeta,
  headerAAD,
  headerKey,
  openEntryHeader,
  openEntrySlicedWithKey,
  openEntryWithKey,
  openStoreLink,
  openStoreMeta,
  sealEntry,
  sealHeader,
  sealStoreLink,
  sealStoreMeta,
  storeInfo,
  storeKey,
  storeLinkAAD,
  storeLinkInfo,
  storeLinkKey,
  storeMetaAAD,
  storeMetaSealKey,
  storeWrapAAD,
  storeWrapInfo,
  storeWrapKey,
  unwrapItemKeys,
  wrapItemKeys,
} from '..';

const VECTORS = join(__dirname, '..', '..', '..', '..', '..', 'spec', 'vectors');

function load<T>(file: string): { vectors: T[] } {
  return JSON.parse(readFileSync(join(VECTORS, file), 'utf8')) as { vectors: T[] };
}

const epochOf = (v: { epoch_hex: string }): bigint => view(fromHex(v.epoch_hex)).getBigUint64(0);

interface HeaderV2Fields {
  content_type: string;
  filename: string;
  body_len: number;
  sha256: string;
}

const headerV2Of = (h: HeaderV2Fields) => ({
  contentType: h.content_type,
  filename: h.filename,
  bodyLen: BigInt(h.body_len),
  sha256: fromHex(h.sha256),
});

describe('entry headers, version 2 (spec/crypto.md §5.5.1)', () => {
  const suite = load<{
    name: string;
    epoch_hex: string;
    entry_id: string;
    group_key: string;
    header_nonce: string;
    header_key: string;
    input: HeaderV2Fields;
    encoded_header: string;
    decoded: HeaderV2Fields;
    aad: string;
    container: string;
  }>('header-v2.json');

  test.each(suite.vectors.map((v) => [v.name, v] as const))('%s', (_name, v) => {
    const epoch = epochOf(v);
    const groupKey = fromHex(v.group_key);
    expect(toHex(headerKey(groupKey, fromHex(v.header_nonce), epoch))).toBe(v.header_key);
    expect(toHex(headerAAD(epoch, v.entry_id))).toBe(v.aad);
    expect(toHex(encodeHeaderV2(headerV2Of(v.input)))).toBe(v.encoded_header);
    expect(decodeHeader(fromHex(v.encoded_header))).toEqual(headerV2Of(v.decoded));
    expect(
      toHex(sealHeader(groupKey, fromHex(v.header_nonce), epoch, v.entry_id, fromHex(v.encoded_header))),
    ).toBe(v.container);
    expect(openEntryHeader(groupKey, fromHex(v.container), epoch, v.entry_id)).toEqual(headerV2Of(v.decoded));
  });
});

interface ChainVector {
  name: string;
  epoch_hex: string;
  group_key: string;
  store_key_info: string;
  store_key: string;
  previous_store_key?: string;
  link_key_info?: string;
  link_key?: string;
  link_aad?: string;
  link?: string;
}

describe('store keys and links (spec/crypto.md §7a.1, §7a.2)', () => {
  const suite = load<ChainVector>('store-chain.json');

  test.each(suite.vectors.map((v) => [v.name, v] as const))('%s', (_name, v) => {
    const epoch = epochOf(v);
    expect(toHex(storeInfo(epoch))).toBe(v.store_key_info);
    const sk = storeKey(fromHex(v.group_key), epoch);
    expect(toHex(sk)).toBe(v.store_key);
    if (v.link === undefined) {
      return;
    }
    expect(toHex(storeLinkInfo(epoch))).toBe(v.link_key_info);
    expect(toHex(storeLinkKey(sk, epoch))).toBe(v.link_key);
    expect(toHex(storeLinkAAD(epoch))).toBe(v.link_aad);
    expect(toHex(sealStoreLink(sk, fromHex(v.previous_store_key!), epoch))).toBe(v.link);
    expect(toHex(openStoreLink(sk, fromHex(v.link), epoch))).toBe(v.previous_store_key);
  });

  test('a client at epoch 3 walks the links back to epoch 1', () => {
    const at = (e: bigint) => suite.vectors.find((v) => epochOf(v) === e)!;
    let sk = fromHex(at(3n).store_key);
    for (const e of [3n, 2n]) {
      sk = openStoreLink(sk, fromHex(at(e).link!), e);
    }
    expect(toHex(sk)).toBe(at(1n).store_key);
  });
});

interface MetaFields {
  name: string;
  description: string;
  tags: string[];
  content_type: string;
  filename: string;
  body_len: number;
  sha256: string;
}

const metaOf = (m: MetaFields) => ({
  name: m.name,
  description: m.description,
  tags: m.tags,
  contentType: m.content_type,
  filename: m.filename,
  bodyLen: BigInt(m.body_len),
  sha256: fromHex(m.sha256),
});

describe('store items (spec/crypto.md §7a.3 – §7a.5)', () => {
  const suite = load<{
    name: string;
    epoch_hex: string;
    group_key: string;
    store_key: string;
    item_id: string;
    frame: { content_type: string; filename: string; created_at_unix_ms: number; body: string };
    encoded_frame: string;
    entry_nonce: string;
    content_key: string;
    container: string;
    meta_key: string;
    wrap_nonce: string;
    wrap_key_info: string;
    wrap_key: string;
    wrap_aad: string;
    wrapped_item_keys: string;
    meta: MetaFields;
    meta_decoded: MetaFields;
    encoded_meta: string;
    meta_rev: number;
    meta_nonce: string;
    meta_seal_key: string;
    meta_aad: string;
    sealed_meta: string;
  }>('store-item.json');

  test.each(suite.vectors.map((v) => [v.name, v] as const))('%s', async (_name, v) => {
    const epoch = epochOf(v);
    const rev = BigInt(v.meta_rev);
    const groupKey = fromHex(v.group_key);

    // Writer: everything from the group key of the epoch it saves at.
    const sk = storeKey(groupKey, epoch);
    expect(toHex(sk)).toBe(v.store_key);
    const frame = encodeFrame({
      contentType: v.frame.content_type,
      filename: v.frame.filename,
      createdAt: BigInt(v.frame.created_at_unix_ms),
      body: fromHex(v.frame.body),
    });
    expect(toHex(frame)).toBe(v.encoded_frame);
    expect(toHex(contentKey(groupKey, fromHex(v.entry_nonce), epoch))).toBe(v.content_key);
    expect(toHex(sealEntry(groupKey, fromHex(v.entry_nonce), epoch, v.item_id, frame))).toBe(v.container);
    expect(toHex(storeWrapInfo(epoch))).toBe(v.wrap_key_info);
    expect(toHex(storeWrapKey(sk, fromHex(v.wrap_nonce), epoch))).toBe(v.wrap_key);
    expect(toHex(storeWrapAAD(epoch, v.item_id))).toBe(v.wrap_aad);
    expect(
      toHex(
        wrapItemKeys(sk, fromHex(v.wrap_nonce), epoch, v.item_id, {
          contentKey: fromHex(v.content_key),
          metaKey: fromHex(v.meta_key),
        }),
      ),
    ).toBe(v.wrapped_item_keys);
    expect(toHex(encodeStoreMeta(metaOf(v.meta)))).toBe(v.encoded_meta);
    expect(toHex(storeMetaSealKey(fromHex(v.meta_key), fromHex(v.meta_nonce)))).toBe(v.meta_seal_key);
    expect(toHex(storeMetaAAD(v.item_id, rev))).toBe(v.meta_aad);
    expect(
      toHex(sealStoreMeta(fromHex(v.meta_key), fromHex(v.meta_nonce), v.item_id, rev, fromHex(v.encoded_meta))),
    ).toBe(v.sealed_meta);

    // Reader: only the store key.
    const keys = unwrapItemKeys(fromHex(v.store_key), fromHex(v.wrapped_item_keys), epoch, v.item_id);
    expect(toHex(keys.contentKey)).toBe(v.content_key);
    expect(toHex(keys.metaKey)).toBe(v.meta_key);
    expect(toHex(openEntryWithKey(keys.contentKey, fromHex(v.container), epoch, v.item_id))).toBe(v.encoded_frame);
    expect(
      toHex(await openEntrySlicedWithKey(keys.contentKey, fromHex(v.container), epoch, v.item_id, async () => {})),
    ).toBe(v.encoded_frame);
    const record = openStoreMeta(keys.metaKey, fromHex(v.sealed_meta), v.item_id, rev);
    expect(decodeStoreMeta(record)).toEqual(metaOf(v.meta_decoded));
  });
});
