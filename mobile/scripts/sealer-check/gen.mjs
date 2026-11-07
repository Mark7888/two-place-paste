/**
 * Writes the cases `SealerCheck.java` holds the native sealer to, one per
 * line as hex — key, nonce, aad, plaintext, ciphertext with tag — with "-"
 * for an empty field:
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
