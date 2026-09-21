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
 * And one it adds: a sync that cannot tell which way to go asks, through
 * `question`, and waits. Whichever surface is on screen renders it.
 */

import { AppState, Linking, type AppStateStatus } from 'react-native';

import type { Client } from '../core';
import { tile } from '../platform';
import { stripCodeEnvelope } from '../core';
import { download, failureMessage, openSession, sync, upload } from './session';

/** DirectionChoice is what the user answers a pending question with. */
export type DirectionChoice = 'up' | 'down';

/**
 * Question is a sync waiting on the user. `id` changes for every question, so
 * a late answer to one that has already been resolved is ignored rather than
 * applied to the next one.
 */
export interface Question {
  id: number;
  /** fromTile is true when the panel that asked it was opened by the tile. */
  fromTile: boolean;
}

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
}

type Listener = () => void;

let client: Client | null = null;
let ready = false;
let notice = '';
let probablyRevoked = false;
let syncing = false;
let question: Question | null = null;
let lastError = '';
let pendingCode = '';
let answer: ((choice: DirectionChoice | null) => void) | null = null;
let nextQuestionId = 1;

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
  syncing: false,
  question: null,
  error: '',
  pendingCode: '',
};

function emit(): void {
  snapshot = {
    client,
    ready,
    inGroup: client?.inGroup ?? false,
    connected: client?.connected ?? false,
    epoch: client?.epoch ?? 0n,
    notice,
    probablyRevoked,
    syncing,
    question,
    error: lastError,
    pendingCode,
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
  if (client === null) {
    return;
  }
  await client.forget();
  probablyRevoked = false;
  notice = '';
  emit();
}

/**
 * answerDirection resolves a pending question. `null` means the user dismissed
 * it, which is an answer: nothing is read and nothing is written.
 */
export function answerDirection(id: number, choice: DirectionChoice | null): void {
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
function ask(fromTile: boolean): Promise<DirectionChoice | null> {
  return new Promise((resolve) => {
    question = { id: nextQuestionId++, fromTile };
    answer = resolve;
    emit();
  });
}

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
      const opened = await openSession({
        onConnected: () => emit(),
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
      });
      client = opened;
      ready = true;
      emit();
      if (opened.inGroup) {
        await opened.connect().catch((err: unknown) => setNotice(failureMessage(err)));
        emit();
      }
      watchForeground(opened);
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
function watchForeground(c: Client): void {
  AppState.addEventListener('change', (state: AppStateStatus) => {
    if (!c.inGroup) {
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
        const choice = await ask(true);
        if (choice === null) {
          finish(false, 'No direction chosen; nothing was synced.');
          return;
        }
        result = choice === 'up' ? await upload(c) : await download(c);
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
