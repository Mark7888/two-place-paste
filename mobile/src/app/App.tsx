/**
 * The app shell: a tab per screen, and the setup gate in front of them.
 *
 * The navigation is five tabs and a piece of state. A navigation library would
 * be a dependency to keep current for a stack that is one level deep
 * (docs/conventions.md §9).
 */

import React, { useState } from 'react';
import { ActivityIndicator, Pressable, SafeAreaView, StatusBar, Text, View } from 'react-native';

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

  if (!session.ready) {
    return (
      <View style={[styles.screen, { justifyContent: 'center' }]}>
        <ActivityIndicator color={colors.accent} />
      </View>
    );
  }

  if (!session.inGroup && tab !== 'pairing' && tab !== 'settings') {
    return <SetupScreen onPair={() => setTab('pairing')} />;
  }

  return (
    <View style={styles.screen}>
      <View style={{ flex: 1 }}>
        {tab === 'sync' && <SyncScreen />}
        {tab === 'history' && <HistoryScreen />}
        {tab === 'devices' && <DevicesScreen />}
        {tab === 'pairing' && <PairingScreen />}
        {tab === 'settings' && <SettingsScreen />}
      </View>
      <View style={styles.tabBar}>
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
    <SafeAreaView style={styles.screen}>
      <StatusBar barStyle="light-content" />
      <SessionProvider>
        <Shell />
      </SessionProvider>
    </SafeAreaView>
  );
}
