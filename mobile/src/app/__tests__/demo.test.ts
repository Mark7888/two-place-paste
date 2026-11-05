import { Client, FakeRelay, MemoryStore } from '../../core';
import { DemoGate, DemoMode, EVER_JOINED_NAME } from '../demo';

const opened: Client[] = [];
const modes: DemoMode[] = [];

afterEach(() => {
  while (modes.length > 0) {
    modes.pop()?.stop();
  }
  while (opened.length > 0) {
    opened.pop()?.disconnect();
  }
});

/** flushed waits for the gate's background write to land. */
const flushed = () => new Promise((resolve) => setImmediate(resolve));

async function openReal(store: MemoryStore, relay = new FakeRelay()): Promise<Client> {
  const client = await Client.open({
    store,
    deviceName: 'Pixel',
    socketFactory: relay.factory,
    healthCheck: async () => false,
    deviceStatus: relay.deviceStatus,
  });
  opened.push(client);
  return client;
}

describe('DemoGate', () => {
  test('offers the demo on a fresh install', async () => {
    const gate = new DemoGate(new MemoryStore());
    expect(gate.available).toBe(false); // not before it has read the store
    await gate.load();
    expect(gate.available).toBe(true);
  });

  test.each([
    ['out of a group', false, false, true],
    ['in the demo group', true, true, true],
    ['in a real group', true, false, false],
  ])('a device %s: is the demo still offered?', async (_name, inGroup, inDemo, offered) => {
    const store = new MemoryStore();
    const gate = new DemoGate(store);
    await gate.load();
    gate.observe(inGroup, inDemo);
    await flushed();
    expect(gate.available).toBe(offered);
    expect(store.peek(EVER_JOINED_NAME)).toBe(offered ? null : '1');
  });

  test('once a device has joined a real group, leaving it does not bring the demo back', async () => {
    const store = new MemoryStore();
    const relay = new FakeRelay();
    const real = await openReal(store, relay);
    const gate = new DemoGate(store);
    await gate.load();

    await real.createGroup(relay.creationURL);
    gate.observe(real.inGroup, false);
    await real.forget();
    gate.observe(real.inGroup, false);
    await flushed();
    expect(gate.available).toBe(false);

    // The next launch reads the same answer.
    const next = new DemoGate(store);
    await next.load();
    expect(next.available).toBe(false);
  });

  test('a device already in a group when it updates is recorded on its first launch', async () => {
    const store = new MemoryStore();
    const relay = new FakeRelay();
    const real = await openReal(store, relay);
    await real.createGroup(relay.creationURL);

    const gate = new DemoGate(store);
    await gate.load();
    expect(gate.available).toBe(true);
    gate.observe(real.inGroup, false);
    await flushed();
    expect(store.peek(EVER_JOINED_NAME)).toBe('1');
  });
});

describe('DemoMode', () => {
  test('the demo never touches the real client’s keys, and hands the same client back', async () => {
    const store = new MemoryStore();
    const real = await openReal(store);
    const before = store.peek('session');
    const publicKey = real.publicKey;

    const mode = new DemoMode();
    modes.push(mode);
    const demo = await mode.start(real, {});
    expect(mode.active).toBe(true);
    expect(demo).not.toBe(real);
    expect(demo.inGroup).toBe(true);
    expect(demo.snapshot().deviceName).toBe('Pixel');
    // A different identity: the demo's keys are its own.
    expect(demo.publicKey).not.toEqual(publicKey);

    expect(mode.stop()).toBe(real);
    expect(mode.active).toBe(false);
    expect(demo.connected).toBe(false);
    expect(store.peek('session')).toBe(before);
    expect(real.inGroup).toBe(false);
    expect(real.publicKey).toEqual(publicKey);
  });

  test('a device in a group cannot start the demo', async () => {
    const relay = new FakeRelay();
    const real = await openReal(new MemoryStore(), relay);
    await real.createGroup(relay.creationURL);
    const mode = new DemoMode();
    await expect(mode.start(real, {})).rejects.toThrow(/not in a group/);
    expect(mode.active).toBe(false);
  });

  test('stop without a demo returns nothing', () => {
    expect(new DemoMode().stop()).toBeNull();
  });
});
