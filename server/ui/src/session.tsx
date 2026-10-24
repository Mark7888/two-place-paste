// Who is signed in. The session itself is an HttpOnly cookie the app never
// sees; what it holds is whether there is one, and the CSRF token the relay
// hands out with it.

import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { ApiError, api, isAbort, messageOf, setCsrfToken, setUnauthorizedHandler } from "./api";

export type SessionStatus = "loading" | "signed-in" | "signed-out" | "error";

interface SessionValue {
  status: SessionStatus;
  /** error says why the session could not be checked, when status is "error". */
  error: string | null;
  signIn: (password: string) => Promise<void>;
  signOut: () => Promise<void>;
}

const SessionContext = createContext<SessionValue | null>(null);

export function SessionProvider({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<SessionStatus>("loading");
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    api.session(controller.signal).then(
      (s) => {
        setCsrfToken(s.csrf);
        setStatus("signed-in");
      },
      (err: unknown) => {
        if (isAbort(err)) return;
        if (err instanceof ApiError && err.status === 401) {
          setStatus("signed-out");
          return;
        }
        setError(messageOf(err));
        setStatus("error");
      },
    );
    return () => controller.abort();
  }, []);

  // Any request that finds the session gone signs the app out, which sends
  // the admin to the sign-in screen.
  useEffect(() => {
    setUnauthorizedHandler(() => {
      setCsrfToken("");
      setStatus("signed-out");
    });
    return () => setUnauthorizedHandler(null);
  }, []);

  const signIn = useCallback(async (password: string) => {
    const s = await api.login(password);
    setCsrfToken(s.csrf);
    setError(null);
    setStatus("signed-in");
  }, []);

  const signOut = useCallback(async () => {
    try {
      await api.logout();
    } finally {
      setCsrfToken("");
      setStatus("signed-out");
    }
  }, []);

  const value = useMemo(() => ({ status, error, signIn, signOut }), [status, error, signIn, signOut]);
  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}

export function useSession(): SessionValue {
  const value = useContext(SessionContext);
  if (!value) throw new Error("useSession is used outside SessionProvider");
  return value;
}
