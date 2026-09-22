/**
 * The group roster, and the named confirmation SPEC §3.3 step 2 requires.
 *
 * The dialog cannot be confirmed before the roster has loaded, and it is not
 * the screen that remembers this: `prepareRevoke` returns a plan carrying the
 * roster, and the only way to revoke anything is to confirm a plan. A screen
 * that skipped the dialog would have nothing to confirm with.
 */

import React, { useCallback, useEffect, useState } from 'react';
import { Text, View } from 'react-native';

import type { Device, Revocation } from '../../core';
import { useSession } from '../SessionContext';
import { failureMessage } from '../session';
import { Button, Card, Notice, Screen, Sheet } from '../ui';
import { colors, styles } from '../theme';

export function DevicesScreen(): React.JSX.Element {
  const session = useSession();
  const [devices, setDevices] = useState<Device[] | null>(null);
  const [plan, setPlan] = useState<Revocation | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState('');
  const [ok, setOk] = useState(true);

  const client = session.client;

  // Opening the screen is the ask. The roster is names and timestamps the
  // relay already holds — it is not clipboard content, and nothing here is
  // fetched while the screen is closed.
  const load = useCallback(() => {
    if (client === null) {
      return;
    }
    setBusy(true);
    void (async () => {
      try {
        await client.connect();
        const roster = await client.devices();
        setDevices(roster.devices);
        setOk(true);
        setMessage('');
      } catch (err) {
        setOk(false);
        setMessage(failureMessage(err));
      } finally {
        setBusy(false);
      }
    })();
  }, [client]);

  useEffect(() => {
    load();
  }, [load]);

  const prepare = (device: Device) => {
    if (client === null || busy) {
      return;
    }
    setBusy(true);
    void (async () => {
      try {
        setPlan(await client.prepareRevoke(device.id));
        setMessage('');
      } catch (err) {
        setOk(false);
        setMessage(failureMessage(err));
      } finally {
        setBusy(false);
      }
    })();
  };

  const confirm = () => {
    if (plan === null || busy) {
      return;
    }
    setBusy(true);
    void (async () => {
      try {
        const remaining = await plan.confirm();
        setDevices(remaining.devices);
        setPlan(null);
        setOk(true);
        setMessage(
          `${plan.target.name} was removed and the group is at epoch ${remaining.epoch}. What is on this device’s clipboard is untouched.`,
        );
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
    <Screen>
      <Text style={styles.title}>Devices</Text>
      <Text style={styles.lede}>
        Everything that holds this group’s key. Removing one re-keys the group, so the removed
        device cannot read anything written afterwards.
      </Text>
      {message !== '' && <Notice message={message} tone={ok ? 'ok' : 'error'} />}
      <Button
        label={busy ? 'Loading…' : 'Refresh'}
        variant="secondary"
        onPress={load}
        disabled={busy}
      />

      {/*
        The confirmation is a sheet rather than a card in the flow: it is the
        one thing on screen waiting for an answer, and as a card it could be
        scrolled away from while it waited.
      */}
      <Sheet
        visible={plan !== null}
        title={plan === null ? 'Remove' : `Remove ${plan.target.name}?`}
        onClose={() => setPlan(null)}
      >
        {plan !== null && (
          <>
            <Text style={styles.text}>
              A new group key will be generated and handed to every device below. Anything written
              before now becomes unreadable to all of them and expires within 24 hours.
            </Text>
            {plan.remaining.map((device) => (
              <Text key={device.id} style={styles.muted}>
                • {device.name}
                {device.self ? ' (this device)' : ''}
              </Text>
            ))}
            <View style={styles.inline}>
              <View style={{ flex: 1 }}>
                <Button label="Cancel" variant="secondary" onPress={() => setPlan(null)} />
              </View>
              <View style={{ flex: 1 }}>
                <Button label="Remove" variant="danger" onPress={confirm} disabled={busy} />
              </View>
            </View>
          </>
        )}
      </Sheet>

      {(devices ?? []).map((device) => (
        <Card key={device.id} title={device.name}>
          <Text style={styles.muted}>
            Joined {device.createdAt.toLocaleString()}
            {device.lastSeen === null ? ' · never connected' : ` · last seen ${device.lastSeen.toLocaleString()}`}
          </Text>
          {device.self ? (
            <Text style={{ color: colors.ok, fontSize: 13 }}>This device</Text>
          ) : (
            <Button
              label="Remove from the group"
              variant="secondary"
              onPress={() => prepare(device)}
              disabled={busy}
            />
          )}
        </Card>
      ))}

      {devices === null && (
        <Card>
          <Text style={styles.muted}>
            The roster is loaded on demand. Removing a device is only possible from a list you
            have seen.
          </Text>
        </Card>
      )}
    </Screen>
  );
}
