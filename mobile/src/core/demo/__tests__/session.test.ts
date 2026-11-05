/**
 * The demo session: what it builds, what its simulated devices do and when,
 * and that `stop` leaves nothing running. Timers go through a scheduler the
 * test fires by hand, so nothing here waits for real time.
 */

import { fromUTF8 } from '../../bytes';
import { DemoSession, DEMO_GROUP_NAME, DEMO_JOIN_DELAY_MS, DEMO_WRITE_INTERVAL_MS, type Scheduler } from '../session';

/** ManualScheduler records timers and runs them when the test says so. */
class ManualScheduler implements Scheduler {
  private nextId = 1;
  readonly intervals = new Map<number, { ms: number; run: () => void }>();
  readonly timeouts = new Map<number, { ms: number; run: () => void }>();

  every(ms: number, run: () => void): () => void {
    const id = this.nextId++;
    this.intervals.set(id, { ms, run });
    return () => this.intervals.delete(id);
  }

  after(ms: number, run: () => void): () => void {
    const id = this.nextId++;
    this.timeouts.set(id, { ms, run });
    return () => this.timeouts.delete(id);
  }

  /** fireIntervals runs every interval timer once. */
  fireIntervals(): void {
    for (const t of [...this.intervals.values()]) {
      t.run();
    }
  }

  /** fireTimeouts runs and removes every one-shot timer. */
  fireTimeouts(): void {
    const due = [...this.timeouts.entries()];
    for (const [id, t] of due) {
      this.timeouts.delete(id);
      t.run();
    }
  }

  get live(): number {
    return this.intervals.size + this.timeouts.size;
  }
}

/** settle lets queued microtasks and immediates run, for work a timer started without returning a promise. */
async function settle(rounds = 20): Promise<void> {
  for (let i = 0; i < rounds; i++) {
    await new Promise((resolve) => setImmediate(resolve));
  }
}

const running: DemoSession[] = [];

afterEach(() => {
  while (running.length > 0) {
    running.pop()?.stop();
  }
  jest.restoreAllMocks();
});

async function start(options: { onActivity?: (m: string) => void; onGroupName?: (n: string) => void } = {}) {
  const scheduler = new ManualScheduler();
  const demo = await DemoSession.start({
    deviceName: 'Pixel',
    scheduler,
    onActivity: options.onActivity,
    handlers: { onGroupName: options.onGroupName },
  });
  running.push(demo);
  return { demo, scheduler };
}

test('the demo puts this phone in a group with two simulated devices, history and a store', async () => {
  let named = '';
  const { demo } = await start({ onGroupName: (n) => (named = n) });
  const phone = demo.client;

  expect(phone.inGroup).toBe(true);
  expect(phone.serverUrl).toBe('https://demo.invalid');
  expect(demo.peerNames).toEqual(['Demo laptop', 'Demo tablet']);

  const roster = await phone.devices();
  expect(roster.devices.map((d) => d.name).sort()).toEqual(['Demo laptop', 'Demo tablet', 'Pixel']);

  const history = await phone.getHistory();
  expect(history.entries).toHaveLength(3);
  const items = await Promise.all(history.entries.map((m) => phone.getEntry(m.id)));
  expect(items.map((it) => it.filename)).toEqual(['shopping-list.txt', 'gradient.png', '']);
  expect([...items[1].body.subarray(0, 4)]).toEqual([0x89, 0x50, 0x4e, 0x47]);
  expect(fromUTF8(items[2].body)).toContain('Welcome to the TwoPlacePaste demo');

  const store = await phone.listStore();
  expect(store.unreadable).toEqual([]);
  expect(store.items.map((it) => it.name)).toEqual(['Gradient', 'Guest Wi-Fi']);
  expect((await phone.fetchStoreItem(store.items[0])).contentType).toBe('image/png');

  await phone.connect();
  await settle();
  expect(named).toBe(DEMO_GROUP_NAME);
});

test('a simulated device copies something new on the timer, and only while the app is in front', async () => {
  const activity: string[] = [];
  const { demo, scheduler } = await start({ onActivity: (m) => activity.push(m) });

  expect(scheduler.intervals.size).toBe(0);
  demo.setForeground(true);
  expect([...scheduler.intervals.values()].map((t) => t.ms)).toEqual([DEMO_WRITE_INTERVAL_MS]);
  demo.setForeground(true);
  expect(scheduler.intervals.size).toBe(1);

  await demo.write();
  expect(activity).toEqual(['Demo laptop copied something new. Sync or open History to see it.']);
  expect((await demo.client.getHistory()).entries).toHaveLength(4);
  expect(fromUTF8((await demo.client.getLatest()).body)).toBe('Meeting moved to 3 pm, room 2.');

  demo.setForeground(false);
  expect(scheduler.intervals.size).toBe(0);
});

test('a pairing code this phone shows is joined by a simulated device a few seconds later', async () => {
  const { demo, scheduler } = await start();
  const phone = demo.client;
  await phone.connect();

  const invitation = await phone.startPairing();
  expect([...scheduler.timeouts.values()].map((t) => t.ms)).toEqual([DEMO_JOIN_DELAY_MS]);

  scheduler.fireTimeouts();
  // The phone asks its user first, as it does for a real device (SPEC §3.2 step 4).
  const request = await invitation.request;
  expect(request.deviceName).toBe('Demo phone');
  await request.confirm();
  const device = await invitation.joined;
  expect(device.name).toBe('Demo phone');
  await settle();
  expect(demo.peerNames).toEqual(['Demo laptop', 'Demo tablet', 'Demo phone']);
});

test('revoking a simulated device rekeys the group, and the remaining one keeps syncing', async () => {
  const { demo } = await start();
  const phone = demo.client;
  const laptop = (await phone.devices()).devices.find((d) => d.name === 'Demo laptop');
  expect(laptop).toBeDefined();

  await (await phone.prepareRevoke(laptop?.id ?? '')).confirm();
  expect(phone.epoch).toBe(2n);
  expect(demo.peerNames).toEqual(['Demo tablet']);

  await settle();
  await demo.write();
  const latest = await phone.getLatest();
  expect(latest.meta?.epoch).toBe(2n);
  expect(fromUTF8(latest.body)).toBe('Meeting moved to 3 pm, room 2.');

  // The store saved at epoch 1 still opens after the rekey.
  expect((await phone.listStore()).unreadable).toEqual([]);
});

test('stop cancels every timer and disconnects every client', async () => {
  const activity: string[] = [];
  const { demo, scheduler } = await start({ onActivity: (m) => activity.push(m) });
  demo.setForeground(true);
  await demo.client.connect();
  await demo.client.startPairing();
  expect(scheduler.live).toBe(2);

  demo.stop();
  expect(scheduler.live).toBe(0);
  expect(demo.client.connected).toBe(false);
  expect(demo.peerNames).toEqual([]);

  await demo.write();
  demo.setForeground(true);
  expect(scheduler.live).toBe(0);
  expect(activity).toEqual([]);
  await expect(demo.client.connect()).rejects.toThrow();
});

test('the demo makes no network request', async () => {
  const fetchSpy = jest.spyOn(globalThis, 'fetch');
  const sockets: string[] = [];
  const original = globalThis.WebSocket;
  // Any use of the platform socket is recorded and refused.
  globalThis.WebSocket = class {
    constructor(url: string) {
      sockets.push(url);
      throw new Error('no network in the demo');
    }
  } as unknown as typeof WebSocket;
  try {
    const { demo, scheduler } = await start();
    demo.setForeground(true);
    scheduler.fireIntervals();
    await demo.client.connect();
    await demo.client.startPairing();
    scheduler.fireTimeouts();
    await settle();
    demo.stop();
  } finally {
    globalThis.WebSocket = original;
  }
  expect(fetchSpy).not.toHaveBeenCalled();
  expect(sockets).toEqual([]);
});
