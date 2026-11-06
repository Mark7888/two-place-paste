/**
 * The groups this device keeps: one in use, the others set aside with their
 * keys (spec/crypto.md §10.1, docs/internal/plans/multi-group-mobile.md).
 * Shown only while "Keep several groups" is on.
 *
 * Switching is the app closing and starting again with the other group, so
 * nothing here knows what the other screens show: the session resets them,
 * and they read the new group when they next load.
 */

import React, { useState } from 'react';
import { Alert, Text, View } from 'react-native';

import type { ParkedGroup } from '../../core';
import { useSession } from '../SessionContext';
import { failureMessage } from '../session';
import { Button, Notice, Screen, Section } from '../ui';
import { styles } from '../theme';

/** groupLabel is what a group is called on screen: its name, or its relay when the admin gave it none. */
export function groupLabel(name: string, serverUrl: string): string {
  return name !== '' ? name : serverUrl !== '' ? serverUrl : 'Unnamed group';
}

export function GroupsScreen(): React.JSX.Element {
  const session = useSession();
  const [message, setMessage] = useState('');
  const [busy, setBusy] = useState(false);
  const state = session.client?.snapshot();
  const current = groupLabel(session.groupName, state?.serverUrl ?? '');

  const run = (op: () => Promise<void>) => {
    if (busy) {
      return;
    }
    setBusy(true);
    setMessage('');
    void op()
      .catch((err: unknown) => setMessage(failureMessage(err)))
      .finally(() => setBusy(false));
  };

  const add = () => {
    Alert.alert(
      'Add a group?',
      session.groupDeleted
        ? `${current} was deleted by the server admin, so its keys are deleted from this phone.\n\nThis phone then joins the new group as a new device, from the setup screen.`
        : `${current} is set aside with its keys, and you can switch back to it from Groups at any time. Nothing syncs with it meanwhile.\n\nThis phone then joins the new group as a new device, from the setup screen.`,
      [
        { text: 'Cancel', style: 'cancel' },
        { text: 'Continue', onPress: () => run(() => session.addGroup()) },
      ],
    );
  };

  return (
    <Screen>
      <Text style={styles.title}>Groups</Text>
      <Text style={styles.muted}>
        One group is in use at a time. The others are set aside with their keys, and switching to
        one picks up where it left off.
      </Text>

      <Section title="In use">
        <View style={styles.rowBody}>
          <Text style={styles.text} numberOfLines={1}>
            {current}
          </Text>
          <Text style={styles.small} numberOfLines={1}>
            {session.groupDeleted
              ? 'Deleted by the server admin'
              : session.probablyRevoked
                ? 'This phone was probably removed from it'
                : (state?.serverUrl ?? '')}
          </Text>
        </View>
      </Section>

      <Section title="Set aside">
        {session.parkedGroups.length === 0 ? (
          <Text style={styles.muted}>
            No other groups. Add one to keep this group and set up another next to it.
          </Text>
        ) : (
          <ParkedGroupList groups={session.parkedGroups} busy={busy} onError={setMessage} />
        )}
      </Section>

      <Button label="Add a group" icon="plus" variant="secondary" onPress={add} disabled={busy} />

      {message !== '' && <Notice message={message} tone="error" />}
    </Screen>
  );
}

/**
 * ParkedGroupList is the groups set aside with Switch and Remove. The setup
 * screen shows it too: there, with no group in use, it is the way back.
 */
export function ParkedGroupList({
  groups,
  busy: outerBusy = false,
  onError,
}: {
  groups: readonly ParkedGroup[];
  busy?: boolean;
  onError: (message: string) => void;
}): React.JSX.Element {
  const session = useSession();
  const [busy, setBusy] = useState('');

  const run = (ref: string, op: () => Promise<void>) => {
    if (busy !== '' || outerBusy) {
      return;
    }
    setBusy(ref);
    onError('');
    void op()
      .catch((err: unknown) => onError(failureMessage(err)))
      .finally(() => setBusy(''));
  };

  const remove = (g: ParkedGroup) => {
    Alert.alert(
      `Remove ${groupLabel(g.groupName, g.serverUrl)}?`,
      'The keys this phone holds for the group are deleted. To use it again, pair this phone with a device that is still in it.\n\nNothing is removed from the relay: the group still lists this phone until another device revokes it, which re-keys the group.',
      [
        { text: 'Cancel', style: 'cancel' },
        {
          text: 'Remove',
          style: 'destructive',
          onPress: () => run(g.ref, () => session.removeGroup(g.ref)),
        },
      ],
    );
  };

  return (
    <View style={{ gap: 14 }}>
      {groups.map((g) => (
        <View key={g.ref} style={{ gap: 8 }}>
          <View style={styles.rowBody}>
            <Text style={styles.text} numberOfLines={1}>
              {groupLabel(g.groupName, g.serverUrl)}
            </Text>
            <Text style={styles.small} numberOfLines={1}>
              {g.serverUrl}
            </Text>
          </View>
          <View style={{ flexDirection: 'row', gap: 8 }}>
            <View style={{ flex: 1 }}>
              <Button
                label={busy === g.ref ? 'Switching…' : 'Switch'}
                icon="sync"
                onPress={() => run(g.ref, () => session.switchGroup(g.ref))}
                disabled={busy !== '' || outerBusy}
              />
            </View>
            <View style={{ flex: 1 }}>
              <Button
                label="Remove"
                icon="trash"
                variant="secondary"
                onPress={() => remove(g)}
                disabled={busy !== '' || outerBusy}
              />
            </View>
          </View>
        </View>
      ))}
    </View>
  );
}
