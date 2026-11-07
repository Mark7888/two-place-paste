/**
 * The panel "Share → TwoPlacePaste" opens over the sharing app.
 *
 * Like the tile's panel (`SyncOverlay.tsx`) it is a second React surface on
 * the app's one JavaScript context, mounted by a transparent activity, so it
 * runs on the app's own session. It asks one question — send what was shared
 * to the group, or save it to the store — and nothing leaves the device until
 * the user answers. Dismissing it is an answer too: nothing is read and nothing
 * is sent.
 *
 * It always goes to the group in use. With "Keep several groups" on, the user
 * switches group in the app first.
 */

import React, { useEffect, useState, useSyncExternalStore } from 'react';
import { ActivityIndicator, BackHandler, Pressable, ScrollView, Text, View } from 'react-native';

import { share, type ClipboardItem, type Shared } from '../platform';
import { ImagePreview } from './PreviewBody';
import { SessionProvider, useSession } from './SessionContext';
import { describeItem, failureMessage } from './session';
import { describeShared, sendShared, storeItemFor, suggestedName, STORED, UNSUPPORTED } from './share';
import { finishShare, getShared, openAppFromShare, subscribeShare } from './shareState';
import { StoreSheet } from './StoreSheet';
import { getStoreSnapshot, index as storeIndex, loadStore, saveItem, subscribeStore } from './storeState';
import { colors, styles } from './theme';
import { Button, Choice, Notice, TransferList } from './ui';
import { storeRoom } from './preview';

/** readShared is the platform's reader, as the function `share.ts` takes. */
const readShared = (id: number, maxBytes: number) => share.read(id, maxBytes);

/** Busy is what the panel is doing while it is not waiting on the user. */
type Busy = 'send' | 'store' | null;

function Panel(): React.JSX.Element {
  const session = useSession();
  const shared = useSyncExternalStore(subscribeShare, getShared);
  const store = useSyncExternalStore(subscribeStore, getStoreSnapshot);
  const [busy, setBusy] = useState<Busy>(null);
  const [error, setError] = useState('');
  const [storing, setStoring] = useState<ClipboardItem | null>(null);

  const id = shared?.id ?? 0;

  // A new share starts the panel over.
  useEffect(() => {
    setBusy(null);
    setError('');
    setStoring(null);
  }, [id]);

  const cancel = () => {
    if (shared === null) {
      share.finish(0, '');
      return;
    }
    finishShare(shared.id, '');
  };

  // Back is Cancel. While an upload runs it is ignored rather than leaving the
  // upload to finish behind a panel that is gone.
  useEffect(() => {
    const sub = BackHandler.addEventListener('hardwareBackPress', () => {
      if (busy === null) {
        cancel();
      }
      return true;
    });
    return () => sub.remove();
  });

  const client = session.client;

  const send = (s: Shared) => {
    if (client === null || busy !== null) {
      return;
    }
    setBusy('send');
    setError('');
    void (async () => {
      try {
        const message = await sendShared(client, s, readShared);
        finishShare(s.id, message);
      } catch (err) {
        setError(failureMessage(err));
      } finally {
        setBusy(null);
      }
    })();
  };

  const toStore = (s: Shared) => {
    if (client === null || busy !== null) {
      return;
    }
    setBusy('store');
    setError('');
    void (async () => {
      try {
        // Listed in the background for the duplicate check and the tag
        // suggestions; the save waits for it either way.
        void loadStore(client);
        setStoring(await storeItemFor(client, s, readShared));
      } catch (err) {
        setError(failureMessage(err));
      } finally {
        setBusy(null);
      }
    })();
  };

  let body: React.JSX.Element;
  if (shared === null || !session.ready) {
    body = (
      <>
        <View style={[styles.rowInline, { paddingVertical: 4 }]}>
          <ActivityIndicator color={colors.accent} />
          <Text style={[styles.text, { flex: 1 }]}>Opening…</Text>
        </View>
        <Button label="Close" variant="secondary" onPress={cancel} />
      </>
    );
  } else if (shared.kind === 'unsupported') {
    body = (
      <>
        <Notice message={UNSUPPORTED} tone="error" />
        <Button label="Close" variant="secondary" onPress={cancel} />
      </>
    );
  } else if (!session.inGroup) {
    body = (
      <>
        <Notice
          message="TwoPlacePaste is not paired yet. Create or join a group in the app, then share again."
          tone="info"
        />
        <Button label="Open TwoPlacePaste" onPress={openAppFromShare} />
        <Button label="Close" variant="secondary" onPress={cancel} />
      </>
    );
  } else {
    const { title, detail } = describeShared(shared);
    body = (
      <>
        <View style={{ gap: 4 }}>
          <Text style={styles.text} numberOfLines={2}>
            {title}
          </Text>
          {detail !== '' && <Text style={styles.small}>{detail}</Text>}
        </View>
        {shared.kind === 'file' && shared.previewUri !== '' && <ImagePreview uri={shared.previewUri} />}
        {shared.kind === 'text' && (
          <ScrollView style={styles.previewBox} contentContainerStyle={styles.previewBoxContent} nestedScrollEnabled>
            <Text style={styles.previewText}>{shared.text}</Text>
          </ScrollView>
        )}
        {session.groupName !== '' && <Text style={styles.small}>Group: {session.groupName}</Text>}
        {error !== '' && <Notice message={error} tone="error" />}
        {busy !== null ? (
          session.transfers.length > 0 ? (
            <TransferList transfers={session.transfers} />
          ) : (
            <View style={[styles.rowInline, { paddingVertical: 4 }]}>
              <ActivityIndicator color={colors.accent} />
              <Text style={[styles.text, { flex: 1 }]}>{busy === 'send' ? 'Sending…' : 'Reading…'}</Text>
            </View>
          )
        ) : (
          <>
            <Choice
              icon="upload"
              title="Send to group"
              why="It becomes the group’s latest entry; your other devices get it on their next sync."
              onPress={() => send(shared)}
            />
            <Choice
              icon="store"
              title="Save to store"
              why="Keep it in the group’s store, with a name and tags. It does not expire."
              onPress={() => toStore(shared)}
            />
            <Button label="Cancel" variant="secondary" onPress={cancel} />
          </>
        )}
      </>
    );
  }

  return (
    <Pressable
      style={styles.overlayRoot}
      accessibilityRole="button"
      accessibilityLabel="Dismiss"
      onPress={() => {
        if (busy === null) {
          cancel();
        }
      }}
    >
      <Pressable style={styles.sheet} onPress={() => undefined}>
        <Text style={styles.sheetTitle}>Share to TwoPlacePaste</Text>
        {body}
      </Pressable>
      {shared !== null && (
        <StoreSheet
          visible={storing !== null}
          title="Save to store"
          what={storing === null ? undefined : describeItem(storing)}
          available={storeRoom(store.limits)}
          initial={{ name: suggestedName(shared), description: '', tags: [] }}
          knownTags={storeIndex.tags().map((t) => t.tag)}
          onClose={() => setStoring(null)}
          onSubmit={async (details, allowDuplicate) => {
            if (client === null || storing === null) {
              return '';
            }
            await saveItem(client, storing, details, allowDuplicate);
            return STORED;
          }}
          onDone={(message) => finishShare(shared.id, message)}
        />
      )}
    </Pressable>
  );
}

export default function ShareOverlay(): React.JSX.Element {
  // No <StatusBar>, for the reason the tile's panel gives: this window sits
  // over another app's screen, and the bars belong to that app.
  return (
    <SessionProvider>
      <Panel />
    </SessionProvider>
  );
}
