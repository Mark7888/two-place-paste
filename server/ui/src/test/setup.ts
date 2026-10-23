// Shared set-up for the component tests (vite.config.ts: test.setupFiles).
import { cleanup } from "@testing-library/react";
import { afterEach, vi } from "vitest";

/** MemoryStorage is a Web Storage kept in memory. */
class MemoryStorage implements Storage {
  private items = new Map<string, string>();

  get length() {
    return this.items.size;
  }
  clear() {
    this.items.clear();
  }
  getItem(key: string) {
    return this.items.get(key) ?? null;
  }
  key(index: number) {
    return [...this.items.keys()][index] ?? null;
  }
  removeItem(key: string) {
    this.items.delete(key);
  }
  setItem(key: string, value: string) {
    this.items.set(key, String(value));
  }
}

// The tests get a localStorage of their own rather than whichever one the
// runtime offers: newer Node versions ship an experimental global
// localStorage that shadows jsdom's and, with no storage file configured,
// leaves window.localStorage undefined.
Object.defineProperty(window, "localStorage", { configurable: true, value: new MemoryStorage() });

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  window.localStorage.clear();
  window.history.replaceState(null, "", "/");
});

// jsdom does not lay pages out, so it has no scrolling to do.
window.scrollTo = () => undefined;
