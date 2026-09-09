/**
 * What this installation is, and the two things a user can do to it: name this
 * device, and leave the group.
 */

import React, { useState } from 'react';
import { Alert, ScrollView, Text } from 'react-native';

import { publicKey, toHex } from '../../core';
import { secureStore, tile } from '../../platform';
import { useSession } from '../SessionContext';
import { failureMessage } from '../session';
import { Button, Card, Status } from '../ui';
import { styles } from '../theme';

export function SettingsScreen(): React.JSX.Element {
  const session = useSession();
  const [message, setMessage] = useState('');
  const client = session.client;
  const state = client?.snapshot();

  const forget = () => {
    Alert.alert(
      'Leave the group?',
      'This device’s keys are deleted from this phone. Nothing is removed from the relay — to stop this device from reading new entries, remove it from another device instead, which re-keys the group.',
      [
        { text: 'Cancel', style: 'cancel' },
        {
          text: 'Leave',
          style: 'destructive',
          onPress: () => {
            void (async () => {
              try {
                client?.disconnect();
                await secureStore.clear('session');
                setMessage('This device’s keys were deleted. Restart the app to start fresh.');
              } catch (err) {
                setMessage(failureMessage(err));
              }
            })();
          },
        },
      ],
    );
  };

  return (
    <ScrollView style={styles.screen} contentContainerStyle={styles.content}>
      <Text style={styles.title}>Settings</Text>

      <Card title="This device">
        <Text style={styles.text}>{state?.deviceName ?? '—'}</Text>
        <Text style={styles.muted}>
          Relay: {state?.serverUrl === '' ? 'not paired' : state?.serverUrl}
        </Text>
        <Text style={styles.muted}>Epoch: {session.epoch.toString()}</Text>
        <Text style={styles.muted}>
          {/*
            The public key is shown; the private key and the group key are in
            the Android Keystore and are never rendered, logged or exported
            (spec/crypto.md §10).
          */}
          Public key: {state === undefined ? '—' : toHex(publicKeyOf(state))}
        </Text>
      </Card>

      <Card title="Quick-settings tile">
        <Text style={styles.muted}>
          {tile.available
            ? 'Add the TwoPlacePaste tile from the quick-settings panel’s edit screen. Tapping it opens the app for a moment — the only time Android lets an app read the clipboard — syncs once, and tells you what happened.'
            : 'This platform has no quick-settings tile.'}
        </Text>
      </Card>

      <Card title="Automatic sync">
        <Text style={styles.muted}>
          There is none, and there will not be one. Android blocks clipboard reads from apps that
          are not focused, and the only ways around that are an accessibility service or posing as
          a keyboard. Both are worse than a tile tap, so the feature is dropped rather than worked
          around.
        </Text>
      </Card>

      <Card title="Leave the group">
        <Button label="Delete this device’s keys" variant="danger" onPress={forget} />
        <Status message={message} />
      </Card>
    </ScrollView>
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
