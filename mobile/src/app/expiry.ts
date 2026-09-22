/**
 * Whether a pairing code has run out, as a hook.
 *
 * A code left on screen past its expiry is worse than no code at all: it still
 * scans, it still looks live, and the failure surfaces on the *other* device —
 * as "expired or unknown" from the relay, which reads like the scan went wrong
 * rather than like the code was stale.
 *
 * The timer fires once, at the instant itself, rather than polling: these
 * lifetimes are minutes, and a ticking interval behind a screen nobody is
 * looking at is work for nothing.
 */

import { useEffect, useState } from 'react';

export function useExpiry(at: Date | null | undefined): boolean {
  const ms = at instanceof Date ? at.getTime() : Number.NaN;
  const [expired, setExpired] = useState(() => Number.isFinite(ms) && ms <= Date.now());

  useEffect(() => {
    if (!Number.isFinite(ms)) {
      setExpired(false);
      return;
    }
    const remaining = ms - Date.now();
    if (remaining <= 0) {
      setExpired(true);
      return;
    }
    setExpired(false);
    const id = setTimeout(() => setExpired(true), remaining);
    return () => clearTimeout(id);
  }, [ms]);

  return expired;
}
