/**
 * The whole app until this device is in a group.
 *
 * There are three ways in (SPEC §3, docs/plans/joiner-emitted-pairing.md):
 * read a code a device that is already in a group is showing, show a code of
 * this device's own for a member to accept, or create the first group from the
 * link a relay's admin page gives an operator. All three are on this one
 * screen, and there is no tab bar behind it — nothing else in the app can do
 * anything without a group key, so offering it would be offering dead ends.
 *
 * Reading and showing are the same pairing, from the two ends. Which one the
 * user wants depends on which device has the screen they are looking at: a
 * phone scanning a desktop, or a desktop — which has no camera by design
 * (SPEC §7.2) — reading a code this phone shows.
 *
 * The scanner accepts any of this system's QR codes and decides from the code
 * itself which flow it is (`enterGroup`), because the user holding the phone up
 * to a screen should not have to have pressed the right button first.
 *
 * Joining lives here and nowhere else, for a reason worth stating: a device
 * that already holds a group key cannot join a second group without
 * discarding the key it has. Moving a paired device to another group is
 * "delete this device's keys" in Settings, which brings the app back here.
 */

import React, { useEffect, useState } from 'react';
import { PermissionsAndroid, Platform, ScrollView, Text, TextInput, View } from 'react-native';
import { Camera, CameraType } from 'react-native-camera-kit';
import QRCode from 'react-native-qrcode-svg';

import type { Offer } from '../../core';
import { fingerprint } from '../../core';
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
  const [relayUrl, setRelayUrl] = useState('');
  const [offer, setOffer] = useState<Offer | null>(null);

  // A code this device stops showing must stop being live: the socket it holds
  // open is the relay's only route to a device with no identity.
  useEffect(() => () => offer?.cancel(), [offer]);

  const show = () => {
    const client = session.client;
    if (client === null || busy || relayUrl.trim() === '') {
      return;
    }
    setBusy(true);
    void (async () => {
      try {
        offer?.cancel();
        const started = await client.startOffer(relayUrl);
        setOffer(started);
        setOk(true);
        setMessage('Show this to a device that is already in the group.');
        started.accepted
          .then(() => {
            setMessage('This device is now in the group. It starts empty by design.');
            setOffer(null);
            session.refresh();
          })
          .catch((err: unknown) => {
            setOk(false);
            setMessage(failureMessage(err));
            setOffer(null);
          });
      } catch (err) {
        setOk(false);
        setMessage(failureMessage(err));
      } finally {
        setBusy(false);
      }
    })();
  };

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

      <Card title="Or show a code from this phone">
        <Text style={styles.muted}>
          If the other device is the one that cannot scan — a desktop has no camera by design —
          this phone can show the code instead. Type the relay’s address: this device does not know
          it yet, and the code has to say where to find it.
        </Text>
        <TextInput
          style={styles.input}
          value={relayUrl}
          onChangeText={setRelayUrl}
          placeholder="https://tpp.example.com"
          placeholderTextColor={colors.muted}
          autoCapitalize="none"
          autoCorrect={false}
          keyboardType="url"
        />
        <Button
          label={offer === null ? 'Show a code' : 'New code'}
          variant="secondary"
          onPress={show}
          disabled={busy || relayUrl.trim() === ''}
        />
        {offer !== null && (
          <View style={{ alignItems: 'center', gap: 10 }}>
            <View style={{ backgroundColor: '#ffffff', padding: 12, borderRadius: 8 }}>
              {/*
                Rendered on device by a bundled library: a code carrying an
                offer must not travel to a remote QR service to be drawn.
              */}
              <QRCode value={offer.code} size={220} />
            </View>
            <Text style={styles.mono} selectable>
              {offer.code}
            </Text>
            <Text style={styles.muted}>
              Valid until {offer.expiresAt.toLocaleTimeString()} · single use
            </Text>
            <Text style={styles.muted}>
              The other device will ask its user to confirm this device’s name and fingerprint
              before it adds anything. This device’s fingerprint is:
            </Text>
            <Text style={styles.mono} selectable>
              {fingerprint(session.client?.publicKey ?? new Uint8Array())}
            </Text>
          </View>
        )}
        {offer !== null && (
          <Button
            label="Withdraw the code"
            variant="secondary"
            onPress={() => {
              offer.cancel();
              setOffer(null);
              setMessage('The code was withdrawn.');
            }}
          />
        )}
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
