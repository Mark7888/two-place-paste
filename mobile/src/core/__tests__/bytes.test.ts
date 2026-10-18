/**
 * The encodings the profile and the wire depend on. They are hand-written
 * (see `bytes.ts`), so they are tested rather than trusted.
 */

import {
  concat,
  decodeBase64Std,
  decodeBase64StdAsync,
  encodeBase64Std,
  encodeBase64StdAsync,
  fromBase64URL,
  fromHex,
  fromUTF8,
  toBase64URL,
  toHex,
  u64be,
  utf8,
} from '../bytes';

describe('hex', () => {
  test('round trips, lowercase and unprefixed', () => {
    const bytes = Uint8Array.from([0x00, 0x0f, 0xa0, 0xff]);
    expect(toHex(bytes)).toBe('000fa0ff');
    expect(fromHex('000FA0FF')).toEqual(bytes);
  });

  test('rejects what is not hex', () => {
    expect(() => fromHex('abc')).toThrow(/odd length/);
    expect(() => fromHex('zz')).toThrow(/non-hex/);
  });
});

describe('utf8', () => {
  test.each([
    ['ascii', 'hello'],
    ['accents', 'café'],
    ['cjk', '日本語のテキスト'],
    ['emoji beyond the BMP', '📋🔐'],
    ['empty', ''],
  ])('%s round trips', (_name, text) => {
    expect(fromUTF8(utf8(text))).toBe(text);
  });

  test('a lone surrogate becomes U+FFFD rather than a crash', () => {
    // A filename arrives from another device. It is display text, and display
    // text must never be able to throw on its way to a label.
    expect(fromUTF8(utf8('a\uD800b'))).toBe('a�b');
  });

  test('malformed bytes decode to U+FFFD', () => {
    expect(fromUTF8(Uint8Array.from([0xff, 0x41]))).toBe('�A');
  });
});

describe('base64url', () => {
  test.each([0, 1, 2, 3, 4, 5, 16, 31, 32])('round trips %i bytes unpadded', (n) => {
    const bytes = Uint8Array.from({ length: n }, (_, i) => (i * 37) & 0xff);
    const encoded = toBase64URL(bytes);
    expect(encoded).not.toContain('=');
    expect(fromBase64URL(encoded)).toEqual(bytes);
  });

  test('tolerates what a clipboard adds, and the standard alphabet', () => {
    const bytes = Uint8Array.from([0xfb, 0xef, 0xbe]);
    expect(toBase64URL(bytes)).toBe('----');
    expect(fromBase64URL(' ++++ \n')).toEqual(bytes);
    expect(fromBase64URL('AA==')).toEqual(Uint8Array.of(0));
  });

  test('rejects a character that is in no alphabet', () => {
    expect(() => fromBase64URL('nope!')).toThrow(/base64url/);
  });
});

describe('u64be', () => {
  test('encodes the epoch the vectors pin, which no JavaScript number can hold', () => {
    expect(toHex(u64be(2n ** 64n - 1n))).toBe('ffffffffffffffff');
    expect(toHex(u64be(1n))).toBe('0000000000000001');
  });
});

describe('concat', () => {
  test('joins in order', () => {
    expect(toHex(concat(Uint8Array.of(1), new Uint8Array(0), Uint8Array.of(2, 3)))).toBe(
      '010203',
    );
  });
});

describe('standard base64 for the native bridge', () => {
  const sample = (n: number) => Uint8Array.from({ length: n }, (_, i) => (i * 131 + 7) & 0xff);

  it('matches Node for every length around a block boundary and padding case', () => {
    for (const n of [0, 1, 2, 3, 4, 5, 24575, 24576, 24577, 100_000]) {
      const b = sample(n);
      const expected = Buffer.from(b).toString('base64');
      expect(encodeBase64Std(b)).toBe(expected);
      expect(decodeBase64Std(expected)).toEqual(b);
      // Unpadded input decodes too.
      expect(decodeBase64Std(expected.replace(/=+$/, ''))).toEqual(b);
    }
  });

  it('rejects what is not base64', () => {
    expect(() => decodeBase64Std('ab-c')).toThrow();
    expect(() => decodeBase64Std('abcde')).toThrow();
  });
});

describe('the yielding base64 codec', () => {
  it('agrees with Node across several yield steps and every padding case', async () => {
    const none = () => Promise.resolve();
    for (const n of [0, 1, 2, 245759, 245760, 245761, 1_000_001]) {
      const b = Uint8Array.from({ length: n }, (_, i) => (i * 29 + 1) & 0xff);
      const expected = Buffer.from(b).toString('base64');
      expect(await encodeBase64StdAsync(b, none)).toBe(expected);
      expect(Buffer.from(await decodeBase64StdAsync(expected, none)).equals(Buffer.from(b))).toBe(true);
    }
  });
});
