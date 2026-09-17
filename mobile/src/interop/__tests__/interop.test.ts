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
 * machine cannot host it (docs/conventions.md §5).
 */

import { Client, TppError, fromUTF8, utf8, type SecureStore } from '../../core';
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

  beforeAll(async () => {
    relay = await Relay.start();
    go = GoPeer.start('Anna — laptop');
    phone = await Client.open({
      store: new InMemoryStore(),
      deviceName: 'Anna — phone',
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

  test('a second phone pairs the other way round: it shows, the Go client accepts', async () => {
    // The direction the wire contract could not express before
    // docs/plans/joiner-emitted-pairing.md, and the one that proves the two
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
