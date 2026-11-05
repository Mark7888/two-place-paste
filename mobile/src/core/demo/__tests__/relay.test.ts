/**
 * The parts of the in-process relay the demo needs and the client tests did
 * not: chunked transfer, the store, history paging and eviction, and the
 * refusal of every address but its own. Each runs the real `Client` against
 * it, so what is checked is that the two agree on the wire.
 */

import { sha256 } from '@noble/hashes/sha2.js';

import { toHex, utf8 } from '../../bytes';
import { Client, type Handlers } from '../../protocol/client';
import { ProtocolError } from '../../protocol/errors';
import type { StoreItem } from '../../protocol/store';
import { ErrorCode } from '../../../protocol/gen/tpp/v1/error';
import { MemoryStore } from '../memoryStore';
import { FakeRelay, type FakeRelayOptions } from '../relay';

const MiB = 1 << 20;

const opened: Client[] = [];

afterEach(() => {
  while (opened.length > 0) {
    opened.pop()?.disconnect();
  }
});

function relayWith(options: FakeRelayOptions = {}): FakeRelay {
  return new FakeRelay({
    limits: {
      maxEntryBytes: (32 * MiB).toString(),
      singleFrameMaxBytes: (10 * MiB).toString(),
      chunkBytes: MiB.toString(),
      inlineMaxBytes: (64 * 1024).toString(),
      headerV2: true,
    },
    store: { maxBytes: 32 * MiB, maxItems: 10, maxItemBytes: 16 * MiB },
    ...options,
  });
}

async function open(relay: FakeRelay, name: string, handlers?: Handlers): Promise<Client> {
  const client = await Client.open({
    store: new MemoryStore(),
    deviceName: name,
    handlers,
    socketFactory: relay.factory,
    healthCheck: async () => false,
    deviceStatus: relay.deviceStatus,
  });
  opened.push(client);
  return client;
}

/** pair runs SPEC §3.2, the inviter's user confirming the join (step 4). */
async function pair(inviter: Client, joiner: Client): Promise<void> {
  const invitation = await inviter.startPairing();
  const joining = joiner.joinPairing(invitation.payload);
  await (await invitation.request).confirm();
  await joining;
  await invitation.joined;
}

/** group creates a group of two on a relay; `handlers` are the second device's. */
async function group(relay: FakeRelay, handlers?: Handlers): Promise<[Client, Client]> {
  const a = await open(relay, 'laptop');
  await a.createGroup(relay.creationURL);
  const b = await open(relay, 'phone', handlers);
  await pair(a, b);
  await b.connect();
  return [a, b];
}

/** signal is a promise a handler resolves, so a test waits for the event rather than for time. */
function signal<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

function filled(size: number, seed: number): Uint8Array {
  const out = new Uint8Array(size);
  for (let i = 0; i < size; i++) {
    out[i] = (i * 31 + seed) & 0xff;
  }
  return out;
}

describe('chunked entries', () => {
  test.each([
    ['larger than one frame: uploaded in chunks, read in ranges', 11 * MiB],
    ['over the inline limit but one frame: put whole, read in ranges', 200 * 1024],
  ])('%s', async (_name, size) => {
    const relay = relayWith();
    const [a, b] = await group(relay);
    const body = filled(size, 7);

    const meta = await a.putEntry({ contentType: 'application/octet-stream', filename: 'blob.bin', body });
    expect(meta.inline).toBe(false);
    expect(relay.uploadCount).toBe(0);

    const got = await b.getLatest();
    expect(got.filename).toBe('blob.bin');
    expect(got.body.length).toBe(size);
    expect(Buffer.from(got.body).equals(Buffer.from(body))).toBe(true);
  }, 30_000);

  test('a small entry stays inline', async () => {
    const relay = relayWith();
    const [a, b] = await group(relay);
    const meta = await a.putEntry({ contentType: 'text/plain', body: utf8('hello') });
    expect(meta.inline).toBe(true);
    expect((await b.getLatest()).body).toEqual(utf8('hello'));
  });

  test('an entry sealed at an old epoch is refused', async () => {
    const relay = relayWith();
    const [a, b] = await group(relay);
    const c = await open(relay, 'tablet');
    await pair(a, c);
    // b falls behind: it is offline while a revokes c and re-keys.
    b.disconnect();
    await (await a.prepareRevoke(c.deviceId)).confirm();

    // putEntry seals before it dials, so the entry is sealed at epoch 1; the
    // key for epoch 2 only arrives on the socket that dial opens.
    const err = await b.putEntry({ contentType: 'text/plain', body: utf8('late') }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ProtocolError);
    expect((err as ProtocolError).code).toBe(ErrorCode.ERROR_CODE_EPOCH_CONFLICT);
    expect(relay.entryCount).toBe(0);
  });
});

describe('history', () => {
  test('pages newest first by limit and before', async () => {
    let clock = 1_000;
    const relay = relayWith({ now: () => clock++ });
    const [a, b] = await group(relay);
    for (const text of ['one', 'two', 'three']) {
      await a.putEntry({ contentType: 'text/plain', body: utf8(text) });
    }

    const first = await b.getHistory({ limit: 2 });
    expect(first.entries).toHaveLength(2);
    expect(first.nextBefore).not.toBeNull();
    const second = await b.getHistory({ limit: 2, before: first.nextBefore ?? undefined });
    expect(second.entries).toHaveLength(1);
    expect(second.nextBefore).toBeNull();

    const bodies = await Promise.all([...first.entries, ...second.entries].map(async (m) => (await b.getEntry(m.id)).body));
    expect(bodies).toEqual([utf8('three'), utf8('two'), utf8('one')]);
  });

  test('the oldest entry is evicted past the cap, and every device is told', async () => {
    const relay = relayWith({ maxEntries: 2 });
    const evicted = signal<string>();
    const [a, b] = await group(relay, { onEntryEvicted: (id) => evicted.resolve(id) });

    const oldest = await a.putEntry({ contentType: 'text/plain', body: utf8('one') });
    await a.putEntry({ contentType: 'text/plain', body: utf8('two') });
    await a.putEntry({ contentType: 'text/plain', body: utf8('three') });

    expect(await evicted.promise).toBe(oldest.id);
    expect(relay.entryCount).toBe(2);
    expect((await b.getHistory()).entries.map((m) => m.id)).not.toContain(oldest.id);
  });
});

describe('store (SPEC §6b)', () => {
  test('a body hash the caller already has is used as given', async () => {
    const relay = relayWith();
    const [a, b] = await group(relay, {});
    const body = utf8('a shared file');
    const right = sha256(body);
    const saved = await a.saveToStore(
      { contentType: 'text/plain', filename: 'f.txt', body, sha256: right },
      { name: 'F', description: '', tags: [] },
    );
    expect(saved.sha256Hex).toBe(toHex(right));
    expect((await b.fetchStoreItem((await b.listStore()).items[0])).body).toEqual(body);

    // Not worked out again: a wrong one is stored as given, and the item then
    // fails its check where it is read, as a corrupted body would.
    const wrong = new Uint8Array(32).fill(1);
    const bad = await a.saveToStore(
      { contentType: 'text/plain', filename: 'g.txt', body: utf8('other'), sha256: wrong },
      { name: 'G', description: '', tags: [] },
    );
    expect(bad.sha256Hex).toBe(toHex(wrong));
    await expect(a.fetchStoreItem(bad)).rejects.toThrow();
  });

  test('save, list, fetch, edit and delete, with the other device told of each', async () => {
    const relay = relayWith();
    const added = signal<StoreItem>();
    const updated = signal<StoreItem>();
    const deleted = signal<string>();
    const [a, b] = await group(relay, {
      onStoreItemAdded: (it) => added.resolve(it),
      onStoreItemUpdated: (it) => updated.resolve(it),
      onStoreItemDeleted: (id) => deleted.resolve(id),
    });

    const saved = await a.saveToStore(
      { contentType: 'text/plain', body: utf8('the wifi password') },
      { name: 'Wi-Fi', description: 'home', tags: ['home'] },
    );
    expect((await added.promise).name).toBe('Wi-Fi');

    const limits = await b.storeLimits();
    expect(limits.enabled).toBe(true);
    expect(limits.usedItems).toBe(1);
    expect(limits.usedBytes).toBe(saved.blobSize);

    const listing = await b.listStore();
    expect(listing.unreadable).toEqual([]);
    expect(listing.items.map((it) => it.name)).toEqual(['Wi-Fi']);
    expect((await b.fetchStoreItem(listing.items[0])).body).toEqual(utf8('the wifi password'));

    const edited = await a.editStoreItem(saved, { name: 'Home Wi-Fi', description: '', tags: [] });
    expect((await updated.promise).name).toBe('Home Wi-Fi');
    expect(edited.metaRev).toBe(1n);

    // b still holds revision 0: its edit must not overwrite a's.
    const stale = await b.editStoreItem(listing.items[0], { name: 'Mine', description: '', tags: [] }).catch((e: unknown) => e);
    expect(stale).toBeInstanceOf(ProtocolError);
    expect((stale as ProtocolError).code).toBe(ErrorCode.ERROR_CODE_REVISION_CONFLICT);

    await a.deleteStoreItem(saved.id);
    expect(await deleted.promise).toBe(saved.id);
    expect((await b.listStore()).items).toEqual([]);
  });

  test('an item larger than one frame is uploaded in chunks and read back', async () => {
    const relay = relayWith();
    const [a, b] = await group(relay);
    const body = filled(11 * MiB, 3);
    await a.saveToStore({ contentType: 'application/octet-stream', filename: 'big.bin', body }, { name: 'Big', description: '', tags: [] });
    expect(relay.uploadCount).toBe(0);

    const [item] = (await b.listStore()).items;
    const got = await b.fetchStoreItem(item);
    expect(Buffer.from(got.body).equals(Buffer.from(body))).toBe(true);
  }, 30_000);

  test('items saved before a rekey still open through the store links', async () => {
    const relay = relayWith();
    const [a, b] = await group(relay);
    const c = await open(relay, 'tablet');
    await pair(a, c);
    await a.saveToStore({ contentType: 'text/plain', body: utf8('kept') }, { name: 'Kept', description: '', tags: [] });

    await (await a.prepareRevoke(c.deviceId)).confirm();
    expect(relay.epoch).toBe(2n);

    const listing = await a.listStore();
    expect(listing.unreadable).toEqual([]);
    expect(listing.items[0].wrapEpoch).toBe(1n);
    expect((await a.fetchStoreItem(listing.items[0])).body).toEqual(utf8('kept'));

    // b, which was online for the rekey, opens it the same way.
    expect((await b.listStore()).items.map((it) => it.name)).toEqual(['Kept']);
  });

  test('a full store refuses before anything is uploaded', async () => {
    const relay = relayWith({ store: { maxBytes: 1024 * 1024, maxItems: 1, maxItemBytes: 1024 * 1024 } });
    const [a] = await group(relay);
    await a.saveToStore({ contentType: 'text/plain', body: utf8('one') }, { name: 'One', description: '', tags: [] });
    const err = await a
      .saveToStore({ contentType: 'text/plain', body: utf8('two') }, { name: 'Two', description: '', tags: [] })
      .catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ProtocolError);
    expect((err as ProtocolError).code).toBe(ErrorCode.ERROR_CODE_QUOTA_EXCEEDED);
    expect(relay.itemCount).toBe(1);
  });

  test('a relay without a store says so', async () => {
    const relay = relayWith({ store: null });
    const [a] = await group(relay);
    expect((await a.storeLimits()).enabled).toBe(false);
  });
});

describe('code lifetimes', () => {
  test('a pairing code is refused once it has expired', async () => {
    let clock = 1_000_000;
    const relay = relayWith({ now: () => clock });
    const a = await open(relay, 'laptop');
    await a.createGroup(relay.creationURL);
    const invitation = await a.startPairing();
    expect(invitation.expiresAt.getTime()).toBe(clock + 300_000);

    clock += 300_000;
    const late = await open(relay, 'phone');
    const err = await late.joinPairing(invitation.payload).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ProtocolError);
    expect((err as ProtocolError).code).toBe(ErrorCode.ERROR_CODE_TOKEN_EXPIRED);
    expect(relay.deviceIds).toHaveLength(1);
  });

  test('an offer is refused once it has expired', async () => {
    let clock = 1_000_000;
    const relay = relayWith({ now: () => clock });
    const member = await open(relay, 'laptop');
    await member.createGroup(relay.creationURL);
    const joiner = await open(relay, 'phone');
    const offer = await joiner.startOffer(relay.baseUrl);
    offer.accepted.catch(() => undefined);
    expect(offer.expiresAt.getTime()).toBe(clock + 300_000);

    clock += 300_000;
    const acceptance = await member.prepareAcceptOffer(offer.code);
    const err = await acceptance.confirm().catch((e: unknown) => e);
    offer.cancel();
    expect(err).toBeInstanceOf(ProtocolError);
    expect((err as ProtocolError).code).toBe(ErrorCode.ERROR_CODE_TOKEN_EXPIRED);
    expect(relay.deviceIds).toHaveLength(1);
  });
});

describe('addresses', () => {
  test.each([
    ['another host', 'https://elsewhere.test/creation-token'],
    ['the right host over plain http', 'http://relay.test/creation-token'],
  ])('a socket to %s is refused', async (_name, url) => {
    const relay = relayWith();
    const client = await open(relay, 'phone');
    await expect(client.createGroup(url)).rejects.toThrow();
    expect(relay.groupId).toBe('');
  });

  test('nothing answers after close', async () => {
    const relay = relayWith();
    const client = await open(relay, 'phone');
    relay.close();
    await expect(client.createGroup(relay.creationURL)).rejects.toThrow();
  });
});
