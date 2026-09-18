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
import { ScrollView, Text, View } from 'react-native';

import { useSession } from '../SessionContext';
import { download, failureMessage, sync, upload } from '../session';
import { Button, Choice, Notice, Section, Sheet, Status } from '../ui';
import { colors, styles } from '../theme';

export function SyncScreen(): React.JSX.Element {
  const session = useSession();
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState('');
  const [ok, setOk] = useState(true);
  const [asking, setAsking] = useState(false);

  const run = (action: 'sync' | 'up' | 'down') => {
    const client = session.client;
    if (client === null || busy) {
      return;
    }
    setBusy(true);
    setMessage('');
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
        setOk(true);
        setMessage(result.message);
      } catch (err) {
        setOk(false);
        setMessage(failureMessage(err));
      } finally {
        setBusy(false);
        session.refresh();
      }
    })();
  };

  return (
    <>
      <ScrollView style={styles.screen} contentContainerStyle={styles.content}>
        <Text style={styles.title}>Sync</Text>
        <Text style={styles.lede}>
          {session.connected
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

        {message !== '' && <Notice message={message} tone={ok ? 'ok' : 'error'} />}
        <Status message={session.notice} />

        {session.probablyRevoked && (
          <Section title="This device may have been removed">
            <Text style={styles.text}>
              The relay is answering but is refusing this device’s connection. That is what a
              revoked device sees. Pair again from another device to rejoin the group.
            </Text>
          </Section>
        )}
      </ScrollView>

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
    </>
  );
}
