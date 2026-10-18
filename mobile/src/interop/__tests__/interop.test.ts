/**
 * The two client implementations, against the real relay.
 *
 * ROADMAP P7's second acceptance criterion: "a scripted test pairs the RN
 * client with a Go client against a local server". This is that script. It
 * runs the actual server binary from `/server` against a real Redis, drives
 * `pkg/tppclient` as a subprocess, and runs this app's own core in the same
 * process as the test — the very code Hermes runs, since `src/core` imports no
 * React Native.
 *
 * It is not part of `npm test`: it needs Go and Redis, which the Node CI job
 * has neither of. `npm run interop` runs it, and it skips with a reason when a
 * machine cannot host it (docs/internal/conventions.md §5).
 */

import { sha256 } from '@noble/hashes/sha2.js';

import {
  Client,
  EntryCache,
  type CacheStore,
  TppError,
  fromUTF8,
  toHex,
  utf8,
  type Progress,
  type SecureStore,
} from '../../core';
import { GoPeer, Relay, missingTool } from '../relay';

/** InMemoryStore stands in for the Android Keystore, which does not exist under Node. */
class InMemoryStore implements SecureStore {
  private readonly values = new Map<string, string>();
  async load(name: string): Promise<string | null> {
    return this.values.get(name) ?? null;
  }
  async save(name: string, value: string): Promise<void> {
    this.values.set(name, value);
  }
  async clear(name: string): Promise<void> {
    this.values.delete(name);
  }
}

/** MemoryCacheStore stands in for the app's cache directory. */
class MemoryCacheStore implements CacheStore {
  readonly files = new Map<string, Uint8Array>();
  async read(name: string) {
    return this.files.get(name) ?? null;
  }
  async write(name: string, data: Uint8Array) {
    this.files.set(name, data.slice());
  }
  async remove(name: string) {
    this.files.delete(name);
  }
  async list() {
    return [...this.files].map(([name, data]) => ({ name, size: data.length }));
  }
}

/** pattern is the body `put-large` writes, so a test can check every byte. */
function matchesPattern(body: Uint8Array): boolean {
  for (let i = 0; i < body.length; i++) {
    if (body[i] !== ((i * 31 + 7) & 0xff)) {
      return false;
    }
  }
  return true;
}

const skip = missingTool();
const describeInterop = skip === null ? describe : describe.skip;

if (skip !== null) {
  // eslint-disable-next-line no-console
  console.warn(`skipping the interop test: ${skip} is not installed, so a real relay cannot be run`);
}

describeInterop('the phone and the Go client are one implementation of one contract', () => {
  jest.setTimeout(300_000);

  let relay: Relay;
  let go: GoPeer;
  let phone: Client;
  const progress: Progress[] = [];
  // onProgress lets a test act in the middle of a transfer.
  let onProgress: ((p: Progress) => void) | null = null;
  const cacheStore = new MemoryCacheStore();

  beforeAll(async () => {
    relay = await Relay.start();
    go = GoPeer.start('Anna — laptop');
    phone = await Client.open({
      store: new InMemoryStore(),
      deviceName: 'Anna — phone',
      cache: new EntryCache(cacheStore),
      handlers: {
        onProgress: (p) => {
          progress.push(p);
          onProgress?.(p);
        },
      },
    });
  });

  afterAll(() => {
    phone?.disconnect();
    go?.stop();
    relay?.stop();
  });

  test('the Go client creates the group and the phone pairs into it (SPEC §3.1, §3.2)', async () => {
    const created = await go.send(`create ${await relay.creationURL('Anna')}`);
    expect(created.epoch).toBe(1);

    // The payload the desktop would render as a QR code is the string the
    // phone's scanner hands to joinPairing, unchanged.
    const invitation = await go.send('pair');
    const joinedPromise = go.send('await-join');
    await phone.joinPairing(String(invitation.payload));
    const joined = await joinedPromise;

    expect(joined.device_name).toBe('Anna — phone');
    expect(phone.inGroup).toBe(true);
    expect(phone.epoch).toBe(1n);
  });

  test('an entry written by Go decrypts on the phone (SPEC §6)', async () => {
    const written = await go.send('put hello from the Go client — ünïcode, 日本語');
    const item = await phone.getLatest();

    expect(fromUTF8(item.body)).toBe('hello from the Go client — ünïcode, 日本語');
    expect(item.contentType).toBe('text/plain; charset=utf-8');
    // The id the writer bound into the AAD is the id the reader used to open
    // it: if these two ever disagreed, nothing would decrypt at all.
    expect(item.meta?.id).toBe(written.entry_id);
  });

  test('an entry written by the phone decrypts in Go', async () => {
    const meta = await phone.putEntry({
      contentType: 'text/plain; charset=utf-8',
      body: utf8('hello from the phone — 📋'),
    });
    const read = await go.send('latest');

    expect(read.body).toBe('hello from the phone — 📋');
    expect(read.entry_id).toBe(meta.id);
    expect(read.epoch).toBe(1);
  });

  test('history lists both entries, newest first, and only when asked', async () => {
    const page = await phone.getHistory({ limit: 10 });
    expect(page.entries.length).toBeGreaterThanOrEqual(2);
    expect(page.entries[0].createdAt.getTime()).toBeGreaterThanOrEqual(
      page.entries[1].createdAt.getTime(),
    );

    const oldest = page.entries[page.entries.length - 1];
    const item = await phone.getEntry(oldest.id);
    expect(item.contentType).toBe('text/plain; charset=utf-8');
  });

  test('each side reads the sealed entry header the other wrote (spec/crypto.md §5.5)', async () => {
    // The phone reads what Go sealed...
    const page = await phone.getHistory({ limit: 10 });
    const fromGo = page.entries[page.entries.length - 1];
    expect(fromGo.header).toEqual({
      contentType: 'text/plain; charset=utf-8',
      filename: '',
      size: utf8('hello from the Go client — ünïcode, 日本語').length,
    });

    // ...and Go reads what the phone sealed.
    const meta = await phone.putEntry({
      contentType: 'application/pdf',
      filename: 'Quarterly report – Q3 ✓.pdf',
      body: utf8('%PDF-1.7'),
    });
    const listed = (await go.send('history')).entries as Record<string, unknown>[];
    expect(listed[0]).toEqual({
      entry_id: meta.id,
      content_type: 'application/pdf',
      filename: 'Quarterly report – Q3 ✓.pdf',
      bytes: 8,
    });
  });

  test('an entry larger than one frame, written by Go, reads on the phone in ranges (SPEC §4.3)', async () => {
    const size = 20 << 20;
    const written = await go.send(`put-large ${size}`);
    progress.length = 0;
    const item = await phone.getLatest();

    expect(item.meta?.id).toBe(written.entry_id);
    expect(item.filename).toBe('large.bin');
    expect(item.body.length).toBe(size);
    let same = true;
    for (let i = 0; i < size; i++) {
      if (item.body[i] !== ((i * 31 + 7) & 0xff)) {
        same = false;
        break;
      }
    }
    expect(same).toBe(true);
    const downloads = progress.filter((p) => p.direction === 'download');
    expect(downloads.length).toBeGreaterThanOrEqual(3);
    // The size is known from the latest-entry answer, so the bar starts at
    // zero rather than appearing only once the first chunk has arrived.
    expect(downloads[0].done).toBe(0);
    // The bar can say what is downloading, from the sealed header.
    expect(downloads.every((p) => p.filename === 'large.bin')).toBe(true);
    expect(downloads[downloads.length - 1].done).toBe(downloads[downloads.length - 1].total);
  });

  test('an entry under one frame but over one chunk still reports download progress', async () => {
    // Past the relay's 8 MiB chunk and under the 10 MiB frame cap: it is read
    // in two ranges, and used to be read with no progress at all.
    const size = 9 << 20;
    const written = await go.send(`put-large ${size}`);
    progress.length = 0;
    const item = await phone.getLatest();

    expect(item.meta?.id).toBe(written.entry_id);
    expect(item.body.length).toBe(size);
    const downloads = progress.filter((p) => p.direction === 'download');
    expect(downloads.length).toBeGreaterThanOrEqual(2);
    expect(downloads[0].done).toBe(0);
    expect(downloads[downloads.length - 1].done).toBe(downloads[downloads.length - 1].total);
  });

  test('a download carries on when the app goes to the background part-way through', async () => {
    const size = 20 << 20;
    const written = await go.send(`put-large ${size}`);
    progress.length = 0;
    let backgrounded = false;
    onProgress = (p) => {
      if (!backgrounded && p.direction === 'download' && p.done > 0) {
        backgrounded = true;
        // What the app does when the user switches to another app.
        phone.disconnect();
      }
    };
    try {
      const item = await phone.getLatest();
      expect(backgrounded).toBe(true);
      expect(item.meta?.id).toBe(written.entry_id);
      expect(item.body.length).toBe(size);
      // The socket was kept for the transfer only, and closed once it ended.
      expect(phone.connected).toBe(false);
    } finally {
      onProgress = null;
      await phone.connect();
    }
  });

  test('a download whose socket drops in the background resumes where it was when the app returns', async () => {
    const size = 20 << 20;
    const written = await go.send(`put-large ${size}`);
    progress.length = 0;
    let dropped = false;
    onProgress = (p) => {
      if (!dropped && p.direction === 'download' && p.done > 0) {
        dropped = true;
        phone.disconnect();
        // The OS kills the socket while the app is away.
        (phone as unknown as { conn: { close(reason: string): void } }).conn.close('simulated drop');
        setTimeout(() => void phone.connect(), 200);
      }
    };
    try {
      const item = await phone.getLatest();
      expect(dropped).toBe(true);
      expect(item.meta?.id).toBe(written.entry_id);
      expect(item.body.length).toBe(size);
      let same = true;
      for (let i = 0; i < size; i++) {
        if (item.body[i] !== ((i * 31 + 7) & 0xff)) {
          same = false;
          break;
        }
      }
      expect(same).toBe(true);
      // It picked up from the range it had, rather than starting over.
      const downloads = progress.filter((x) => x.direction === 'download');
      const done = downloads.map((x) => x.done);
      expect(done).toEqual([...done].sort((a, b) => a - b));
    } finally {
      onProgress = null;
    }
  });

  test('two large downloads take turns rather than splitting the link', async () => {
    const a = String((await go.send(`put-large ${12 << 20}`)).entry_id);
    const b = String((await go.send(`put-large ${12 << 20}`)).entry_id);
    progress.length = 0;
    const [ia, ib] = await Promise.all([phone.getEntry(a), phone.getEntry(b)]);
    expect(matchesPattern(ia.body)).toBe(true);
    expect(matchesPattern(ib.body)).toBe(true);

    const downloads = progress.filter((p) => p.direction === 'download');
    // One of them was told to wait…
    expect(downloads.some((p) => p.waiting)).toBe(true);
    // …and while one was moving, the other never was: no running report for
    // one entry falls between two unfinished running reports of the other.
    let active: string | null = null;
    for (const p of downloads.filter((x) => !x.waiting)) {
      if (active !== null && active !== p.entryId) {
        throw new Error(`${p.entryId} moved while ${active} was still running`);
      }
      active = p.done < p.total ? p.entryId : null;
    }
  });

  test('reading an entry that is already being read joins that download', async () => {
    const id = String((await go.send(`put-large ${20 << 20}`)).entry_id);
    progress.length = 0;
    const [one, two] = await Promise.all([phone.getEntry(id), phone.getEntry(id)]);
    expect(matchesPattern(one.body)).toBe(true);
    // Compared as bytes: Jest's deep equality takes over a minute on 20 MB.
    expect(matchesPattern(two.body)).toBe(true);
    expect(two.body.length).toBe(one.body.length);
    const done = progress.filter((p) => p.direction === 'download' && !p.waiting).map((p) => p.done);
    // Each range was read once.
    expect(new Set(done).size).toBe(done.length);
  });

  test('a large entry read again within the hour comes from the cache, not the relay', async () => {
    const id = String((await go.send(`put-large ${20 << 20}`)).entry_id);
    await phone.getEntry(id);
    expect(cacheStore.files.has(`${id}.bin`)).toBe(true);

    progress.length = 0;
    const again = await phone.getEntry(id);
    expect(matchesPattern(again.body)).toBe(true);
    expect(progress.filter((p) => p.direction === 'download')).toEqual([]);

    // A cached copy that no longer opens is dropped and read from the relay.
    const bin = cacheStore.files.get(`${id}.bin`);
    if (bin === undefined) {
      throw new Error('the entry left the cache');
    }
    bin[100] ^= 0xff;
    const repaired = await phone.getEntry(id);
    expect(matchesPattern(repaired.body)).toBe(true);
    expect(progress.some((p) => p.direction === 'download')).toBe(true);
  });

  test('an entry larger than one frame, written by the phone, uploads in chunks and reads in Go', async () => {
    const body = new Uint8Array(20 << 20);
    for (let i = 0; i < body.length; i++) {
      body[i] = (i * 17 + 3) & 0xff;
    }
    progress.length = 0;
    const meta = await phone.putEntry({
      contentType: 'application/octet-stream',
      filename: 'from-the-phone.bin',
      body,
    });
    const read = await go.send('latest-digest');

    expect(read.entry_id).toBe(meta.id);
    expect(read.length).toBe(body.length);
    expect(read.sha256).toBe(toHex(sha256(body)));
    expect(read.filename).toBe('from-the-phone.bin');
    const uploads = progress.filter((p) => p.direction === 'upload');
    expect(uploads.length).toBeGreaterThanOrEqual(4);
    expect(uploads[uploads.length - 1].done).toBe(meta.size);
    expect(uploads.every((p) => p.filename === 'from-the-phone.bin')).toBe(true);
  });

  test('a second phone pairs the other way round: it shows, the Go client accepts', async () => {
    // The direction the wire contract could not express before
    // docs/internal/plans/joiner-emitted-pairing.md, and the one that proves the two
    // implementations agree on PairingCode: this phone encodes the offer, the
    // Go client decodes it, and the group key travels back.
    const second = await Client.open({
      store: new InMemoryStore(),
      deviceName: 'Anna — second phone',
    });
    try {
      const offer = await second.startOffer(relay.baseUrl);

      // Reading the code changes nothing: it returns what a dialog renders,
      // and nothing is admitted until the confirm below.
      const read = await go.send(`read-offer ${offer.code}`);
      expect(read.device_name).toBe('Anna — second phone');
      expect(String(read.fingerprint)).toMatch(/^[0-9A-F]{4}( [0-9A-F]{4}){3}$/);
      expect(second.inGroup).toBe(false);

      const admitted = await go.send('accept-offer');
      await offer.accepted;

      expect(second.inGroup).toBe(true);
      expect(second.deviceId).toBe(admitted.device_id);

      // The group key really crossed: an entry the Go client wrote earlier
      // stays unreadable, but one it writes now does not.
      await go.send('put written for the second phone');
      const item = await second.getLatest();
      expect(fromUTF8(item.body)).toBe('written for the second phone');
    } finally {
      second.disconnect();
    }
  });

  test('a revocation by the Go client locks the phone out (SPEC §3.3)', async () => {
    const roster = (await go.send('devices')) as unknown as {
      devices: { id: string; name: string; this: boolean }[];
    };
    const target = roster.devices.find((device) => !device.this);
    expect(target?.name).toBe('Anna — phone');

    const result = await go.send(`revoke ${target?.id ?? ''}`);
    expect(result.epoch).toBe(2);

    // The phone's socket is gone with its record, and nothing it holds can
    // read what the group writes next.
    phone.disconnect();
    await expect(phone.connect()).rejects.toBeInstanceOf(TppError);

    await go.send('put written after the rekey');
    const epoch = await go.send('epoch');
    expect(epoch.epoch).toBe(2);
  });
});
