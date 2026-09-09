/**
 * The group roster, and the named confirmation SPEC §3.3 step 2 requires.
 *
 * The dialog cannot be confirmed before the roster has loaded, and it is not
 * the screen that remembers this: `prepareRevoke` returns a plan carrying the
 * roster, and the only way to revoke anything is to confirm a plan. A screen
 * that skipped the dialog would have nothing to confirm with.
 */

import React, { useState } from 'react';
import { ScrollView, Text, View } from 'react-native';

import type { Device, Revocation } from '../../core';
import { useSession } from '../SessionContext';
import { failureMessage } from '../session';
import { Button, Card, Status } from '../ui';
import { colors, styles } from '../theme';

export function DevicesScreen(): React.JSX.Element {
  const session = useSession();
  const [devices, setDevices] = useState<Device[] | null>(null);
  const [plan, setPlan] = useState<Revocation | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState('');
  const [ok, setOk] = useState(true);

  const load = () => {
    const client = session.client;
    if (client === null || busy) {
      return;
    }
    setBusy(true);
    void (async () => {
      try {
        await client.connect();
        const roster = await client.devices();
        setDevices(roster.devices);
        setOk(true);
        setMessage(`${roster.devices.length} devices at epoch ${roster.epoch}.`);
      } catch (err) {
        setOk(false);
        setMessage(failureMessage(err));
      } finally {
        setBusy(false);
      }
    })();
  };

  const prepare = (device: Device) => {
    const client = session.client;
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
    <ScrollView style={styles.screen} contentContainerStyle={styles.content}>
      <Text style={styles.title}>Devices</Text>
      <Button label="Load the group’s devices" onPress={load} disabled={busy} />
      <Status message={message} ok={ok} />

      {plan !== null && (
        <Card title={`Remove ${plan.target.name}?`}>
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
          <View style={styles.row}>
            <View style={{ flex: 1 }}>
              <Button label="Cancel" variant="secondary" onPress={() => setPlan(null)} />
            </View>
            <View style={{ flex: 1 }}>
              <Button label="Remove" variant="danger" onPress={confirm} disabled={busy} />
            </View>
          </View>
        </Card>
      )}

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
    </ScrollView>
  );
}
