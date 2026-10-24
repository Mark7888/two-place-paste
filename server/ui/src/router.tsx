// A small router over the History API. The admin has a handful of screens and
// no nested layouts, so this is all it needs, and it keeps the bundle to React
// alone.
//
// It also carries the one-off confirmation ("Group renamed.") that the old
// server-rendered screens showed after a form's redirect. The notice lives in
// memory with the location it was shown for: the next navigation clears it,
// and a reload never brings it back.

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type AnchorHTMLAttributes,
  type MouseEvent,
  type ReactNode,
} from "react";

export interface Location {
  pathname: string;
  search: string;
  /** key changes on every navigation, even to the same URL. */
  key: number;
  /** mount changes when the screen does: a navigation to another path, or
   *  to the same one through a link. It stays the same when only the query
   *  string changes in place — a search being typed, a table paged — so the
   *  screen keeps its state and the search box keeps its focus. */
  mount: number;
}

export interface NavigateOptions {
  replace?: boolean;
  /** notice is shown on the screen navigated to. */
  notice?: string;
  /** stay changes the URL without mounting the screen afresh: for state that
   *  lives in the query string. A notice on screen stays, too. */
  stay?: boolean;
}

interface RouterValue {
  location: Location;
  notice: string | null;
  navigate: (to: string, options?: NavigateOptions) => void;
  /** notify shows a notice on the current screen, or clears it with null. */
  notify: (notice: string | null) => void;
}

const RouterContext = createContext<RouterValue | null>(null);

function here(key: number, mount: number): Location {
  return { pathname: window.location.pathname, search: window.location.search, key, mount };
}

export function RouterProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState(() => ({ location: here(0, 0), notice: null as string | null }));

  useEffect(() => {
    // Back and forward within one screen — through searches and pages — keep
    // it mounted; to another screen they mount that one.
    const onPop = () =>
      setState((s) => {
        const same = window.location.pathname === s.location.pathname;
        return {
          location: here(s.location.key + 1, same ? s.location.mount : s.location.mount + 1),
          notice: same ? s.notice : null,
        };
      });
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, []);

  const navigate = useCallback((to: string, options: NavigateOptions = {}) => {
    if (options.replace) window.history.replaceState(null, "", to);
    else window.history.pushState(null, "", to);
    setState((s) => ({
      location: here(s.location.key + 1, options.stay ? s.location.mount : s.location.mount + 1),
      notice: options.stay ? s.notice : (options.notice ?? null),
    }));
    if (!options.stay) window.scrollTo(0, 0);
  }, []);

  const notify = useCallback((notice: string | null) => {
    setState((s) => ({ ...s, notice }));
    // A notice is at the top of the page; the form that caused it may not be.
    if (notice) window.scrollTo(0, 0);
  }, []);

  const value = useMemo(
    () => ({ location: state.location, notice: state.notice, navigate, notify }),
    [state, navigate, notify],
  );
  return <RouterContext.Provider value={value}>{children}</RouterContext.Provider>;
}

export function useRouter(): RouterValue {
  const value = useContext(RouterContext);
  if (!value) throw new Error("useRouter is used outside RouterProvider");
  return value;
}

/** ParamPatch changes query parameters: a string sets one, null removes it. */
export type ParamPatch = Record<string, string | null>;

/** withParams is the current path with its query string patched. Parameters
 *  are written in a fixed order, so the same state is always the same URL. */
export function withParams(location: Pick<Location, "pathname" | "search">, patch: ParamPatch): string {
  const params = new URLSearchParams(location.search);
  for (const [name, value] of Object.entries(patch)) {
    if (value === null || value === "") params.delete(name);
    else params.set(name, value);
  }
  params.sort();
  const query = params.toString();
  return query ? `${location.pathname}?${query}` : location.pathname;
}

/** useSearchParams reads the query string and changes it in place, without
 *  mounting the screen again. A replace is for state that changes as fast as
 *  someone types; a push is for steps worth going back to. */
export function useSearchParams(): [URLSearchParams, (patch: ParamPatch, options?: { replace?: boolean }) => void] {
  const { location, navigate } = useRouter();
  const params = useMemo(() => new URLSearchParams(location.search), [location.search]);
  const update = useCallback(
    (patch: ParamPatch, options: { replace?: boolean } = {}) => {
      // Read the URL as it is now, not as it was at the last render: two
      // changes in one tick must not undo each other.
      const to = withParams(window.location, patch);
      if (to === window.location.pathname + window.location.search) return;
      navigate(to, { replace: options.replace, stay: true });
    },
    [navigate],
  );
  return [params, update];
}

/** useQueryParam reads one parameter of the current URL's query string. */
export function useQueryParam(name: string): string {
  const { location } = useRouter();
  return new URLSearchParams(location.search).get(name) ?? "";
}

/** matchPath matches a pathname against a pattern such as
 *  "/admin/groups/:handle" and returns the decoded parameters, or null. */
export function matchPath(pattern: string, pathname: string): Record<string, string> | null {
  const want = pattern.split("/");
  const got = pathname.split("/");
  if (want.length !== got.length) return null;
  const params: Record<string, string> = {};
  for (let i = 0; i < want.length; i++) {
    const w = want[i];
    const g = got[i];
    if (w.startsWith(":")) {
      if (g === "") return null;
      try {
        params[w.slice(1)] = decodeURIComponent(g);
      } catch {
        return null;
      }
    } else if (w !== g) {
      return null;
    }
  }
  return params;
}

type LinkProps = Omit<AnchorHTMLAttributes<HTMLAnchorElement>, "href"> & {
  to: string;
  /** stay navigates in place, as useSearchParams does: for links that only
   *  change the query string, such as a filter. */
  stay?: boolean;
};

/** Link is an <a> that navigates inside the app. A click with a modifier key,
 *  or any button but the main one, is left to the browser, so "open in a new
 *  tab" still works. */
export function Link({ to, stay, onClick, children, ...rest }: LinkProps) {
  const { navigate } = useRouter();
  const handleClick = (event: MouseEvent<HTMLAnchorElement>) => {
    onClick?.(event);
    if (
      event.defaultPrevented ||
      event.button !== 0 ||
      event.metaKey ||
      event.ctrlKey ||
      event.shiftKey ||
      event.altKey ||
      rest.target
    ) {
      return;
    }
    event.preventDefault();
    navigate(to, { stay });
  };
  return (
    <a href={to} onClick={handleClick} {...rest}>
      {children}
    </a>
  );
}
