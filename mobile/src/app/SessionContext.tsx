/**
 * The app's session: one client, its connection state, and the rule that the
 * socket is held only while the app is foregrounded (SPEC §5.2).
 *
 * The tile's sync runs through this same session when the app is already
 * running, which is why the connection lifecycle lives here rather than in a
 * screen.
 */

import React, {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import { AppState, type AppStateStatus } from 'react-native';

import type { Client } from '../core';
import { tile } from '../platform';
import { failureMessage, openSession, sync } from './session';

/** SessionState is what the screens render from. */
export interface SessionState {
  client: Client | null;
  /** ready is false only while the secure store is being opened on launch. */
  ready: boolean;
  inGroup: boolean;
  connected: boolean;
  epoch: bigint;
  /** notice is the last thing that happened, in one line: a rekey, a pairing, a lost connection. */
  notice: string;
  /** probablyRevoked is set when the relay is up but refuses this device's socket (SPEC §3.3 step 5). */
  probablyRevoked: boolean;
  /** refresh re-reads the client's own state into React after a flow changed it. */
  refresh(): void;
  setNotice(message: string): void;
}

const SessionContext = createContext<SessionState | null>(null);

/** useSession is how a screen reaches the client. */
export function useSession(): SessionState {
  const value = useContext(SessionContext);
  if (value === null) {
    throw new Error('useSession used outside SessionProvider');
  }
  return value;
}

export function SessionProvider({ children }: { children: React.ReactNode }): React.JSX.Element {
  const [client, setClient] = useState<Client | null>(null);
  const [ready, setReady] = useState(false);
  const [version, setVersion] = useState(0);
  const [notice, setNotice] = useState('');
  const [probablyRevoked, setProbablyRevoked] = useState(false);
  const syncing = useRef(false);

  const refresh = useCallback(() => setVersion((v) => v + 1), []);

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        const opened = await openSession({
          onConnected: () => refresh(),
          onDisconnected: () => refresh(),
          onEpoch: (epoch) => {
            // A rekey installs a key and nothing else: the local clipboard is
            // never touched here (SPEC §3.3).
            setNotice(`The group was re-keyed; this device is at epoch ${epoch}.`);
            refresh();
          },
          onDeviceRevoked: () => {
            setNotice('A device was removed from the group.');
            refresh();
          },
          onDevicePaired: (device) => setNotice(`${device.name} joined the group.`),
          onProbablyRevoked: () => setProbablyRevoked(true),
        });
        if (cancelled) {
          return;
        }
        setClient(opened);
        setReady(true);
        if (opened.inGroup) {
          await opened.connect().catch((err: unknown) => setNotice(failureMessage(err)));
          refresh();
        }
      } catch (err) {
        setNotice(failureMessage(err));
        setReady(true);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [refresh]);

  // The socket follows the foreground, which is the whole of SPEC §5.2 for
  // this platform: connected while the user is here, gone when they leave.
  useEffect(() => {
    if (client === null) {
      return;
    }
    const onChange = (state: AppStateStatus) => {
      if (!client.inGroup) {
        return;
      }
      if (state === 'active') {
        void client.connect().catch(() => setNotice('The relay could not be reached.'));
      } else {
        client.disconnect();
      }
      refresh();
    };
    const subscription = AppState.addEventListener('change', onChange);
    return () => {
      subscription.remove();
      client.disconnect();
    };
  }, [client, refresh]);

  // A tile tap that reaches a running app: one sync, then the tile is told how
  // it went (SPEC §7.1).
  useEffect(() => {
    if (client === null || !tile.available) {
      return;
    }
    const runOnce = () => {
      if (syncing.current) {
        return;
      }
      syncing.current = true;
      void (async () => {
        try {
          if (!client.inGroup) {
            tile.report(false, 'TwoPlacePaste is not paired yet.');
            return;
          }
          await client.connect();
          const result = await sync(client);
          tile.report(result.direction !== 'nothing', result.message);
          setNotice(result.message);
        } catch (err) {
          tile.report(false, failureMessage(err));
          setNotice(failureMessage(err));
        } finally {
          syncing.current = false;
          refresh();
        }
      })();
    };

    const unsubscribe = tile.requests(runOnce);
    // A cold start: the tap that launched the app is waiting to be claimed.
    void tile.pending().then((pending) => {
      if (pending) {
        runOnce();
      }
    });
    return unsubscribe;
  }, [client, refresh]);

  const value = useMemo<SessionState>(
    () => ({
      client,
      ready,
      inGroup: client?.inGroup ?? false,
      connected: client?.connected ?? false,
      epoch: client?.epoch ?? 0n,
      notice,
      probablyRevoked,
      refresh,
      setNotice,
    }),
    // `version` is the dependency that makes a change inside the client — a new
    // epoch, a dropped socket — reach React, since the client is mutable and
    // is deliberately not a React value.
    [client, ready, notice, probablyRevoked, refresh, version],
  );

  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}
