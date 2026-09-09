/**
 * History, fetched only when asked (SPEC §6, §7.1).
 *
 * Nothing on this screen runs on mount, on reconnect or on a pull: the fetch
 * button is the only thing that lists the group's entries, because a client
 * that listed them by itself would be making the user's clipboard history
 * travel without being asked. Tapping an entry decrypts that one entry and
 * copies it to this device.
 */

import React, { useState } from 'react';
import { FlatList, Pressable, Text, View } from 'react-native';

import type { EntryMeta } from '../../core';
import { clipboard } from '../../platform';
import { useSession } from '../SessionContext';
import { describeItem, failureMessage, toClipboardItem } from '../session';
import { Button, Card, Status } from '../ui';
import { colors, styles } from '../theme';

export function HistoryScreen(): React.JSX.Element {
  const session = useSession();
  const [entries, setEntries] = useState<EntryMeta[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState('');
  const [ok, setOk] = useState(true);

  const fetchHistory = () => {
    const client = session.client;
    if (client === null || busy) {
      return;
    }
    setBusy(true);
    void (async () => {
      try {
        await client.connect();
        const page = await client.getHistory({ limit: 50 });
        setEntries(page.entries);
        setOk(true);
        setMessage(
          page.entries.length === 0 ? 'The group has no entries.' : `${page.entries.length} entries.`,
        );
      } catch (err) {
        setOk(false);
        setMessage(failureMessage(err));
      } finally {
        setBusy(false);
      }
    })();
  };

  const copy = (meta: EntryMeta) => {
    const client = session.client;
    if (client === null || busy) {
      return;
    }
    setBusy(true);
    void (async () => {
      try {
        const item = await client.getEntry(meta.id);
        await clipboard.write(toClipboardItem(item));
        setOk(true);
        setMessage(`Copied ${describeItem(item)} to this device.`);
      } catch (err) {
        setOk(false);
        setMessage(failureMessage(err));
      } finally {
        setBusy(false);
      }
    })();
  };

  return (
    <View style={styles.screen}>
      <View style={styles.content}>
        <Text style={styles.title}>History</Text>
        <Button label="Fetch history" onPress={fetchHistory} disabled={busy} />
        <Status message={message} ok={ok} />
      </View>

      <FlatList
        data={entries ?? []}
        keyExtractor={(item) => item.id}
        contentContainerStyle={styles.content}
        ListEmptyComponent={
          <Card>
            <Text style={styles.muted}>
              Nothing is listed until you press fetch. History is never pulled on its own.
            </Text>
          </Card>
        }
        renderItem={({ item }) => (
          <Pressable onPress={() => copy(item)} disabled={busy}>
            <View style={styles.card}>
              {/*
                The relay knows a size, an epoch and two timestamps, and nothing
                else: the content type and the filename are inside the
                ciphertext, so a listing cannot show them (SPEC §2.3).
              */}
              <Text style={styles.text}>{item.createdAt.toLocaleString()}</Text>
              <Text style={styles.muted}>
                {item.size} bytes · epoch {item.epoch.toString()} · expires{' '}
                {item.expiresAt.toLocaleString()}
              </Text>
              <Text style={{ color: colors.accent, fontSize: 13 }}>Tap to copy to this device</Text>
            </View>
          </Pressable>
        )}
      />
    </View>
  );
}
