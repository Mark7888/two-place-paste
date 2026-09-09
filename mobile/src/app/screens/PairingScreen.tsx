/**
 * Pairing, in all three directions SPEC §3.2 asks for: show a QR, scan a QR,
 * and paste a token. The payload is one string in every direction, so this
 * screen shows the same text under the QR code that the scanner accepts.
 *
 * The QR code is rendered on device. A payload carrying a pairing token must
 * not travel to a remote QR service to be drawn.
 */

import React, { useState } from 'react';
import { Linking, Platform, ScrollView, Text, TextInput, View } from 'react-native';
import { PermissionsAndroid } from 'react-native';
import QRCode from 'react-native-qrcode-svg';
import { Camera, CameraType } from 'react-native-camera-kit';

import type { Invitation } from '../../core';
import { useSession } from '../SessionContext';
import { failureMessage } from '../session';
import { Button, Card, Status } from '../ui';
import { colors, styles } from '../theme';

type Mode = 'idle' | 'showing' | 'scanning';

export function PairingScreen(): React.JSX.Element {
  const session = useSession();
  const [mode, setMode] = useState<Mode>('idle');
  const [invitation, setInvitation] = useState<Invitation | null>(null);
  const [pasted, setPasted] = useState('');
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState('');
  const [ok, setOk] = useState(true);

  const show = () => {
    const client = session.client;
    if (client === null || busy) {
      return;
    }
    setBusy(true);
    void (async () => {
      try {
        await client.connect();
        const started = await client.startPairing();
        setInvitation(started);
        setMode('showing');
        setOk(true);
        setMessage('Scan this from the joining device, or copy the text below.');
        // The inviter is the only device holding the group key, so it stays on
        // this screen until it has wrapped it for the joiner (§3.2 step 4).
        started.joined
          .then((device) => setMessage(`${device.name} joined the group.`))
          .catch(() => undefined);
      } catch (err) {
        setOk(false);
        setMessage(failureMessage(err));
      } finally {
        setBusy(false);
      }
    })();
  };

  const join = (payload: string) => {
    const client = session.client;
    if (client === null || busy || payload.trim() === '') {
      return;
    }
    setBusy(true);
    setMode('idle');
    void (async () => {
      try {
        await client.joinPairing(payload);
        setOk(true);
        setMessage('This device is now in the group. It starts empty by design.');
      } catch (err) {
        setOk(false);
        setMessage(failureMessage(err));
      } finally {
        setBusy(false);
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
          setMessage('Scanning needs the camera. You can paste the pairing code instead.');
          return;
        }
      }
      setMode('scanning');
    })();
  };

  if (mode === 'scanning') {
    return (
      <View style={styles.screen}>
        <Camera
          style={{ flex: 1 }}
          cameraType={CameraType.Back}
          scanBarcode
          onReadCode={(event) => join(event.nativeEvent.codeStringValue)}
        />
        <View style={styles.content}>
          <Button label="Cancel" variant="secondary" onPress={() => setMode('idle')} />
        </View>
      </View>
    );
  }

  return (
    <ScrollView style={styles.screen} contentContainerStyle={styles.content}>
      <Text style={styles.title}>Pairing</Text>
      <Status message={message} ok={ok} />

      {session.inGroup && (
        <Card title="Add a device to this group">
          <Button label="Show a pairing code" onPress={show} disabled={busy} />
          {mode === 'showing' && invitation !== null && (
            <View style={{ alignItems: 'center', gap: 10 }}>
              <View style={{ backgroundColor: '#ffffff', padding: 12, borderRadius: 8 }}>
                <QRCode value={invitation.payload} size={220} />
              </View>
              <Text style={styles.mono} selectable>
                {invitation.payload}
              </Text>
              <Text style={styles.muted}>
                Valid until {invitation.expiresAt.toLocaleTimeString()} · single use
              </Text>
            </View>
          )}
        </Card>
      )}

      <Card title={session.inGroup ? 'Join another group' : 'Join a group'}>
        <Text style={styles.muted}>
          Scan the code another device is showing, or paste it. Both carry the same string.
        </Text>
        <Button label="Scan a QR code" variant="secondary" onPress={scan} disabled={busy} />
        <TextInput
          style={styles.input}
          value={pasted}
          onChangeText={setPasted}
          placeholder="Paste a pairing code"
          placeholderTextColor={colors.muted}
          autoCapitalize="none"
          autoCorrect={false}
          multiline
        />
        <Button label="Join with this code" onPress={() => join(pasted)} disabled={busy} />
      </Card>

      <Card title="Where a pairing code comes from">
        <Text style={styles.muted}>
          Another device that is already in the group shows one. To create the first group
          instead, open your relay’s admin page and use the creation link it gives you.
        </Text>
        <Button
          label="Open the relay’s admin page"
          variant="secondary"
          disabled={session.client?.serverUrl === '' || session.client === null}
          onPress={() => {
            const base = session.client?.serverUrl ?? '';
            if (base !== '') {
              void Linking.openURL(`${base}/admin/`);
            }
          }}
        />
      </Card>
    </ScrollView>
  );
}
