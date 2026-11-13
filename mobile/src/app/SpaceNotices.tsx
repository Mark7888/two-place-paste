/**
 * The out-of-space banners and popup (SPEC §7), over every tab.
 *
 * Two banners are advice — the store is nearly full, history entries were
 * removed early — and close with ✕ until next triggered; Settings can hide
 * them for good. A save or upload the relay refused for lack of room opens a
 * native dialog whatever that setting says. When the relay names its
 * operator's website, each of them offers to open it, with the group's id.
 */

import React, { useEffect, useRef, useSyncExternalStore } from 'react';
import { Alert, Linking, Pressable, Text, View } from 'react-native';

import { Icon } from './icons';
import { useSession } from './SessionContext';
import {
  dismissSpace,
  getSpaceSnapshot,
  spaceView,
  subscribeSpace,
  type SpaceBanner,
  type SpaceFailure,
} from './spaceState';
import { getStoreSnapshot, subscribeStore } from './storeState';
import { formatSize } from './preview';
import { colors } from './theme';

export const OPEN_OPERATOR_WEBSITE = 'Open operator website';

/** operatorHost is the host a link to the operator's website shows, read by hand: Hermes has no URL parser to lean on. */
export function operatorHost(url: string): string {
  const rest = url.replace(/^[A-Za-z]+:\/\//, '');
  const end = rest.search(/[/?#]/);
  return end < 0 ? rest : rest.slice(0, end);
}

/** openOperatorWebsite opens the link in the browser. */
export function openOperatorWebsite(url: string): void {
  void Linking.openURL(url).catch(() => undefined);
}

export const FAILURES: Record<SpaceFailure, { title: string; body: string }> = {
  store_full: {
    title: 'The store is full',
    body: 'That did not fit in the group’s store. Delete some items, or ask your relay operator for more room.',
  },
  relay_store_full: {
    title: 'The relay’s storage is full',
    body: 'The group’s store has room, but the relay’s storage for every store together does not. Contact your relay operator.',
  },
  relay_full: {
    title: 'The relay can’t store this right now',
    body: 'Try again later. If it keeps happening for a few minutes, contact your relay operator.',
  },
};

function plural(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`;
}

export function SpaceNotices(): React.JSX.Element | null {
  const session = useSession();
  const space = useSyncExternalStore(subscribeSpace, getSpaceSnapshot);
  const store = useSyncExternalStore(subscribeStore, getStoreSnapshot);
  const view = spaceView(space, store.limits);
  const link = session.operatorLink;
  const dismiss = (banner: SpaceBanner) => dismissSpace(banner, getStoreSnapshot().limits);

  // One dialog per refusal: failureSeq tells a new one from the one already shown.
  const shown = useRef(0);
  useEffect(() => {
    if (view.failure === null || view.failureSeq === shown.current) {
      return;
    }
    shown.current = view.failureSeq;
    const { title, body } = FAILURES[view.failure];
    const ok = { text: 'OK', onPress: () => dismiss('failure') };
    Alert.alert(
      title,
      body,
      link === null
        ? [ok]
        : [
            {
              text: OPEN_OPERATOR_WEBSITE,
              onPress: () => {
                openOperatorWebsite(link);
                dismiss('failure');
              },
            },
            ok,
          ],
      { cancelable: true, onDismiss: () => dismiss('failure') },
    );
  });

  if (session.demo) {
    return null;
  }
  const banners: { key: SpaceBanner; icon: 'store' | 'history'; text: string }[] = [];
  if (view.storeNearlyFull) {
    const used: string[] = [];
    if (view.storeMaxBytes > 0) {
      used.push(`${formatSize(view.storeUsedBytes)} of ${formatSize(view.storeMaxBytes)}`);
    }
    if (view.storeMaxItems > 0) {
      used.push(`${view.storeUsedItems} of ${plural(view.storeMaxItems, 'item', 'items')}`);
    }
    banners.push({
      key: 'store',
      icon: 'store',
      text: `Your store is almost full (${used.join(', ')}). Delete some items, or ask your relay operator for more room.`,
    });
  }
  if (view.evicted > 0) {
    banners.push({
      key: 'evicted',
      icon: 'history',
      text: `History is full: the relay removed ${plural(view.evicted, 'older entry', 'older entries')} early to make room for new ones. Ask your relay operator for more room.`,
    });
  }
  if (banners.length === 0) {
    return null;
  }
  return (
    <View style={{ paddingHorizontal: 12, paddingTop: 8, gap: 8 }}>
      {banners.map((b) => (
        <View
          key={b.key}
          accessibilityRole="alert"
          style={{
            flexDirection: 'row',
            alignItems: 'flex-start',
            gap: 10,
            padding: 12,
            borderRadius: 12,
            backgroundColor: colors.warnSoft,
          }}
        >
          <Icon name={b.icon} size={16} color={colors.warn} />
          <View style={{ flex: 1, gap: 6 }}>
            <Text style={{ color: colors.warn, fontSize: 14, lineHeight: 20 }}>{b.text}</Text>
            {link !== null && (
              <Pressable
                accessibilityRole="link"
                accessibilityLabel={OPEN_OPERATOR_WEBSITE}
                onPress={() => openOperatorWebsite(link)}
                hitSlop={8}
              >
                <Text style={{ color: colors.warn, fontWeight: '600', textDecorationLine: 'underline' }}>
                  {OPEN_OPERATOR_WEBSITE}
                </Text>
              </Pressable>
            )}
          </View>
          <Pressable
            accessibilityRole="button"
            accessibilityLabel="Close"
            onPress={() => dismiss(b.key)}
            hitSlop={12}
          >
            <Icon name="close" size={16} color={colors.warn} />
          </Pressable>
        </View>
      ))}
    </View>
  );
}
