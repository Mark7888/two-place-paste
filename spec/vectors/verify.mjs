// Reference consumer for the TwoPlacePaste crypto vectors.
//
// An independent implementation of spec/crypto.md in TypeScript-flavoured JS,
// written against the libraries that document pins, that reproduces every
// vector in this directory. It is not part of any build and nothing imports it:
// it exists to prove the corpus is genuinely language-neutral, and to give the
// React Native client (ROADMAP P6a) a worked starting point.
//
//   npm i @noble/curves @noble/hashes @noble/ciphers
//   node spec/vectors/verify.mjs
//
// Exit status is 0 only if every check passes.

import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { x25519 } from '@noble/curves/ed25519.js';
import { hkdf } from '@noble/hashes/hkdf.js';
import { sha256 } from '@noble/hashes/sha2.js';
import { xchacha20poly1305 } from '@noble/ciphers/chacha.js';

const DIR = dirname(fileURLToPath(import.meta.url));
const load = (file) => JSON.parse(readFileSync(join(DIR, file), 'utf8'));

// --- encoding helpers (spec/crypto.md §2.1) --------------------------------

const hex = (b) => Buffer.from(b).toString('hex');
const unhex = (s) => Uint8Array.from(Buffer.from(s, 'hex'));
const ascii = (s) => new TextEncoder().encode(s);
const utf8 = ascii;

const cat = (...parts) => {
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let off = 0;
  for (const p of parts) { out.set(p, off); off += p.length; }
  return out;
};

const u16be = (n) => { const b = new Uint8Array(2); new DataView(b.buffer).setUint16(0, n); return b; };
const u32be = (n) => { const b = new Uint8Array(4); new DataView(b.buffer).setUint32(0, n); return b; };
const u64be = (n) => { const b = new Uint8Array(8); new DataView(b.buffer).setBigUint64(0, BigInt(n)); return b; };

// Epochs are read from `epoch_hex`, never from the JSON number: a u64 above
// 2^53 does not survive JSON.parse (spec/crypto.md §9.1).
const epochBytes = (v) => unhex(v.epoch_hex);

// --- the profile (spec/crypto.md §4, §5, §6) -------------------------------

const VERSION = 0x01;
const ZERO_NONCE = new Uint8Array(24);

const wrapInfo = (ephemeralPub, recipientPub) => cat(ascii('tpp/v1/wrap'), ephemeralPub, recipientPub);
const wrapAAD = (epoch) => cat(ascii('tpp/v1/wrap-aad'), Uint8Array.of(VERSION), epoch);
const wrapKey = (shared, ephemeralPub, recipientPub) =>
  hkdf(sha256, shared, undefined, wrapInfo(ephemeralPub, recipientPub), 32);

function wrap(groupKey, recipientPub, ephemeralPriv, epoch) {
  const ephemeralPub = x25519.getPublicKey(ephemeralPriv);
  const key = wrapKey(x25519.getSharedSecret(ephemeralPriv, recipientPub), ephemeralPub, recipientPub);
  const ct = xchacha20poly1305(key, ZERO_NONCE, wrapAAD(epoch)).encrypt(groupKey);
  return cat(Uint8Array.of(VERSION), ephemeralPub, ct);
}

function unwrap(wrapped, recipientPriv, epoch) {
  if (wrapped.length !== 81) throw new Error('wrapped key has exactly one valid length');
  if (wrapped[0] !== VERSION) throw new Error(`unsupported version ${wrapped[0]}`);
  const ephemeralPub = wrapped.slice(1, 33);
  const recipientPub = x25519.getPublicKey(recipientPriv);
  const key = wrapKey(x25519.getSharedSecret(recipientPriv, ephemeralPub), ephemeralPub, recipientPub);
  return xchacha20poly1305(key, ZERO_NONCE, wrapAAD(epoch)).decrypt(wrapped.slice(33));
}

const entryInfo = (epoch) => cat(ascii('tpp/v1/entry'), epoch);
const entryAAD = (epoch, entryID) => {
  const id = utf8(entryID);
  return cat(ascii('tpp/v1/entry-aad'), Uint8Array.of(VERSION), epoch, u32be(id.length), id);
};
const contentKey = (groupKey, nonce, epoch) => hkdf(sha256, groupKey, nonce, entryInfo(epoch), 32);

function sealEntry(groupKey, nonce, epoch, entryID, frame) {
  const ct = xchacha20poly1305(contentKey(groupKey, nonce, epoch), nonce, entryAAD(epoch, entryID)).encrypt(frame);
  return cat(Uint8Array.of(VERSION), nonce, ct);
}

function openEntry(groupKey, container, epoch, entryID) {
  if (container.length < 41) throw new Error('container is too short to hold a nonce and a tag');
  return openEntryWithKey(contentKey(groupKey, container.slice(1, 25), epoch), container, epoch, entryID);
}

// §7a.4: a store item is opened with its content key, not the group key.
function openEntryWithKey(key, container, epoch, entryID) {
  if (container.length < 41) throw new Error('container is too short to hold a nonce and a tag');
  if (container[0] !== VERSION) throw new Error(`unsupported version ${container[0]}`);
  const nonce = container.slice(1, 25);
  return xchacha20poly1305(key, nonce, entryAAD(epoch, entryID)).decrypt(container.slice(25));
}

function encodeFrame(f) {
  const contentType = utf8(f.content_type);
  const filename = utf8(f.filename);
  const body = unhex(f.body);
  if (contentType.length === 0) throw new Error('content_type is mandatory');
  if (f.pad_len !== 0) throw new Error('pad_len must be zero in v1');
  return cat(
    Uint8Array.of(0x01),
    u16be(contentType.length), contentType,
    u16be(filename.length), filename,
    u64be(f.created_at_unix_ms),
    u32be(f.pad_len),
    u32be(body.length), body,
  );
}

function decodeFrame(b) {
  let i = 0;
  const take = (n) => {
    if (n < 0 || b.length - i < n) throw new Error('length prefix exceeds the remaining input');
    return b.slice(i, i += n);
  };
  const view = (n) => new DataView(take(n).slice().buffer);
  if (take(1)[0] !== 0x01) throw new Error('unsupported frame version');
  const contentType = new TextDecoder().decode(take(view(2).getUint16(0)));
  if (contentType.length === 0) throw new Error('content_type is mandatory');
  const filename = new TextDecoder().decode(take(view(2).getUint16(0)));
  const createdAt = view(8).getBigUint64(0);
  if (view(4).getUint32(0) !== 0) throw new Error('pad_len must be zero in v1');
  const body = take(view(4).getUint32(0));
  if (i !== b.length) throw new Error('trailing bytes after frame');
  return { content_type: contentType, filename, created_at_unix_ms: createdAt, pad_len: 0, body: hex(body) };
}

// §5.5 entry header: fixed 256 bytes (v1) or 512 bytes (v2), sealed under its
// own key and nonce.
const HEADER_SIZE = 256;
const HEADER_V2_SIZE = 512;

const cutUTF8 = (b, n) => {
  if (b.length <= n) return b;
  while (n > 0 && (b[n] & 0xc0) === 0x80) n--;
  return b.slice(0, n);
};

function encodeHeader(h) {
  let contentType = utf8(h.content_type);
  if (contentType.length === 0) throw new Error('content_type is mandatory');
  if (contentType.length > 128) contentType = utf8('application/octet-stream');
  const filename = cutUTF8(utf8(h.filename), HEADER_SIZE - 13 - contentType.length);
  const body = cat(Uint8Array.of(0x01), u16be(contentType.length), contentType,
    u16be(filename.length), filename, u64be(h.body_len));
  return cat(body, new Uint8Array(HEADER_SIZE - body.length));
}

function encodeHeaderV2(h) {
  let contentType = utf8(h.content_type);
  if (contentType.length === 0) throw new Error('content_type is mandatory');
  if (contentType.length > 128) contentType = utf8('application/octet-stream');
  const sum = unhex(h.sha256);
  if (sum.length !== 32) throw new Error('a v2 header carries a 32-byte hash');
  const filename = cutUTF8(utf8(h.filename), 255);
  const body = cat(Uint8Array.of(0x02), u16be(contentType.length), contentType,
    u16be(filename.length), filename, u64be(h.body_len), sum);
  return cat(body, new Uint8Array(HEADER_V2_SIZE - body.length));
}

function decodeHeader(b) {
  if (b.length === 0) throw new Error('empty header');
  const version = b[0];
  if (version !== 0x01 && version !== 0x02) throw new Error('unsupported header version');
  if (b.length !== (version === 0x01 ? HEADER_SIZE : HEADER_V2_SIZE)) throw new Error('each header version has exactly one size');
  let i = 1;
  const take = (n) => {
    if (b.length - i < n) throw new Error('length prefix exceeds the header');
    return b.slice(i, i += n);
  };
  const view = (n) => new DataView(take(n).slice().buffer);
  const contentType = new TextDecoder().decode(take(view(2).getUint16(0)));
  if (contentType.length === 0) throw new Error('content_type is mandatory');
  const filenameBytes = take(view(2).getUint16(0));
  const filename = new TextDecoder().decode(filenameBytes);
  const bodyLen = view(8).getBigUint64(0);
  let sha = '';
  if (version === 0x02) {
    if (filenameBytes.length > 255) throw new Error('a v2 filename is at most 255 bytes');
    sha = hex(take(32));
  }
  if (b.slice(i).some((c) => c !== 0)) throw new Error('header padding is not zero');
  return { content_type: contentType, filename, body_len: bodyLen, sha256: sha };
}

const headerInfo = (epoch) => cat(ascii('tpp/v1/header'), epoch);
const headerAAD = (epoch, entryID) => {
  const id = utf8(entryID);
  return cat(ascii('tpp/v1/header-aad'), Uint8Array.of(VERSION), epoch, u32be(id.length), id);
};
const headerKey = (groupKey, nonce, epoch) => hkdf(sha256, groupKey, nonce, headerInfo(epoch), 32);

function sealHeader(groupKey, nonce, epoch, entryID, encoded) {
  const ct = xchacha20poly1305(headerKey(groupKey, nonce, epoch), nonce, headerAAD(epoch, entryID)).encrypt(encoded);
  return cat(Uint8Array.of(VERSION), nonce, ct);
}

function openHeader(groupKey, container, epoch, entryID) {
  if (container.length !== 297 && container.length !== 553) throw new Error('a sealed header is 297 or 553 bytes');
  if (container[0] !== VERSION) throw new Error(`unsupported version ${container[0]}`);
  const nonce = container.slice(1, 25);
  return xchacha20poly1305(headerKey(groupKey, nonce, epoch), nonce, headerAAD(epoch, entryID))
    .decrypt(container.slice(25));
}

// §7a store ---------------------------------------------------------------

const storeInfo = (epoch) => cat(ascii('tpp/v1/store'), epoch);
const storeKey = (groupKey, epoch) => hkdf(sha256, groupKey, undefined, storeInfo(epoch), 32);

const prevEpoch = (epoch) => u64be(new DataView(epoch.slice().buffer).getBigUint64(0) - 1n);
const linkInfo = (epoch) => cat(ascii('tpp/v1/store-link'), epoch);
const linkAAD = (epoch) => cat(ascii('tpp/v1/store-link-aad'), Uint8Array.of(VERSION), prevEpoch(epoch), epoch);
const linkKey = (sk, epoch) => hkdf(sha256, sk, undefined, linkInfo(epoch), 32);
const epochBelow2 = (epoch) => new DataView(epoch.slice().buffer).getBigUint64(0) < 2n;

function sealLink(newKey, oldKey, epoch) {
  if (epochBelow2(epoch)) throw new Error('epoch 1 has no link');
  return cat(Uint8Array.of(VERSION), xchacha20poly1305(linkKey(newKey, epoch), ZERO_NONCE, linkAAD(epoch)).encrypt(oldKey));
}

function openLink(sk, link, epoch) {
  if (link.length !== 49) throw new Error('a link is exactly 49 bytes');
  if (epochBelow2(epoch)) throw new Error('epoch 1 has no link');
  if (link[0] !== VERSION) throw new Error(`unsupported version ${link[0]}`);
  return xchacha20poly1305(linkKey(sk, epoch), ZERO_NONCE, linkAAD(epoch)).decrypt(link.slice(1));
}

const wrapItemInfo = (epoch) => cat(ascii('tpp/v1/store-wrap'), epoch);
const wrapItemAAD = (epoch, itemID) => {
  const id = utf8(itemID);
  return cat(ascii('tpp/v1/store-wrap-aad'), Uint8Array.of(VERSION), epoch, u32be(id.length), id);
};
const wrapItemKey = (sk, nonce, epoch) => hkdf(sha256, sk, nonce, wrapItemInfo(epoch), 32);

function wrapItemKeys(sk, nonce, epoch, itemID, ck, mk) {
  return cat(Uint8Array.of(VERSION), nonce,
    xchacha20poly1305(wrapItemKey(sk, nonce, epoch), nonce, wrapItemAAD(epoch, itemID)).encrypt(cat(ck, mk)));
}

function unwrapItemKeys(sk, wrapped, epoch, itemID) {
  if (wrapped.length !== 105) throw new Error('wrapped item keys are exactly 105 bytes');
  if (wrapped[0] !== VERSION) throw new Error(`unsupported version ${wrapped[0]}`);
  const nonce = wrapped.slice(1, 25);
  const keys = xchacha20poly1305(wrapItemKey(sk, nonce, epoch), nonce, wrapItemAAD(epoch, itemID)).decrypt(wrapped.slice(25));
  return { contentKey: keys.slice(0, 32), metaKey: keys.slice(32) };
}

const META_SIZE = 2048;

function checkMeta(m) {
  const tags = m.tags.map(utf8);
  if (utf8(m.name).length === 0 || utf8(m.name).length > 200) throw new Error('name is 1 to 200 bytes');
  if (utf8(m.description).length > 800) throw new Error('description is at most 800 bytes');
  if (tags.length > 16) throw new Error('at most 16 tags');
  const seen = new Set();
  for (const t of tags) {
    if (t.length === 0 || t.length > 32) throw new Error('a tag is 1 to 32 bytes');
    if (seen.has(hex(t))) throw new Error('duplicate tag');
    seen.add(hex(t));
  }
}

function encodeMeta(m) {
  checkMeta(m);
  let contentType = utf8(m.content_type);
  if (contentType.length === 0) throw new Error('content_type is mandatory');
  if (contentType.length > 128) contentType = utf8('application/octet-stream');
  const str = (b) => cat(u16be(b.length), b);
  const tags = m.tags.map(utf8);
  const body = cat(Uint8Array.of(0x01), u64be(m.body_len), unhex(m.sha256),
    str(contentType), str(cutUTF8(utf8(m.filename), 255)), str(utf8(m.name)), str(utf8(m.description)),
    Uint8Array.of(tags.length), ...tags.map((t) => cat(Uint8Array.of(t.length), t)));
  return cat(body, new Uint8Array(META_SIZE - body.length));
}

function decodeMeta(b) {
  if (b.length !== META_SIZE) throw new Error('a record is exactly 2048 bytes');
  if (b[0] !== 0x01) throw new Error('unsupported record version');
  let i = 1;
  const take = (n) => {
    if (b.length - i < n) throw new Error('length prefix exceeds the record');
    return b.slice(i, i += n);
  };
  const view = (n) => new DataView(take(n).slice().buffer);
  const dec = (x) => new TextDecoder().decode(x);
  const bodyLen = view(8).getBigUint64(0);
  const sum = hex(take(32));
  const ct = take(view(2).getUint16(0));
  const fn = take(view(2).getUint16(0));
  const name = dec(take(view(2).getUint16(0)));
  const description = dec(take(view(2).getUint16(0)));
  if (ct.length === 0 || ct.length > 128 || fn.length > 255) throw new Error('content type or filename out of bounds');
  const n = take(1)[0];
  const tags = [];
  for (let k = 0; k < n; k++) tags.push(dec(take(take(1)[0])));
  const m = { name, description, tags, content_type: dec(ct), filename: dec(fn), body_len: bodyLen, sha256: sum };
  checkMeta(m);
  if (b.slice(i).some((c) => c !== 0)) throw new Error('record padding is not zero');
  return m;
}

const metaAAD = (itemID, rev) => {
  const id = utf8(itemID);
  return cat(ascii('tpp/v1/store-meta-aad'), Uint8Array.of(VERSION), u32be(id.length), id, u64be(rev));
};
const metaSealKey = (mk, nonce) => hkdf(sha256, mk, nonce, ascii('tpp/v1/store-meta'), 32);

function sealMeta(mk, nonce, itemID, rev, encoded) {
  return cat(Uint8Array.of(VERSION), nonce, xchacha20poly1305(metaSealKey(mk, nonce), nonce, metaAAD(itemID, rev)).encrypt(encoded));
}

function openMeta(mk, sealed, itemID, rev) {
  if (sealed.length !== 2089) throw new Error('a sealed record is exactly 2089 bytes');
  if (sealed[0] !== VERSION) throw new Error(`unsupported version ${sealed[0]}`);
  const nonce = sealed.slice(1, 25);
  return xchacha20poly1305(metaSealKey(mk, nonce), nonce, metaAAD(itemID, rev)).decrypt(sealed.slice(25));
}

// --- the checks ------------------------------------------------------------

let passed = 0;
const failures = [];

const eq = (name, got, want) => {
  if (got === want) { passed++; return; }
  failures.push(`${name}\n  got  ${got}\n  want ${want}`);
};

const mustThrow = (name, fn) => {
  try { fn(); } catch { passed++; return; }
  failures.push(`${name}\n  expected an error, got success`);
};

for (const v of load('x25519.json').vectors) {
  eq(`x25519/${v.name}: public key`, hex(x25519.getPublicKey(unhex(v.private_key))), v.public_key);
  if (v.shared_secret) {
    eq(`x25519/${v.name}: shared secret`,
      hex(x25519.getSharedSecret(unhex(v.private_key), unhex(v.peer_public_key))), v.shared_secret);
  }
}

for (const v of load('wrap.json').vectors) {
  const epoch = epochBytes(v);
  const ephemeralPub = x25519.getPublicKey(unhex(v.ephemeral_private_key));
  eq(`wrap/${v.name}: ephemeral public key`, hex(ephemeralPub), v.ephemeral_public_key);
  const shared = x25519.getSharedSecret(unhex(v.ephemeral_private_key), unhex(v.recipient_public_key));
  eq(`wrap/${v.name}: shared secret`, hex(shared), v.shared_secret);
  eq(`wrap/${v.name}: hkdf info`, hex(wrapInfo(ephemeralPub, unhex(v.recipient_public_key))), v.hkdf_info);
  eq(`wrap/${v.name}: wrap key`, hex(wrapKey(shared, ephemeralPub, unhex(v.recipient_public_key))), v.wrap_key);
  eq(`wrap/${v.name}: aad`, hex(wrapAAD(epoch)), v.aad);
  eq(`wrap/${v.name}: wrapped key`,
    hex(wrap(unhex(v.group_key), unhex(v.recipient_public_key), unhex(v.ephemeral_private_key), epoch)),
    v.wrapped_key);
  eq(`wrap/${v.name}: unwrap`, hex(unwrap(unhex(v.wrapped_key), unhex(v.recipient_private_key), epoch)), v.group_key);
}

for (const v of load('derive.json').vectors) {
  const epoch = epochBytes(v);
  eq(`derive/${v.name}: hkdf info`, hex(entryInfo(epoch)), v.hkdf_info);
  eq(`derive/${v.name}: hkdf salt`, hex(unhex(v.nonce)), v.hkdf_salt);
  eq(`derive/${v.name}: content key`, hex(contentKey(unhex(v.group_key), unhex(v.nonce), epoch)), v.content_key);
}

for (const v of load('frame.json').vectors) {
  eq(`frame/${v.name}: encode`, hex(encodeFrame(v.frame)), v.encoded);
  const back = decodeFrame(unhex(v.encoded));
  eq(`frame/${v.name}: decode content_type`, back.content_type, v.frame.content_type);
  eq(`frame/${v.name}: decode filename`, back.filename, v.frame.filename);
  eq(`frame/${v.name}: decode created_at`, back.created_at_unix_ms, BigInt(v.frame.created_at_unix_ms));
  eq(`frame/${v.name}: decode body`, back.body, v.frame.body);
}

for (const v of load('entry.json').vectors) {
  const epoch = epochBytes(v);
  eq(`entry/${v.name}: frame`, hex(encodeFrame(v.frame)), v.encoded_frame);
  eq(`entry/${v.name}: aad`, hex(entryAAD(epoch, v.entry_id)), v.aad);
  eq(`entry/${v.name}: content key`, hex(contentKey(unhex(v.group_key), unhex(v.nonce), epoch)), v.content_key);
  eq(`entry/${v.name}: container`,
    hex(sealEntry(unhex(v.group_key), unhex(v.nonce), epoch, v.entry_id, unhex(v.encoded_frame))), v.container);
  eq(`entry/${v.name}: open`,
    hex(openEntry(unhex(v.group_key), unhex(v.container), epoch, v.entry_id)), v.encoded_frame);
}

for (const v of load('header.json').vectors) {
  const epoch = epochBytes(v);
  eq(`header/${v.name}: header key`, hex(headerKey(unhex(v.group_key), unhex(v.header_nonce), epoch)), v.header_key);
  eq(`header/${v.name}: aad`, hex(headerAAD(epoch, v.entry_id)), v.aad);
  eq(`header/${v.name}: encode`, hex(encodeHeader(v.input)), v.encoded_header);
  const back = decodeHeader(unhex(v.encoded_header));
  eq(`header/${v.name}: decode content_type`, back.content_type, v.decoded.content_type);
  eq(`header/${v.name}: decode filename`, back.filename, v.decoded.filename);
  eq(`header/${v.name}: decode body_len`, back.body_len, BigInt(v.decoded.body_len));
  eq(`header/${v.name}: container`,
    hex(sealHeader(unhex(v.group_key), unhex(v.header_nonce), epoch, v.entry_id, unhex(v.encoded_header))), v.container);
  eq(`header/${v.name}: open`,
    hex(openHeader(unhex(v.group_key), unhex(v.container), epoch, v.entry_id)), v.encoded_header);
}

for (const v of load('header-v2.json').vectors) {
  const epoch = epochBytes(v);
  eq(`header-v2/${v.name}: header key`, hex(headerKey(unhex(v.group_key), unhex(v.header_nonce), epoch)), v.header_key);
  eq(`header-v2/${v.name}: encode`, hex(encodeHeaderV2(v.input)), v.encoded_header);
  const back = decodeHeader(unhex(v.encoded_header));
  eq(`header-v2/${v.name}: decode content_type`, back.content_type, v.decoded.content_type);
  eq(`header-v2/${v.name}: decode filename`, back.filename, v.decoded.filename);
  eq(`header-v2/${v.name}: decode body_len`, back.body_len, BigInt(v.decoded.body_len));
  eq(`header-v2/${v.name}: decode sha256`, back.sha256, v.decoded.sha256);
  eq(`header-v2/${v.name}: container`,
    hex(sealHeader(unhex(v.group_key), unhex(v.header_nonce), epoch, v.entry_id, unhex(v.encoded_header))), v.container);
  eq(`header-v2/${v.name}: open`,
    hex(openHeader(unhex(v.group_key), unhex(v.container), epoch, v.entry_id)), v.encoded_header);
}

const chain = new Map();
for (const v of load('store-chain.json').vectors) {
  const epoch = epochBytes(v);
  eq(`store-chain/${v.name}: store key info`, hex(storeInfo(epoch)), v.store_key_info);
  const sk = storeKey(unhex(v.group_key), epoch);
  eq(`store-chain/${v.name}: store key`, hex(sk), v.store_key);
  chain.set(v.epoch_hex, v);
  if (v.link) {
    eq(`store-chain/${v.name}: link key info`, hex(linkInfo(epoch)), v.link_key_info);
    eq(`store-chain/${v.name}: link key`, hex(linkKey(sk, epoch)), v.link_key);
    eq(`store-chain/${v.name}: link aad`, hex(linkAAD(epoch)), v.link_aad);
    eq(`store-chain/${v.name}: link`, hex(sealLink(sk, unhex(v.previous_store_key), epoch)), v.link);
    eq(`store-chain/${v.name}: open link`, hex(openLink(sk, unhex(v.link), epoch)), v.previous_store_key);
  }
}
// Walk from epoch 3 back to epoch 1 with nothing but epoch 3's store key and the links.
{
  let sk = unhex(chain.get(hex(u64be(3))).store_key);
  for (const e of [3, 2]) sk = openLink(sk, unhex(chain.get(hex(u64be(e))).link), u64be(e));
  eq('store-chain: walk 3 → 1', hex(sk), chain.get(hex(u64be(1))).store_key);
}

for (const v of load('store-item.json').vectors) {
  const epoch = epochBytes(v);
  const n = `store-item/${v.name}`;
  const sk = storeKey(unhex(v.group_key), epoch);
  eq(`${n}: store key`, hex(sk), v.store_key);
  eq(`${n}: encoded frame`, hex(encodeFrame(v.frame)), v.encoded_frame);
  eq(`${n}: content key`, hex(contentKey(unhex(v.group_key), unhex(v.entry_nonce), epoch)), v.content_key);
  eq(`${n}: container`, hex(sealEntry(unhex(v.group_key), unhex(v.entry_nonce), epoch, v.item_id, unhex(v.encoded_frame))), v.container);
  eq(`${n}: wrap key info`, hex(wrapItemInfo(epoch)), v.wrap_key_info);
  eq(`${n}: wrap key`, hex(wrapItemKey(sk, unhex(v.wrap_nonce), epoch)), v.wrap_key);
  eq(`${n}: wrap aad`, hex(wrapItemAAD(epoch, v.item_id)), v.wrap_aad);
  eq(`${n}: wrapped item keys`,
    hex(wrapItemKeys(sk, unhex(v.wrap_nonce), epoch, v.item_id, unhex(v.content_key), unhex(v.meta_key))), v.wrapped_item_keys);
  const keys = unwrapItemKeys(sk, unhex(v.wrapped_item_keys), epoch, v.item_id);
  eq(`${n}: unwrap content key`, hex(keys.contentKey), v.content_key);
  eq(`${n}: unwrap meta key`, hex(keys.metaKey), v.meta_key);
  eq(`${n}: open container`, hex(openEntryWithKey(keys.contentKey, unhex(v.container), epoch, v.item_id)), v.encoded_frame);
  eq(`${n}: encode meta`, hex(encodeMeta(v.meta)), v.encoded_meta);
  const m = decodeMeta(unhex(v.encoded_meta));
  eq(`${n}: decode meta`, JSON.stringify({ ...m, body_len: Number(m.body_len) }), JSON.stringify(v.meta_decoded));
  eq(`${n}: meta seal key`, hex(metaSealKey(unhex(v.meta_key), unhex(v.meta_nonce))), v.meta_seal_key);
  eq(`${n}: meta aad`, hex(metaAAD(v.item_id, BigInt(v.meta_rev))), v.meta_aad);
  eq(`${n}: sealed meta`,
    hex(sealMeta(unhex(v.meta_key), unhex(v.meta_nonce), v.item_id, BigInt(v.meta_rev), unhex(v.encoded_meta))), v.sealed_meta);
  eq(`${n}: open meta`, hex(openMeta(keys.metaKey, unhex(v.sealed_meta), v.item_id, BigInt(v.meta_rev))), v.encoded_meta);
}

for (const v of load('failure.json').vectors) {
  const name = `failure/${v.name}`;
  switch (v.operation) {
    case 'unwrap':
      mustThrow(name, () => unwrap(unhex(v.wrapped_key), unhex(v.recipient_private_key), epochBytes(v)));
      break;
    case 'open_entry':
      mustThrow(name, () => openEntry(unhex(v.group_key), unhex(v.container), epochBytes(v), v.entry_id));
      break;
    case 'decode_frame':
      mustThrow(name, () => decodeFrame(unhex(v.encoded_frame)));
      break;
    case 'open_header':
      mustThrow(name, () => openHeader(unhex(v.group_key), unhex(v.container), epochBytes(v), v.entry_id));
      break;
    case 'decode_header':
      mustThrow(name, () => decodeHeader(unhex(v.encoded_header)));
      break;
    case 'open_entry_with_key':
      mustThrow(name, () => openEntryWithKey(unhex(v.content_key), unhex(v.container), epochBytes(v), v.entry_id));
      break;
    case 'open_store_link':
      mustThrow(name, () => openLink(unhex(v.store_key), unhex(v.link), epochBytes(v)));
      break;
    case 'unwrap_item_keys':
      mustThrow(name, () => unwrapItemKeys(unhex(v.store_key), unhex(v.wrapped_item_keys), epochBytes(v), v.item_id));
      break;
    case 'open_store_meta':
      mustThrow(name, () => openMeta(unhex(v.meta_key), unhex(v.sealed_meta), v.item_id, BigInt(v.meta_rev ?? 0)));
      break;
    case 'decode_store_meta':
      mustThrow(name, () => decodeMeta(unhex(v.encoded_meta)));
      break;
    default:
      failures.push(`${name}\n  unknown operation ${v.operation}`);
  }
}

for (const f of failures) console.error(`FAIL ${f}`);
console.log(`${passed} checks passed, ${failures.length} failed`);
process.exit(failures.length === 0 ? 0 : 1);
