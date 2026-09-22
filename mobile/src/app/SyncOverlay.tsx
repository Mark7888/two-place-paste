/**
 * The panel a quick-settings tap opens (SPEC §7.1).
 *
 * Android will not let an app read the clipboard unless it is focused, so a
 * tile tap has to bring a window of this app forward. It does not have to be
 * the *whole* app: this is a second React surface on the same JavaScript
 * context, mounted by a transparent activity, so what the user sees is their
 * own screen dimmed with a small panel over the middle of it — and what runs
 * underneath is the same session the app uses, not a second copy of it.
 *
 * The panel has three states and no navigation: it is working, it is asking
 * which way the sync should go, or it is saying what happened just before it
 * closes itself. There is nothing to tap through, because a tile tap is one
 * action and this is the whole of it.
 */

import React from 'react';
import { ActivityIndicator, Pressable, Text, View } from 'react-native';

import { tile } from '../platform';
import { SessionProvider, useSession } from './SessionContext';
import { Button, Choice, Notice } from './ui';
import { colors, styles } from './theme';

function Panel(): React.JSX.Element {
  const session = useSession();
  const question = session.question;
  const error = session.error;

  const dismiss = () => {
    if (question !== null) {
      // Dismissing is an answer. The store reports it, and closes this window.
      session.answer(question.id, null);
      return;
    }
    session.clearError();
    tile.closeOverlay();
  };

  return (
    <Pressable
      style={styles.overlayRoot}
      accessibilityRole="button"
      accessibilityLabel="Dismiss"
      onPress={dismiss}
    >
      <Pressable style={styles.sheet} onPress={() => undefined}>
        {question !== null ? (
          <>
            <Text style={styles.sheetTitle}>Which way should this sync go?</Text>
            <Text style={styles.muted}>
              This device cannot say when its clipboard was last set, so there is nothing to compare
              the group’s latest entry against. Whichever you pick overwrites the other side.
            </Text>
            <Choice
              icon="upload"
              title="Sync up"
              why="Send this device’s clipboard to the group."
              onPress={() => session.answer(question.id, 'up')}
            />
            <Choice
              icon="download"
              title="Sync down"
              why="Copy the group’s latest entry to this device."
              onPress={() => session.answer(question.id, 'down')}
            />
          </>
        ) : error !== '' ? (
          // A failure stays on screen. The store does not close the panel for
          // an error, because a toast that disappears in two seconds is how a
          // sync silently not happening comes to look like one that did — the
          // user is told what went wrong and dismisses it themselves.
          <>
            <Text style={styles.sheetTitle}>That did not work</Text>
            <Notice message={error} tone="error" />
            <Button label="Close" variant="secondary" onPress={dismiss} />
          </>
        ) : session.syncing || !session.ready ? (
          <View style={[styles.rowInline, { paddingVertical: 4 }]}>
            <ActivityIndicator color={colors.accent} />
            <Text style={[styles.text, { flex: 1 }]}>Syncing…</Text>
          </View>
        ) : (
          // The store closes this window as soon as it has a *successful*
          // outcome, so this is the last frame before that — and what the user
          // sees if the close is ever slower than the render.
          <Notice message={session.notice === '' ? 'Done.' : session.notice} tone="ok" />
        )}
      </Pressable>
    </Pressable>
  );
}

export default function SyncOverlay(): React.JSX.Element {
  // No <StatusBar> here on purpose. This window is transparent and is sitting
  // over someone else's screen: the bar's icons belong to that screen, and
  // restyling them for a panel that closes in a second would leave the app
  // underneath looking wrong for as long as it takes Android to hand the
  // setting back.
  return (
    <SessionProvider>
      <Panel />
    </SessionProvider>
  );
}
