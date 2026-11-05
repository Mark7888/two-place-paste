import { crc32, inflateSync } from 'node:zlib';

import { samplePNG } from '../png';

/** chunks splits a PNG into its chunks, checking each one's CRC on the way. */
function chunks(png: Uint8Array): { type: string; data: Buffer }[] {
  const buf = Buffer.from(png);
  const out: { type: string; data: Buffer }[] = [];
  let at = 8;
  while (at < buf.length) {
    const length = buf.readUInt32BE(at);
    const type = buf.toString('latin1', at + 4, at + 8);
    const data = buf.subarray(at + 8, at + 8 + length);
    expect(buf.readUInt32BE(at + 8 + length)).toBe(crc32(buf.subarray(at + 4, at + 8 + length)));
    out.push({ type, data });
    at += 12 + length;
  }
  return out;
}

test.each([
  ['the demo’s size', 96, 64],
  ['one pixel', 1, 1],
  // Over 64 KiB of pixel data, so more than one stored block.
  ['several deflate blocks', 200, 120],
])('samplePNG writes a valid PNG: %s', (_name, width, height) => {
  const png = samplePNG(width, height);
  expect([...png.subarray(0, 8)]).toEqual([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);

  const parts = chunks(png);
  expect(parts.map((c) => c.type)).toEqual(['IHDR', 'IDAT', 'IEND']);
  expect(parts[0].data.readUInt32BE(0)).toBe(width);
  expect(parts[0].data.readUInt32BE(4)).toBe(height);

  // zlib checks the Adler-32 trailer as it inflates.
  const raw = inflateSync(parts[1].data);
  expect(raw.length).toBe((1 + width * 3) * height);
});
