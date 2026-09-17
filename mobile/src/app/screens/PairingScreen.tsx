/**
 * Inviting another device into this group (SPEC §3.2).
 *
 * This screen only ever *shows* a code. That is not a simplification of the
 * spec, it is the shape of the flow: the payload travels from the device that
 * is already in the group to the one that is not, because only a member can
 * mint a pairing token against the relay. So the member displays, and the
 * joiner scans or pastes — a phone scans this screen's QR, and a desktop,
 * which has no camera by design (SPEC §7.2), pastes the string under it.
 *
 * Both forms are the same string, which is why they sit together: whichever
 * the joining device can take, it is reading the same payload.
 *
 * There is deliberately no scanner here, for two separate reasons.
 *
 * A device holding a group key cannot join another group without discarding
 * that key, so the way to move this phone elsewhere is Settings — not a
 * button that would have to mean "leave the group" in disguise.
 *
 * And the other direction — an unpaired device showing a code that this one
 * accepts — is not in the wire contract at all: only an authenticated
 * connection may mint a pairing token, so a device with no group key has
 * nothing to show. Making pairing symmetric needs new message types and a
 * confirmation gate on the accepting member; the plan is
 * docs/plans/joiner-emitted-pairing.md.
 */

import React, { useState } from 'react';
import { Linking, ScrollView, Text, View } from 'react-native';
import QRCode from 'react-native-qrcode-svg';

import type { Invitation } from '../../core';
import { useSession } from '../SessionContext';
import { failureMessage } from '../session';
import { Button, Card, Status } from '../ui';
import { styles } from '../theme';

export function PairingScreen(): React.JSX.Element {
  const session = useSession();
  const [invitation, setInvitation] = useState<Invitation | null>(null);
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

  return (
    <ScrollView style={styles.screen} contentContainerStyle={styles.content}>
      <Text style={styles.title}>Pairing</Text>

      <Card title="Add a device to this group">
        <Text style={styles.muted}>
          The code is short-lived and pairs exactly one device. The joining device scans it, or
          pastes the same string — codes always travel from a device that is in the group to one
          that is not.
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
                Rendered on device by a bundled library: a payload carrying a
                pairing token must not travel to a remote QR service to be
                drawn.
              */}
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

      <Status message={message} ok={ok} />

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
    </ScrollView>
  );
}
