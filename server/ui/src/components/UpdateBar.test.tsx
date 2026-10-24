import { act, fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";
import { CHECK_EVERY, CHECK_GAP } from "../hooks/useUpdateCheck";
import { page } from "../lib/build";
import { UpdateBar } from "./UpdateBar";

const RUNNING = "aaaaaaaaaaaa";
const NEWER = "bbbbbbbbbbbb";

/** serve answers /admin/version.json with whatever build relay.build holds,
 *  and counts the requests. */
function serve(build: string | null) {
  const relay = { build, requests: 0 };
  const fetch = vi.fn(async (url: string, init?: RequestInit) => {
    expect(url).toBe("/admin/version.json");
    expect(init?.cache).toBe("no-store");
    relay.requests++;
    if (relay.build === null) return new Response("404 page not found", { status: 404 });
    return Response.json({ build: relay.build });
  });
  vi.stubGlobal("fetch", fetch);
  return relay;
}

/** settle lets the pending fetches and the renders they cause finish. */
const settle = () => act(async () => {
  for (let i = 0; i < 5; i++) await Promise.resolve();
});

describe("UpdateBar", () => {
  test.each([
    { name: "the relay serves the running build", served: RUNNING, running: RUNNING },
    { name: "the relay has no UI build", served: null, running: RUNNING },
    { name: "the dev server, which has no build id", served: NEWER, running: "__TPP_BUILD__" },
  ])("stays hidden when $name", async ({ served, running }) => {
    serve(served);
    render(<UpdateBar running={running} />);
    await settle();
    expect(screen.queryByRole("status")).toBeNull();
  });

  test("appears when the relay serves a newer build, and Update reloads", async () => {
    serve(NEWER);
    const reload = vi.spyOn(page, "reload").mockImplementation(() => undefined);
    render(<UpdateBar running={RUNNING} />);
    expect(await screen.findByText("A new version of the admin is available.")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Update" }));
    expect(screen.getByRole("button", { name: "Updating…" })).toHaveProperty("disabled", true);
    await settle();
    expect(reload).toHaveBeenCalledTimes(1);
  });

  test("a failed check is tried again later", async () => {
    vi.useFakeTimers();
    try {
      vi.stubGlobal("fetch", vi.fn(async () => Promise.reject(new TypeError("Failed to fetch"))));
      render(<UpdateBar running={RUNNING} />);
      await settle();
      expect(screen.queryByRole("status")).toBeNull();
      serve(NEWER);
      await act(async () => vi.advanceTimersByTime(CHECK_EVERY));
      await settle();
      expect(screen.getByRole("status")).toBeTruthy();
    } finally {
      vi.useRealTimers();
    }
  });

  test("checks again every few minutes and when the app comes back, not on every focus", async () => {
    vi.useFakeTimers();
    try {
      const relay = serve(RUNNING);
      render(<UpdateBar running={RUNNING} />);
      await settle();
      expect(relay.requests).toBe(1);

      // Back to the app at once: too soon to ask again.
      fireEvent.focus(window);
      await settle();
      expect(relay.requests).toBe(1);

      // The relay is upgraded; the admin comes back a while later.
      relay.build = NEWER;
      await act(async () => vi.advanceTimersByTime(CHECK_GAP));
      fireEvent.focus(window);
      await settle();
      expect(relay.requests).toBe(2);
      expect(screen.getByRole("status")).toBeTruthy();

      // The timer keeps checking, and a rollback takes the offer back.
      relay.build = RUNNING;
      await act(async () => vi.advanceTimersByTime(CHECK_EVERY));
      await settle();
      expect(relay.requests).toBeGreaterThanOrEqual(3);
      expect(screen.queryByRole("status")).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  test("Later hides it until the relay has yet another build", async () => {
    vi.useFakeTimers();
    try {
      const relay = serve(NEWER);
      render(<UpdateBar running={RUNNING} />);
      await settle();
      fireEvent.click(screen.getByRole("button", { name: "Later" }));
      expect(screen.queryByRole("status")).toBeNull();

      await act(async () => vi.advanceTimersByTime(CHECK_EVERY));
      await settle();
      expect(screen.queryByRole("status")).toBeNull();

      relay.build = "cccccccccccc";
      await act(async () => vi.advanceTimersByTime(CHECK_EVERY));
      await settle();
      expect(screen.getByRole("status")).toBeTruthy();
    } finally {
      vi.useRealTimers();
    }
  });
});
