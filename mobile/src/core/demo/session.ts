/**
 * The demo: a group with a simulated relay and simulated devices, entirely in
 * this process (docs/internal/plans/demo-mode.md).
 *
 * It exists for app-store review. A reviewer has no relay, and this system has
 * no public one to point them at, so the demo gives them one in memory: the
 * phone's client is the real `Client`, doing real crypto over real wire
 * frames, and only the far end of its socket is simulated. Every screen works
 * because nothing on this side knows it is a demo.
 *
 * The simulated devices are real clients too, joined through the real pairing
 * flow. Two of them, so that revoking one still leaves another to sync with.
 *
 * Nothing here touches the network or the device's real key storage: the
 * relay refuses any address but its own reserved one, health checks never
 * fetch, and every client keeps its keys in a MemoryStore that dies with the
 * process. `stop` drops all of it.
 */

import { utf8 } from '../bytes';
import { randomBytes } from '../random';
import { Client, type Handlers } from '../protocol/client';
import { encodePairingPayload } from '../protocol/pairing';
import { MemoryStore } from './memoryStore';
import { samplePNG } from './png';
import { FakeRelay } from './relay';

/**
 * DEMO_RELAY_URL is the demo relay's address. `.invalid` is reserved by
 * RFC 2606 and never resolves, so even a bug that dialled it for real would
 * reach nothing.
 */
export const DEMO_RELAY_URL = 'https://demo.invalid';

/** DEMO_WRITE_INTERVAL_MS is how often a simulated device copies something new while the app is in front. */
export const DEMO_WRITE_INTERVAL_MS = 90_000;

/** DEMO_JOIN_DELAY_MS is how long after this phone shows a pairing code a simulated device joins with it. */
export const DEMO_JOIN_DELAY_MS = 5_000;

/** DEMO_GROUP_NAME is what the demo's admin calls its group. */
export const DEMO_GROUP_NAME = 'Demo group';

const MiB = 1 << 20;

/**
 * Scheduler is the demo's clock for its own timers. The app uses real timers;
 * the tests drive these by hand instead of waiting.
 */
export interface Scheduler {
  /** every runs `run` every `ms` until the returned function is called. */
  every(ms: number, run: () => void): () => void;
  /** after runs `run` once after `ms` unless the returned function is called first. */
  after(ms: number, run: () => void): () => void;
}

/** timerScheduler is the Scheduler over the platform's timers. */
export const timerScheduler: Scheduler = {
  every(ms, run) {
    const id = setInterval(run, ms);
    return () => clearInterval(id);
  },
  after(ms, run) {
    const id = setTimeout(run, ms);
    return () => clearTimeout(id);
  },
};

/** DemoOptions configures a demo. */
export interface DemoOptions {
  /** deviceName is this phone's name in the demo group. */
  deviceName: string;
  /** handlers are this phone's client's callbacks, the same the app gives its real client. */
  handlers?: Handlers;
  scheduler?: Scheduler;
  now?: () => Date;
  /** onActivity is told, in one line, when a simulated device did something the user should hear about. */
  onActivity?: (message: string) => void;
}

/** Peer is one simulated device. */
interface Peer {
  name: string;
  client: Client;
}

/**
 * SAMPLE_LINES are what the simulated devices copy, in turn. Invented text,
 * none of it anyone's.
 */
const SAMPLE_LINES = [
  'Meeting moved to 3 pm, room 2.',
  'https://example.com/recipes/lemon-cake',
  'Order number: 4417-2290',
  'Remember to water the plants.',
  'The quick brown fox jumps over the lazy dog.',
  'Flight TP 512 leaves at 07:40 from gate B12.',
];

const TEXT = 'text/plain; charset=utf-8';

/** DemoSession is one running demo. */
export class DemoSession {
  private readonly peers: Peer[] = [];
  private readonly cancels = new Set<() => void>();
  private stopWriting: (() => void) | null = null;
  private writing = false;
  private nextLine = 0;
  private joined = 0;
  private stopped = false;

  private constructor(
    private readonly relay: FakeRelay,
    /** client is this phone's client in the demo group. The app swaps it in for its real one. */
    readonly client: Client,
    private readonly options: DemoOptions,
    private readonly scheduler: Scheduler,
  ) {}

  /**
   * start builds the demo group and returns once this phone is in it, with
   * history and the store already holding something to look at.
   */
  static async start(options: DemoOptions): Promise<DemoSession> {
    const now = options.now ?? (() => new Date());
    const relay = new FakeRelay({
      baseUrl: DEMO_RELAY_URL,
      now: () => now().getTime(),
      groupName: DEMO_GROUP_NAME,
      // Smaller than a real relay's limits, on purpose: everything the demo
      // holds is held in the app's own memory.
      limits: {
        maxEntryBytes: (16 * MiB).toString(),
        singleFrameMaxBytes: (10 * MiB).toString(),
        chunkBytes: MiB.toString(),
        inlineMaxBytes: (64 * 1024).toString(),
        headerV2: true,
      },
      store: { maxBytes: 32 * MiB, maxItems: 50, maxItemBytes: 16 * MiB },
      maxEntries: 20,
      maxEntryBytes: 48 * MiB,
    });
    const opener = (deviceName: string, handlers?: Handlers) =>
      Client.open({
        store: new MemoryStore(),
        deviceName,
        handlers,
        now,
        socketFactory: relay.factory,
        // Answered here, never fetched: the demo makes no request at all.
        healthCheck: async () => true,
        deviceStatus: relay.deviceStatus,
      });

    const opened: Client[] = [];
    try {
      const laptop = await opener('Demo laptop');
      opened.push(laptop);
      await laptop.createGroup(relay.creationURL);

      const phone = await opener(options.deviceName, options.handlers);
      opened.push(phone);
      await join(laptop, phone);

      const tablet = await opener('Demo tablet');
      opened.push(tablet);
      await join(laptop, tablet);

      const demo = new DemoSession(relay, phone, options, options.scheduler ?? timerScheduler);
      demo.peers.push({ name: 'Demo laptop', client: laptop }, { name: 'Demo tablet', client: tablet });
      await demo.seed(laptop, tablet);
      relay.onRevoked = (deviceId) => demo.dropPeer(deviceId);
      relay.onPairingStarted = (inviterId, token) => demo.answerPairing(inviterId, token);
      return demo;
    } catch (err) {
      for (const c of opened) {
        c.disconnect();
      }
      relay.close();
      throw err;
    }
  }

  /** peerNames are the simulated devices still in the group, for a test to assert on. */
  get peerNames(): string[] {
    return this.peers.map((p) => p.name);
  }

  /**
   * setForeground starts or stops the simulated devices' writing. They copy
   * something new only while the app is in front, as a real phone only syncs
   * while it is (SPEC §5.2).
   */
  setForeground(active: boolean): void {
    if (this.stopped) {
      return;
    }
    if (active && this.stopWriting === null) {
      this.stopWriting = this.scheduler.every(DEMO_WRITE_INTERVAL_MS, () => void this.write());
    } else if (!active && this.stopWriting !== null) {
      this.stopWriting();
      this.stopWriting = null;
    }
  }

  /**
   * stop ends the demo: every timer is cancelled, every simulated client and
   * this phone's demo client are disconnected, and the relay forgets what it
   * held. Nothing of the demo runs after this returns.
   */
  stop(): void {
    if (this.stopped) {
      return;
    }
    this.stopped = true;
    this.stopWriting?.();
    this.stopWriting = null;
    for (const cancel of this.cancels) {
      cancel();
    }
    this.cancels.clear();
    for (const peer of this.peers) {
      peer.client.disconnect();
    }
    this.peers.length = 0;
    this.client.disconnect();
    this.relay.close();
  }

  /**
   * write has the first simulated device still in the group copy the next
   * sample line. A write still running when the next is due is not doubled.
   */
  async write(): Promise<void> {
    const peer = this.peers[0];
    if (this.stopped || this.writing || peer === undefined) {
      return;
    }
    this.writing = true;
    try {
      const line = SAMPLE_LINES[this.nextLine++ % SAMPLE_LINES.length];
      await peer.client.putEntry({ contentType: TEXT, body: utf8(line) });
      if (!this.stopped) {
        this.options.onActivity?.(`${peer.name} copied something new. Sync or open History to see it.`);
      }
    } catch {
      // A write that fails while the demo is being closed is not news.
    } finally {
      this.writing = false;
    }
  }

  /** seed fills history and the store, so every screen has something on it from the start. */
  private async seed(laptop: Client, tablet: Client): Promise<void> {
    const image = samplePNG(96, 64);
    await laptop.putEntry({
      contentType: TEXT,
      body: utf8(
        'Welcome to the TwoPlacePaste demo. This text was copied on “Demo laptop” and arrived here end-to-end encrypted.',
      ),
    });
    await tablet.putEntry({ contentType: 'image/png', filename: 'gradient.png', body: image });
    await laptop.putEntry({
      contentType: 'text/plain',
      filename: 'shopping-list.txt',
      body: utf8('Milk\nBread\nCoffee beans\nLemons\n'),
    });

    await laptop.saveToStore(
      { contentType: TEXT, body: utf8('Guest Wi-Fi: Demo-Guest / password: sunflower-42') },
      { name: 'Guest Wi-Fi', description: 'For visitors. A made-up example.', tags: ['home'] },
    );
    await tablet.saveToStore(
      { contentType: 'image/png', filename: 'gradient.png', body: image },
      { name: 'Gradient', description: 'A sample image.', tags: ['pictures'] },
    );
  }

  /**
   * answerPairing has a new simulated device join a pairing code this phone
   * shows, a few seconds later, so the pairing screen completes as it would
   * with a real device scanning it. Codes the simulated devices mint
   * themselves (while the demo is being built) are not answered.
   */
  private answerPairing(inviterId: string, token: string): void {
    if (this.stopped || inviterId !== this.client.deviceId) {
      return;
    }
    const cancel = this.scheduler.after(DEMO_JOIN_DELAY_MS, () => {
      this.cancels.delete(cancel);
      void this.joinWith(token);
    });
    this.cancels.add(cancel);
  }

  private async joinWith(token: string): Promise<void> {
    if (this.stopped) {
      return;
    }
    this.joined++;
    const name = this.joined === 1 ? 'Demo phone' : `Demo phone ${this.joined}`;
    const client = await Client.open({
      store: new MemoryStore(),
      deviceName: name,
      socketFactory: this.relay.factory,
      healthCheck: async () => true,
      deviceStatus: this.relay.deviceStatus,
    });
    // The payload a scanned code would carry. Its ephemeral key is carried,
    // not trusted (Client.startPairing), so a fresh random one does.
    const payload = encodePairingPayload({
      serverUrl: DEMO_RELAY_URL,
      pairingToken: token,
      inviterEphemeralPublicKey: randomBytes(32),
    });
    try {
      await client.joinPairing(payload);
    } catch {
      // The code expired or the demo closed first: nobody joins.
      client.disconnect();
      return;
    }
    if (this.stopped) {
      client.disconnect();
      return;
    }
    this.peers.push({ name, client });
  }

  /** dropPeer stops a simulated device this phone revoked, so it stops trying to reconnect. */
  private dropPeer(deviceId: string): void {
    const index = this.peers.findIndex((p) => p.client.deviceId === deviceId);
    if (index !== -1) {
      this.peers[index].client.disconnect();
      this.peers.splice(index, 1);
    }
  }
}

/** join pairs `joiner` into `member`'s group through the ordinary invitation flow. */
async function join(member: Client, joiner: Client): Promise<void> {
  const invitation = await member.startPairing();
  await joiner.joinPairing(invitation.payload);
  await invitation.joined;
}
