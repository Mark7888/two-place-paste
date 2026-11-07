/**
 * The app's session, as a module rather than as React state.
 *
 * It used to live inside `SessionProvider`, which meant the client, the socket
 * and the tile handler only existed while a React tree was mounted. That was
 * fine while the app had one screen to mount — and it is exactly what stopped
 * a quick-settings tap from being answered by anything smaller than the whole
 * app: the tile's handler was an effect, and an effect needs a surface.
 *
 * Here the session is created when the JavaScript bundle loads (`index.js`
 * calls `init`), so it is running before any window is on screen and is shared
 * by every window that appears. That is what lets the tile open a small panel
 * over whatever the user is doing — a second React surface on the same
 * JavaScript context — instead of launching the app (SPEC §7.1).
 *
 * The rules it keeps are the ones that were in the provider:
 *
 *  - the socket follows the foreground and nothing else (SPEC §5.2);
 *  - a tile tap produces exactly one sync, never two;
 *  - a rekey installs a key and never touches the clipboard (SPEC §3.3).
 *
 * And one it adds: a sync that needs the user — it cannot tell which way to go,
 * or it received a file the clipboard cannot hold — asks, through `question`,
 * and waits. Whichever surface is on screen renders it.
 */

import { AppState, Linking, type AppStateStatus } from 'react-native';

import type { Client, Handlers, ParkedGroup, Progress } from '../core';
import { secureStore, tile } from '../platform';
import { stripCodeEnvelope } from '../core';
import {
  download,
  failureMessage,
  handleFile,
  isFileChoice,
  openSession,
  sync,
  upload,
  type FileChoice,
} from './session';
import { DemoGate, DemoMode } from './demo';
import { clearStore, invalidate as invalidateStore, storeEvents } from './storeState';

/** DirectionChoice is what the user answers a direction question with. */
export type DirectionChoice = 'up' | 'down';

/** Answer is any answer to a pending question: a direction, or what to do with a file. */
export type Answer = DirectionChoice | FileChoice;

/**
 * Question is a sync waiting on the user. `id` changes for every question, so
 * a late answer to one that has already been resolved is ignored rather than
 * applied to the next one.
 *
 * `direction` asks which way to sync; `file` asks what happens to a received
 * entry the clipboard cannot hold — saved to Downloads, opened or shared, or
 * copied when `canCopy` says it can be — and `name` is how it is described to
 * the user.
 */
export type Question = {
  id: number;
  /** fromTile is true when the panel that asked it was opened by the tile. */
  fromTile: boolean;
} & ({ kind: 'direction' } | FileQuestion);

/** FileQuestion is the `file` kind of question: which entry it is, and whether Copy is offered. */
type FileQuestion = { kind: 'file'; name: string; detail: string; canCopy: boolean };

/** Snapshot is what a screen renders from. */
export interface Snapshot {
  client: Client | null;
  /** ready is false only while the secure store is being opened on launch. */
  ready: boolean;
  inGroup: boolean;
  connected: boolean;
  epoch: bigint;
  /** notice is the last thing that happened, in one line. */
  notice: string;
  /** probablyRevoked is set when the relay is up but refuses this device's socket (SPEC §3.3 step 5). */
  probablyRevoked: boolean;
  /** groupDeleted is set when the relay has said the server admin deleted this device's group. */
  groupDeleted: boolean;
  /** groupName is the group's display name as the server admin set it, or "". */
  groupName: string;
  /** syncing is true while a sync this store started is running. */
  syncing: boolean;
  /** question is a direction the user has to choose before a sync can go on. */
  question: Question | null;
  /**
   * error is the last thing that went wrong, and is cleared when it has been
   * shown. It is kept apart from `notice` because the two are not the same
   * kind of message: a notice is what happened, an error is what did not.
   */
  error: string;
  /**
   * pendingCode is a pairing code that arrived from outside the app — a
   * scanned link opened through the tpp:// scheme. The pairing screen claims
   * it; until one does, it waits rather than being dropped, because the app
   * may still have been starting when the link arrived.
   */
  pendingCode: string;
  /**
   * transfers are the chunked uploads and downloads in flight, oldest first:
   * one running, and any others waiting their turn (`Progress.waiting`).
   */
  transfers: readonly Progress[];
  /**
   * evicted is the last entry the relay removed early to stay within its
   * storage limit; seq changes with each one, so a screen can act on every
   * eviction, not just the first.
   */
  evicted: { id: string; seq: number } | null;
  /** demo is true while the demo runs in place of the device's real client. */
  demo: boolean;
  /** demoAvailable is true while the setup screen may offer the demo: until this device first joins a real group. */
  demoAvailable: boolean;
  /** multiGroup is the "Keep several groups" setting (docs/internal/plans/multi-group-mobile.md). */
  multiGroup: boolean;
  /** activeRef names the group in use, or is "" in none. */
  activeRef: string;
  /** parkedGroups are the groups set aside on this device. Empty in the demo. */
  parkedGroups: readonly ParkedGroup[];
}

/**
 * MULTI_GROUP_NAME is the secure-store entry that holds the "Keep several
 * groups" setting: "1", or nothing. The app has no other settings store, and
 * the demo gate keeps its flag the same way.
 */
export const MULTI_GROUP_NAME = 'multiGroup';

type Listener = () => void;

let client: Client | null = null;
let ready = false;
let notice = '';
let probablyRevoked = false;
let syncing = false;
let question: Question | null = null;
let lastError = '';
let pendingCode = '';
/** transfers is keyed by direction and entry id, so an entry's progress replaces its own last report. */
const transfers = new Map<string, Progress>();
let evicted: { id: string; seq: number } | null = null;
let answer: ((choice: Answer | null) => void) | null = null;
/** demoMode holds the real client aside while a demo runs. */
const demoMode = new DemoMode();
/** gate is read once the secure store is open; until then the demo is not offered. */
let gate: DemoGate | null = null;
let nextQuestionId = 1;
/** multiGroup is the "Keep several groups" setting, read once the secure store is open. */
let multiGroup = false;

let started = false;
const listeners = new Set<Listener>();

// The snapshot is rebuilt only when something changes, because
// `useSyncExternalStore` compares it by identity and would loop forever on a
// fresh object per read.
let snapshot: Snapshot = {
  client: null,
  ready: false,
  inGroup: false,
  connected: false,
  epoch: 0n,
  notice: '',
  probablyRevoked: false,
  groupDeleted: false,
  groupName: '',
  syncing: false,
  question: null,
  error: '',
  pendingCode: '',
  transfers: [],
  evicted: null,
  demo: false,
  demoAvailable: false,
  multiGroup: false,
  activeRef: '',
  parkedGroups: [],
};

function emit(): void {
  // Every change passes through here, so this is where a first real group is
  // noticed — whichever flow joined it.
  gate?.observe(client?.inGroup ?? false, demoMode.active);
  snapshot = {
    client,
    ready,
    inGroup: client?.inGroup ?? false,
    connected: client?.connected ?? false,
    epoch: client?.epoch ?? 0n,
    notice,
    probablyRevoked,
    groupDeleted: client?.groupDeleted ?? false,
    groupName: client?.inGroup ? client.groupName : '',
    syncing,
    question,
    error: lastError,
    pendingCode,
    transfers: [...transfers.values()],
    evicted,
    demo: demoMode.active,
    demoAvailable: !demoMode.active && (gate?.available ?? false),
    multiGroup,
    activeRef: demoMode.active ? '' : (client?.activeRef ?? ''),
    parkedGroups: demoMode.active ? [] : (client?.parkedGroups() ?? []),
  };
  for (const listener of listeners) {
    listener();
  }
}

export function subscribe(listener: Listener): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function getSnapshot(): Snapshot {
  return snapshot;
}

/** refresh re-reads the client's own state, which is mutable and deliberately not a React value. */
export function refresh(): void {
  emit();
}

export function setNotice(message: string): void {
  notice = message;
  emit();
}

/** setError records a failure for a screen to render. */
export function setError(message: string): void {
  lastError = message;
  emit();
}

/** clearError drops an error the user has seen. */
export function clearError(): void {
  if (lastError !== '') {
    lastError = '';
    emit();
  }
}

/**
 * claimPendingCode hands a deep-linked pairing code to exactly one screen and
 * clears it, so returning to that screen later does not re-run the pairing.
 */
export function claimPendingCode(): string {
  const code = pendingCode;
  pendingCode = '';
  if (code !== '') {
    emit();
  }
  return code;
}

/**
 * forget deletes this device's keys and puts the app back to its setup screen,
 * with no restart. It is how a paired device is moved to another group: there
 * is no second way in while it still holds a group key.
 */
export async function forget(): Promise<void> {
  if (demoMode.active) {
    closeDemo();
    return;
  }
  if (client === null) {
    return;
  }
  await client.forget();
  clearStore();
  probablyRevoked = false;
  notice = '';
  emit();
}

/**
 * setMultiGroup turns "Keep several groups" on or off. Off hides the Groups
 * tab and nothing else: the group in use stays in use and the others keep
 * their keys.
 */
export async function setMultiGroup(on: boolean): Promise<void> {
  if (on) {
    await secureStore.save(MULTI_GROUP_NAME, '1');
  } else {
    await secureStore.clear(MULTI_GROUP_NAME);
  }
  multiGroup = on;
  emit();
}

/** groupClient is the client the group operations act on, when they may run at all. */
function groupClient(): Client {
  if (!multiGroup) {
    throw new Error('Turn on “Keep several groups” in Settings to use more than one group.');
  }
  if (demoMode.active) {
    throw new Error('Close the demo first.');
  }
  if (client === null) {
    throw new Error('TwoPlacePaste is still starting.');
  }
  if (syncing) {
    throw new Error('Wait for the sync to finish first.');
  }
  return client;
}

/**
 * switchGroup makes a group set aside the one in use. The client stops the
 * group in use as the app closing would, and is connected again as a launch
 * with the other group would be — if the app is on screen.
 */
export async function switchGroup(ref: string): Promise<void> {
  const c = groupClient();
  await c.switchGroup(ref);
  resetScreens();
  notice = `Switched to ${c.groupName !== '' ? c.groupName : 'the other group'}.`;
  emit();
  if (AppState.currentState === 'active') {
    await c.connect().catch((err: unknown) => setNotice(failureMessage(err)));
    emit();
  }
}

/**
 * addGroup sets the group in use aside. With no group key the shell shows the
 * setup screen, where the new group is created or joined — and where the
 * groups set aside are offered too, so the user is never stuck there.
 */
export async function addGroup(): Promise<void> {
  const c = groupClient();
  await c.parkActive();
  resetScreens();
  emit();
}

/** removeGroup deletes the keys of a group set aside. It is local, like leaving. */
export async function removeGroup(ref: string): Promise<void> {
  const c = groupClient();
  await c.removeParked(ref);
  emit();
}

/** resetScreens drops what the screens showed of the group that is going away. */
function resetScreens(): void {
  clearStore();
  transfers.clear();
  probablyRevoked = false;
  notice = '';
  lastError = '';
  evicted = null;
}

/**
 * startDemo puts a demo group in place of the device's real client
 * (docs/internal/plans/demo-mode.md). Only a device that has never been in a
 * real group is offered it, and only from the setup screen.
 */
export async function startDemo(): Promise<void> {
  const real = client;
  if (real === null || real.inGroup || demoMode.active || !(gate?.available ?? false)) {
    return;
  }
  try {
    const demo = await demoMode.start(real, {
      handlers,
      onActivity: (message) => setNotice(message),
    });
    client = demo;
    resetScreens();
    demoMode.setForeground(AppState.currentState === 'active');
    emit();
    await demo.connect().catch((err: unknown) => setNotice(failureMessage(err)));
  } catch (err) {
    lastError = failureMessage(err);
  }
  emit();
}

/**
 * closeDemo ends the demo and puts the device's real client back, which is
 * still on the setup screen: the demo never touched it.
 */
export function closeDemo(): void {
  const real = demoMode.stop();
  if (real === null) {
    return;
  }
  client = real;
  resetScreens();
  emit();
}

/**
 * answerQuestion resolves a pending question. `null` means the user dismissed
 * it, which is an answer: nothing is read and nothing is written.
 */
export function answerQuestion(id: number, choice: Answer | null): void {
  if (question === null || question.id !== id) {
    return;
  }
  const resolve = answer;
  question = null;
  answer = null;
  emit();
  resolve?.(choice);
}

/** ask puts a question and waits for whichever surface is on screen to answer it. */
function ask(
  fromTile: boolean,
  what: { kind: 'direction' } | FileQuestion,
): Promise<Answer | null> {
  return new Promise((resolve) => {
    question = { id: nextQuestionId++, fromTile, ...what };
    answer = resolve;
    emit();
  });
}

/** progressListeners hear every transfer's progress, as the screens' list does. */
const progressListeners = new Set<(p: Progress) => void>();

/**
 * onTransferProgress subscribes to the progress of every chunked transfer —
 * how the background uploads move their notification along. The returned
 * function unsubscribes.
 */
export function onTransferProgress(listener: (p: Progress) => void): () => void {
  progressListeners.add(listener);
  return () => {
    progressListeners.delete(listener);
  };
}

/**
 * handlers are the client's callbacks. The real client and a demo's are given
 * the same ones, so every screen behaves the same in either.
 */
const handlers: Handlers = {
  onConnected: () => {
    // Store events sent while this device was away were missed: the
    // store is listed again when next needed.
    invalidateStore();
    emit();
  },
  onDisconnected: () => emit(),
  onEpoch: (epoch) => {
    // A rekey installs a key and nothing else: the local clipboard is
    // never touched here (SPEC §3.3).
    notice = `The group was re-keyed; this device is at epoch ${epoch}.`;
    emit();
  },
  onDeviceRevoked: () => {
    notice = 'A device was removed from the group.';
    emit();
  },
  onDevicePaired: (device) => {
    notice = `${device.name} joined the group.`;
    emit();
  },
  onProbablyRevoked: () => {
    probablyRevoked = true;
    emit();
  },
  onGroupDeleted: () => {
    notice = 'The server admin deleted this group.';
    emit();
  },
  onGroupName: () => emit(),
  onStoreItemAdded: storeEvents.put,
  onStoreItemUpdated: storeEvents.put,
  onStoreItemDeleted: storeEvents.remove,
  onStoreState: storeEvents.state,
  onEntryEvicted: (id) => {
    evicted = { id, seq: (evicted?.seq ?? 0) + 1 };
    emit();
  },
  onProgress: (p) => {
    for (const listener of progressListeners) {
      listener(p);
    }
    const key = `${p.direction}:${p.entryId}`;
    if (p.done < p.total) {
      transfers.set(key, p);
    } else {
      transfers.delete(key);
    }
    emit();
  },
};

/**
 * init opens the session. It is called once, from `index.js`, before any
 * window exists — which is the point: the tile's handler has to be listening
 * whether or not a screen is mounted.
 */
export function init(): void {
  if (started) {
    return;
  }
  started = true;

  void (async () => {
    try {
      const opened = await openSession(handlers);
      client = opened;
      gate = new DemoGate(secureStore);
      await gate.load();
      multiGroup = await secureStore.load(MULTI_GROUP_NAME).then(
        (v) => v === '1',
        // A setting that cannot be read is the default: off.
        () => false,
      );
      ready = true;
      emit();
      if (opened.inGroup) {
        await opened.connect().catch((err: unknown) => setNotice(failureMessage(err)));
        emit();
      }
      watchForeground();
      // A tap that arrived while the store was opening has been held; this is
      // where it is answered.
      drainTileRequest();
    } catch (err) {
      notice = failureMessage(err);
      ready = true;
      emit();
    }
  })();

  // Subscribed before the secure store has finished opening, and deliberately:
  // on a cold start the tap that launched this process is already waiting, and
  // a listener attached after the client is ready would be attached after the
  // event it exists for.
  watchTile();
  watchLinks();
}

/**
 * watchLinks takes in pairing codes that arrive from outside the app.
 *
 * A QR code carries a link to the user's own relay; the page that link lands
 * on hands the code back through this app's `tpp://` scheme. Both paths reach
 * here — the cold start that the link caused (`getInitialURL`) and the link
 * that arrives while the app is already up (the `url` event).
 *
 * The code is only ever *stored*. Nothing is paired from a link on its own:
 * admitting a device hands over the group key, so the decision stays with the
 * user on a screen that shows what they are agreeing to (SPEC §3.2).
 */
function watchLinks(): void {
  const take = (url: string | null | undefined) => {
    if (url === null || url === undefined || url === '') {
      return;
    }
    const code = stripCodeEnvelope(url);
    // A link with no fragment is not a code, and `stripCodeEnvelope` would
    // hand back the whole URL. Nothing is stored in that case.
    if (code === '' || code === url.trim()) {
      return;
    }
    if (demoMode.active) {
      // A code from a real relay means nothing to the demo's, which would
      // refuse to dial it anyway. Said plainly rather than failed later.
      lastError = 'Close the demo first to join a real group.';
      emit();
      return;
    }
    pendingCode = code;
    emit();
  };

  Linking.addEventListener('url', (event) => take(event.url));
  void Linking.getInitialURL()
    .then(take)
    .catch(() => {
      // A platform that cannot answer simply has no link waiting.
    });
}

/**
 * watchForeground is the whole of SPEC §5.2 for this platform: connected while
 * the user is here, gone when they leave. The overlay counts as here — it is a
 * window of this app, and the sync it was opened for needs the socket.
 */
function watchForeground(): void {
  AppState.addEventListener('change', (state: AppStateStatus) => {
    // The client is looked up on every change: a demo swaps it.
    demoMode.setForeground(state === 'active');
    const c = client;
    if (c === null || !c.inGroup) {
      return;
    }
    if (state === 'active') {
      void c.connect().catch(() => setNotice('The relay could not be reached.'));
    } else {
      c.disconnect();
    }
    emit();
  });
}

/** held is a tile tap that arrived before the client was open. */
let held = false;

/**
 * watchTile answers a tile tap with exactly one sync (SPEC §7.1).
 *
 * A tap can find this module in any of three states, and all three end in one
 * sync: the client is open and the tap runs now; the client is still opening
 * and the tap is held until it is; or this process was started *by* the tap,
 * in which case the native side has it recorded and hands it over once.
 */
function watchTile(): void {
  if (!tile.available) {
    return;
  }
  tile.requests(runTileSync);
  // A cold start: the tap that launched this process is waiting to be claimed.
  // The flag is claimed exactly once, so a tap can never produce two syncs.
  void tile.pending().then((pending) => {
    if (pending) {
      runTileSync();
    }
  });
}

/** drainTileRequest runs a tap that arrived while the store was still opening. */
function drainTileRequest(): void {
  if (held) {
    held = false;
    runTileSync();
  }
}

function runTileSync(): void {
  const c = client;
  if (c === null) {
    // Still opening the secure store. Hold it; init runs it once the client
    // exists, so a tap during a cold start is answered rather than dropped.
    held = true;
    return;
  }
  if (syncing) {
    return;
  }
  syncing = true;
  emit();

  const finish = (ok: boolean, message: string) => {
    tile.report(ok, message);
    if (ok) {
      notice = message;
      lastError = '';
      emit();
      // Nothing left to say: the panel is a window over whatever the user was
      // doing, so it goes, and the tile's subtitle carries the outcome.
      tile.closeOverlay();
      return;
    }
    // A failure is not dismissed on the user's behalf. The panel stays up
    // holding the reason, with a button to close it — a toast that vanishes in
    // two seconds is how a sync silently not happening looks like a sync.
    lastError = message;
    notice = '';
    emit();
  };

  // The user dismissed a question: not a failure, so nothing is left on screen.
  const cancel = (message: string) => {
    tile.report(false, message);
    notice = message;
    emit();
    tile.closeOverlay();
  };

  void (async () => {
    try {
      if (!c.inGroup) {
        finish(false, 'TwoPlacePaste is not paired yet.');
        return;
      }
      await c.connect();
      let result = await sync(c);
      if (result.direction === 'ambiguous') {
        // A tap is the one moment this app may read the clipboard, so a
        // direction it cannot work out is a question to put now, on the panel
        // that is already on screen — not a toast saying the choice was the
        // user's to make, on a surface with no way to make it.
        const choice = await ask(true, { kind: 'direction' });
        if (choice === null) {
          cancel('No direction chosen; nothing was synced.');
          return;
        }
        result = choice === 'up' ? await upload(c) : await download(c);
      }
      if (result.direction === 'file' && result.file !== undefined) {
        // A file the clipboard cannot hold: keep it, look at it now, or send
        // it on. It is asked before the download, so the panel shows which
        // file it is.
        const file = result.file;
        const choice = await ask(true, {
          kind: 'file',
          name: file.name,
          detail: file.detail,
          canCopy: file.canCopy,
        });
        if (!isFileChoice(choice) || (choice === 'copy' && !file.canCopy)) {
          cancel('The file was not saved, opened or shared.');
          return;
        }
        result = await handleFile(c, file, choice);
      }
      finish(result.direction !== 'nothing', result.message);
    } catch (err) {
      finish(false, failureMessage(err));
    } finally {
      syncing = false;
      emit();
    }
  })();
}
