/**
 * Bringing another device into this group (SPEC §3.2,
 * docs/plans/joiner-emitted-pairing.md).
 *
 * Pairing runs in both directions, and this screen has both, because which one
 * the user wants depends on which device has the screen they are looking at.
 *
 * **Showing a code.** This phone holds the group key, mints a pairing token and
 * displays it. The joining device scans it, or — a desktop has no camera by
 * design (SPEC §7.2) — pastes the string under it. Both forms are the same
 * string, which is why they sit together.
 *
 * **Reading a code.** The other device holds no group key, so it cannot mint a
 * token; the relay holds an *offer* for it instead, and this phone admits it.
 * That is the half that needs a confirmation, and needs it normatively.
 *
 * The asymmetry is worth being plain about. A hostile code a *joiner* reads
 * costs it a failed pairing: it holds no key to lose. A hostile code a *member*
 * reads costs the group its key, because accepting admits a device and hands it
 * everything this group copies from now on. So the button that admits anything
 * does not exist until the code has been read and this screen has rendered the
 * offering device's name and the fingerprint of its public key — and the client
 * enforces that too, in `prepareAcceptOffer`, rather than trusting this screen
 * to remember (SPEC §3.3 step 2, applied here for the same reason).
 *
 * There is deliberately no scanner for *joining another group* here. A device
 * holding a group key cannot join a second one without discarding the key it
 * has, so the way to move this phone elsewhere is Settings — not a button that
 * would have to mean "leave the group" in disguise.
 */

import React, { useEffect, useState } from 'react';
import { Linking, PermissionsAndroid, Platform, Text, TextInput, View } from 'react-native';
import { Camera, CameraType } from 'react-native-camera-kit';
import QRCode from 'react-native-qrcode-svg';

import type { Invitation, OfferAcceptance } from '../../core';
import { classifyCode, pairingLink } from '../../core';
import { useSession } from '../SessionContext';
import { failureMessage } from '../session';
import { Button, Card, CopyButton, Notice, Screen } from '../ui';
import { colors, styles } from '../theme';

export function PairingScreen(): React.JSX.Element {
  const session = useSession();
  const [invitation, setInvitation] = useState<Invitation | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState('');
  const [ok, setOk] = useState(true);
  const [typed, setTyped] = useState('');
  const [scanning, setScanning] = useState(false);
  const [pending, setPending] = useState<OfferAcceptance | null>(null);

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
        setOk(true);
        setMessage('Scan this from the joining device, or send it the text below.');
        // The inviter is the only device holding the group key, so it wraps it
        // for the joiner as soon as the relay reports the join (§3.2 step 4).
        // That happens on the client's own connection; this only reports it.
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

  /**
   * link wraps a code in the link the QR carries: this group's own relay,
   * with the code in the fragment. Built here rather than in the core's
   * `startPairing` because only the screen knows which relay this device is
   * talking to, and a self-hosted deployment has no other name for it.
   */
  const link = (code: string) => pairingLink(session.client?.serverUrl ?? '', code);

  // A code that arrived through a scanned link lands in the session store; the
  // pairing screen is where it belongs, so it is claimed here. It is only ever
  // *read*, never acted on: admitting a device hands over the group key, so
  // the confirmation below is the only way in (SPEC §3.2).
  const pendingCode = session.pendingCode;
  useEffect(() => {
    if (pendingCode === '') {
      return;
    }
    const claimed = session.claimPendingCode();
    if (claimed !== '') {
      setTyped(claimed);
      read(claimed);
    }
    // Keyed on the arriving code alone: claiming it clears the trigger, so
    // this runs once per link rather than once per render.
  }, [pendingCode]);

  // read decodes a code and stops. Nothing is wrapped and no device is
  // admitted here: that is `admit`, below, and it only exists once this has
  // produced something to show the user.
  const read = (code: string) => {
    const client = session.client;
    if (client === null || busy || code.trim() === '') {
      return;
    }
    setBusy(true);
    setScanning(false);
    void (async () => {
      try {
        if (classifyCode(code) !== 'offer-code') {
          // Telling the user which code they are holding beats a failure
          // further in: an invitation here is one this device could have shown
          // itself, and a creation link belongs to a device with no group.
          throw new Error(
            'That is not a code from a device waiting to join. Open this app on that device and use “Show a code”.',
          );
        }
        setPending(await client.prepareAcceptOffer(code));
        setOk(true);
        setMessage('');
      } catch (err) {
        setOk(false);
        setMessage(failureMessage(err));
      } finally {
        setBusy(false);
      }
    })();
  };

  const admit = () => {
    if (pending === null || busy) {
      return;
    }
    setBusy(true);
    void (async () => {
      try {
        const device = await pending.confirm();
        setPending(null);
        setTyped('');
        setOk(true);
        setMessage(`${device.name} was added to the group.`);
        session.refresh();
      } catch (err) {
        setOk(false);
        setMessage(failureMessage(err));
      } finally {
        setBusy(false);
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
          onReadCode={(event) => read(event.nativeEvent.codeStringValue)}
        />
        <View style={styles.content}>
          <Text style={styles.muted}>
            Point the camera at the code the joining device is showing. Nothing is added until you
            have read its name and fingerprint and said yes.
          </Text>
          <Button label="Cancel" variant="secondary" onPress={() => setScanning(false)} />
        </View>
      </View>
    );
  }

  return (
    <Screen>
      <Text style={styles.title}>Pairing</Text>

      <Card title="Add a device to this group">
        <Text style={styles.muted}>
          The code is short-lived and pairs exactly one device. The joining device scans it, or
          pastes the same string.
        </Text>
        <Button
          label={invitation === null ? 'Show a pairing code' : 'New pairing code'}
          onPress={show}
          disabled={busy}
        />
        {invitation !== null && (
          <View style={{ alignItems: 'center', gap: 10 }}>
            <View style={{ backgroundColor: '#ffffff', padding: 12, borderRadius: 8 }}>
              {/*
                Rendered on device by a bundled library: a code carrying a
                pairing token must not travel to a remote QR service to be
                drawn.

                What it carries is a link into this group's own relay, not the
                bare code. A general-purpose scanner — Google Lens, a camera
                app — shows a bare base64url string as text and offers nothing
                to open; as a link it is something to tap, and the relay's page
                hands it back to this app. The code is in the fragment, so the
                relay never receives it.
              */}
              <QRCode value={link(invitation.payload)} size={220} />
            </View>
            <Text style={styles.mono} selectable>
              {link(invitation.payload)}
            </Text>
            <CopyButton
              value={link(invitation.payload)}
              label="Copy the code"
              onResult={(text, good) => {
                setOk(good);
                setMessage(text);
              }}
            />
            <Text style={styles.muted}>
              Valid until {invitation.expiresAt.toLocaleTimeString()} · single use
            </Text>
          </View>
        )}
      </Card>

      <Card title="Or read a code the other device is showing">
        <Text style={styles.muted}>
          The other way round, for when that device is the one with the screen you are looking at —
          a desktop, which cannot scan. It shows a code; read it here.
        </Text>
        <Button label="Scan its code" variant="secondary" onPress={scan} disabled={busy} />
        <TextInput
          style={styles.input}
          value={typed}
          onChangeText={(text) => {
            setTyped(text);
            setPending(null);
          }}
          placeholder="Or paste the code here"
          placeholderTextColor={colors.muted}
          autoCapitalize="none"
          autoCorrect={false}
          multiline
        />
        <Button
          label="Read the code"
          variant="secondary"
          onPress={() => read(typed)}
          disabled={busy || typed.trim() === ''}
        />
      </Card>

      {/*
        The confirmation of docs/plans/joiner-emitted-pairing.md §5. It is the
        whole of the user's protection here, so it says what is at stake and it
        shows the fingerprint, which is the only part of this that a hostile
        code cannot choose freely.
      */}
      {pending !== null && (
        <Card title={`Add ${pending.deviceName} to this group?`}>
          <Text style={styles.muted}>
            This gives that device the group key, and everything this group copies from now on. The
            name above is whatever it calls itself; the fingerprint below is what actually
            identifies it.
          </Text>
          <Text style={styles.muted}>Check that it matches the fingerprint that device is showing:</Text>
          <Text style={styles.mono} selectable>
            {pending.fingerprint}
          </Text>
          <Button label={`Add ${pending.deviceName}`} onPress={admit} disabled={busy} />
          <Button
            label="Cancel"
            variant="secondary"
            onPress={() => setPending(null)}
            disabled={busy}
          />
        </Card>
      )}

      {message !== '' && <Notice message={message} tone={ok ? 'ok' : 'error'} />}

      <Card title="Moving this device to another group">
        <Text style={styles.muted}>
          This device can only be in one group, because it holds one group key. Delete its keys in
          Settings and the app returns to its setup screen, where it can join or create another.
        </Text>
      </Card>

      <Card title="The relay’s admin page">
        <Text style={styles.muted}>
          Where creation links and the device roster live, for the operator of this relay.
        </Text>
        <Button
          label="Open in a browser"
          variant="secondary"
          disabled={session.client === null || session.client.serverUrl === ''}
          onPress={() => {
            const base = session.client?.serverUrl ?? '';
            if (base !== '') {
              void Linking.openURL(`${base}/admin/`);
            }
          }}
        />
      </Card>
    </Screen>
  );
}
