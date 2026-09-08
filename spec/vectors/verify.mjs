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
  if (container[0] !== VERSION) throw new Error(`unsupported version ${container[0]}`);
  const nonce = container.slice(1, 25);
  return xchacha20poly1305(contentKey(groupKey, nonce, epoch), nonce, entryAAD(epoch, entryID))
    .decrypt(container.slice(25));
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
    default:
      failures.push(`${name}\n  unknown operation ${v.operation}`);
  }
}

for (const f of failures) console.error(`FAIL ${f}`);
console.log(`${passed} checks passed, ${failures.length} failed`);
process.exit(failures.length === 0 ? 0 : 1);
