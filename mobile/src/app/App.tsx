/**
 * The app shell: a tab per screen, the setup gate in front of them, and the
 * window insets.
 *
 * The navigation is five tabs and a piece of state. A navigation library would
 * be a dependency to keep current for a stack that is one level deep
 * (docs/conventions.md §9).
 *
 * # The tab bar
 *
 * The previous bar could show two tabs lit or none, and a tab could refuse a
 * tap. Three things caused it and all three are fixed here:
 *
 *  - the highlight was applied to the label's style array while the id it
 *    compared against could be one no tab renders — an unpaired device reset
 *    the tab from an effect, so for one frame the content and the bar
 *    disagreed. The active id is now *derived* during render and validated
 *    against the tab list, so exactly one tab is ever current and it is the
 *    one whose screen is mounted;
 *  - the tap target was a label with padding around it, well under Android's
 *    48dp, so a press near the edge of a tab landed between two of them and
 *    did nothing. Each tab is now a 60dp-high target that fills its column;
 *  - the bar sits above the navigation-bar inset rather than padding into it,
 *    so no part of a target is underneath the system's own gesture area.
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

import React, { useState } from 'react';
import { ActivityIndicator, Pressable, StatusBar, Text, View } from 'react-native';
import { SafeAreaProvider, useSafeAreaInsets } from 'react-native-safe-area-context';

import { Icon, type IconName } from './icons';
import { SessionProvider, useSession } from './SessionContext';
import { DevicesScreen } from './screens/DevicesScreen';
import { HistoryScreen } from './screens/HistoryScreen';
import { PairingScreen } from './screens/PairingScreen';
import { SettingsScreen } from './screens/SettingsScreen';
import { SetupScreen } from './screens/SetupScreen';
import { SyncScreen } from './screens/SyncScreen';
import { colors, styles } from './theme';

type Tab = 'sync' | 'history' | 'devices' | 'pairing' | 'settings';

const TABS: { id: Tab; label: string; icon: IconName }[] = [
  { id: 'sync', label: 'Sync', icon: 'sync' },
  { id: 'history', label: 'History', icon: 'history' },
  { id: 'devices', label: 'Devices', icon: 'devices' },
  { id: 'pairing', label: 'Pairing', icon: 'link' },
  { id: 'settings', label: 'Settings', icon: 'settings' },
];

const SCREENS: Record<Tab, () => React.JSX.Element> = {
  sync: SyncScreen,
  history: HistoryScreen,
  devices: DevicesScreen,
  pairing: PairingScreen,
  settings: SettingsScreen,
};

function Shell(): React.JSX.Element {
  const session = useSession();
  const [requested, setRequested] = useState<Tab>('sync');
  const insets = useSafeAreaInsets();

  // Left and right matter too: in landscape the navigation bar moves to one
  // side, and a display cutout can take a strip of either edge.
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

  // The one place the current tab is decided. Deriving it here — rather than
  // correcting the state from an effect after the fact — is what guarantees
  // that the screen on show and the tab lit in the bar are the same tab, on
  // every frame including the first one after a group is joined or left.
  const active: Tab = TABS.some((t) => t.id === requested) ? requested : 'sync';
  const Screen = SCREENS[active];

  return (
    <View style={[styles.screen, frame]}>
      <View style={{ flex: 1 }}>
        <Screen />
      </View>
      <View
        accessibilityRole="tablist"
        style={[styles.tabBar, { paddingBottom: insets.bottom }]}
      >
        {TABS.map((entry) => {
          const selected = entry.id === active;
          return (
            <Pressable
              key={entry.id}
              accessibilityRole="tab"
              accessibilityLabel={entry.label}
              accessibilityState={{ selected }}
              style={styles.tab}
              // A press that drifts a little is still a press: without this a
              // thumb that moves 3px on the way up cancels the tap.
              pressRetentionOffset={{ top: 12, bottom: 12, left: 12, right: 12 }}
              android_ripple={{ color: colors.accentSoft, borderless: false }}
              onPress={() => setRequested(entry.id)}
            >
              <View style={[styles.tabIconWrap, selected && styles.tabIconWrapActive]}>
                <Icon
                  name={entry.icon}
                  size={20}
                  color={selected ? colors.accent : colors.muted}
                />
              </View>
              <Text
                numberOfLines={1}
                style={[styles.tabLabel, selected && styles.tabLabelActive]}
              >
                {entry.label}
              </Text>
            </Pressable>
          );
        })}
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
