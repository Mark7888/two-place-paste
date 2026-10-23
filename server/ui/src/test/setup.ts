// Shared set-up for the component tests (vite.config.ts: test.setupFiles).
import { cleanup } from "@testing-library/react";
import { afterEach, vi } from "vitest";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  window.localStorage.clear();
  window.history.replaceState(null, "", "/");
});

// jsdom does not lay pages out, so it has no scrolling to do.
window.scrollTo = () => undefined;
