/**
 * Several groups on one installation (spec/crypto.md §10.1): switching between
 * two groups on two in-process relays, against the records the secure store
 * ends up holding.
 */

import { fromUTF8, utf8 } from '../../bytes';
import { Client } from '../client';
import { TppError } from '../errors';
import { PARKED_STATE_NAME, decodeParked, encodeParked, groupRef } from '../groups';
import { decodeState } from '../state';
import { FakeRelay, MemoryStore } from '../../demo';

const opened: Client[] = [];

afterEach(() => {
  while (opened.length > 0) {
    opened.pop()?.disconnect();
  }
});

/** twoRelays are two relays, one group each, and a socket factory that dials whichever a URL names. */
function twoRelays(): { home: FakeRelay; work: FakeRelay; options: Omit<Parameters<typeof Client.open>[0], 'store' | 'deviceName'> } {
  const home = new FakeRelay({ baseUrl: 'https://home.test', groupName: 'Home' });
  const work = new FakeRelay({ baseUrl: 'https://work.test', groupName: 'Work', groupId: 'group-work' });
  const pick = (url: string) => (url.includes('work.test') ? work : home);
  return {
    home,
    work,
    options: {
      socketFactory: (url) => pick(url).factory(url),
      healthCheck: async () => false,
      deviceStatus: (base, id) => pick(base).deviceStatus(base, id),
    },
  };
}

async function openOn(
  options: ReturnType<typeof twoRelays>['options'],
  store: MemoryStore,
  name: string,
): Promise<Client> {
  const client = await Client.open({ ...options, store, deviceName: name });
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

/** until polls a condition the client reaches through an event, not a returned promise. */
async function until(what: string, cond: () => boolean): Promise<void> {
  for (let i = 0; i < 200; i++) {
    if (cond()) {
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  throw new Error(`timed out waiting for ${what}`);
}

const text = (s: string) => ({ contentType: 'text/plain; charset=utf-8', filename: '', body: utf8(s) });

describe('groupRef', () => {
  test('matches the Go core', () => {
    // Values from pkg/tppclient GroupRef: both clients name a group the same way.
    expect(groupRef('group-a')).toBe('77c32c1e889930fb');
    expect(groupRef('0f3c2a9e-7d1b-4c55-9a3e-6b2f1e8d4c70')).toBe('dfd80379bcd3bc9c');
    expect(groupRef('')).toBe('');
  });
});

describe('several groups (spec/crypto.md §10.1)', () => {
  test('add a group, switch back after a rekey, and survive a restart', async () => {
    const { home, work, options } = twoRelays();
    const store = new MemoryStore();
    const phone = await openOn(options, store, 'phone');
    await phone.createGroup(home.creationURL);
    const laptop = await openOn(options, new MemoryStore(), 'laptop');
    await pair(phone, laptop);
    const tablet = await openOn(options, new MemoryStore(), 'tablet');
    await pair(phone, tablet);
    const inHome = phone.snapshot();
    const homeRef = phone.activeRef;

    // Add Work: Home is set aside and Work is joined from a fresh identity.
    await phone.parkActive();
    expect(phone.inGroup).toBe(false);
    expect(phone.connected).toBe(false);
    expect(phone.parkedGroups()).toEqual([{ ref: homeRef, groupName: 'Home', serverUrl: 'https://home.test' }]);
    await phone.createGroup(work.creationURL);
    expect(phone.snapshot().devicePrivateKey).not.toEqual(inHome.devicePrivateKey);
    await phone.putEntry(text('in work'));

    // Home re-keys while the phone is away in Work.
    const revocation = await laptop.prepareRevoke(tablet.deviceId);
    await revocation.confirm();
    await laptop.putEntry(text('in home, after the rekey'));

    // Back to Home: it catches up like any device that was offline.
    await phone.switchGroup(homeRef);
    expect(phone.connected).toBe(false);
    expect(phone.snapshot().devicePrivateKey).toEqual(inHome.devicePrivateKey);
    await phone.connect();
    await until('the new epoch', () => phone.epoch === 2n);
    expect(fromUTF8((await phone.getLatest()).body)).toBe('in home, after the rekey');

    // What is stored is what a build without this feature would open.
    expect(decodeState(store.peek('session') ?? '').groupId).toBe(inHome.groupId);
    const parked = decodeParked(store.peek(PARKED_STATE_NAME) ?? '');
    expect(parked.map((g) => g.groupName)).toEqual(['Work']);

    // A restart opens Home with Work still set aside.
    phone.disconnect();
    const restarted = await openOn(options, store, 'phone');
    expect(restarted.activeRef).toBe(homeRef);
    expect(restarted.epoch).toBe(2n);
    const [workGroup] = restarted.parkedGroups();
    expect(workGroup.groupName).toBe('Work');
    await restarted.switchGroup(workGroup.ref);
    await restarted.connect();
    expect(fromUTF8((await restarted.getLatest()).body)).toBe('in work');
  });

  test("the operator link follows the active group's relay", async () => {
    const { home, work, options } = twoRelays();
    home.operatorUrl = 'https://home.test/help';
    const phone = await openOn(options, new MemoryStore(), 'phone');
    await phone.createGroup(home.creationURL);
    await until('the home website', () => phone.operatorUrl !== '');
    expect(phone.operatorLink).toBe('https://home.test/help?group_id=group-1');
    const homeRef = phone.activeRef;

    // Work's relay names no website: none of Home's is left over.
    await phone.parkActive();
    expect(phone.operatorLink).toBeNull();
    await phone.createGroup(work.creationURL);
    await until('the work group name', () => phone.groupName === 'Work');
    expect(phone.operatorLink).toBeNull();

    // Back to Home: nothing until its relay answers again.
    await phone.switchGroup(homeRef);
    expect(phone.operatorLink).toBeNull();
    await phone.connect();
    await until('the home website again', () => phone.operatorUrl !== '');
    expect(phone.operatorLink).toBe('https://home.test/help?group_id=group-1');
  });

  test('an interrupted switch leaves a duplicate the next open removes', async () => {
    const { home, work, options } = twoRelays();
    const store = new MemoryStore();
    const phone = await openOn(options, store, 'phone');
    await phone.createGroup(home.creationURL);
    const homeState = phone.snapshot();
    await phone.parkActive();
    await phone.createGroup(work.creationURL);
    phone.disconnect();
    // As if a switch to Home stopped after writing the active entry.
    await store.save('session', store.peek('session') ?? '');
    await store.save(PARKED_STATE_NAME, encodeParked([homeState, phone.snapshot()]));

    const restarted = await openOn(options, store, 'phone');
    expect(restarted.parkedGroups().map((g) => g.groupName)).toEqual(['Home']);
    expect(decodeParked(store.peek(PARKED_STATE_NAME) ?? '').map((g) => g.groupName)).toEqual(['Home']);
  });

  test('a record from a newer build is shown as nothing and never overwritten', async () => {
    const { home, options } = twoRelays();
    const store = new MemoryStore();
    const first = await openOn(options, store, 'phone');
    await first.createGroup(home.creationURL);
    first.disconnect();
    const newer = JSON.stringify({ version: 2, groups: [] });
    await store.save(PARKED_STATE_NAME, newer);

    const phone = await openOn(options, store, 'phone');
    expect(phone.parkedGroups()).toEqual([]);
    await expect(phone.parkActive()).rejects.toMatchObject({ kind: 'parked-unreadable' });
    expect(phone.inGroup).toBe(true);
    expect(store.peek(PARKED_STATE_NAME)).toBe(newer);
  });

  test('a group the admin deleted is discarded, not set aside', async () => {
    const { home, work, options } = twoRelays();
    const store = new MemoryStore();
    const phone = await openOn(options, store, 'phone');
    await phone.createGroup(home.creationURL);
    phone.disconnect();
    home.deleteGroup();
    await expect(phone.connect()).rejects.toThrow();
    expect(phone.groupDeleted).toBe(true);

    await phone.parkActive();
    expect(phone.parkedGroups()).toEqual([]);
    expect(store.peek(PARKED_STATE_NAME)).toBeNull();
    await phone.createGroup(work.creationURL);
    expect(phone.groupDeleted).toBe(false);
  });

  test('remove and forget touch only what they name', async () => {
    const { home, work, options } = twoRelays();
    const store = new MemoryStore();
    const phone = await openOn(options, store, 'phone');
    await phone.createGroup(home.creationURL);
    const homeRef = phone.activeRef;
    await phone.parkActive();
    await phone.createGroup(work.creationURL);

    // The group in use is not removable this way, and an unknown ref is refused.
    await expect(phone.removeParked(phone.activeRef)).rejects.toMatchObject({ kind: 'unknown-group' });
    await expect(phone.switchGroup('nope')).rejects.toBeInstanceOf(TppError);

    // Leaving the group in use keeps Home, which is still there to switch to.
    await phone.forget();
    expect(phone.inGroup).toBe(false);
    expect(phone.parkedGroups().map((g) => g.ref)).toEqual([homeRef]);
    await phone.switchGroup(homeRef);
    expect(phone.inGroup).toBe(true);
    expect(phone.parkedGroups()).toEqual([]);

    // Removing the last group set aside removes the entry.
    await phone.parkActive();
    expect(store.peek(PARKED_STATE_NAME)).not.toBeNull();
    await phone.removeParked(homeRef);
    expect(phone.parkedGroups()).toEqual([]);
    expect(store.peek(PARKED_STATE_NAME)).toBeNull();
  });
});
