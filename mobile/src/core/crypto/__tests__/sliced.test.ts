import { openEntry, openEntrySliced, sealEntry } from '../entry';

const now = () => Promise.resolve();

describe('openEntrySliced', () => {
  const key = Uint8Array.from({ length: 32 }, (_, i) => i * 7);
  const nonce = Uint8Array.from({ length: 24 }, (_, i) => 200 - i);

  it('opens exactly what openEntry opens, at every size around a slice and block edge', async () => {
    for (const n of [0, 1, 15, 16, 17, 63, 64, 65, 262143, 262144, 262145, 1_000_003]) {
      const frame = Uint8Array.from({ length: n }, (_, i) => (i * 13 + 5) & 0xff);
      const container = sealEntry(key, nonce, 4n, 'entry-x', frame);
      const sliced = await openEntrySliced(key, container, 4n, 'entry-x', now);
      expect(Buffer.from(sliced).equals(Buffer.from(openEntry(key, container, 4n, 'entry-x')))).toBe(true);
      expect(Buffer.from(sliced).equals(Buffer.from(frame))).toBe(true);
    }
  });

  it('refuses a tampered container, a wrong id and a wrong epoch, before decrypting', async () => {
    const frame = new Uint8Array(300_000).fill(9);
    const container = sealEntry(key, nonce, 4n, 'entry-x', frame);
    const bad = container.slice();
    bad[1000] ^= 1;
    await expect(openEntrySliced(key, bad, 4n, 'entry-x', now)).rejects.toThrow('invalid tag');
    await expect(openEntrySliced(key, container, 4n, 'entry-y', now)).rejects.toThrow('invalid tag');
    await expect(openEntrySliced(key, container, 5n, 'entry-x', now)).rejects.toThrow('invalid tag');
  });

  it('hands control back between slices, so the app can answer a tap', async () => {
    const frame = new Uint8Array(2 << 20);
    const container = sealEntry(key, nonce, 4n, 'entry-x', frame);
    let pauses = 0;
    await openEntrySliced(key, container, 4n, 'entry-x', async () => {
      pauses++;
    });
    // 2 MiB in 256 KiB slices, once to authenticate and once to decrypt.
    expect(pauses).toBe(16);
  });
});
