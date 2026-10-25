/**
 * The store across the two client implementations, against the real relay
 * (SPEC §6b, spec/crypto.md §7a).
 *
 * An item one side saves the other opens, lists by name and tag, and reads
 * byte for byte; an edit made on one is read on the other at its new
 * revision; and both survive two rekeys — one carried out by the Go client,
 * whose link the phone opens, and one carried out by the phone, whose link
 * the Go client opens. That last part is what nothing short of this test can
 * check: each side's link is only ever opened by the other.
 */

import { sha256 } from '@noble/hashes/sha2.js';

import { Client, fromUTF8, toHex, utf8, type SecureStore } from '../../core';
import { GoPeer, Relay, missingTool, waitFor } from '../relay';

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

interface GoItem {
  id: string;
  name: string;
  description: string;
  tags: string[];
  content_type: string;
  filename: string;
  body_size: number;
  sha256: string;
  meta_rev: number;
  wrap_epoch: number;
}

const skip = missingTool();
const describeInterop = skip === null ? describe : describe.skip;

describeInterop('the store reads the same on the phone and in Go', () => {
  jest.setTimeout(300_000);

  let relay: Relay;
  let go: GoPeer;
  let tablet: GoPeer;
  let watch: GoPeer;
  let phone: Client;
  const added: string[] = [];
  let goNoteId = '';
  let phoneFileId = '';

  const goList = async () => {
    const r = await go.send('store-list');
    return { items: r.items as GoItem[], unreadable: r.unreadable as number };
  };

  beforeAll(async () => {
    relay = await Relay.start();
    go = GoPeer.start('Anna — laptop');
    tablet = GoPeer.start('Anna — tablet');
    watch = GoPeer.start('Anna — watch');
    phone = await Client.open({
      store: new InMemoryStore(),
      deviceName: 'Anna — phone',
      handlers: { onStoreItemAdded: (it) => added.push(it.id) },
    });
    await go.send(`create ${await relay.creationURL('Store')}`);
    const invitation = await go.send('pair');
    const joined = go.send('await-join');
    await phone.joinPairing(String(invitation.payload));
    await joined;
    await phone.connect();
  });

  afterAll(() => {
    phone?.disconnect();
    for (const peer of [go, tablet, watch]) {
      peer?.stop();
    }
    relay?.stop();
  });

  test('the store is off until the admin turns it on', async () => {
    expect((await phone.storeLimits()).enabled).toBe(false);
    await relay.enableStore('Store');
    const limits = await phone.storeLimits();
    expect(limits.enabled).toBe(true);
    expect(limits.availableBytes).toBeGreaterThan(0);
  });

  test('an item saved in Go opens on the phone, and the phone hears of it', async () => {
    const saved = await go.send(
      `store-save ${JSON.stringify({
        name: 'Home wifi',
        description: 'the router in the hall — ünïcode',
        tags: ['home', 'network'],
        content_type: 'text/plain; charset=utf-8',
        body: 'correct horse battery staple',
      })}`,
    );
    goNoteId = String(saved.id);
    await waitFor('the phone to hear of the item', async () => added.includes(goNoteId));

    const listing = await phone.listStore();
    expect(listing.unreadable).toEqual([]);
    const note = listing.items.find((it) => it.id === goNoteId);
    expect(note?.name).toBe('Home wifi');
    expect(note?.description).toBe('the router in the hall — ünïcode');
    expect(note?.tags).toEqual(['home', 'network']);
    expect(note?.sha256Hex).toBe(toHex(sha256(utf8('correct horse battery staple'))));

    const item = await phone.fetchStoreItem(note!);
    expect(fromUTF8(item.body)).toBe('correct horse battery staple');
  });

  test('an item saved on the phone, larger than one frame, opens in Go byte for byte', async () => {
    const body = new Uint8Array(11 << 20).map((_, i) => (i * 13 + 5) & 0xff);
    const it = await phone.saveToStore(
      { contentType: 'application/pdf', filename: 'manual.pdf', body },
      { name: 'Boiler manual', description: '', tags: ['home'] },
    );
    phoneFileId = it.id;
    expect(it.wrapEpoch).toBe(1n);

    const { items } = await goList();
    const seen = items.find((x) => x.id === phoneFileId);
    expect(seen?.name).toBe('Boiler manual');
    expect(seen?.filename).toBe('manual.pdf');
    expect(seen?.body_size).toBe(body.length);

    const fetched = await go.send(`store-fetch ${phoneFileId}`);
    expect(fetched.length).toBe(body.length);
    expect(fetched.sha256).toBe(toHex(sha256(body)));
  });

  test('an edit made on the phone is read in Go at its new revision', async () => {
    const note = (await phone.listStore()).items.find((it) => it.id === goNoteId)!;
    const edited = await phone.editStoreItem(note, { name: 'Wifi', description: 'renamed on the phone', tags: ['home'] });
    expect(edited.metaRev).toBe(1n);

    const seen = (await goList()).items.find((x) => x.id === goNoteId);
    expect(seen).toMatchObject({ name: 'Wifi', description: 'renamed on the phone', tags: ['home'], meta_rev: 1 });
  });

  test('after the Go client rekeys, the phone opens the older items through its link', async () => {
    const invitation = await go.send('pair');
    const joined = go.send('await-join');
    const tabletJoined = await tablet.send(`join ${String(invitation.payload)}`);
    await joined;
    await go.send(`revoke ${String(tabletJoined.device_id)}`);
    await waitFor('the phone to reach epoch 2', async () => phone.epoch === 2n);

    const listing = await phone.listStore();
    expect(listing.unreadable).toEqual([]);
    expect(listing.items.map((it) => it.id).sort()).toEqual([goNoteId, phoneFileId].sort());
    const file = listing.items.find((it) => it.id === phoneFileId)!;
    const item = await phone.fetchStoreItem(file);
    expect(item.body.length).toBe(11 << 20);
  });

  test('after the phone rekeys, the Go client opens every item through the phone’s link', async () => {
    const invitation = await phone.startPairing();
    const watchJoined = await watch.send(`join ${invitation.payload}`);
    await invitation.joined;
    const revocation = await phone.prepareRevoke(String(watchJoined.device_id));
    const roster = await revocation.confirm();
    expect(roster.epoch).toBe(3n);
    await waitFor('the Go client to reach epoch 3', async () => (await go.send('epoch')).epoch === 3);

    // Saved at epoch 3: wrapped under a key neither revoked device ever held.
    const fresh = await phone.saveToStore(
      { contentType: 'text/plain; charset=utf-8', body: utf8('after two rekeys') },
      { name: 'Fresh', description: '', tags: [] },
    );
    expect(fresh.wrapEpoch).toBe(3n);

    const { items, unreadable } = await goList();
    expect(unreadable).toBe(0);
    const byId = new Map(items.map((it) => [it.id, it]));
    expect(byId.get(goNoteId)?.wrap_epoch).toBe(1);
    expect(byId.get(phoneFileId)?.wrap_epoch).toBe(1);
    expect(byId.get(fresh.id)?.wrap_epoch).toBe(3);
    expect((await go.send(`store-fetch ${goNoteId}`)).body).toBe('correct horse battery staple');
    expect((await go.send(`store-fetch ${fresh.id}`)).body).toBe('after two rekeys');
  });

  test('an item deleted on the phone is gone in Go', async () => {
    await phone.deleteStoreItem(phoneFileId);
    const { items } = await goList();
    expect(items.some((it) => it.id === phoneFileId)).toBe(false);
  });
});
