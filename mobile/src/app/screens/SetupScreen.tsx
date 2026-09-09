/**
 * The first launch: this device is not in a group yet.
 *
 * Two ways in, both from SPEC §3: create a group from the link the relay's
 * admin page gives an operator, or join one another device is inviting it to.
 */

import React, { useState } from 'react';
import { ScrollView, Text, TextInput } from 'react-native';

import { useSession } from '../SessionContext';
import { failureMessage } from '../session';
import { Button, Card, Status } from '../ui';
import { colors, styles } from '../theme';

export function SetupScreen({ onPair }: { onPair: () => void }): React.JSX.Element {
  const session = useSession();
  const [creationURL, setCreationURL] = useState('');
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState('');
  const [ok, setOk] = useState(true);

  const create = () => {
    const client = session.client;
    if (client === null || busy) {
      return;
    }
    setBusy(true);
    void (async () => {
      try {
        await client.createGroup(creationURL);
        setOk(true);
        setMessage('The group was created and this device holds its first key.');
      } catch (err) {
        setOk(false);
        setMessage(failureMessage(err));
      } finally {
        setBusy(false);
        session.refresh();
      }
    })();
  };

  return (
    <ScrollView style={styles.screen} contentContainerStyle={styles.content}>
      <Text style={styles.title}>TwoPlacePaste</Text>
      <Text style={styles.muted}>
        This device is not in a group yet. Its keypair has been generated and stays on this phone.
      </Text>

      <Card title="Join a group">
        <Text style={styles.muted}>
          Another device that is already in the group can show you a pairing code.
        </Text>
        <Button label="Scan or paste a pairing code" onPress={onPair} />
      </Card>

      <Card title="Create the first group">
        <Text style={styles.muted}>
          Sign in to your relay’s admin page, generate a creation link, and paste it here.
        </Text>
        <TextInput
          style={styles.input}
          value={creationURL}
          onChangeText={setCreationURL}
          placeholder="https://relay.example.com/<token>"
          placeholderTextColor={colors.muted}
          autoCapitalize="none"
          autoCorrect={false}
          inputMode="url"
        />
        <Button label="Create the group" onPress={create} disabled={busy} />
      </Card>

      <Status message={message} ok={ok} />
    </ScrollView>
  );
}
