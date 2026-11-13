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

import type { Client, ParkedGroup, Progress } from '../core';
import {
  answerQuestion,
  claimPendingCode,
  clearError,
  addGroup,
  closeDemo,
  forget,
  getSnapshot,
  init,
  refresh,
  removeGroup,
  setError,
  setHideSpaceBanners,
  setMultiGroup,
  setNotice,
  startDemo,
  subscribe,
  switchGroup,
  type Answer,
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
  /** groupDeleted is set when the relay has said the server admin deleted this device's group. */
  groupDeleted: boolean;
  /** groupName is the group's display name as the server admin set it, or "". */
  groupName: string;
  /** syncing is true while a sync the store started — a tile tap — is running. */
  syncing: boolean;
  /** question is a sync waiting on the user: a direction to pick, or what to do with a file. */
  question: Question | null;
  /** error is the last failure, for a screen to render and then clear. */
  error: string;
  /** pendingCode is a pairing code that arrived through a scanned link. */
  pendingCode: string;
  /** transfers are the large entries moving in chunks right now, or waiting to. */
  transfers: readonly Progress[];
  /** evicted is the last entry the relay removed early; seq changes with each one. */
  evicted: { id: string; seq: number } | null;
  /** demo is true while the demo runs in place of this device's real group (docs/internal/plans/demo-mode.md). */
  demo: boolean;
  /** demoAvailable is true while the setup screen may offer the demo: until this device first joins a real group. */
  demoAvailable: boolean;
  /** multiGroup is the "Keep several groups" setting: it shows the Groups tab. */
  multiGroup: boolean;
  /** operatorLink is the relay operator's website, with the group id added, or null. */
  operatorLink: string | null;
  /** hideSpaceBanners is the "Hide out-of-space banners" setting. */
  hideSpaceBanners: boolean;
  /** activeRef names the group in use, or is "" in none. */
  activeRef: string;
  /** parkedGroups are the groups set aside on this device. */
  parkedGroups: readonly ParkedGroup[];
  setError(message: string): void;
  clearError(): void;
  /** claimPendingCode hands the deep-linked code to exactly one screen. */
  claimPendingCode(): string;
  /** answer resolves that question. Dismissing is an answer: nothing is synced. */
  answer(id: number, choice: Answer | null): void;
  /** refresh re-reads the client's own state after a flow changed it. */
  refresh(): void;
  setNotice(message: string): void;
  /**
   * forget deletes this device's keys and puts the app back to its setup
   * screen, with no restart. It is how a paired device is moved to another
   * group: there is no second way in while it still holds a group key.
   */
  forget(): Promise<void>;
  /** startDemo starts the demo from the setup screen. A failure is left in `error`. */
  startDemo(): Promise<void>;
  /** closeDemo ends the demo and returns to the setup screen, with the device's real keys as they were. */
  closeDemo(): void;
  /** setMultiGroup turns "Keep several groups" on or off. Off keeps every group. */
  setMultiGroup(on: boolean): Promise<void>;
  /** setHideSpaceBanners turns "Hide out-of-space banners" on or off. */
  setHideSpaceBanners(on: boolean): Promise<void>;
  /** switchGroup makes a group set aside the one in use, and connects it. */
  switchGroup(ref: string): Promise<void>;
  /** addGroup sets the group in use aside; the setup screen then creates or joins the new one. */
  addGroup(): Promise<void>;
  /** removeGroup deletes the keys of a group set aside. */
  removeGroup(ref: string): Promise<void>;
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
      groupDeleted: snapshot.groupDeleted,
      groupName: snapshot.groupName,
      syncing: snapshot.syncing,
      question: snapshot.question,
      error: snapshot.error,
      pendingCode: snapshot.pendingCode,
      transfers: snapshot.transfers,
      evicted: snapshot.evicted,
      demo: snapshot.demo,
      demoAvailable: snapshot.demoAvailable,
      multiGroup: snapshot.multiGroup,
      operatorLink: snapshot.operatorLink,
      hideSpaceBanners: snapshot.hideSpaceBanners,
      activeRef: snapshot.activeRef,
      parkedGroups: snapshot.parkedGroups,
      answer: answerQuestion,
      setError,
      clearError,
      claimPendingCode,
      refresh,
      setNotice,
      forget,
      startDemo,
      closeDemo,
      setMultiGroup,
      setHideSpaceBanners,
      switchGroup,
      addGroup,
      removeGroup,
    }),
    [snapshot],
  );

  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}
