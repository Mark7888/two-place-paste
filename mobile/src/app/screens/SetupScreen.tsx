/**
 * The whole app until this device is in a group.
 *
 * There are exactly two ways in (SPEC §3): join a group a device that is
 * already in one is inviting this device to, or create the first group from
 * the link a relay's admin page gives an operator. Both are on this one
 * screen, and there is no tab bar behind it — nothing else in the app can do
 * anything without a group key, so offering it would be offering dead ends.
 *
 * The scanner accepts either of this system's two QR codes and decides from
 * the code itself which flow it is (`enterGroup`), because the user holding
 * the phone up to a screen should not have to have pressed the right button
 * first.
 *
 * Joining lives here and nowhere else, for a reason worth stating: a device
 * that already holds a group key cannot join a second group without
 * discarding the key it has. Moving a paired device to another group is
 * "delete this device's keys" in Settings, which brings the app back here.
 */

import React, { useState } from 'react';
import { PermissionsAndroid, Platform, ScrollView, Text, TextInput, View } from 'react-native';
import { Camera, CameraType } from 'react-native-camera-kit';

import { useSession } from '../SessionContext';
import { enterGroup, failureMessage } from '../session';
import { Button, Card, Status } from '../ui';
import { colors, styles } from '../theme';

export function SetupScreen(): React.JSX.Element {
  const session = useSession();
  const [typed, setTyped] = useState('');
  const [scanning, setScanning] = useState(false);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState('');
  const [ok, setOk] = useState(true);

  const submit = (code: string) => {
    const client = session.client;
    if (client === null || busy || code.trim() === '') {
      return;
    }
    setBusy(true);
    setScanning(false);
    void (async () => {
      try {
        setOk(true);
        setMessage(await enterGroup(client, code));
      } catch (err) {
        setOk(false);
        setMessage(failureMessage(err));
      } finally {
        setBusy(false);
        // Success puts the client in a group, which is what swaps this screen
        // for the rest of the app.
        session.refresh();
      }
    })();
  };

  const scan = () => {
    void (async () => {
      if (Platform.OS === 'android') {
        const granted = await PermissionsAndroid.request(PermissionsAndroid.PERMISSIONS.CAMERA);
        if (granted !== PermissionsAndroid.RESULTS.GRANTED) {
          setOk(false);
          setMessage('Scanning needs the camera. You can paste the code instead.');
          return;
        }
      }
      setMessage('');
      setScanning(true);
    })();
  };

  // While the scanner is open it is the screen: a viewfinder and one way out.
  if (scanning) {
    return (
      <View style={styles.screen}>
        <Camera
          style={{ flex: 1 }}
          cameraType={CameraType.Back}
          scanBarcode
          onReadCode={(event) => submit(event.nativeEvent.codeStringValue)}
        />
        <View style={styles.content}>
          <Text style={styles.muted}>
            Point the camera at a pairing code, or at the creation link on your relay’s admin page.
          </Text>
          <Button label="Cancel" variant="secondary" onPress={() => setScanning(false)} />
        </View>
      </View>
    );
  }

  return (
    <ScrollView style={styles.screen} contentContainerStyle={styles.content}>
      <Text style={styles.title}>TwoPlacePaste</Text>
      <Text style={styles.muted}>
        This device is not in a group yet. Its keypair has been generated and stays on this phone.
      </Text>

      <Card title="Join a group">
        <Text style={styles.muted}>
          On a device that is already in the group, open Pairing and show its code. Scan it, or
          paste it below — both carry the same string.
        </Text>
        <Button label="Scan a QR code" onPress={scan} disabled={busy} />
      </Card>

      <Card title="Create the first group">
        <Text style={styles.muted}>
          For the first device only. Sign in to your relay’s admin page and scan the creation link
          it shows, with the same button above, or paste it below.
        </Text>
      </Card>

      <Card title="Or paste a code">
        <Text style={styles.muted}>
          A pairing code or a creation link. Which one it is, this screen works out for itself.
        </Text>
        <TextInput
          style={styles.input}
          value={typed}
          onChangeText={setTyped}
          placeholder="Pairing code, or https://relay.example.com/<token>"
          placeholderTextColor={colors.muted}
          autoCapitalize="none"
          autoCorrect={false}
          multiline
        />
        <Button
          label="Continue"
          variant="secondary"
          onPress={() => submit(typed)}
          disabled={busy || typed.trim() === ''}
        />
      </Card>

      <Status message={message} ok={ok} />
    </ScrollView>
  );
}
