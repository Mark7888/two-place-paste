/**
 * The confirmation that admits a device, wherever the user happens to be.
 *
 * A code can arrive when nothing is watching for it: a scanned link opens the
 * app through the `tpp://` scheme and lands on whatever tab was last shown.
 * Putting this on the Pairing screen meant a request the user had just scanned
 * sat there unseen until they thought to go and look for it — so it lives at
 * the shell instead, above every tab, and appears the moment a code arrives.
 *
 * It is a modal for the same reason the revocation dialog is: this is the one
 * decision in the app that hands over the group key, and SPEC §3.2 requires the
 * user to have seen the joining device's name and fingerprint before it can be
 * confirmed. A section further down a scrolling screen can be scrolled past;
 * this cannot.
 */

import React, { useEffect, useState } from 'react';
import { Text, View } from 'react-native';

import type { OfferAcceptance } from '../core';
import { classifyCode } from '../core';
import { useSession } from './SessionContext';
import { failureMessage } from './session';
import { Button, Notice, Sheet } from './ui';
import { styles } from './theme';

export function PairingRequest(): React.JSX.Element | null {
  const session = useSession();
  const [pending, setPending] = useState<OfferAcceptance | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [done, setDone] = useState('');

  const client = session.client;
  const pendingCode = session.pendingCode;

  // Claimed here rather than on the Pairing screen: the shell is mounted
  // whatever tab is showing, so a code is never waiting on a screen the user
  // has not opened. The setup screen claims it instead while this device has
  // no group, and the two are never mounted at once.
  useEffect(() => {
    if (pendingCode === '' || client === null) {
      return;
    }
    const code = session.claimPendingCode();
    if (code === '') {
      return;
    }
    setError('');
    setDone('');
    if (classifyCode(code) !== 'offer-code') {
      setError(
        'That code is not from a device waiting to join. On that device, open TwoPlacePaste and use “Show a code”.',
      );
      return;
    }
    setBusy(true);
    void (async () => {
      try {
        setPending(await client.prepareAcceptOffer(code));
      } catch (err) {
        setError(failureMessage(err));
      } finally {
        setBusy(false);
      }
    })();
    // Keyed on the arriving code alone: claiming it clears the trigger.
  }, [pendingCode, client]);

  const dismiss = () => {
    setPending(null);
    setError('');
    setDone('');
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
        setDone(`${device.name} was added to the group.`);
        session.setNotice(`${device.name} joined the group.`);
      } catch (err) {
        setError(failureMessage(err));
      } finally {
        setBusy(false);
        session.refresh();
      }
    })();
  };

  const visible = pending !== null || error !== '' || done !== '';
  if (!visible) {
    return null;
  }

  return (
    <Sheet
      visible={visible}
      title={pending === null ? 'Pairing' : `Add ${pending.deviceName} to this group?`}
      onClose={dismiss}
    >
      {pending !== null && (
        <>
          <Text style={styles.text}>
            This gives that device the group key, and everything this group copies from now on. The
            name above is whatever it calls itself; the fingerprint below is what actually
            identifies it.
          </Text>
          <Text style={styles.muted}>
            Check that it matches the fingerprint that device is showing:
          </Text>
          <Text style={styles.mono} selectable>
            {pending.fingerprint}
          </Text>
        </>
      )}
      {error !== '' && <Notice message={error} tone="error" />}
      {done !== '' && <Notice message={done} tone="ok" />}
      <View style={{ gap: 10 }}>
        {pending !== null && (
          <Button
            label={busy ? 'Adding…' : `Add ${pending.deviceName}`}
            onPress={admit}
            disabled={busy}
          />
        )}
        <Button
          label={pending === null ? 'Close' : 'Cancel'}
          variant="secondary"
          onPress={dismiss}
          disabled={busy}
        />
      </View>
    </Sheet>
  );
}
