/**
 * The open-source licenses of everything the app is built from
 * (LICENSING.md), opened from Settings.
 *
 * It is a full-screen modal rather than a Sheet: the Sheet's panel and scrim
 * are Pressables, and on Android a Pressable above a scrolling list can take
 * the touch responder from it when a fling comes to rest, leaving the list
 * unable to scroll. Here nothing that handles touches sits above the list.
 *
 * The text is read when the screen opens rather than at startup: it is only
 * ever wanted here, and it is several hundred kilobytes.
 */

import React, { useEffect, useState } from 'react';
import { ActivityIndicator, FlatList, Modal, Pressable, Text, View } from 'react-native';
import { useSafeAreaInsets } from 'react-native-safe-area-context';

import { notices } from '../platform';
import { Icon } from './icons';
import { splitNotices } from './notices';
import { colors, styles, touchTarget } from './theme';

export function LicensesScreen({
  visible,
  onClose,
}: {
  visible: boolean;
  onClose: () => void;
}): React.JSX.Element {
  const insets = useSafeAreaInsets();
  // undefined while loading; null when this build carries no notices.
  const [pieces, setPieces] = useState<string[] | null | undefined>(undefined);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    if (!visible || pieces !== undefined) {
      return;
    }
    let live = true;
    notices
      .read()
      .then((text) => {
        if (live) setPieces(text === null ? null : splitNotices(text));
      })
      .catch(() => {
        if (live) setFailed(true);
      });
    return () => {
      live = false;
    };
  }, [visible, pieces]);

  let body: React.JSX.Element;
  if (failed) {
    body = <Text style={[styles.muted, { padding: 16 }]}>The licenses could not be read.</Text>;
  } else if (pieces === undefined) {
    body = <ActivityIndicator style={{ padding: 16 }} color={colors.accent} />;
  } else if (pieces === null) {
    body = (
      <Text style={[styles.muted, { padding: 16 }]}>
        This build carries no license list. Release builds do; see LICENSING.md in the source
        code.
      </Text>
    );
  } else {
    // Not `selectable`: on Android a selectable text view takes the touches
    // that would otherwise scroll the list. Clipping is off for the same
    // reason it is often turned off on Android lists: rows taken out of the
    // view tree mid-fling are what leaves a list unresponsive.
    body = (
      <FlatList
        style={{ flex: 1 }}
        contentContainerStyle={{ padding: 16, paddingBottom: 16 + insets.bottom }}
        data={pieces}
        keyExtractor={(_, i) => String(i)}
        initialNumToRender={4}
        windowSize={5}
        removeClippedSubviews={false}
        renderItem={({ item }) => <Text style={[styles.small, { marginBottom: 18 }]}>{item}</Text>}
      />
    );
  }

  return (
    <Modal
      visible={visible}
      animationType="slide"
      statusBarTranslucent
      navigationBarTranslucent
      onRequestClose={onClose}
    >
      <View style={[styles.screen, { paddingTop: insets.top }]}>
        <View style={[styles.rowInline, { paddingHorizontal: 16, paddingVertical: 8 }]}>
          <Text style={[styles.sheetTitle, { flex: 1 }]}>Open-source licenses</Text>
          <Pressable
            accessibilityRole="button"
            accessibilityLabel="Close"
            onPress={onClose}
            hitSlop={12}
            style={{ width: touchTarget, height: touchTarget, alignItems: 'center', justifyContent: 'center' }}
          >
            <Icon name="close" size={22} color={colors.muted} />
          </Pressable>
        </View>
        {body}
      </View>
    </Modal>
  );
}
