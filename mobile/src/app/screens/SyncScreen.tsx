/**
 * Manual sync, with the direction said out loud (SPEC §6, §7.1).
 *
 * Three controls, not one: "Sync now" uses the timestamp comparison SPEC §6
 * defines, and the two directional buttons exist because that comparison is
 * not always available and a wrong guess overwrites whichever side it chose
 * against.
 */

import React, { useState } from 'react';
import { ScrollView, Text, View } from 'react-native';

import { useSession } from '../SessionContext';
import { download, failureMessage, sync, upload } from '../session';
import { Button, Card, Status } from '../ui';
import { styles } from '../theme';

export function SyncScreen(): React.JSX.Element {
  const session = useSession();
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState('');
  const [ok, setOk] = useState(true);

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
    <ScrollView style={styles.screen} contentContainerStyle={styles.content}>
      <Text style={styles.title}>Sync</Text>

      <Card>
        <Text style={styles.muted}>
          {session.connected
            ? `Connected · epoch ${session.epoch}`
            : 'Not connected. The app holds a connection only while it is open.'}
        </Text>
        <Button label="Sync now" onPress={() => run('sync')} disabled={busy} />
        <Text style={styles.muted}>
          Uses whichever is newer: this device’s clipboard, or the group’s latest entry.
        </Text>
      </Card>

      <Card title="Or choose the direction">
        <Button
          label="Send this clipboard to the group"
          variant="secondary"
          onPress={() => run('up')}
          disabled={busy}
        />
        <Button
          label="Copy the group’s latest entry here"
          variant="secondary"
          onPress={() => run('down')}
          disabled={busy}
        />
      </Card>

      <View>
        <Status message={message} ok={ok} />
        <Status message={session.notice} />
      </View>

      {session.probablyRevoked && (
        <Card title="This device may have been removed">
          <Text style={styles.text}>
            The relay is answering but is refusing this device’s connection. That is what a
            revoked device sees. Pair again from another device to rejoin the group.
          </Text>
        </Card>
      )}
    </ScrollView>
  );
}
