/**
 * The client's rules, driven against the in-process relay in `src/core/demo/relay.ts`:
 * the flows of SPEC §3 and §6, the epoch handling of spec/crypto.md §7, and
 * the two places this client is required to refuse.
 */

import { xchacha20poly1305 } from '@noble/ciphers/chacha.js';
import { sha256 } from '@noble/hashes/sha2.js';

import { concat, fromUTF8, toHex, u8, utf8 } from '../../bytes';
import {
  HEADER_CONTAINER_SIZE,
  HEADER_V2_CONTAINER_SIZE,
  generateDeviceKey,
  generateGroupKey,
  openStoreLink,
  sealStoreLink,
  storeKey,
  wrap,
} from '../../crypto';
import { Client, type ExternalBody, type Handlers } from '../client';
import type { SocketFactory } from '../connection';
import { Envelope, MessageType } from '../../../protocol/gen/tpp/v1/envelope';
import { ErrorCode } from '../../../protocol/gen/tpp/v1/error';
import { TppError, describe as describeError } from '../errors';
import { classifyCode, decodePairingPayload, fingerprint } from '../pairing';
import { FakeRelay, MemoryStore } from '../../demo';

/**
 * Every client a test opens is disconnected afterwards. A connected client
 * holds a reconnect timer, and a timer that outlives its test is how a suite
 * starts hanging on a machine that is not the one it was written on.
 */
const opened: Client[] = [];

afterEach(() => {
  while (opened.length > 0) {
    opened.pop()?.disconnect();
  }
});

async function open(relay: FakeRelay, store: MemoryStore, name: string, handlers?: Handlers): Promise<Client> {
  const client = await Client.open({
    store,
    deviceName: name,
    socketFactory: relay.factory,
    healthCheck: async () => false,
    deviceStatus: relay.deviceStatus,
    handlers,
  });
  opened.push(client);
  return client;
}

/**
 * pair runs SPEC §3.2 end to end between two clients of one relay, with the
 * inviter's user confirming the fingerprint the joiner shows.
 */
async function pair(inviter: Client, joiner: Client): Promise<void> {
  const invitation = await inviter.startPairing();
  const joining = joiner.joinPairing(invitation.payload);
  const request = await invitation.request;
  expect(request.fingerprint).toBe(fingerprint(joiner.publicKey));
  await request.confirm();
  await joining;
  await invitation.joined;
}

describe('group creation (SPEC §3.1)', () => {
  test('the creating device holds epoch 1 and a group key the relay never saw', async () => {
    const relay = new FakeRelay();
    const store = new MemoryStore();
    const client = await open(relay, store, 'phone');

    expect(client.inGroup).toBe(false);
    await client.createGroup(relay.creationURL);

    expect(client.inGroup).toBe(true);
    expect(client.epoch).toBe(1n);
    expect(client.connected).toBe(true);
    // The state is written through on every step, so a restart mid-flow does
    // not lose the identity the relay already knows about.
    expect(store.peek('session')).toContain(client.deviceId);
  });

  test('a consumed creation token is a relay error, not a crash', async () => {
    const relay = new FakeRelay();
    const first = await open(relay, new MemoryStore(), 'phone');
    await first.createGroup(relay.creationURL);

    const second = await open(relay, new MemoryStore(), 'laptop');
    await expect(second.createGroup(relay.creationURL)).rejects.toThrow(/one token, one group/);
    expect(second.inGroup).toBe(false);
  });
});

describe('pairing (SPEC §3.2)', () => {
  test('the joiner ends at the inviter’s epoch, having decrypted nothing on the way', async () => {
    const relay = new FakeRelay();
    const inviter = await open(relay, new MemoryStore(), 'laptop');
    await inviter.createGroup(relay.creationURL);
    const joiner = await open(relay, new MemoryStore(), 'phone');

    await pair(inviter, joiner);

    expect(joiner.inGroup).toBe(true);
    expect(joiner.epoch).toBe(inviter.epoch);
    expect(joiner.snapshot().groupKey).toEqual(inviter.snapshot().groupKey);
  });

  test('the payload is one string in both directions', async () => {
    const relay = new FakeRelay();
    const inviter = await open(relay, new MemoryStore(), 'laptop');
    await inviter.createGroup(relay.creationURL);

    const invitation = await inviter.startPairing();
    const decoded = decodePairingPayload(invitation.payload);
    expect(decoded.pairingToken).toBe(invitation.token);
    expect(decoded.serverUrl).toBe(inviter.serverUrl);
    expect(decoded.inviterEphemeralPublicKey).toHaveLength(32);

    // What a QR scanner hands back, and what a paste can carry: the same
    // string with whitespace and padding around it.
    const joiner = await open(relay, new MemoryStore(), 'phone');
    const joining = joiner.joinPairing(`  ${invitation.payload}==\n`);
    await (await invitation.request).confirm();
    await joining;
    expect(joiner.inGroup).toBe(true);
  });

  test('a new device starts empty and does not go looking for what it cannot read', async () => {
    const relay = new FakeRelay();
    const inviter = await open(relay, new MemoryStore(), 'laptop');
    await inviter.createGroup(relay.creationURL);
    await inviter.putEntry({ contentType: 'text/plain; charset=utf-8', body: utf8('before') });

    const joiner = await open(relay, new MemoryStore(), 'phone');
    await pair(inviter, joiner);

    // Pairing pulled no history: the count is still whatever the tests asked
    // for explicitly, which is none.
    expect(relay.historyRequests).toBe(0);
  });
});

describe('joiner-emitted pairing (docs/internal/plans/joiner-emitted-pairing.md)', () => {
  test('the device with no group key shows the code, and ends at the member’s epoch', async () => {
    const relay = new FakeRelay();
    const member = await open(relay, new MemoryStore(), 'laptop');
    await member.createGroup(relay.creationURL);
    const joiner = await open(relay, new MemoryStore(), 'phone');

    // The relay URL is the one thing the user supplies in this direction: a
    // device with no group has no relay URL either.
    const offer = await joiner.startOffer('https://relay.test');
    expect(classifyCode(offer.code)).toBe('offer-code');

    const prepared = await member.prepareAcceptOffer(offer.code);
    expect(prepared.deviceName).toBe('phone');
    // The fingerprint the member would show is the one the offering device
    // would show for its own key. That comparison is the user's protection.
    expect(prepared.fingerprint).toBe(fingerprint(joiner.publicKey));

    const admitted = await prepared.confirm();
    await offer.accepted;

    expect(joiner.inGroup).toBe(true);
    expect(joiner.epoch).toBe(member.epoch);
    expect(joiner.snapshot().groupKey).toEqual(member.snapshot().groupKey);
    expect(admitted.id).toBe(joiner.deviceId);
  });

  test('preparing changes nothing: only confirm admits a device', async () => {
    const relay = new FakeRelay();
    const member = await open(relay, new MemoryStore(), 'laptop');
    await member.createGroup(relay.creationURL);
    const joiner = await open(relay, new MemoryStore(), 'phone');
    const before = relay.deviceIds.length;

    const offer = await joiner.startOffer('https://relay.test');
    const prepared = await member.prepareAcceptOffer(offer.code);

    // This is the gate of §5, asserted in the client rather than in a screen:
    // the user can still decide against it after reading the dialog, and until
    // they say yes the group is untouched.
    expect(relay.deviceIds).toHaveLength(before);
    expect(joiner.inGroup).toBe(false);

    await prepared.confirm();
    await offer.accepted;
    expect(relay.deviceIds).toHaveLength(before + 1);

    // One code, one device: a double-tapped dialog admits one.
    await expect(prepared.confirm()).rejects.toThrow();
    expect(relay.deviceIds).toHaveLength(before + 1);

    offer.cancel();
  });

  test('a second member holding the same code is refused', async () => {
    const relay = new FakeRelay();
    const laptop = await open(relay, new MemoryStore(), 'laptop');
    await laptop.createGroup(relay.creationURL);
    const desktop = await open(relay, new MemoryStore(), 'desktop');
    await pair(laptop, desktop);
    const joiner = await open(relay, new MemoryStore(), 'phone');

    const offer = await joiner.startOffer('https://relay.test');
    const first = await laptop.prepareAcceptOffer(offer.code);
    const second = await desktop.prepareAcceptOffer(offer.code);

    await first.confirm();
    await offer.accepted;
    await expect(second.confirm()).rejects.toThrow();
  });

  test('the two kinds of code are told apart, and neither is mistaken for the other', async () => {
    const relay = new FakeRelay();
    const member = await open(relay, new MemoryStore(), 'laptop');
    await member.createGroup(relay.creationURL);
    const joiner = await open(relay, new MemoryStore(), 'phone');

    const invitation = await member.startPairing();
    const offer = await joiner.startOffer('https://relay.test');

    expect(classifyCode(invitation.payload)).toBe('pairing-code');
    expect(classifyCode(offer.code)).toBe('offer-code');
    expect(classifyCode(relay.creationURL)).toBe('creation-url');
    expect(classifyCode('not a code at all')).toBe('unknown');

    // The whole reason PairingCode exists: without a discriminator, protobuf
    // would decode an offer as an invitation with a nonsense token rather than
    // refusing it.
    await expect(member.prepareAcceptOffer(invitation.payload)).rejects.toThrow();
    expect(() => decodePairingPayload(offer.code)).toThrow();

    // And what a clipboard adds is still tolerated.
    const prepared = await member.prepareAcceptOffer(`  ${offer.code}==\n`);
    expect(prepared.deviceName).toBe('phone');

    offer.cancel();
  });

  test('a device that already holds a group key cannot offer itself elsewhere', async () => {
    const relay = new FakeRelay();
    const member = await open(relay, new MemoryStore(), 'laptop');
    await member.createGroup(relay.creationURL);

    await expect(member.startOffer('https://relay.test')).rejects.toThrow(TppError);
  });
});

describe('entries (SPEC §6)', () => {
  test('a round trip between two devices carries the content type and the filename in the ciphertext', async () => {
    const relay = new FakeRelay();
    const laptop = await open(relay, new MemoryStore(), 'laptop');
    await laptop.createGroup(relay.creationURL);
    const phone = await open(relay, new MemoryStore(), 'phone');
    await pair(laptop, phone);

    await laptop.putEntry({
      contentType: 'application/pdf',
      filename: 'notes.pdf',
      body: utf8('%PDF-1.7 pretend'),
    });
    const item = await phone.getLatest();

    expect(item.contentType).toBe('application/pdf');
    expect(item.filename).toBe('notes.pdf');
    expect(fromUTF8(item.body)).toBe('%PDF-1.7 pretend');
  });

  test('a relay that predates chunked transfer gets its old limits, and nothing above one frame is sent to it', async () => {
    const relay = new FakeRelay();
    const laptop = await open(relay, new MemoryStore(), 'laptop');
    await laptop.createGroup(relay.creationURL);

    // The fake relay answers EntryLimitsRequest as an old relay does: it
    // does not know the message.
    const limits = await laptop.limits();
    expect(limits.chunkBytes).toBe(0);
    expect(limits.maxEntryBytes).toBe(10 << 20);

    const err: unknown = await laptop
      .putEntry({ contentType: 'application/octet-stream', body: new Uint8Array((10 << 20) + 1) })
      .catch((e: unknown) => e);
    expect(err).toBeInstanceOf(TppError);
    expect((err as TppError).kind).toBe('too-large');

    // Entries that fit one frame are unaffected.
    await laptop.putEntry({ contentType: 'text/plain', body: utf8('small') });
    expect(fromUTF8((await laptop.getLatest()).body)).toBe('small');
  });

  test('an entry the relay filed under another id is refused, not decrypted', async () => {
    const relay = new FakeRelay();
    const laptop = await open(relay, new MemoryStore(), 'laptop');
    await laptop.createGroup(relay.creationURL);

    relay.rewriteEntryId = 'not-the-bound-id';
    await expect(
      laptop.putEntry({ contentType: 'text/plain; charset=utf-8', body: utf8('hello') }),
    ).rejects.toThrow(/different id than the one bound/);
  });

  test('history is fetched only when it is asked for', async () => {
    const relay = new FakeRelay();
    const laptop = await open(relay, new MemoryStore(), 'laptop');
    await laptop.createGroup(relay.creationURL);
    await laptop.putEntry({ contentType: 'text/plain; charset=utf-8', body: utf8('one') });
    await laptop.getLatest();
    expect(relay.historyRequests).toBe(0);

    const page = await laptop.getHistory({ limit: 10 });
    expect(relay.historyRequests).toBe(1);
    expect(page.entries).toHaveLength(1);

    const item = await laptop.getEntry(page.entries[0].id);
    expect(fromUTF8(item.body)).toBe('one');
  });

  test('each entry carries a sealed header the history reads without a fetch', async () => {
    const relay = new FakeRelay();
    const laptop = await open(relay, new MemoryStore(), 'laptop');
    await laptop.createGroup(relay.creationURL);
    const put = await laptop.putEntry({
      contentType: 'image/png',
      filename: 'shot.png',
      body: new Uint8Array(1234),
    });
    expect(put.header).toEqual({ contentType: 'image/png', filename: 'shot.png', size: 1234 });

    const page = await laptop.getHistory({ limit: 10 });
    expect(page.entries[0].header).toEqual({
      contentType: 'image/png',
      filename: 'shot.png',
      size: 1234,
    });
  });

  // spec/crypto.md §5.5.1: a version 2 header only to a relay that says it
  // takes one, and a version 1 header to any other. The filename is 300 bytes:
  // a v1 header keeps what fits beside the content type, a v2 one 255.
  const limitsWith = (headerV2: boolean) => ({
    maxEntryBytes: '10485760',
    singleFrameMaxBytes: '10485760',
    chunkBytes: '0',
    inlineMaxBytes: '0',
    headerV2,
  });
  test.each([
    ['a relay that predates the limits request', null, HEADER_CONTAINER_SIZE, 228],
    ['a relay without version 2 headers', limitsWith(false), HEADER_CONTAINER_SIZE, 228],
    ['a relay with version 2 headers', limitsWith(true), HEADER_V2_CONTAINER_SIZE, 255],
  ])('the header written to %s', async (_name, limits, size, kept) => {
    const relay = new FakeRelay();
    relay.limits = limits;
    const laptop = await open(relay, new MemoryStore(), 'laptop');
    await laptop.createGroup(relay.creationURL);
    const filename = 'f'.repeat(296) + '.pdf';
    await laptop.putEntry({ contentType: 'application/pdf', filename, body: utf8('%PDF-') });

    expect(relay.lastHeader()).toHaveLength(size);
    const page = await laptop.getHistory({ limit: 10 });
    expect(page.entries[0].header?.filename).toBe(filename.slice(0, kept));
  });
});

describe('a body hash the caller already has', () => {
  const v2 = {
    maxEntryBytes: '10485760',
    singleFrameMaxBytes: '10485760',
    chunkBytes: '0',
    inlineMaxBytes: '0',
    headerV2: true,
  };

  test('goes into the header as given, instead of being worked out again', async () => {
    const relay = new FakeRelay();
    relay.limits = v2;
    const laptop = await open(relay, new MemoryStore(), 'laptop');
    await laptop.createGroup(relay.creationURL);
    const given = new Uint8Array(32).fill(0xab);
    await laptop.putEntry({ contentType: 'application/pdf', filename: 'a.pdf', body: utf8('%PDF-'), sha256: given });
    const page = await laptop.getHistory({ limit: 10 });
    expect(page.entries[0].header?.sha256Hex).toBe('ab'.repeat(32));
  });

  test('is worked out when none is given', async () => {
    const relay = new FakeRelay();
    relay.limits = v2;
    const laptop = await open(relay, new MemoryStore(), 'laptop');
    await laptop.createGroup(relay.creationURL);
    const body = utf8('%PDF-');
    await laptop.putEntry({ contentType: 'application/pdf', filename: 'a.pdf', body });
    const page = await laptop.getHistory({ limit: 10 });
    expect(page.entries[0].header?.sha256Hex).toBe(toHex(sha256(body)));
  });

  test('of the wrong length is refused before anything is sent', async () => {
    const relay = new FakeRelay();
    relay.limits = v2;
    const laptop = await open(relay, new MemoryStore(), 'laptop');
    await laptop.createGroup(relay.creationURL);
    await expect(
      laptop.putEntry({ contentType: 'text/plain', body: utf8('x'), sha256: new Uint8Array(16) }),
    ).rejects.toThrow('a body hash is 32 bytes, not 16');
    expect((await laptop.getHistory({ limit: 10 })).entries).toHaveLength(0);
  });
});

describe('a body sealed outside JavaScript (ExternalBody)', () => {
  /** external seals as the platform should: the head, then the frame head and body under the AEAD. */
  const external = (body: Uint8Array, tamper?: (c: Uint8Array) => Uint8Array): ExternalBody => ({
    size: body.length,
    sha256: sha256(body),
    seal: async ({ key, nonce, aad, head, frameHead }) => {
      const sealed = xchacha20poly1305(key, nonce, aad).encrypt(concat(frameHead, body));
      const container = concat(head, sealed);
      return tamper ? tamper(container) : container;
    },
  });

  test('writes an entry another device opens to the same body', async () => {
    const relay = new FakeRelay();
    const laptop = await open(relay, new MemoryStore(), 'laptop');
    await laptop.createGroup(relay.creationURL);
    const phone = await open(relay, new MemoryStore(), 'phone');
    await pair(laptop, phone);
    const body = utf8('a file the platform staged');
    await phone.putEntry({ contentType: 'text/plain', filename: 'f.txt', external: external(body) });
    const got = await laptop.getLatest();
    expect(fromUTF8(got.body)).toBe('a file the platform staged');
    expect(got.filename).toBe('f.txt');
  });

  test('saves a store item another device opens to the same body', async () => {
    const relay = new FakeRelay({ store: { maxBytes: 1 << 20, maxItems: 10, maxItemBytes: 1 << 20 } });
    const laptop = await open(relay, new MemoryStore(), 'laptop');
    await laptop.createGroup(relay.creationURL);
    const body = utf8('kept');
    const saved = await laptop.saveToStore(
      { contentType: 'text/plain', filename: 'k.txt', external: external(body) },
      { name: 'K', description: '', tags: [] },
    );
    expect(fromUTF8((await laptop.fetchStoreItem(saved)).body)).toBe('kept');
  });

  test.each([
    ['a byte short', (c: Uint8Array) => c.subarray(0, c.length - 1)],
    ['under another head', (c: Uint8Array) => concat(u8(c[0] ^ 1), c.subarray(1))],
  ])('refuses a container %s, before anything is sent', async (_name, tamper) => {
    const relay = new FakeRelay();
    const laptop = await open(relay, new MemoryStore(), 'laptop');
    await laptop.createGroup(relay.creationURL);
    await expect(
      laptop.putEntry({ contentType: 'text/plain', external: external(utf8('x'), tamper) }),
    ).rejects.toThrow('the sealed body is');
    expect((await laptop.getHistory({ limit: 10 })).entries).toHaveLength(0);
  });
});

describe('a read whose socket is lost under it', () => {
  /**
   * dropOn kills the socket, as a socket error would, when a request of
   * `type` is sent on it — the first `left.n` times once armed, across reconnects.
   */
  const dropOn = (inner: SocketFactory, type: MessageType, left: { n: number }): SocketFactory => {
    return (url) => {
      const socket = inner(url);
      let onError: (reason: string) => void = () => {};
      return {
        ...socket,
        onError: (handler) => {
          onError = handler;
          socket.onError(handler);
        },
        send: (frame) => {
          if (left.n > 0 && Envelope.decode(frame).type === type) {
            left.n--;
            socket.close();
            queueMicrotask(() => onError('socket error'));
            return;
          }
          socket.send(frame);
        },
      };
    };
  };

  async function setUp(type: MessageType, times = 1): Promise<Client> {
    const relay = new FakeRelay();
    const left = { n: 0 };
    const client = await Client.open({
      store: new MemoryStore(),
      deviceName: 'phone',
      socketFactory: dropOn(relay.factory, type, left),
      healthCheck: async () => false,
      deviceStatus: relay.deviceStatus,
    });
    opened.push(client);
    await client.createGroup(relay.creationURL);
    await client.putEntry({ contentType: 'text/plain', body: utf8('kept') });
    left.n = times;
    return client;
  }

  test('is asked again on a fresh socket, once, and answers', async () => {
    const client = await setUp(MessageType.MESSAGE_TYPE_ENTRY_HISTORY_REQUEST);
    const page = await client.getHistory({ limit: 10 });
    expect(page.entries).toHaveLength(1);
  });

  test('the latest entry, too', async () => {
    const client = await setUp(MessageType.MESSAGE_TYPE_ENTRY_LATEST_REQUEST);
    expect(fromUTF8((await client.getLatest()).body)).toBe('kept');
  });

  test('fails as before when the fresh socket is lost too', async () => {
    const client = await setUp(MessageType.MESSAGE_TYPE_ENTRY_HISTORY_REQUEST, 2);
    await expect(client.getHistory({ limit: 10 })).rejects.toMatchObject({ kind: 'disconnected' });
  });

  test('a write is not asked again: it may have been carried out', async () => {
    const client = await setUp(MessageType.MESSAGE_TYPE_ENTRY_PUT_REQUEST);
    await expect(client.putEntry({ contentType: 'text/plain', body: utf8('again') })).rejects.toMatchObject({
      kind: 'disconnected',
    });
    // Only the first entry made it.
    expect((await client.getHistory({ limit: 10 })).entries).toHaveLength(1);
  });
});

describe('revocation and rekey (SPEC §3.3, spec/crypto.md §7)', () => {
  test('a rekey sends the store link that leads back to the old epoch (spec/crypto.md §7a.2)', async () => {
    const relay = new FakeRelay();
    const laptop = await open(relay, new MemoryStore(), 'laptop');
    await laptop.createGroup(relay.creationURL);
    const phone = await open(relay, new MemoryStore(), 'phone');
    await pair(laptop, phone);
    const before = laptop.snapshot().groupKey;

    await (await laptop.prepareRevoke(phone.deviceId)).confirm();
    const after = laptop.snapshot().groupKey;
    const link = relay.storeLinks.get(2n);
    expect(link).toHaveLength(49);
    expect(openStoreLink(storeKey(after, 2n), link!, 2n)).toEqual(storeKey(before, 1n));
  });

  test('a device behind a rekey does not seal a store link from its old key', async () => {
    const relay = new FakeRelay();
    const laptop = await open(relay, new MemoryStore(), 'laptop');
    await laptop.createGroup(relay.creationURL);
    const phone = await open(relay, new MemoryStore(), 'phone');
    await pair(laptop, phone);

    // Another device re-keyed, and the laptop has not picked its key up yet.
    relay.epoch = 2n;
    const plan = await laptop.prepareRevoke(phone.deviceId);
    await expect(plan.confirm()).rejects.toMatchObject({ kind: 'epoch-ahead' });
    expect(relay.deviceIds).toHaveLength(2);
    expect(relay.storeLinks.size).toBe(0);
  });

  test('nothing is revoked until the roster the user was shown is confirmed', async () => {
    const relay = new FakeRelay();
    const laptop = await open(relay, new MemoryStore(), 'laptop');
    await laptop.createGroup(relay.creationURL);
    const phone = await open(relay, new MemoryStore(), 'phone');
    await pair(laptop, phone);

    const plan = await laptop.prepareRevoke(phone.deviceId);
    expect(plan.target.name).toBe('phone');
    expect(plan.roster.devices.map((d) => d.name).sort()).toEqual(['laptop', 'phone']);
    expect(plan.remaining.map((d) => d.name)).toEqual(['laptop']);
    // Preparing changed nothing: the group is still at its old epoch with both
    // devices in it.
    expect(relay.epoch).toBe(1n);
    expect(relay.deviceIds).toHaveLength(2);

    const remaining = await plan.confirm();
    expect(remaining.epoch).toBe(2n);
    expect(laptop.epoch).toBe(2n);
    expect(relay.deviceIds).toHaveLength(1);

    await expect(plan.confirm()).rejects.toThrow(/already been carried out/);
  });

  test('a device that was away picks up its wrapped key on the next connect', async () => {
    const relay = new FakeRelay();
    const laptop = await open(relay, new MemoryStore(), 'laptop');
    await laptop.createGroup(relay.creationURL);
    const phone = await open(relay, new MemoryStore(), 'phone');
    const tablet = await open(relay, new MemoryStore(), 'tablet');
    await pair(laptop, phone);
    await pair(laptop, tablet);

    // The tablet is in the background — Android holds no socket there (§5.2).
    tablet.disconnect();

    const plan = await laptop.prepareRevoke(phone.deviceId);
    await plan.confirm();
    expect(tablet.epoch).toBe(1n);

    await tablet.connect();
    await settle();
    expect(tablet.epoch).toBe(2n);
    expect(tablet.snapshot().groupKey).toEqual(laptop.snapshot().groupKey);

    // And it can read what was written after the rekey, which is the point.
    await laptop.putEntry({ contentType: 'text/plain; charset=utf-8', body: utf8('after') });
    expect(fromUTF8((await tablet.getLatest()).body)).toBe('after');
  });

  test('an entry from an older epoch is skipped, not decrypted with a key that was kept', async () => {
    const relay = new FakeRelay();
    const laptop = await open(relay, new MemoryStore(), 'laptop');
    await laptop.createGroup(relay.creationURL);
    const phone = await open(relay, new MemoryStore(), 'phone');
    await pair(laptop, phone);

    await laptop.putEntry({ contentType: 'text/plain; charset=utf-8', body: utf8('old epoch') });
    const plan = await laptop.prepareRevoke(phone.deviceId);
    await plan.confirm();

    // The entry predates the rekey. The old key is gone by construction: the
    // client holds exactly one (epoch, group key) pair.
    await expect(laptop.getLatest()).rejects.toMatchObject({ kind: 'stale-entry' });
  });

  test('a revoked device cannot reconnect', async () => {
    const relay = new FakeRelay();
    const laptop = await open(relay, new MemoryStore(), 'laptop');
    await laptop.createGroup(relay.creationURL);
    const phone = await open(relay, new MemoryStore(), 'phone');
    await pair(laptop, phone);

    const plan = await laptop.prepareRevoke(phone.deviceId);
    await plan.confirm();

    await expect(phone.connect()).rejects.toThrow();
  });
});

describe('what the server admin does to the group', () => {
  /** until waits for a condition the client reaches on its own, over microtasks. */
  async function until(cond: () => boolean): Promise<void> {
    for (let i = 0; i < 200 && !cond(); i++) {
      await new Promise((resolve) => setTimeout(resolve, 1));
    }
    expect(cond()).toBe(true);
  }

  test('the client learns the group name on connect and hears a rename', async () => {
    const relay = new FakeRelay();
    const store = new MemoryStore();
    const names: string[] = [];
    const laptop = await Client.open({
      store,
      deviceName: 'laptop',
      socketFactory: relay.factory,
      healthCheck: async () => false,
      deviceStatus: relay.deviceStatus,
      handlers: { onGroupName: (n) => names.push(n) },
    });
    opened.push(laptop);
    await laptop.createGroup(relay.creationURL);

    await until(() => laptop.groupName === 'Family');
    relay.rename('Friends');
    await until(() => laptop.groupName === 'Friends');
    expect(names).toEqual(['Family', 'Friends']);
    // Kept with the state, for a screen to show before it connects.
    expect(store.peek('session')).toContain('"group_name":"Friends"');
  });

  test('the client learns the operator website and links to it with its group id', async () => {
    const relay = new FakeRelay({ operatorUrl: 'https://tpp.example.com/help?lang=en' });
    const urls: string[] = [];
    const laptop = await Client.open({
      store: new MemoryStore(),
      deviceName: 'laptop',
      socketFactory: relay.factory,
      healthCheck: async () => false,
      deviceStatus: relay.deviceStatus,
      handlers: { onOperatorUrl: (u) => urls.push(u) },
    });
    opened.push(laptop);
    expect(laptop.operatorLink).toBeNull();
    await laptop.createGroup(relay.creationURL);

    await until(() => laptop.operatorUrl !== '');
    expect(urls).toEqual(['https://tpp.example.com/help?lang=en']);
    expect(laptop.operatorLink).toBe('https://tpp.example.com/help?lang=en&group_id=group-1');

    // Forgetting the group forgets its relay's website with it.
    await laptop.forget();
    expect(laptop.operatorUrl).toBe('');
    expect(laptop.operatorLink).toBeNull();
  });

  test('a website the app may not open is taken as none', async () => {
    const relay = new FakeRelay({ operatorUrl: 'javascript:alert(1)' });
    const laptop = await Client.open({
      store: new MemoryStore(),
      deviceName: 'laptop',
      socketFactory: relay.factory,
      healthCheck: async () => false,
      deviceStatus: relay.deviceStatus,
    });
    opened.push(laptop);
    await laptop.createGroup(relay.creationURL);
    await until(() => laptop.groupName === 'Family');
    expect(laptop.operatorUrl).toBe('');
    expect(laptop.operatorLink).toBeNull();
  });

  test('a deleted group closes the socket and stops the client for good', async () => {
    const relay = new FakeRelay();
    let deleted = 0;
    const laptop = await Client.open({
      store: new MemoryStore(),
      deviceName: 'laptop',
      socketFactory: relay.factory,
      healthCheck: async () => false,
      deviceStatus: relay.deviceStatus,
      handlers: { onGroupDeleted: () => deleted++ },
    });
    opened.push(laptop);
    await laptop.createGroup(relay.creationURL);

    relay.deleteGroup();
    await until(() => laptop.groupDeleted);
    expect(deleted).toBe(1);
    await expect(laptop.getLatest()).rejects.toMatchObject({ kind: 'group-deleted' });

    // Leaving the group is the way out, and it clears the flag.
    await laptop.forget();
    expect(laptop.groupDeleted).toBe(false);
  });

  test('a device that was away learns of the deletion from the relay’s 410', async () => {
    const relay = new FakeRelay();
    const store = new MemoryStore();
    const laptop = await open(relay, store, 'laptop');
    await laptop.createGroup(relay.creationURL);
    laptop.disconnect();
    relay.deleteGroup();

    let deleted = false;
    const later = await Client.open({
      store,
      deviceName: 'laptop',
      socketFactory: relay.factory,
      healthCheck: async () => false,
      deviceStatus: relay.deviceStatus,
      handlers: { onGroupDeleted: () => (deleted = true) },
    });
    opened.push(later);
    await expect(later.connect()).rejects.toThrow();
    expect(deleted).toBe(true);
    expect(later.groupDeleted).toBe(true);
  });
});

describe('leaving the group (the only way back to setup)', () => {
  test('forget clears the group and the key, and issues a new identity', async () => {
    const relay = new FakeRelay();
    const store = new MemoryStore();
    const laptop = await open(relay, store, 'laptop');
    await laptop.createGroup(relay.creationURL);

    const before = laptop.snapshot();
    expect(laptop.inGroup).toBe(true);

    await laptop.forget();

    expect(laptop.inGroup).toBe(false);
    expect(laptop.connected).toBe(false);
    expect(laptop.epoch).toBe(0n);
    expect(laptop.deviceId).toBe('');
    expect(laptop.serverUrl).toBe('');
    // The group key is gone, so nothing on this device can read the group's
    // entries any more.
    expect(laptop.snapshot().groupKey).toHaveLength(0);
    // And this is a new device as far as any future group is concerned.
    expect(laptop.snapshot().devicePrivateKey).not.toEqual(before.devicePrivateKey);

    // Persisted, not just in memory: a restart must not resurrect the group.
    const persisted = store.peek('session') ?? '';
    expect(persisted).not.toContain(before.deviceId);
    const restarted = await open(relay, store, 'laptop');
    expect(restarted.inGroup).toBe(false);
  });

  test('a forgotten device can pair into a group again', async () => {
    const relay = new FakeRelay();
    const laptop = await open(relay, new MemoryStore(), 'laptop');
    await laptop.createGroup(relay.creationURL);
    const phone = await open(relay, new MemoryStore(), 'phone');
    await pair(laptop, phone);

    await phone.forget();
    expect(phone.inGroup).toBe(false);

    await pair(laptop, phone);
    expect(phone.inGroup).toBe(true);
    expect(phone.snapshot().groupKey).toEqual(laptop.snapshot().groupKey);
  });
});

describe('what the client refuses locally', () => {
  test('every flow needs a group first', async () => {
    const client = await open(new FakeRelay(), new MemoryStore(), 'phone');
    for (const call of [
      () => client.connect(),
      () => client.devices(),
      () => client.getLatest(),
      () => client.getHistory(),
      () => client.putEntry({ contentType: 'text/plain', body: new Uint8Array(0) }),
    ]) {
      await expect(call()).rejects.toMatchObject({ kind: 'no-group' });
    }
  });

  test('a device may not revoke itself', async () => {
    const relay = new FakeRelay();
    const laptop = await open(relay, new MemoryStore(), 'laptop');
    await laptop.createGroup(relay.creationURL);
    await expect(laptop.prepareRevoke(laptop.deviceId)).rejects.toThrow(/may not revoke itself/);
  });

  test('an entry needs a content type', async () => {
    const relay = new FakeRelay();
    const laptop = await open(relay, new MemoryStore(), 'laptop');
    await laptop.createGroup(relay.creationURL);
    await expect(laptop.putEntry({ contentType: '', body: utf8('x') })).rejects.toThrow(
      /needs a content type/,
    );
  });

  test('a pairing code that is not one is rejected before anything is dialled', async () => {
    const client = await open(new FakeRelay(), new MemoryStore(), 'phone');
    await expect(client.joinPairing('this is not a pairing code')).rejects.toBeInstanceOf(
      TppError,
    );
    expect(describeError(new TppError('invalid', 'readable'))).toBe('readable');
  });
});

/** settle lets queued microtasks and the timers around them run. */
const settle = () => new Promise((resolve) => setTimeout(resolve, 10));

describe('a relay that lies (SPEC §3.2 step 4, spec/crypto.md §7.1)', () => {
  /** twoDevices is a laptop and a phone in one group, both online at epoch 1. */
  async function twoDevices(handlers?: Handlers): Promise<{ relay: FakeRelay; laptop: Client; phone: Client }> {
    const relay = new FakeRelay();
    const laptop = await open(relay, new MemoryStore(), 'laptop', handlers);
    await laptop.createGroup(relay.creationURL);
    const phone = await open(relay, new MemoryStore(), 'phone');
    await pair(laptop, phone);
    return { relay, laptop, phone };
  }

  /** offered resolves with what the client did with the next key it was offered. */
  function offered(relay: FakeRelay, store: MemoryStore, name: string) {
    let settleOutcome: (outcome: { installed?: bigint; refused?: TppError }) => void = () => {};
    const outcome = new Promise<{ installed?: bigint; refused?: TppError }>((resolve) => {
      settleOutcome = resolve;
    });
    const client = Client.open({
      store,
      deviceName: name,
      socketFactory: relay.factory,
      healthCheck: async () => false,
      deviceStatus: relay.deviceStatus,
      handlers: {
        onEpoch: (epoch) => settleOutcome({ installed: epoch }),
        onKeyRefused: (_epoch, err) => settleOutcome({ refused: err }),
      },
    });
    return { client, outcome };
  }

  test.each([
    ['links it sealed itself', null, true],
    ['no link into the new epoch', null, false],
    ['no links at all: a relay that predates the store', ErrorCode.ERROR_CODE_UNSUPPORTED_MESSAGE, false],
    ['no links while the store is off: a v1.2 relay', ErrorCode.ERROR_CODE_STORE_DISABLED, false],
  ])('a key the relay made is refused, with %s', async (_name, refuseLinks, forgeLink) => {
    const relay = new FakeRelay();
    const laptopStore = new MemoryStore();
    const laptop = await open(relay, laptopStore, 'laptop');
    await laptop.createGroup(relay.creationURL);
    const before = laptop.snapshot().groupKey;
    laptop.disconnect();

    const forged = generateGroupKey();
    if (forgeLink) {
      relay.storeLinks.set(2n, sealStoreLink(storeKey(forged, 2n), storeKey(generateGroupKey(), 1n), 2n));
    }
    relay.refuseLinks = refuseLinks;

    const { client, outcome } = offered(relay, laptopStore, 'laptop');
    const reopened = await client;
    opened.push(reopened);
    await reopened.connect();
    relay.pushWrappedKey(reopened.deviceId, 2n, wrap(forged, reopened.publicKey, 2n));

    const got = await outcome;
    expect(got.installed).toBeUndefined();
    expect(got.refused?.kind).toBe('unverified-key');
    expect(reopened.epoch).toBe(1n);
    expect(reopened.snapshot().groupKey).toEqual(before);
  });

  test('an honest rekey still goes through, across two epochs at once', async () => {
    const relay = new FakeRelay();
    const laptop = await open(relay, new MemoryStore(), 'laptop');
    await laptop.createGroup(relay.creationURL);
    const tabletStore = new MemoryStore();
    const tablet = await open(relay, tabletStore, 'tablet');
    await pair(laptop, tablet);
    for (const name of ['phone', 'watch']) {
      const other = await open(relay, new MemoryStore(), name);
      await pair(laptop, other);
    }
    tablet.disconnect();

    const roster = await laptop.devices();
    for (const name of ['phone', 'watch']) {
      const target = roster.devices.find((d) => d.name === name);
      await (await laptop.prepareRevoke(target?.id ?? '')).confirm();
    }
    expect(laptop.epoch).toBe(3n);

    const { client, outcome } = offered(relay, tabletStore, 'tablet');
    const reopened = await client;
    opened.push(reopened);
    await reopened.connect();
    expect(await outcome).toEqual({ installed: 3n });
    expect(reopened.snapshot().groupKey).toEqual(laptop.snapshot().groupKey);
  });

  test('a join nobody asked for becomes no request, and wraps nothing', async () => {
    const requests: string[] = [];
    let renamed: () => void = () => {};
    const settledRename = new Promise<void>((resolve) => {
      renamed = resolve;
    });
    const { relay, laptop } = await twoDevices({
      onJoinRequest: (r) => requests.push(r.deviceName),
      onGroupName: (name) => {
        if (name === 'settled') renamed();
      },
    });
    requests.length = 0; // the phone's own pairing above was asked for
    const uploadsBefore = relay.keyHandovers;

    relay.pushJoinNotice(laptop.deviceId, 'never-minted', 'intruder', generateDeviceKey());
    // Events run in order: once a rename pushed after the notice is seen, the
    // notice has been handled.
    relay.rename('settled');
    await settledRename;
    expect(requests).toEqual([]);
    expect(relay.keyHandovers).toBe(uploadsBefore);
  });

  test('one invitation admits one device, and a turned-down one gets nothing', async () => {
    const { relay, laptop } = await twoDevices();
    const invitation = await laptop.startPairing();
    const joinerKey = generateDeviceKey();

    relay.pushJoinNotice(laptop.deviceId, invitation.token, 'phone 2', joinerKey);
    relay.pushJoinNotice(laptop.deviceId, invitation.token, 'phone 3', joinerKey);
    const request = await invitation.request;
    expect(request.deviceName).toBe('phone 2');
    expect(request.fingerprint).toBe(fingerprint(joinerKey));

    const uploadsBefore = relay.keyHandovers;
    request.reject();
    await expect(invitation.joined).rejects.toMatchObject({ kind: 'join-rejected' });
    await expect(request.confirm()).rejects.toThrow(/already been answered/);
    expect(relay.keyHandovers).toBe(uploadsBefore);
  });
});
