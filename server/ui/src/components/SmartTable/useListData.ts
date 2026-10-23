import { useCallback, useEffect, useState } from "react";
import { BASE, isAbort, request } from "../../api/client";
import type { TablePage } from "../../api";

export interface ListData<R> {
  /** data is the last page that arrived, kept while a newer one loads or
   *  after a newer one fails, so the table does not flash. */
  data: R | null;
  /** dataQuery is the query data answers. */
  dataQuery: string | null;
  loading: boolean;
  error: Error | null;
  reload: () => void;
}

/** useListData fetches one page of a list whenever its query changes. A
 *  request that a newer one overtakes is cancelled, and its answer dropped
 *  even if it arrives anyway. */
export function useListData<R extends TablePage<unknown>>(endpoint: string, query: string): ListData<R> {
  const [state, setState] = useState<{ data: R | null; dataQuery: string | null; loading: boolean; error: Error | null }>({
    data: null,
    dataQuery: null,
    loading: true,
    error: null,
  });
  const [nonce, setNonce] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    const path = (endpoint.startsWith(BASE) ? endpoint.slice(BASE.length) : endpoint) + "?" + query;
    setState((s) => ({ ...s, loading: true }));
    request<R>("GET", path, undefined, controller.signal).then(
      (data) => {
        if (controller.signal.aborted) return;
        setState({ data, dataQuery: query, loading: false, error: null });
      },
      (err: unknown) => {
        if (controller.signal.aborted || isAbort(err)) return;
        setState((s) => ({ ...s, loading: false, error: err instanceof Error ? err : new Error(String(err)) }));
      },
    );
    return () => controller.abort();
  }, [endpoint, query, nonce]);

  const reload = useCallback(() => setNonce((n) => n + 1), []);
  return { ...state, reload };
}
