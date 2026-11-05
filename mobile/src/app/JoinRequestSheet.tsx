/**
 * The question a device asking to join puts to this one (SPEC §3.2 step 4).
 *
 * The joining device used the code this device showed, and its public key
 * reached this device through the relay — so a relay that wanted the group key
 * could have put its own in its place. The fingerprint is what catches that:
 * the user compares it with what the joining device shows, and only then is
 * anything handed over.
 *
 * Like PairingRequest it sits above every tab: the request arrives whenever
 * the other device gets round to it, and the user may have moved on by then.
 */

import React, { useEffect, useState } from 'react';
import { Text, View } from 'react-native';

import { useSession } from './SessionContext';
import { failureMessage } from './session';
import { dropExpiredJoins } from './sessionStore';
import { Button, Notice, Sheet } from './ui';
import { styles } from './theme';

export function JoinRequestSheet(): React.JSX.Element | null {
  const session = useSession();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const request = session.joinRequests[0] ?? null;

  // A request whose code ran out has nothing left to confirm: the relay
  // refuses its key. It is dropped when that moment comes.
  useEffect(() => {
    if (request === null) {
      return;
    }
    const wait = request.expiresAt.getTime() - Date.now();
    const timer = setTimeout(() => dropExpiredJoins(new Date()), Math.max(wait, 0));
    return () => clearTimeout(timer);
  }, [request]);

  if (request === null && error === '') {
    return null;
  }

  const answer = (confirm: boolean) => {
    if (request === null || busy) {
      return;
    }
    setBusy(true);
    setError('');
    void (async () => {
      try {
        const device = await session.answerJoin(request, confirm);
        session.setNotice(
          device === null
            ? 'The device was turned down. It got nothing from this group.'
            : `${device.name} joined the group.`,
        );
      } catch (err) {
        setError(failureMessage(err));
      } finally {
        setBusy(false);
        session.refresh();
      }
    })();
  };

  const name = request?.deviceName || 'A device';
  return (
    <Sheet
      visible
      dismissable={request === null}
      title={request === null ? 'Pairing' : `${name} wants to join this group`}
      // Closing is not an answer: the request stays until it gets one, or its
      // code runs out. Only an error can be closed.
      onClose={() => (request === null ? setError('') : undefined)}
    >
      {request !== null && (
        <>
          <Text style={styles.text}>
            It used the pairing code this device showed. Adding it gives it the group key and
            everything this group copies from now on.
          </Text>
          <Text style={styles.muted}>
            Check that this fingerprint is the one that device is showing. If it is not, turn it
            down: something other than your device answered the code.
          </Text>
          <Text style={styles.mono} selectable>
            {request.fingerprint}
          </Text>
        </>
      )}
      {error !== '' && <Notice message={error} tone="error" />}
      <View style={{ gap: 10 }}>
        {request !== null && (
          <>
            <Button
              label={busy ? 'Adding…' : `Add ${request.deviceName}`}
              onPress={() => answer(true)}
              disabled={busy}
            />
            <Button label="Turn it down" variant="secondary" onPress={() => answer(false)} disabled={busy} />
          </>
        )}
        {request === null && <Button label="Close" variant="secondary" onPress={() => setError('')} />}
      </View>
    </Sheet>
  );
}
