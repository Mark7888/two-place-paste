/**
 * History: the group's entry metadata, listed when the screen is opened
 * (SPEC §6, §7.1).
 *
 * Opening this tab is the user asking — that is the whole of the rule, and the
 * fetch button that used to stand in front of it was asking twice. Nothing
 * here runs while the tab is closed, nothing runs on reconnect, and no entry's
 * body is fetched until the user picks one. What is listed is what the relay
 * itself holds: a size, an epoch and two timestamps, never content.
 *
 * Pulling down refetches, because a list that loaded a minute ago is a list
 * that may have missed a sync.
 */

import React, { useCallback, useEffect, useRef, useState } from 'react';
import { FlatList, Pressable, RefreshControl, Text, View } from 'react-native';

import type { EntryMeta } from '../../core';
import { clipboard } from '../../platform';
import { useSession } from '../SessionContext';
import { describeItem, failureMessage, toClipboardItem } from '../session';
import { Notice } from '../ui';
import { colors, styles } from '../theme';

export function HistoryScreen(): React.JSX.Element {
  const session = useSession();
  const [entries, setEntries] = useState<EntryMeta[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [copying, setCopying] = useState(false);
  const [message, setMessage] = useState('');
  const [ok, setOk] = useState(true);

  const client = session.client;
  // A fetch in flight must not be started twice — the screen mounts, and a
  // pull can arrive before the first one has answered.
  const inFlight = useRef(false);

  const load = useCallback(async () => {
    if (client === null || inFlight.current) {
      return;
    }
    inFlight.current = true;
    setLoading(true);
    try {
      await client.connect();
      const page = await client.getHistory({ limit: 50 });
      setEntries(page.entries);
      setOk(true);
      setMessage('');
    } catch (err) {
      setOk(false);
      setMessage(failureMessage(err));
    } finally {
      inFlight.current = false;
      setLoading(false);
    }
  }, [client]);

  useEffect(() => {
    void load();
  }, [load]);

  const copy = (meta: EntryMeta) => {
    if (client === null || copying) {
      return;
    }
    setCopying(true);
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
        setCopying(false);
      }
    })();
  };

  // An entry from an older epoch predates a rekey: this device no longer holds
  // the key it was written under, so tapping it cannot do anything.
  const readable = (meta: EntryMeta) => meta.epoch === session.epoch;

  return (
    <View style={styles.screen}>
      <View style={{ paddingHorizontal: 16, paddingTop: 16, gap: 8 }}>
        <Text style={styles.title}>History</Text>
        <Text style={styles.lede}>
          Newest first. Entries expire within 24 hours. Tap one to copy it to this device.
        </Text>
        {message !== '' && <Notice message={message} tone={ok ? 'ok' : 'error'} />}
      </View>

      <FlatList
        data={entries ?? []}
        keyExtractor={(item) => item.id}
        contentContainerStyle={styles.content}
        refreshControl={
          <RefreshControl
            refreshing={loading}
            onRefresh={() => void load()}
            tintColor={colors.accent}
            colors={[colors.accent]}
          />
        }
        ListEmptyComponent={
          <View style={styles.card}>
            <Text style={styles.muted}>
              {loading
                ? 'Loading the group’s entries…'
                : entries === null
                  ? 'Pull down to load the group’s entries.'
                  : 'The group has no entries yet.'}
            </Text>
          </View>
        }
        renderItem={({ item }) => {
          const open = readable(item);
          return (
            <Pressable
              accessibilityRole="button"
              accessibilityLabel={`Entry from ${item.createdAt.toLocaleString()}`}
              accessibilityState={{ disabled: !open || copying }}
              onPress={() => copy(item)}
              disabled={!open || copying}
              android_ripple={{ color: colors.accentSoft }}
              style={[styles.card, !open && { opacity: 0.55 }]}
            >
              {/*
                The relay knows a size, an epoch and two timestamps, and nothing
                else: the content type and the filename are inside the
                ciphertext, so a listing cannot show them (SPEC §2.3).
              */}
              <Text style={styles.text}>{item.createdAt.toLocaleString()}</Text>
              <Text style={styles.small}>
                {item.size} bytes · epoch {item.epoch.toString()} · expires{' '}
                {item.expiresAt.toLocaleString()}
              </Text>
              <Text style={{ color: open ? colors.accent : colors.faint, fontSize: 13 }}>
                {open ? 'Tap to copy to this device' : 'Predates the last re-key — cannot be opened'}
              </Text>
            </Pressable>
          );
        }}
      />
    </View>
  );
}
