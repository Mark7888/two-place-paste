/**
 * The app shell: a tab per screen, the setup gate in front of them, and the
 * window insets.
 *
 * The navigation is five tabs and a piece of state. A navigation library would
 * be a dependency to keep current for a stack that is one level deep
 * (docs/conventions.md §9).
 *
 * # Insets
 *
 * This app is edge-to-edge, which is not a choice: Android 15 enforces it for
 * anything targeting SDK 35 or later, and SDK 36 is what React Native 0.87
 * targets. The window therefore extends under the status bar and under the
 * navigation bar, and every pixel of that has to be accounted for here —
 * `SafeAreaView` from `react-native` is deprecated and does nothing at all on
 * Android, so this uses `react-native-safe-area-context`, which reads the real
 * `WindowInsets` from the platform.
 *
 * The treatment is the conventional one: the app's own background paints the
 * full window, including behind both bars, and the content is padded in by the
 * insets so nothing the user has to read or tap ends up underneath them. The
 * tab bar is padded at the bottom rather than lifted, so its surface continues
 * behind the navigation bar instead of leaving a stripe of a different colour.
 */

import React, { useEffect, useState } from 'react';
import { ActivityIndicator, Pressable, StatusBar, Text, View } from 'react-native';
import { SafeAreaProvider, useSafeAreaInsets } from 'react-native-safe-area-context';

import { SessionProvider, useSession } from './SessionContext';
import { DevicesScreen } from './screens/DevicesScreen';
import { HistoryScreen } from './screens/HistoryScreen';
import { PairingScreen } from './screens/PairingScreen';
import { SettingsScreen } from './screens/SettingsScreen';
import { SetupScreen } from './screens/SetupScreen';
import { SyncScreen } from './screens/SyncScreen';
import { colors, styles } from './theme';

type Tab = 'sync' | 'history' | 'devices' | 'pairing' | 'settings';

const TABS: { id: Tab; label: string }[] = [
  { id: 'sync', label: 'Sync' },
  { id: 'history', label: 'History' },
  { id: 'devices', label: 'Devices' },
  { id: 'pairing', label: 'Pairing' },
  { id: 'settings', label: 'Settings' },
];

function Shell(): React.JSX.Element {
  const session = useSession();
  const [tab, setTab] = useState<Tab>('sync');
  const paired = session.inGroup;
  useEffect(() => {
    if (!paired) {
      setTab('sync');
    }
  }, [paired]);
  // Left and right matter too: in landscape the navigation bar moves to one
  // side, and a display cutout can take a strip of either edge.
  const insets = useSafeAreaInsets();
  const frame = {
    paddingTop: insets.top,
    paddingLeft: insets.left,
    paddingRight: insets.right,
  };

  if (!session.ready) {
    return (
      <View style={[styles.screen, frame, { justifyContent: 'center' }]}>
        <ActivityIndicator color={colors.accent} />
      </View>
    );
  }

  // Until this device is in a group, the setup screen is the app: there is no
  // tab bar, because every other screen needs a group key to do anything and a
  // tab that leads nowhere is worse than no tab. It takes the bottom inset
  // itself, which the tab bar would otherwise have handled.
  if (!session.inGroup) {
    return (
      <View style={[styles.screen, frame, { paddingBottom: insets.bottom }]}>
        <SetupScreen />
      </View>
    );
  }

  return (
    <View style={[styles.screen, frame]}>
      <View style={{ flex: 1 }}>
        {tab === 'sync' && <SyncScreen />}
        {tab === 'history' && <HistoryScreen />}
        {tab === 'devices' && <DevicesScreen />}
        {tab === 'pairing' && <PairingScreen />}
        {tab === 'settings' && <SettingsScreen />}
      </View>
      <View style={[styles.tabBar, { paddingBottom: insets.bottom }]}>
        {TABS.map((entry) => (
          <Pressable
            key={entry.id}
            accessibilityRole="tab"
            accessibilityState={{ selected: tab === entry.id }}
            style={styles.tab}
            onPress={() => setTab(entry.id)}
          >
            <Text style={[styles.tabLabel, tab === entry.id && styles.tabLabelActive]}>
              {entry.label}
            </Text>
          </Pressable>
        ))}
      </View>
    </View>
  );
}

export default function App(): React.JSX.Element {
  return (
    <SafeAreaProvider style={styles.screen}>
      {/*
        Only the icon style is settable. React Native 0.87 removed
        StatusBar's `translucent` and `backgroundColor` props, because under
        edge-to-edge there is nothing for them to do: the bar is transparent
        and what shows through it is this app's own background. `light-content`
        is for the dark palette in theme.ts.
      */}
      <StatusBar barStyle="light-content" />
      <SessionProvider>
        <Shell />
      </SessionProvider>
    </SafeAreaProvider>
  );
}
