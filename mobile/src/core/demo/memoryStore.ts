/**
 * MemoryStore is a SecureStore that keeps its values in memory.
 *
 * The tests use it in place of the Keystore, and the demo uses it for every
 * client it opens, which is what keeps the demo's keys out of the device's
 * real storage: nothing written here survives the process.
 */

import type { SecureStore } from '../protocol/state';

export class MemoryStore implements SecureStore {
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

  /** peek is what a test asserts against when it needs to know what was persisted. */
  peek(name: string): string | null {
    return this.values.get(name) ?? null;
  }
}
