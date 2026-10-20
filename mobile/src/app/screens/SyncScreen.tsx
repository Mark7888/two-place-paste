/**
 * Manual sync, with the direction said out loud (SPEC §6, §7.1).
 *
 * "Sync now" is the button; the two directional ones are underneath because
 * the timestamp comparison SPEC §6 defines is not always available, and a
 * wrong guess overwrites whichever side it chose against. When the comparison
 * cannot be made, this no longer reports that as an outcome and stop — it asks,
 * in a dialog with the two answers on it, and carries out the one picked.
 */

import React, { useState } from 'react';
import { Text, View } from 'react-native';

import { useSession } from '../SessionContext';
import type { SavedFile } from '../../platform';
import {
  download,
  failureMessage,
  handleFile,
  sync,
  upload,
  type FileChoice,
  type PendingFile,
} from '../session';
import {
  Button,
  Choice,
  FileChoices,
  Notice,
  OutcomeNotice,
  Screen,
  Section,
  Sheet,
  Status,
  TransferList,
} from '../ui';
import { colors, styles } from '../theme';

export function SyncScreen(): React.JSX.Element {
  const session = useSession();
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState('');
  // saved is the file the last outcome saved, which its notice offers to open.
  const [saved, setSaved] = useState<SavedFile | null>(null);
  const [ok, setOk] = useState(true);
  const [asking, setAsking] = useState(false);
  // A file the clipboard cannot hold, waiting for save-or-open. It is asked
  // before the download, so the sheet appears at once and names the file.
  const [file, setFile] = useState<PendingFile | null>(null);

  const run = (action: 'sync' | 'up' | 'down') => {
    const client = session.client;
    if (client === null || busy) {
      return;
    }
    setBusy(true);
    setMessage('');
    setSaved(null);
    void (async () => {
      try {
        await client.connect();
        const result =
          action === 'sync'
            ? await sync(client)
            : action === 'up'
              ? await upload(client)
              : await download(client);
        if (result.direction === 'ambiguous') {
          // Not an outcome to report — a question to put.
          setAsking(true);
          return;
        }
        setAsking(false);
        if (result.direction === 'file' && result.file !== undefined) {
          setFile(result.file);
          return;
        }
        setOk(true);
        setMessage(result.message);
      } catch (err) {
        setOk(false);
        setMessage(failureMessage(err));
        setSaved(null);
      } finally {
        setBusy(false);
        session.refresh();
      }
    })();
  };

  const choose = (choice: FileChoice) => {
    const pending = file;
    const client = session.client;
    if (pending === null || client === null || busy) {
      return;
    }
    // The sheet closes at once; the download shows on the progress bar, and
    // the screen stays usable while it runs.
    setFile(null);
    setBusy(true);
    setMessage('');
    setSaved(null);
    void (async () => {
      try {
        const result = await handleFile(client, pending, choice);
        setOk(true);
        setMessage(result.message);
        setSaved(result.saved ?? null);
      } catch (err) {
        // The question comes back with the reason, so the other option is
        // one tap away — "no app opens this" is exactly when Save is the
        // answer, and by now the entry is downloaded and cached.
        setOk(false);
        setMessage(failureMessage(err));
        setSaved(null);
        setFile(pending);
      } finally {
        setBusy(false);
      }
    })();
  };

  return (
    <>
      <Screen>
        <Text style={styles.title}>Sync</Text>
        {session.groupName !== '' && <Text style={styles.muted}>{session.groupName}</Text>}
        <Text style={styles.lede}>
          {session.groupDeleted
            ? 'This group was deleted.'
            : session.connected
              ? `Connected · epoch ${session.epoch}`
              : 'Not connected. The app holds a connection only while it is open.'}
        </Text>

        <View style={{ gap: 10 }}>
          <Button label="Sync now" icon="sync" onPress={() => run('sync')} disabled={busy} />
          <Text style={styles.small}>
            Uses whichever is newer: this device’s clipboard, or the group’s latest entry. If it
            cannot tell, it asks.
          </Text>
        </View>

        <Section title="Or choose the direction">
          <Button
            label="Send this clipboard to the group"
            icon="upload"
            variant="secondary"
            onPress={() => run('up')}
            disabled={busy}
          />
          <Button
            label="Copy the group’s latest entry here"
            icon="download"
            variant="secondary"
            onPress={() => run('down')}
            disabled={busy}
          />
        </Section>

        {/*
          Three sources, one place. `message` is this screen's own last action;
          `session.error` is a failure from elsewhere — a tile tap whose panel
          has since closed — which would otherwise have existed only as a toast
          nobody was looking at; `session.notice` is what merely happened.
        */}
        <TransferList transfers={session.transfers} />
        {message !== '' && (
          <OutcomeNotice
            message={message}
            ok={ok}
            saved={saved}
            onError={(m) => {
              setOk(false);
              setMessage(m);
              setSaved(null);
            }}
          />
        )}
        {session.error !== '' && (
          <View style={{ gap: 8 }}>
            <Notice message={session.error} tone="error" />
            <Button label="Dismiss" variant="secondary" onPress={() => session.clearError()} />
          </View>
        )}
        <Status message={session.notice} />

        {session.groupDeleted && (
          <Section title="The group was deleted">
            <Text style={styles.text}>
              The server admin deleted this device’s group, so nothing more syncs here. Leave the
              group in Settings, then create a new group with a fresh link or pair with a device in
              another group.
            </Text>
          </Section>
        )}
        {!session.groupDeleted && session.probablyRevoked && (
          <Section title="This device may have been removed">
            <Text style={styles.text}>
              The relay is answering but is refusing this device’s connection. That is what a
              revoked device sees. Pair again from another device to rejoin the group.
            </Text>
          </Section>
        )}
      </Screen>

      <Sheet
        visible={asking}
        title="Which way should this sync go?"
        onClose={() => setAsking(false)}
      >
        <Text style={styles.muted}>
          This device cannot say when its clipboard was last set, so there is nothing to compare
          the group’s latest entry against. Whichever you pick overwrites the other side.
        </Text>
        <Choice
          icon="upload"
          title="Sync up"
          why="Send this device’s clipboard to the group. It becomes the latest entry."
          disabled={busy}
          onPress={() => run('up')}
        />
        <Choice
          icon="download"
          title="Sync down"
          why="Copy the group’s latest entry here, replacing this device’s clipboard."
          disabled={busy}
          onPress={() => run('down')}
        />
        <Text style={[styles.small, { color: colors.faint }]}>
          Nothing has been read or written yet.
        </Text>
      </Sheet>

      <Sheet visible={file !== null} title="Save or open?" onClose={() => setFile(null)}>
        <FileChoices
          name={file?.name ?? ''}
          detail={file?.detail ?? ''}
          disabled={busy}
          onChoose={choose}
        />
        {!ok && message !== '' && <Notice message={message} tone="error" />}
      </Sheet>
    </>
  );
}
