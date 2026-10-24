import { useCallback, useState } from "react";
import { ApiError, messageOf } from "../api";
import { useRouter } from "../router";

export interface Action {
  /** run performs a change. It clears the screen's notice and error first,
   *  and on failure shows the relay's message at the top of the screen, the
   *  way a refused form used to come back. It resolves to whether the change
   *  went through. */
  run: (change: () => Promise<void>) => Promise<boolean>;
  busy: boolean;
  error: string | null;
}

export function useAction(): Action {
  const { notify } = useRouter();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const run = useCallback(
    async (change: () => Promise<void>) => {
      setBusy(true);
      setError(null);
      notify(null);
      try {
        await change();
        return true;
      } catch (err) {
        // A lost session sends the admin to sign in; there is nothing to say
        // on this screen.
        if (!(err instanceof ApiError && err.status === 401)) {
          setError(messageOf(err));
          window.scrollTo(0, 0);
        }
        return false;
      } finally {
        setBusy(false);
      }
    },
    [notify],
  );

  return { run, busy, error };
}
