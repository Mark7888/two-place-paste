/**
 * Writes the cases `SealerCheck.java` holds the native crypto and range
 * capture to. AEAD cases go to the first file, one per line as hex — key,
 * nonce, aad, plaintext, ciphertext with tag — with "-" for an empty field:
 *
 *  - every vector in spec/vectors/entry.json, which the Go and TypeScript
 *    clients pass too;
 *  - random inputs at every size from 0 to 300 bytes and around the block,
 *    buffer and slice edges, sealed by `@noble/ciphers`, the AEAD the rest of
 *    the app uses.
 */

import { xchacha20poly1305 } from '@noble/ciphers/chacha.js';
import { Buffer } from 'node:buffer';
import { randomBytes } from 'node:crypto';
import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import process from 'node:process';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const hex = (b) => (b.length > 0 ? Buffer.from(b).toString('hex') : '-');
const lines = [];

const spec = JSON.parse(readFileSync(join(here, '../../../spec/vectors/entry.json'), 'utf8'));
for (const v of spec.vectors) {
  // A container is version (1 byte) and nonce (24) before the sealed frame.
  lines.push([v.content_key, v.nonce, v.aad, v.encoded_frame || '-', v.container.slice(2 + 48)].join(' '));
}

const sizes = [];
for (let n = 0; n <= 300; n++) {
  sizes.push(n);
}
sizes.push(1023, 1024, 1025, 4095, 4096, 65535, 65536, 65537, 262145, 1000003);
for (const n of sizes) {
  const key = randomBytes(32);
  const nonce = randomBytes(24);
  const aad = randomBytes(n % 50);
  const plain = randomBytes(n);
  const sealed = xchacha20poly1305(key, nonce, aad).encrypt(plain);
  lines.push([hex(key), hex(nonce), hex(aad), hex(plain), hex(sealed)].join(' '));
}

writeFileSync(process.argv[2], `${lines.join('\n')}\n`);

// Range capture cases go to the second file, one per line as hex: a frame,
// then the frame RangeCapture.split must give back (the payload's field 3
// taken out, every other byte kept) and that field's contents, or "null" for
// a frame it must leave alone. The frames are built by the small encoder
// below, apart from the Java code they check.
const varint = (n) => {
  const out = [];
  let v = BigInt(n);
  while (v >= 0x80n) {
    out.push(Number(v & 0x7fn) | 0x80);
    v >>= 7n;
  }
  out.push(Number(v));
  return Buffer.from(out);
};
const key = (field, wire) => varint(field * 8 + wire);
const bytesField = (field, b) => Buffer.concat([key(field, 2), varint(b.length), b]);
const varintField = (field, n) => Buffer.concat([key(field, 0), varint(n)]);
const envelope = (id, type, payload) =>
  Buffer.concat([bytesField(1, Buffer.from(id)), varintField(2, type), ...(payload.length > 0 ? [bytesField(3, payload)] : [])]);

const capture = [];
const meta = Buffer.concat([bytesField(1, Buffer.from('entry-id')), varintField(3, 123456789)]);
for (const n of [1, 127, 128, 300, 70000]) {
  const data = randomBytes(n);
  const id = `req-${n}`;
  const rest = Buffer.concat([bytesField(1, meta), varintField(2, 8 << 20)]);
  const frame = envelope(id, 69, Buffer.concat([rest, bytesField(3, data)]));
  capture.push([frame, envelope(id, 69, rest), data].map((b) => b.toString('hex')).join(' '));
  // The data field first, and fields of every wire type around it.
  const odd = [varintField(2, 5), Buffer.concat([key(7, 5), Buffer.alloc(4, 1)]), Buffer.concat([key(8, 1), Buffer.alloc(8, 2)])];
  const shuffled = envelope(id, 81, Buffer.concat([bytesField(3, data), ...odd]));
  capture.push([shuffled, envelope(id, 81, Buffer.concat(odd)), data].map((b) => b.toString('hex')).join(' '));
}
// Only data: what is left is an empty payload, which is left out.
const lone = randomBytes(40);
capture.push([envelope('x', 69, bytesField(3, lone)), envelope('x', 69, Buffer.alloc(0)), lone].map((b) => b.toString('hex')).join(' '));
// Left alone: the field twice, no field at all, no payload, a broken frame.
const twice = envelope('d', 69, Buffer.concat([bytesField(3, Buffer.from('a')), bytesField(3, Buffer.from('b'))]));
capture.push(`${twice.toString('hex')} null`);
capture.push(`${envelope('n', 69, varintField(2, 1)).toString('hex')} null`);
capture.push(`${envelope('e', 69, Buffer.alloc(0)).toString('hex')} null`);
capture.push(`${envelope('t', 69, bytesField(3, Buffer.from('abc'))).subarray(0, 9).toString('hex')} null`);
writeFileSync(process.argv[3], `${capture.join('\n')}\n`);
