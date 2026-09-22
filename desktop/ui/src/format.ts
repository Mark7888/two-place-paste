import { useEffect, useState } from "react";

export function bytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

export function when(iso?: string): string {
  const t = parse(iso);
  return t === null ? "—" : t.toLocaleString();
}

export function relative(iso?: string): string {
  const t = parse(iso);
  if (t === null) return "never";
  const secs = Math.round((Date.now() - t.getTime()) / 1000);
  if (secs < 5) return "just now";
  if (secs < 60) return `${secs} s ago`;
  if (secs < 3600) return `${Math.round(secs / 60)} min ago`;
  if (secs < 86400) return `${Math.round(secs / 3600)} h ago`;
  return when(iso);
}

/**
 * useNow re-renders the caller on an interval, so a relative timestamp counts
 * up on its own.
 *
 * Without it "changed 2 min ago" is only ever as fresh as the last event from
 * the service — which, on a machine nobody is touching, is never: the line
 * says "2 min ago" for an hour. This is display only and asks the service for
 * nothing; the value itself still arrives by push.
 */
export function useNow(intervalMs = 30_000): void {
  const [, setTick] = useState(0);
  useEffect(() => {
    const id = window.setInterval(() => setTick((n) => n + 1), intervalMs);
    return () => window.clearInterval(id);
  }, [intervalMs]);
}

function parse(iso?: string): Date | null {
  if (!iso) return null;
  const t = new Date(iso);
  if (Number.isNaN(t.getTime()) || t.getTime() === 0) return null;
  return t;
}

/**
 * useExpiry reports whether an instant has passed, and re-renders when it does.
 *
 * A pairing code on screen after its expiry is worse than no code: it scans,
 * it looks live, and the failure only arrives on the device that read it. The
 * timer fires once, at the moment itself, rather than polling.
 */
export function useExpiry(iso?: string): boolean {
  const at = iso ? new Date(iso).getTime() : Number.NaN;
  const [expired, setExpired] = useState(() => Number.isFinite(at) && at <= Date.now());

  useEffect(() => {
    if (!Number.isFinite(at)) {
      setExpired(false);
      return;
    }
    const remaining = at - Date.now();
    if (remaining <= 0) {
      setExpired(true);
      return;
    }
    setExpired(false);
    const id = window.setTimeout(() => setExpired(true), remaining);
    return () => window.clearTimeout(id);
  }, [at]);

  return expired;
}
