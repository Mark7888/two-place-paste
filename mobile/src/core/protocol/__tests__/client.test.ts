/**
 * The client's rules, driven against the in-process relay in `fakeRelay.ts`:
 * the flows of SPEC §3 and §6, the epoch handling of spec/crypto.md §7, and
 * the two places this client is required to refuse.
 */

import { fromUTF8, utf8 } from '../../bytes';
import { Client } from '../client';
import { TppError, describe as describeError } from '../errors';
import { classifyCode, decodePairingPayload, fingerprint } from '../pairing';
import { FakeRelay, MemoryStore } from './fakeRelay';

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

async function open(relay: FakeRelay, store: MemoryStore, name: string): Promise<Client> {
  const client = await Client.open({
    store,
    deviceName: name,
    socketFactory: relay.factory,
    healthCheck: async () => false,
  });
  opened.push(client);
  return client;
}

/** pair runs SPEC §3.2 end to end between two clients of one relay. */
async function pair(inviter: Client, joiner: Client): Promise<void> {
  const invitation = await inviter.startPairing();
  await joiner.joinPairing(invitation.payload);
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
    await joiner.joinPairing(`  ${invitation.payload}==\n`);
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

describe('joiner-emitted pairing (docs/plans/joiner-emitted-pairing.md)', () => {
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
});

describe('revocation and rekey (SPEC §3.3, spec/crypto.md §7)', () => {
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
