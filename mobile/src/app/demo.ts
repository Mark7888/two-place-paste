/**
 * The app's side of the demo (docs/internal/plans/demo-mode.md): when it is
 * offered, and the swap between the device's real client and the demo's.
 *
 * Kept apart from `sessionStore`, which imports React Native, so that the two
 * rules that matter here — the demo never touches the real keys, and the
 * offer disappears for good once the device has been in a real group — are
 * tested in Node like the rest of the core.
 */

import type { Client, DemoOptions, SecureStore } from '../core';
import { DemoSession } from '../core';

/**
 * EVER_JOINED_NAME is the secure-store entry that records that this device
 * has been in a real group. It holds no key and no content: only "1".
 */
export const EVER_JOINED_NAME = 'everJoined';

/**
 * DemoGate decides whether the setup screen offers the demo: only until this
 * device first joins a real group.
 *
 * Leaving a group replaces the client's whole state (`Client.forget`), so the
 * state cannot say whether the device was ever in one; this separate entry
 * does, and nothing in the app clears it. Clearing the app's storage removes
 * it with everything else, which is what brings the offer back.
 */
export class DemoGate {
  private joined: boolean | null = null;

  constructor(private readonly store: SecureStore) {}

  /** load reads the entry. Until it has, the demo is not offered. */
  async load(): Promise<void> {
    try {
      this.joined = (await this.store.load(EVER_JOINED_NAME)) !== null;
    } catch {
      // A store that cannot be read offers nothing new: the demo stays hidden.
      this.joined = true;
    }
  }

  /** available reports whether the demo may be offered. */
  get available(): boolean {
    return this.joined === false;
  }

  /**
   * observe is told the app's state whenever it changes. The first time the
   * real client is in a group, the entry is written. A demo group never
   * counts: it is not the device's.
   */
  observe(inGroup: boolean, inDemo: boolean): void {
    if (!inGroup || inDemo || this.joined !== false) {
      return;
    }
    this.joined = true;
    void this.store.save(EVER_JOINED_NAME, '1').catch(() => {
      // Not written: the next launch sees a device in a group and writes it then.
    });
  }
}

/**
 * DemoMode holds the device's real client aside while a demo runs, and hands
 * it back when the demo closes. The real client is never given to the demo,
 * so the demo cannot read or write its keys.
 */
export class DemoMode {
  private real: Client | null = null;
  private session: DemoSession | null = null;
  private starting = false;

  /** active reports whether a demo is running (or starting). */
  get active(): boolean {
    return this.session !== null || this.starting;
  }

  /**
   * start disconnects the real client, starts a demo under its device name and
   * returns the demo's client. Only a device with no group can start one.
   */
  async start(real: Client, options: Omit<DemoOptions, 'deviceName'>): Promise<Client> {
    if (this.active) {
      throw new Error('a demo is already running');
    }
    if (real.inGroup) {
      throw new Error('the demo is only for a device that is not in a group');
    }
    this.starting = true;
    try {
      real.disconnect();
      const session = await DemoSession.start({ ...options, deviceName: real.snapshot().deviceName });
      this.real = real;
      this.session = session;
      return session.client;
    } finally {
      this.starting = false;
    }
  }

  /** setForeground tells the demo whether the app is in front. */
  setForeground(active: boolean): void {
    this.session?.setForeground(active);
  }

  /** stop ends the demo and returns the real client, or null when no demo was running. */
  stop(): Client | null {
    const real = this.real;
    this.session?.stop();
    this.session = null;
    this.real = null;
    return real;
  }
}
