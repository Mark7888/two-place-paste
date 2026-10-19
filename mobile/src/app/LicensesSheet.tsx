/**
 * The open-source licenses of everything the app is built from
 * (LICENSING.md), opened from Settings.
 *
 * The text is read when the sheet opens rather than at startup: it is only
 * ever wanted here, and it is several hundred kilobytes.
 */

import React, { useEffect, useState } from 'react';
import { ActivityIndicator, FlatList, Text, useWindowDimensions } from 'react-native';

import { notices } from '../platform';
import { splitNotices } from './notices';
import { Sheet } from './ui';
import { colors, styles } from './theme';

export function LicensesSheet({
  visible,
  onClose,
}: {
  visible: boolean;
  onClose: () => void;
}): React.JSX.Element {
  const { height } = useWindowDimensions();
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
    body = <Text style={styles.muted}>The licenses could not be read.</Text>;
  } else if (pieces === undefined) {
    body = <ActivityIndicator color={colors.accent} />;
  } else if (pieces === null) {
    body = (
      <Text style={styles.muted}>
        This build carries no license list. Release builds do; see LICENSING.md in the source
        code.
      </Text>
    );
  } else {
    body = (
      <FlatList
        style={{ maxHeight: height * 0.65 }}
        data={pieces}
        keyExtractor={(_, i) => String(i)}
        initialNumToRender={4}
        renderItem={({ item }) => (
          <Text style={[styles.small, { marginBottom: 18 }]} selectable>
            {item}
          </Text>
        )}
      />
    );
  }

  return (
    <Sheet visible={visible} title="Open-source licenses" onClose={onClose}>
      {body}
    </Sheet>
  );
}
