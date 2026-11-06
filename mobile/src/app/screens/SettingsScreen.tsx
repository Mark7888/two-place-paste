/**
 * What this installation is, and the two things a user can do to it: name this
 * device, and leave the group. In the demo, leaving is "Close demo": there is
 * no relay to warn about, only a simulation to discard.
 *
 * The screen used to explain itself at length — four cards, each with a
 * paragraph, one of them a rationale for a feature that does not exist. The
 * facts are now rows, the two policies that a user might otherwise go looking
 * for are one line each, and the long explanation lives where the decision is
 * made: in the dialog that leaves the group.
 */

import React, { useEffect, useState } from 'react';
import { Alert, Linking, Switch, Text, View } from 'react-native';

import { publicKey, toHex } from '../../core';
import { appInfo, tile, type AppVersion } from '../../platform';
import { builtAt } from '../buildStamp';
import { LicensesScreen } from '../LicensesScreen';
import { useSession } from '../SessionContext';
import { failureMessage } from '../session';
import { Button, Notice, Screen, Section } from '../ui';
import { colors, styles } from '../theme';

export function SettingsScreen(): React.JSX.Element {
  const session = useSession();
  const [message, setMessage] = useState('');
  const [showLicenses, setShowLicenses] = useState(false);
  const [build, setBuild] = useState<AppVersion | null>(null);
  const client = session.client;
  const state = client?.snapshot();

  useEffect(() => {
    let live = true;
    appInfo.version().then(
      (v) => live && setBuild(v),
      // The rows stay "—": a version that cannot be read is not worth an error notice.
      () => undefined,
    );
    return () => {
      live = false;
    };
  }, []);

  const forget = () => {
    Alert.alert(
      'Leave the group?',
      'This device’s keys are deleted from this phone and the app returns to its setup screen, where it can join or create another group.\n\nNothing is removed from the relay: to stop this device from reading what the group writes next, revoke it from another device, which re-keys the group.',
      [
        { text: 'Cancel', style: 'cancel' },
        {
          text: 'Leave',
          style: 'destructive',
          onPress: () => {
            void (async () => {
              try {
                // The app follows the client: with no group key the shell
                // shows the setup screen, so there is nothing to restart.
                await session.forget();
              } catch (err) {
                setMessage(failureMessage(err));
              }
            })();
          },
        },
      ],
    );
  };

  // Turning it off hides the Groups tab and nothing else: the group in use
  // stays in use, and the ones set aside keep their keys.
  const toggleMultiGroup = (on: boolean) => {
    void session.setMultiGroup(on).then(
      () =>
        setMessage(
          on ? '' : 'The group in use stays. The others are kept for when you turn this back on.',
        ),
      (err: unknown) => setMessage(failureMessage(err)),
    );
  };

  const closeDemo = () => {
    Alert.alert(
      'Close the demo?',
      'Its group, history and store are discarded and the app returns to its setup screen.',
      [
        { text: 'Cancel', style: 'cancel' },
        { text: 'Close demo', style: 'destructive', onPress: () => session.closeDemo() },
      ],
    );
  };

  const relay = session.demo
    ? 'Demo (simulated)'
    : state?.serverUrl === ''
      ? 'not paired'
      : (state?.serverUrl ?? '—');

  return (
    <Screen>
      <Text style={styles.title}>Settings</Text>

      <Section title="This device">
        <Fact label="Name" value={state?.deviceName ?? '—'} />
        <Fact label="Relay" value={relay} />
        <Fact label="Key generation" value={`epoch ${session.epoch.toString()}`} />
        {/*
          The public key is shown; the private key and the group key are in the
          Android Keystore and are never rendered, logged or exported
          (spec/crypto.md §10).
        */}
        <Fact
          label="Public key"
          value={state === undefined ? '—' : toHex(publicKeyOf(state))}
          mono
        />
      </Section>

      <Section title="Syncing">
        <Text style={styles.text}>Quick-settings tile</Text>
        <Text style={styles.muted}>
          {tile.available
            ? 'Add the TwoPlacePaste tile from the quick-settings edit screen. A tap opens a small panel over whatever you are doing, syncs once, and says what happened.'
            : 'This platform has no quick-settings tile.'}
        </Text>
        <Text style={styles.text}>No background sync</Text>
        <Text style={styles.muted}>
          Android blocks clipboard reads from apps that are not focused, and the ways around that
          are an accessibility service or posing as a keyboard. Both are worse than a tile tap.
        </Text>
      </Section>

      <Section title="Group">
        {session.demo ? (
          <View style={{ gap: 10 }}>
            <Text style={styles.muted}>
              This is the demo: its relay and other devices are simulated on this phone. Closing it
              discards them and returns the app to its setup screen.
            </Text>
            <Button label="Close demo" variant="danger" onPress={closeDemo} />
          </View>
        ) : (
          <View style={{ gap: 10 }}>
            <Text style={styles.muted}>
              {session.multiGroup
                ? 'Deletes this device’s keys for the group in use and returns the app to its setup screen. The groups set aside keep their keys.'
                : 'Deletes this device’s keys and returns the app to its setup screen. This is also how the device is moved to another group, unless Keep several groups is on.'}
            </Text>
            <Button label="Leave the group" variant="danger" onPress={forget} />
          </View>
        )}
        {!session.demo && (
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: 12 }}>
            <View style={styles.rowBody}>
              <Text style={styles.text}>Keep several groups</Text>
              <Text style={styles.muted}>
                Adds a Groups tab to keep other groups next to this one and switch between them. Off
                by default.
              </Text>
            </View>
            <Switch
              accessibilityLabel="Keep several groups"
              value={session.multiGroup}
              onValueChange={toggleMultiGroup}
              trackColor={{ false: colors.border, true: colors.accent }}
            />
          </View>
        )}
      </Section>

      <Section title="Version">
        <Fact label="Version" value={build === null || build.version === '' ? '—' : build.version} mono />
        <Fact label="Built" value={builtLabel(build)} />
      </Section>

      <Section title="About">
        <View style={{ gap: 10 }}>
          <Text style={styles.muted}>
            TwoPlacePaste is open source under the Mozilla Public License 2.0, and is built on other
            open-source software.
          </Text>
          <Button
            label="Source code"
            variant="secondary"
            onPress={() => void Linking.openURL(SOURCE_URL)}
          />
          <Button
            label="Open-source licenses"
            variant="secondary"
            onPress={() => setShowLicenses(true)}
          />
        </View>
      </Section>

      {message !== '' && <Notice message={message} tone="error" />}

      <LicensesScreen visible={showLicenses} onClose={() => setShowLicenses(false)} />
    </Screen>
  );
}

/** SOURCE_URL is where the app's source code is published. */
const SOURCE_URL = 'https://github.com/Mark7888/two-place-paste';

/** builtLabel says when the running build was made, as the desktop's Updates section does. */
function builtLabel(build: AppVersion | null): string {
  if (build === null || build.version === '') {
    return '—';
  }
  return builtAt(build.stamp)?.toLocaleString() ?? 'locally';
}

/** Fact is one label-and-value row. */
function Fact({
  label,
  value,
  mono,
}: {
  label: string;
  value: string;
  mono?: boolean;
}): React.JSX.Element {
  return (
    <View style={styles.rowBody}>
      <Text style={styles.small}>{label}</Text>
      <Text style={mono === true ? styles.mono : styles.text} numberOfLines={mono === true ? 2 : 1}>
        {value}
      </Text>
    </View>
  );
}

/** publicKeyOf derives the public half for display, and renders nothing rather than throwing if the key is unusable. */
function publicKeyOf(state: { devicePrivateKey: Uint8Array }): Uint8Array {
  try {
    return publicKey(state.devicePrivateKey);
  } catch {
    return new Uint8Array(0);
  }
}
