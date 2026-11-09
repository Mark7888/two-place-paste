/**
 * The Transfers panel: every large upload and download in the one transfer
 * queue (`transfers.ts`) — what it is, which way it goes, to or from where,
 * and where it is — so the user can see what is moving, what is waiting its
 * turn, and how the last few ended.
 *
 * It shows nothing while the queue is empty and nothing has finished, so it
 * can sit on every screen a transfer starts from.
 */

import React, { useSyncExternalStore } from 'react';
import { Pressable, Text, View } from 'react-native';

import { Icon } from './icons';
import { formatSize } from './preview';
import { colors, styles } from './theme';
import type { Transfer } from './transferQueue';
import { getTransfers, positionText, subscribeTransfers, transferQueue } from './transfers';

/** whereText says which way a transfer goes, and to or from where. */
export function whereText(t: Pick<Transfer, 'direction' | 'target'>): string {
  const where = t.target === 'store' ? 'the store' : 'the group';
  return t.direction === 'upload' ? `To ${where}` : `From ${where}`;
}

/** stateText is where a transfer is, in a few words. */
export function stateText(t: Transfer): string {
  switch (t.state) {
    case 'waiting':
      return 'Waiting';
    case 'running': {
      const phase = t.phase !== '' ? t.phase : t.direction === 'upload' ? 'Preparing…' : 'Downloading…';
      return t.total > 0 ? `${phase} ${Math.floor((t.done * 100) / t.total)}%` : phase;
    }
    case 'done':
      return t.message !== '' ? t.message : 'Done';
    case 'failed':
      return t.message !== '' ? t.message : 'Failed';
  }
}

export function TransferPanel(): React.JSX.Element | null {
  const transfers = useSyncExternalStore(subscribeTransfers, getTransfers);
  if (transfers.length === 0) {
    return null;
  }
  const position = positionText();
  const anyFinished = transfers.some((t) => t.state === 'done' || t.state === 'failed');
  return (
    <View style={[styles.card, { gap: 10 }]} accessibilityLabel="Transfers">
      <View style={styles.rowInline}>
        <Text style={[styles.heading, { flex: 1 }]}>
          Transfers{position !== '' ? ` · ${position}` : ''}
        </Text>
        {anyFinished && (
          <Pressable accessibilityRole="button" onPress={() => transferQueue.clearFinished()} hitSlop={12}>
            <Text style={{ color: colors.accent, fontSize: 14, fontWeight: '600' }}>Clear</Text>
          </Pressable>
        )}
      </View>
      {transfers.map((t) => (
        <TransferRow key={t.id} transfer={t} />
      ))}
    </View>
  );
}

function TransferRow({ transfer: t }: { transfer: Transfer }): React.JSX.Element {
  const finished = t.state === 'done' || t.state === 'failed';
  const tone = t.state === 'failed' ? colors.danger : t.state === 'done' ? colors.ok : colors.muted;
  const percent = t.total > 0 ? Math.min(100, Math.floor((t.done * 100) / t.total)) : 0;
  const details = [whereText(t), t.size > 0 ? formatSize(t.size) : ''].filter((x) => x !== '').join(' · ');
  return (
    <View
      style={{ flexDirection: 'row', gap: 12, alignItems: 'flex-start', opacity: finished ? 0.8 : 1 }}
      accessibilityLabel={`${t.direction === 'upload' ? 'Upload' : 'Download'}: ${t.name}. ${details}. ${stateText(t)}`}
    >
      <View style={{ paddingTop: 2 }}>
        <Icon
          name={t.state === 'done' ? 'check' : t.state === 'failed' ? 'alert' : t.direction === 'upload' ? 'upload' : 'download'}
          size={20}
          color={t.state === 'running' ? colors.accent : tone}
        />
      </View>
      <View style={{ flex: 1, gap: 2 }}>
        <Text style={[styles.text, { fontWeight: '600' }]} numberOfLines={1} ellipsizeMode="middle">
          {t.name}
        </Text>
        <Text style={styles.small}>{details}</Text>
        <Text style={[styles.small, { color: t.state === 'running' ? colors.text : tone }]} numberOfLines={2}>
          {stateText(t)}
        </Text>
        {t.state === 'running' && (
          <View style={{ height: 4, borderRadius: 2, backgroundColor: colors.border, marginTop: 4, overflow: 'hidden' }}>
            <View style={{ height: 4, width: t.total > 0 ? `${percent}%` : '30%', backgroundColor: colors.accent }} />
          </View>
        )}
      </View>
    </View>
  );
}
