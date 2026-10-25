/**
 * The store (spec/crypto.md §7a). A store item outlives the epoch it was saved
 * in, so nothing about it may depend on keeping an old group key:
 *
 * - Each epoch has a store key, derived from its group key.
 * - A rekey uploads a link: the previous store key sealed under the new one.
 *   From the current store key a client walks the links back to any epoch.
 * - The blob is an ordinary entry container (§5.2). Its content key and a
 *   random metadata key are wrapped under the store key of the epoch the item
 *   was saved at.
 * - The metadata record — name, description, tags, type, filename, size,
 *   hash — is a fixed-size record sealed under the metadata key, so its length
 *   tells the relay nothing.
 */

import { xchacha20poly1305 } from '@noble/ciphers/chacha.js';
import { hkdf } from '@noble/hashes/hkdf.js';
import { sha256 } from '@noble/hashes/sha2.js';

import { concat, fromUTF8, toHex, u16be, u32be, u64be, u8, utf8, view } from '../bytes';
import { truncateUTF8 } from './header';
import { NONCE_SIZE, TAG_SIZE, VERSION } from './keys';

/** STORE_KEY_SIZE is the length of a store key. */
export const STORE_KEY_SIZE = 32;
/** STORE_LINK_SIZE is the fixed length of a link (§7a.2). */
export const STORE_LINK_SIZE = 1 + STORE_KEY_SIZE + TAG_SIZE;
/** META_KEY_SIZE is the length of an item's metadata key. */
export const META_KEY_SIZE = 32;
/** WRAPPED_ITEM_KEYS_SIZE is the fixed length of an item's wrapped keys (§7a.3). */
export const WRAPPED_ITEM_KEYS_SIZE = 1 + NONCE_SIZE + 32 + META_KEY_SIZE + TAG_SIZE;
/** STORE_META_SIZE is the fixed length of an encoded metadata record (§7a.5). */
export const STORE_META_SIZE = 2048;
/** STORE_META_CONTAINER_SIZE is the fixed length of a sealed record. */
export const STORE_META_CONTAINER_SIZE = 1 + NONCE_SIZE + STORE_META_SIZE + TAG_SIZE;

/** Limits on what a user types (§7a.5): refused, never cut. */
export const STORE_MAX_NAME = 200;
export const STORE_MAX_DESCRIPTION = 800;
export const STORE_MAX_TAGS = 16;
export const STORE_MAX_TAG = 32;

const STORE_MAX_FILENAME = 255;
const MAX_CONTENT_TYPE = 128;
const FALLBACK_CONTENT_TYPE = 'application/octet-stream';
const STORE_META_VERSION = 0x01;
const ZERO_NONCE = new Uint8Array(NONCE_SIZE);

/** storeInfo is the HKDF info of a store key: "tpp/v1/store" || u64be(epoch). */
export function storeInfo(epoch: bigint): Uint8Array {
  return concat(utf8('tpp/v1/store'), u64be(epoch));
}

/** storeKey derives the store key of an epoch from that epoch's group key (§7a.1). */
export function storeKey(groupKey: Uint8Array, epoch: bigint): Uint8Array {
  if (groupKey.length !== 32) {
    throw new Error(`group key is ${groupKey.length} bytes, want 32`);
  }
  return hkdf(sha256, groupKey, undefined, storeInfo(epoch), STORE_KEY_SIZE);
}

/** storeLinkInfo is the HKDF info of the key sealing the link into `epoch`. */
export function storeLinkInfo(epoch: bigint): Uint8Array {
  return concat(utf8('tpp/v1/store-link'), u64be(epoch));
}

/** storeLinkAAD binds both epochs a link joins: epoch-1 and epoch. */
export function storeLinkAAD(epoch: bigint): Uint8Array {
  return concat(utf8('tpp/v1/store-link-aad'), u8(VERSION), u64be(epoch - 1n), u64be(epoch));
}

/** storeLinkKey derives the single-use key that seals the link into `epoch`. */
export function storeLinkKey(key: Uint8Array, epoch: bigint): Uint8Array {
  if (key.length !== STORE_KEY_SIZE) {
    throw new Error(`store key is ${key.length} bytes, want ${STORE_KEY_SIZE}`);
  }
  return hkdf(sha256, key, undefined, storeLinkInfo(epoch), 32);
}

/**
 * sealStoreLink seals the store key of epoch-1 under the store key of `epoch`:
 * version || ciphertext || tag. The nonce is zero because the link key seals
 * exactly one message.
 */
export function sealStoreLink(newStoreKey: Uint8Array, oldStoreKey: Uint8Array, epoch: bigint): Uint8Array {
  if (epoch < 2n) {
    throw new Error('epoch 1 has no link');
  }
  if (oldStoreKey.length !== STORE_KEY_SIZE) {
    throw new Error(`store key is ${oldStoreKey.length} bytes, want ${STORE_KEY_SIZE}`);
  }
  const ct = xchacha20poly1305(storeLinkKey(newStoreKey, epoch), ZERO_NONCE, storeLinkAAD(epoch)).encrypt(
    oldStoreKey,
  );
  return concat(u8(VERSION), ct);
}

/**
 * openStoreLink returns the store key of epoch-1 from the store key of
 * `epoch`. Links only lead backwards: nothing here yields a newer key.
 */
export function openStoreLink(key: Uint8Array, link: Uint8Array, epoch: bigint): Uint8Array {
  if (link.length !== STORE_LINK_SIZE) {
    throw new Error(`link is ${link.length} bytes, want ${STORE_LINK_SIZE}`);
  }
  if (epoch < 2n) {
    throw new Error('epoch 1 has no link');
  }
  if (link[0] !== VERSION) {
    throw new Error(`unsupported link version ${link[0]}`);
  }
  return xchacha20poly1305(storeLinkKey(key, epoch), ZERO_NONCE, storeLinkAAD(epoch)).decrypt(link.subarray(1));
}

/** storeWrapInfo is the HKDF info of an item wrap key. */
export function storeWrapInfo(epoch: bigint): Uint8Array {
  return concat(utf8('tpp/v1/store-wrap'), u64be(epoch));
}

/** storeWrapAAD binds an item's wrapped keys to the epoch and the item. */
export function storeWrapAAD(epoch: bigint, itemID: string): Uint8Array {
  const id = utf8(itemID);
  return concat(utf8('tpp/v1/store-wrap-aad'), u8(VERSION), u64be(epoch), u32be(id.length), id);
}

/** storeWrapKey derives the key that wraps one item's keys. */
export function storeWrapKey(key: Uint8Array, nonce: Uint8Array, epoch: bigint): Uint8Array {
  if (key.length !== STORE_KEY_SIZE) {
    throw new Error(`store key is ${key.length} bytes, want ${STORE_KEY_SIZE}`);
  }
  if (nonce.length !== NONCE_SIZE) {
    throw new Error(`wrap nonce is ${nonce.length} bytes, want ${NONCE_SIZE}`);
  }
  return hkdf(sha256, key, nonce, storeWrapInfo(epoch), 32);
}

/** ItemKeys are the two keys a store item's wrap protects. */
export interface ItemKeys {
  contentKey: Uint8Array;
  metaKey: Uint8Array;
}

/** wrapItemKeys wraps an item's keys under the store key of the epoch it is saved at (§7a.3). */
export function wrapItemKeys(
  key: Uint8Array,
  nonce: Uint8Array,
  epoch: bigint,
  itemID: string,
  keys: ItemKeys,
): Uint8Array {
  if (keys.contentKey.length !== 32 || keys.metaKey.length !== META_KEY_SIZE) {
    throw new Error('item keys are 32 bytes each');
  }
  const ct = xchacha20poly1305(storeWrapKey(key, nonce, epoch), nonce, storeWrapAAD(epoch, itemID)).encrypt(
    concat(keys.contentKey, keys.metaKey),
  );
  return concat(u8(VERSION), nonce, ct);
}

/**
 * unwrapItemKeys reverses wrapItemKeys. `epoch` is the epoch the item was
 * wrapped at, as the relay reports it; the caller walks the links to that
 * epoch's store key first.
 */
export function unwrapItemKeys(key: Uint8Array, wrapped: Uint8Array, epoch: bigint, itemID: string): ItemKeys {
  if (wrapped.length !== WRAPPED_ITEM_KEYS_SIZE) {
    throw new Error(`wrapped item keys are ${wrapped.length} bytes, want ${WRAPPED_ITEM_KEYS_SIZE}`);
  }
  if (wrapped[0] !== VERSION) {
    throw new Error(`unsupported wrap version ${wrapped[0]}`);
  }
  const nonce = wrapped.subarray(1, 1 + NONCE_SIZE);
  const keys = xchacha20poly1305(storeWrapKey(key, nonce, epoch), nonce, storeWrapAAD(epoch, itemID)).decrypt(
    wrapped.subarray(1 + NONCE_SIZE),
  );
  return { contentKey: keys.slice(0, 32), metaKey: keys.slice(32) };
}

/**
 * StoreMeta is an item's metadata record: what the Store tab lists, searches
 * and filters on, none of which the relay may see. `tags` is a set: no tag is
 * empty, and none appears twice.
 */
export interface StoreMeta {
  name: string;
  description: string;
  tags: string[];
  contentType: string;
  filename: string;
  bodyLen: bigint;
  sha256: Uint8Array;
}

function checkStoreMeta(name: Uint8Array, description: Uint8Array, tags: Uint8Array[]): void {
  if (name.length === 0) {
    throw new Error('an item has a name');
  }
  if (name.length > STORE_MAX_NAME) {
    throw new Error(`a name is at most ${STORE_MAX_NAME} bytes`);
  }
  if (description.length > STORE_MAX_DESCRIPTION) {
    throw new Error(`a description is at most ${STORE_MAX_DESCRIPTION} bytes`);
  }
  if (tags.length > STORE_MAX_TAGS) {
    throw new Error(`an item has at most ${STORE_MAX_TAGS} tags`);
  }
  const seen = new Set<string>();
  for (const t of tags) {
    if (t.length === 0 || t.length > STORE_MAX_TAG) {
      throw new Error(`a tag is 1 to ${STORE_MAX_TAG} bytes`);
    }
    const k = toHex(t);
    if (seen.has(k)) {
      throw new Error('duplicate tag');
    }
    seen.add(k);
  }
}

const str16 = (b: Uint8Array): Uint8Array => concat(u16be(b.length), b);

/**
 * encodeStoreMeta serializes a record into exactly STORE_META_SIZE bytes
 * (§7a.5). A content type over 128 bytes becomes application/octet-stream and
 * a filename is cut to 255 bytes, as in a v2 header; the fields a user types
 * are refused when over their limits.
 */
export function encodeStoreMeta(m: StoreMeta): Uint8Array {
  const name = utf8(m.name);
  const description = utf8(m.description);
  const tags = m.tags.map(utf8);
  checkStoreMeta(name, description, tags);
  let contentType = utf8(m.contentType);
  if (contentType.length === 0) {
    throw new Error('content_type is mandatory');
  }
  if (contentType.length > MAX_CONTENT_TYPE) {
    contentType = utf8(FALLBACK_CONTENT_TYPE);
  }
  if (m.sha256.length !== 32) {
    throw new Error('a record carries the 32-byte body hash');
  }
  const body = concat(
    u8(STORE_META_VERSION),
    u64be(m.bodyLen),
    m.sha256,
    str16(contentType),
    str16(truncateUTF8(utf8(m.filename), STORE_MAX_FILENAME)),
    str16(name),
    str16(description),
    u8(tags.length),
    ...tags.map((t) => concat(u8(t.length), t)),
  );
  return concat(body, new Uint8Array(STORE_META_SIZE - body.length));
}

/** decodeStoreMeta parses a record, rejecting anything but its canonical encoding. */
export function decodeStoreMeta(encoded: Uint8Array): StoreMeta {
  if (encoded.length !== STORE_META_SIZE) {
    throw new Error(`record is ${encoded.length} bytes, want ${STORE_META_SIZE}`);
  }
  if (encoded[0] !== STORE_META_VERSION) {
    throw new Error('unsupported record version');
  }
  let offset = 1;
  const take = (n: number): Uint8Array => {
    if (encoded.length - offset < n) {
      throw new Error('length prefix exceeds the record');
    }
    const slice = encoded.subarray(offset, offset + n);
    offset += n;
    return slice;
  };
  const bodyLen = view(take(8)).getBigUint64(0);
  const hash = take(32).slice();
  const contentType = take(view(take(2)).getUint16(0));
  const filename = take(view(take(2)).getUint16(0));
  const name = take(view(take(2)).getUint16(0));
  const description = take(view(take(2)).getUint16(0));
  if (contentType.length === 0 || contentType.length > MAX_CONTENT_TYPE || filename.length > STORE_MAX_FILENAME) {
    throw new Error('content type or filename out of bounds');
  }
  const count = take(1)[0];
  const tags: Uint8Array[] = [];
  for (let i = 0; i < count; i++) {
    tags.push(take(take(1)[0]));
  }
  checkStoreMeta(name, description, tags);
  for (let i = offset; i < encoded.length; i++) {
    if (encoded[i] !== 0) {
      throw new Error('record padding is not zero');
    }
  }
  return {
    name: fromUTF8(name),
    description: fromUTF8(description),
    tags: tags.map(fromUTF8),
    contentType: fromUTF8(contentType),
    filename: fromUTF8(filename),
    bodyLen,
    sha256: hash,
  };
}

/** storeMetaAAD binds a sealed record to its item and revision. */
export function storeMetaAAD(itemID: string, rev: bigint): Uint8Array {
  const id = utf8(itemID);
  return concat(utf8('tpp/v1/store-meta-aad'), u8(VERSION), u32be(id.length), id, u64be(rev));
}

/** storeMetaSealKey derives the key that seals one revision of a record. */
export function storeMetaSealKey(metaKey: Uint8Array, nonce: Uint8Array): Uint8Array {
  if (metaKey.length !== META_KEY_SIZE) {
    throw new Error(`meta key is ${metaKey.length} bytes, want ${META_KEY_SIZE}`);
  }
  if (nonce.length !== NONCE_SIZE) {
    throw new Error(`meta nonce is ${nonce.length} bytes, want ${NONCE_SIZE}`);
  }
  return hkdf(sha256, metaKey, nonce, utf8('tpp/v1/store-meta'), 32);
}

/** sealStoreMeta encrypts an encoded record as revision `rev`. */
export function sealStoreMeta(
  metaKey: Uint8Array,
  nonce: Uint8Array,
  itemID: string,
  rev: bigint,
  encoded: Uint8Array,
): Uint8Array {
  if (encoded.length !== STORE_META_SIZE) {
    throw new Error(`record is ${encoded.length} bytes, want ${STORE_META_SIZE}`);
  }
  const ct = xchacha20poly1305(storeMetaSealKey(metaKey, nonce), nonce, storeMetaAAD(itemID, rev)).encrypt(encoded);
  return concat(u8(VERSION), nonce, ct);
}

/**
 * openStoreMeta decrypts a sealed record. `rev` is the revision the relay
 * reports; a record sealed as another revision does not open, so an older edit
 * cannot be replayed as the current one.
 */
export function openStoreMeta(metaKey: Uint8Array, sealed: Uint8Array, itemID: string, rev: bigint): Uint8Array {
  if (sealed.length !== STORE_META_CONTAINER_SIZE) {
    throw new Error(`sealed record is ${sealed.length} bytes, want ${STORE_META_CONTAINER_SIZE}`);
  }
  if (sealed[0] !== VERSION) {
    throw new Error(`unsupported record version ${sealed[0]}`);
  }
  const nonce = sealed.subarray(1, 1 + NONCE_SIZE);
  return xchacha20poly1305(storeMetaSealKey(metaKey, nonce), nonce, storeMetaAAD(itemID, rev)).decrypt(
    sealed.subarray(1 + NONCE_SIZE),
  );
}
