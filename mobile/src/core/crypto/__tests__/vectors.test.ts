/**
 * The cross-language crypto vectors (spec/vectors), run against this client's
 * implementation of spec/crypto.md.
 *
 * The fixtures are read from the repository rather than copied into this
 * project on purpose: they are the contract between this client and the Go one
 * (`pkg/tppclient/tppcrypto`), and a copy is a contract that can drift. A
 * client that cannot reproduce every vector does not ship
 * (docs/conventions.md §11).
 *
 * Epochs come from `epoch_hex`, never from the JSON number: a u64 above 2^53
 * does not survive JSON.parse, and one vector uses 2^64-1.
 */

import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import { fromHex, toHex, view } from '../../bytes';
import {
  contentKey,
  decodeFrame,
  encodeFrame,
  entryAAD,
  entryInfo,
  openEntry,
  publicKey,
  sealEntry,
  sharedSecret,
  unwrap,
  wrap,
  wrapAAD,
  wrapInfo,
  wrapKey,
} from '..';

// src/core/crypto/__tests__ -> the repository root, then the shared corpus.
const VECTORS = join(__dirname, '..', '..', '..', '..', '..', 'spec', 'vectors');

interface Suite<T> {
  suite: string;
  profile: string;
  vectors: T[];
}

function load<T>(file: string): Suite<T> {
  return JSON.parse(readFileSync(join(VECTORS, file), 'utf8')) as Suite<T>;
}

const epochOf = (v: { epoch_hex: string }): bigint => view(fromHex(v.epoch_hex)).getBigUint64(0);

interface FrameVector {
  content_type: string;
  filename: string;
  created_at_unix_ms: number;
  pad_len: number;
  body: string;
}

const frameOf = (f: FrameVector) => ({
  contentType: f.content_type,
  filename: f.filename,
  createdAt: BigInt(f.created_at_unix_ms),
  body: fromHex(f.body),
});

describe('the corpus is the profile this client implements', () => {
  test('index.json counts every suite this file runs', () => {
    const index = JSON.parse(readFileSync(join(VECTORS, 'index.json'), 'utf8')) as {
      profile: string;
      total_vectors: number;
      suites: { file: string; vectors: number }[];
    };
    expect(index.profile).toBe('tpp-crypto-v1');

    let counted = 0;
    for (const suite of index.suites) {
      const loaded = load<unknown>(suite.file);
      expect(loaded.vectors).toHaveLength(suite.vectors);
      counted += loaded.vectors.length;
    }
    expect(counted).toBe(index.total_vectors);
  });
});

describe('x25519 (spec/crypto.md §3)', () => {
  const suite = load<{
    name: string;
    private_key: string;
    public_key: string;
    peer_public_key?: string;
    shared_secret?: string;
  }>('x25519.json');

  test.each(suite.vectors.map((v) => [v.name, v] as const))('%s', (_name, v) => {
    expect(toHex(publicKey(fromHex(v.private_key)))).toBe(v.public_key);
    if (v.shared_secret && v.peer_public_key) {
      expect(toHex(sharedSecret(fromHex(v.private_key), fromHex(v.peer_public_key)))).toBe(
        v.shared_secret,
      );
    }
  });
});

describe('wrap (spec/crypto.md §4)', () => {
  const suite = load<{
    name: string;
    epoch_hex: string;
    group_key: string;
    recipient_private_key: string;
    recipient_public_key: string;
    ephemeral_private_key: string;
    ephemeral_public_key: string;
    shared_secret: string;
    hkdf_info: string;
    wrap_key: string;
    aad: string;
    wrapped_key: string;
  }>('wrap.json');

  test.each(suite.vectors.map((v) => [v.name, v] as const))('%s', (_name, v) => {
    const epoch = epochOf(v);
    const ephemeralPublic = publicKey(fromHex(v.ephemeral_private_key));
    const recipientPublic = fromHex(v.recipient_public_key);
    const shared = sharedSecret(fromHex(v.ephemeral_private_key), recipientPublic);

    // Every intermediate is asserted, not just the container: when a client's
    // ciphertext is wrong, these say which step is wrong.
    expect(toHex(ephemeralPublic)).toBe(v.ephemeral_public_key);
    expect(toHex(shared)).toBe(v.shared_secret);
    expect(toHex(wrapInfo(ephemeralPublic, recipientPublic))).toBe(v.hkdf_info);
    expect(toHex(wrapKey(shared, ephemeralPublic, recipientPublic))).toBe(v.wrap_key);
    expect(toHex(wrapAAD(epoch))).toBe(v.aad);
    expect(
      toHex(
        wrap(fromHex(v.group_key), recipientPublic, epoch, fromHex(v.ephemeral_private_key)),
      ),
    ).toBe(v.wrapped_key);
    expect(toHex(unwrap(fromHex(v.wrapped_key), fromHex(v.recipient_private_key), epoch))).toBe(
      v.group_key,
    );
  });
});

describe('content key derivation (spec/crypto.md §5.1)', () => {
  const suite = load<{
    name: string;
    epoch_hex: string;
    group_key: string;
    nonce: string;
    hkdf_info: string;
    hkdf_salt: string;
    content_key: string;
  }>('derive.json');

  test.each(suite.vectors.map((v) => [v.name, v] as const))('%s', (_name, v) => {
    const epoch = epochOf(v);
    expect(toHex(entryInfo(epoch))).toBe(v.hkdf_info);
    expect(v.hkdf_salt).toBe(v.nonce);
    expect(toHex(contentKey(fromHex(v.group_key), fromHex(v.nonce), epoch))).toBe(v.content_key);
  });
});

describe('the plaintext frame (spec/crypto.md §6)', () => {
  const suite = load<{ name: string; frame: FrameVector; encoded: string }>('frame.json');

  test.each(suite.vectors.map((v) => [v.name, v] as const))('%s', (_name, v) => {
    expect(toHex(encodeFrame(frameOf(v.frame)))).toBe(v.encoded);

    const back = decodeFrame(fromHex(v.encoded));
    expect(back.contentType).toBe(v.frame.content_type);
    expect(back.filename).toBe(v.frame.filename);
    expect(back.createdAt).toBe(BigInt(v.frame.created_at_unix_ms));
    expect(toHex(back.body)).toBe(v.frame.body);
  });
});

describe('entries end to end (spec/crypto.md §5)', () => {
  const suite = load<{
    name: string;
    epoch_hex: string;
    entry_id: string;
    group_key: string;
    nonce: string;
    frame: FrameVector;
    encoded_frame: string;
    aad: string;
    content_key: string;
    container: string;
  }>('entry.json');

  test.each(suite.vectors.map((v) => [v.name, v] as const))('%s', (_name, v) => {
    const epoch = epochOf(v);
    expect(toHex(encodeFrame(frameOf(v.frame)))).toBe(v.encoded_frame);
    expect(toHex(entryAAD(epoch, v.entry_id))).toBe(v.aad);
    expect(toHex(contentKey(fromHex(v.group_key), fromHex(v.nonce), epoch))).toBe(v.content_key);
    expect(
      toHex(
        sealEntry(
          fromHex(v.group_key),
          fromHex(v.nonce),
          epoch,
          v.entry_id,
          fromHex(v.encoded_frame),
        ),
      ),
    ).toBe(v.container);
    expect(toHex(openEntry(fromHex(v.group_key), fromHex(v.container), epoch, v.entry_id))).toBe(
      v.encoded_frame,
    );
  });
});

describe('inputs that must be rejected (spec/vectors/failure.json)', () => {
  const suite = load<{
    name: string;
    operation: 'unwrap' | 'open_entry' | 'decode_frame';
    expect: string;
    epoch_hex: string;
    recipient_private_key?: string;
    wrapped_key?: string;
    group_key?: string;
    container?: string;
    entry_id?: string;
    encoded_frame?: string;
  }>('failure.json');

  test.each(suite.vectors.map((v) => [v.name, v] as const))('%s', (_name, v) => {
    expect(v.expect).toBe('error');
    // Returning plaintext for any of these is broken, not lenient.
    switch (v.operation) {
      case 'unwrap':
        expect(() =>
          unwrap(fromHex(v.wrapped_key!), fromHex(v.recipient_private_key!), epochOf(v)),
        ).toThrow();
        break;
      case 'open_entry':
        expect(() =>
          openEntry(fromHex(v.group_key!), fromHex(v.container!), epochOf(v), v.entry_id!),
        ).toThrow();
        break;
      case 'decode_frame':
        expect(() => decodeFrame(fromHex(v.encoded_frame!))).toThrow();
        break;
      default:
        throw new Error(`unknown operation ${v.operation as string}`);
    }
  });
});
