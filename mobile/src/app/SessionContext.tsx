/**
 * The React face of the session.
 *
 * The session itself lives in `sessionStore`, outside any component tree, so
 * that the client, the socket and the tile handler exist before a window does
 * — see the note at the top of that file. This is the thin part: it subscribes
 * a component tree to that store and hands screens the same `useSession` they
 * always had.
 */

import React, { createContext, useContext, useMemo, useSyncExternalStore } from 'react';

import type { Client } from '../core';
import {
  answerDirection,
  claimPendingCode,
  clearError,
  forget,
  getSnapshot,
  init,
  refresh,
  setError,
  setNotice,
  subscribe,
  type DirectionChoice,
  type Question,
} from './sessionStore';

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
  /** syncing is true while a sync the store started — a tile tap — is running. */
  syncing: boolean;
  /** question is a sync waiting on the user to pick a direction. */
  question: Question | null;
  /** error is the last failure, for a screen to render and then clear. */
  error: string;
  /** pendingCode is a pairing code that arrived through a scanned link. */
  pendingCode: string;
  setError(message: string): void;
  clearError(): void;
  /** claimPendingCode hands the deep-linked code to exactly one screen. */
  claimPendingCode(): string;
  /** answer resolves that question. Dismissing is an answer: nothing is synced. */
  answer(id: number, choice: DirectionChoice | null): void;
  /** refresh re-reads the client's own state after a flow changed it. */
  refresh(): void;
  setNotice(message: string): void;
  /**
   * forget deletes this device's keys and puts the app back to its setup
   * screen, with no restart. It is how a paired device is moved to another
   * group: there is no second way in while it still holds a group key.
   */
  forget(): Promise<void>;
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
  // Opening is idempotent: two surfaces — the app and the tile's panel — mount
  // two providers over one session, and the second call does nothing.
  init();
  const snapshot = useSyncExternalStore(subscribe, getSnapshot);

  const value = useMemo<SessionState>(
    () => ({
      client: snapshot.client,
      ready: snapshot.ready,
      inGroup: snapshot.inGroup,
      connected: snapshot.connected,
      epoch: snapshot.epoch,
      notice: snapshot.notice,
      probablyRevoked: snapshot.probablyRevoked,
      syncing: snapshot.syncing,
      question: snapshot.question,
      error: snapshot.error,
      pendingCode: snapshot.pendingCode,
      answer: answerDirection,
      setError,
      clearError,
      claimPendingCode,
      refresh,
      setNotice,
      forget,
    }),
    [snapshot],
  );

  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}
