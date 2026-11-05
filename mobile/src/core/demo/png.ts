/**
 * A PNG drawn in code, for the demo's sample image.
 *
 * The repository takes no binary blobs beyond its icons and screenshots
 * (CLAUDE.md), so the demo's picture is generated rather than shipped. It is
 * an RGB gradient written with deflate's "stored" blocks: no compression, and
 * so no compressor to depend on. At the size the demo uses that costs a few
 * kilobytes, which is nothing.
 */

/** samplePNG returns a width × height gradient as PNG bytes. */
export function samplePNG(width: number, height: number): Uint8Array {
  // Each row starts with filter type 0 (none), then three bytes a pixel.
  const stride = 1 + width * 3;
  const raw = new Uint8Array(stride * height);
  for (let y = 0; y < height; y++) {
    raw[y * stride] = 0;
    for (let x = 0; x < width; x++) {
      const at = y * stride + 1 + x * 3;
      raw[at] = Math.round(40 + (x / Math.max(width - 1, 1)) * 60); // R
      raw[at + 1] = Math.round(110 + (y / Math.max(height - 1, 1)) * 90); // G
      raw[at + 2] = 240; // B
    }
  }

  const header = new Uint8Array(13);
  writeU32(header, 0, width);
  writeU32(header, 4, height);
  header[8] = 8; // bit depth
  header[9] = 2; // colour type: RGB
  // Compression, filter and interlace methods are all 0.

  return concat([
    new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', header),
    chunk('IDAT', zlibStored(raw)),
    chunk('IEND', new Uint8Array(0)),
  ]);
}

/** zlibStored wraps data in a zlib stream of uncompressed deflate blocks (RFC 1950, RFC 1951 §3.2.4). */
function zlibStored(data: Uint8Array): Uint8Array {
  const parts: Uint8Array[] = [new Uint8Array([0x78, 0x01])];
  const max = 0xffff;
  let off = 0;
  do {
    const end = Math.min(off + max, data.length);
    const block = data.subarray(off, end);
    const head = new Uint8Array(5);
    head[0] = end === data.length ? 1 : 0; // BFINAL on the last block, BTYPE 00
    head[1] = block.length & 0xff;
    head[2] = block.length >>> 8;
    head[3] = ~block.length & 0xff;
    head[4] = (~block.length >>> 8) & 0xff;
    parts.push(head, block);
    off = end;
  } while (off < data.length);
  const trailer = new Uint8Array(4);
  writeU32(trailer, 0, adler32(data));
  parts.push(trailer);
  return concat(parts);
}

function chunk(type: string, data: Uint8Array): Uint8Array {
  const out = new Uint8Array(12 + data.length);
  writeU32(out, 0, data.length);
  for (let i = 0; i < 4; i++) {
    out[4 + i] = type.charCodeAt(i);
  }
  out.set(data, 8);
  writeU32(out, 8 + data.length, crc32(out.subarray(4, 8 + data.length)));
  return out;
}

let crcTable: Uint32Array | null = null;

function crc32(data: Uint8Array): number {
  if (crcTable === null) {
    crcTable = new Uint32Array(256);
    for (let n = 0; n < 256; n++) {
      let c = n;
      for (let k = 0; k < 8; k++) {
        c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
      }
      crcTable[n] = c >>> 0;
    }
  }
  let crc = 0xffffffff;
  for (const byte of data) {
    crc = crcTable[(crc ^ byte) & 0xff] ^ (crc >>> 8);
  }
  return (crc ^ 0xffffffff) >>> 0;
}

function adler32(data: Uint8Array): number {
  let a = 1;
  let b = 0;
  for (const byte of data) {
    a = (a + byte) % 65521;
    b = (b + a) % 65521;
  }
  return ((b << 16) | a) >>> 0;
}

function writeU32(buf: Uint8Array, at: number, value: number): void {
  buf[at] = (value >>> 24) & 0xff;
  buf[at + 1] = (value >>> 16) & 0xff;
  buf[at + 2] = (value >>> 8) & 0xff;
  buf[at + 3] = value & 0xff;
}

function concat(parts: Uint8Array[]): Uint8Array {
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let at = 0;
  for (const p of parts) {
    out.set(p, at);
    at += p.length;
  }
  return out;
}
