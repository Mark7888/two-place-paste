import { useEffect, useState } from "react";
import { isBuildId } from "../lib/build";

/** CHECK_EVERY is how often an open admin asks which build the relay serves. */
export const CHECK_EVERY = 5 * 60_000;

/** CHECK_GAP is the least time between two checks that a return to the app
 *  starts: switching windows back and forth should not be a request each. */
export const CHECK_GAP = 30_000;

/** useUpdateCheck asks the relay which UI build it serves (/admin/version.json,
 *  written by the build) and compares it with the running one. It returns the
 *  relay's build when they differ, and null while they match or nobody knows.
 *
 *  It checks when the app starts, every few minutes while it is in view, and
 *  when it comes back to the foreground or back online — the moments an
 *  installed app, which can stay open for days, is most likely to be behind.
 *  It asks for nothing private and needs no session, and a failed check is
 *  simply tried again later.
 *
 *  When an update is found it also asks the service worker to update, so the
 *  new build is fetched in the background and ready for the reload. */
export function useUpdateCheck(running: string): string | null {
  const [latest, setLatest] = useState<string | null>(null);

  useEffect(() => {
    // The dev server serves the source as it changes and has no build id.
    if (!isBuildId(running)) return;
    let stopped = false;
    let busy = false;
    let last = -Infinity;

    const check = async (force: boolean) => {
      if (busy || document.visibilityState === "hidden") return;
      if (!force && Date.now() - last < CHECK_GAP) return;
      busy = true;
      last = Date.now();
      try {
        const response = await fetch("/admin/version.json", { cache: "no-store", credentials: "same-origin" });
        // A 404 is a relay built without the UI: there is nothing to offer.
        if (!response.ok) return;
        const { build } = (await response.json()) as { build?: unknown };
        if (!stopped && isBuildId(build)) setLatest(build === running ? null : build);
      } catch {
        // Offline, or the relay is restarting into the new build: the next
        // check will see it.
      } finally {
        busy = false;
      }
    };

    const onReturn = () => void check(false);
    const onVisibility = () => document.visibilityState === "visible" && onReturn();
    void check(true);
    const timer = window.setInterval(() => void check(true), CHECK_EVERY);
    window.addEventListener("focus", onReturn);
    window.addEventListener("online", onReturn);
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      stopped = true;
      window.clearInterval(timer);
      window.removeEventListener("focus", onReturn);
      window.removeEventListener("online", onReturn);
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [running]);

  useEffect(() => {
    if (!latest) return;
    navigator.serviceWorker
      ?.getRegistration("/admin/")
      .then((registration) => registration?.update())
      .catch(() => undefined);
  }, [latest]);

  return latest;
}
