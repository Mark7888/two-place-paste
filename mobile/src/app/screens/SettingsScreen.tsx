/**
 * What this installation is, and the two things a user can do to it: name this
 * device, and leave the group.
 *
 * The screen used to explain itself at length — four cards, each with a
 * paragraph, one of them a rationale for a feature that does not exist. The
 * facts are now rows, the two policies that a user might otherwise go looking
 * for are one line each, and the long explanation lives where the decision is
 * made: in the dialog that leaves the group.
 */

import React, { useState } from 'react';
import { Alert, ScrollView, Text, View } from 'react-native';

import { publicKey, toHex } from '../../core';
import { tile } from '../../platform';
import { useSession } from '../SessionContext';
import { failureMessage } from '../session';
import { Button, Notice, Section } from '../ui';
import { styles } from '../theme';

export function SettingsScreen(): React.JSX.Element {
  const session = useSession();
  const [message, setMessage] = useState('');
  const client = session.client;
  const state = client?.snapshot();

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

  return (
    <ScrollView style={styles.screen} contentContainerStyle={styles.content}>
      <Text style={styles.title}>Settings</Text>

      <Section title="This device">
        <Fact label="Name" value={state?.deviceName ?? '—'} />
        <Fact label="Relay" value={state?.serverUrl === '' ? 'not paired' : (state?.serverUrl ?? '—')} />
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
        <View style={{ gap: 10 }}>
          <Text style={styles.muted}>
            Deletes this device’s keys and returns the app to its setup screen. This is also how the
            device is moved to another group.
          </Text>
          <Button label="Leave the group" variant="danger" onPress={forget} />
        </View>
      </Section>

      {message !== '' && <Notice message={message} tone="error" />}
    </ScrollView>
  );
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
