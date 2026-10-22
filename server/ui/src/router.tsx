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
  /** key changes on every navigation, even to the same URL, so a screen can
   *  be mounted afresh and load its data again. */
  key: number;
}

export interface NavigateOptions {
  replace?: boolean;
  /** notice is shown on the screen navigated to. */
  notice?: string;
}

interface RouterValue {
  location: Location;
  notice: string | null;
  navigate: (to: string, options?: NavigateOptions) => void;
  /** notify shows a notice on the current screen, or clears it with null. */
  notify: (notice: string | null) => void;
}

const RouterContext = createContext<RouterValue | null>(null);

function here(key: number): Location {
  return { pathname: window.location.pathname, search: window.location.search, key };
}

export function RouterProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState(() => ({ location: here(0), notice: null as string | null }));

  useEffect(() => {
    const onPop = () => setState((s) => ({ location: here(s.location.key + 1), notice: null }));
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, []);

  const navigate = useCallback((to: string, options: NavigateOptions = {}) => {
    if (options.replace) window.history.replaceState(null, "", to);
    else window.history.pushState(null, "", to);
    setState((s) => ({ location: here(s.location.key + 1), notice: options.notice ?? null }));
    window.scrollTo(0, 0);
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

/** useQueryParam reads one parameter of the current URL's query string. */
export function useQueryParam(name: string): string {
  const { location } = useRouter();
  return new URLSearchParams(location.search).get(name) ?? "";
}

/** matchPath matches a pathname against a pattern such as
 *  "/admin/groups/:id" and returns the decoded parameters, or null. */
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

type LinkProps = Omit<AnchorHTMLAttributes<HTMLAnchorElement>, "href"> & { to: string };

/** Link is an <a> that navigates inside the app. A click with a modifier key,
 *  or any button but the main one, is left to the browser, so "open in a new
 *  tab" still works. */
export function Link({ to, onClick, children, ...rest }: LinkProps) {
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
    navigate(to);
  };
  return (
    <a href={to} onClick={handleClick} {...rest}>
      {children}
    </a>
  );
}
