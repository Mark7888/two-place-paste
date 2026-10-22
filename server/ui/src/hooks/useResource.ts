import { useCallback, useEffect, useState, type DependencyList } from "react";
import { isAbort } from "../api";

export interface Resource<T> {
  data: T | null;
  error: Error | null;
  loading: boolean;
  /** version counts successful loads. Forms are keyed by it, so that after a
   *  save they show what the relay stored rather than what was typed. */
  version: number;
  reload: () => void;
}

/** useResource loads a screen's data, again whenever deps change or reload is
 *  called. A load that is overtaken by a newer one, or by the screen going
 *  away, is cancelled and its answer dropped. The last good data stays while
 *  a reload runs or after it fails. */
export function useResource<T>(load: (signal: AbortSignal) => Promise<T>, deps: DependencyList): Resource<T> {
  const [state, setState] = useState<{ data: T | null; error: Error | null; loading: boolean; version: number }>({
    data: null,
    error: null,
    loading: true,
    version: 0,
  });
  const [nonce, setNonce] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    setState((s) => ({ ...s, loading: true }));
    load(controller.signal).then(
      (data) => setState((s) => ({ data, error: null, loading: false, version: s.version + 1 })),
      (err: unknown) => {
        if (controller.signal.aborted || isAbort(err)) return;
        const error = err instanceof Error ? err : new Error(String(err));
        setState((s) => ({ ...s, error, loading: false }));
      },
    );
    return () => controller.abort();
    // `load` is a new function on every render; deps say when it changed.
  }, [...deps, nonce]);

  const reload = useCallback(() => setNonce((n) => n + 1), []);
  return { ...state, reload };
}
