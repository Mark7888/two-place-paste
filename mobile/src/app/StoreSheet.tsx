/**
 * The store's save and edit sheet: a name, a description and tags, with the
 * limits as counters — the record refuses rather than cuts what does not fit
 * (spec/crypto.md §7a.5).
 *
 * A save of something the store already holds comes back refused, naming the
 * items it duplicates; the sheet shows them and offers "Save anyway".
 */

import React, { useEffect, useRef, useState } from 'react';
import { Pressable, ScrollView, Text, TextInput, View } from 'react-native';

import { STORE_LIMITS, utf8, type StoreDetails, type StoreItem } from '../core';
import { Icon } from './icons';
import { formatSize } from './preview';
import { failureMessage } from './session';
import { DuplicateError } from './storeState';
import { colors, styles } from './theme';
import { Button, Notice, Sheet } from './ui';

const bytesOf = (s: string): number => utf8(s).length;

export function StoreSheet({
  visible,
  title,
  what,
  initial,
  knownTags,
  available,
  saveLabel = 'Save',
  onSubmit,
  onDone,
  onClose,
}: {
  visible: boolean;
  title: string;
  /** what is being saved, in a few words: "the clipboard", "report.pdf". */
  what?: string;
  initial: StoreDetails;
  knownTags: readonly string[];
  /** available is the room left in the store, to show; undefined when not known. */
  available?: number;
  saveLabel?: string;
  /** onSubmit sends the save or edit and resolves with a message; allowDuplicate is "Save anyway". */
  onSubmit: (details: StoreDetails, allowDuplicate: boolean) => Promise<string>;
  onDone: (message: string) => void;
  onClose: () => void;
}): React.JSX.Element {
  const [name, setName] = useState(initial.name);
  const [description, setDescription] = useState(initial.description);
  const [tags, setTags] = useState<string[]>(initial.tags);
  const [draft, setDraft] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [duplicates, setDuplicates] = useState<StoreItem[] | null>(null);

  // Opening resets the sheet; a parent re-rendering while it is open must
  // not wipe what the user typed.
  const initialRef = useRef(initial);
  initialRef.current = initial;
  useEffect(() => {
    if (!visible) {
      return;
    }
    const init = initialRef.current;
    setName(init.name);
    setDescription(init.description);
    setTags(init.tags);
    setDraft('');
    setError('');
    setDuplicates(null);
  }, [visible]);

  const addTag = (raw: string) => {
    const t = raw.trim().replace(/,$/, '').trim();
    if (t === '' || tags.includes(t)) {
      setDraft('');
      return;
    }
    if (tags.length >= STORE_LIMITS.tags) {
      setError(`An item carries at most ${STORE_LIMITS.tags} tags.`);
      return;
    }
    if (bytesOf(t) > STORE_LIMITS.tag) {
      setError(`A tag is at most ${STORE_LIMITS.tag} bytes.`);
      return;
    }
    setError('');
    setTags([...tags, t]);
    setDraft('');
  };

  const nameBytes = bytesOf(name.trim());
  const descBytes = bytesOf(description);
  const invalid = nameBytes === 0 || nameBytes > STORE_LIMITS.name || descBytes > STORE_LIMITS.description;

  const submit = async (allowDuplicate: boolean) => {
    const pending = draft.trim();
    const allTags = pending !== '' && !tags.includes(pending) ? [...tags, pending] : tags;
    setBusy(true);
    setError('');
    try {
      const message = await onSubmit({ name: name.trim(), description, tags: allTags }, allowDuplicate);
      onDone(message);
    } catch (err) {
      if (err instanceof DuplicateError) {
        setDuplicates(err.duplicates);
      } else {
        setError(failureMessage(err));
      }
    } finally {
      setBusy(false);
    }
  };

  const suggestions = knownTags.filter((t) => !tags.includes(t) && (draft === '' || t.startsWith(draft.trim())));

  return (
    <Sheet visible={visible} title={title} onClose={onClose}>
      {duplicates !== null ? (
        <View style={{ gap: 12 }}>
          <Notice
            tone="info"
            message="The store already holds this content. Save it again anyway — under another name, say — or go back."
          />
          {duplicates.map((d) => (
            <View key={d.id} style={styles.card}>
              <Text style={styles.text}>{d.name}</Text>
              <Text style={styles.small}>saved {d.savedAt.toLocaleString()}</Text>
            </View>
          ))}
          <Button label={busy ? 'Saving…' : 'Save anyway'} onPress={() => void submit(true)} disabled={busy} />
          <Button label="Back" variant="secondary" onPress={() => setDuplicates(null)} disabled={busy} />
        </View>
      ) : (
        <ScrollView keyboardShouldPersistTaps="handled" contentContainerStyle={{ gap: 12 }}>
          {what !== undefined && (
            <Text style={styles.small}>
              Saving {what}
              {available !== undefined ? ` · ${formatSize(available)} left in the store` : ''}
            </Text>
          )}
          {error !== '' && <Notice message={error} tone="error" />}

          <Field label="Name" count={`${nameBytes}/${STORE_LIMITS.name}`} over={nameBytes > STORE_LIMITS.name}>
            <TextInput
              style={styles.input}
              value={name}
              onChangeText={setName}
              placeholder="What is it?"
              placeholderTextColor={colors.faint}
              accessibilityLabel="Name"
            />
          </Field>
          <Field
            label="Description"
            count={`${descBytes}/${STORE_LIMITS.description}`}
            over={descBytes > STORE_LIMITS.description}
          >
            <TextInput
              style={[styles.input, { minHeight: 72, textAlignVertical: 'top' }]}
              value={description}
              onChangeText={setDescription}
              multiline
              accessibilityLabel="Description"
            />
          </Field>
          <Field label="Tags" count={`${tags.length}/${STORE_LIMITS.tags}`} over={false}>
            {tags.length > 0 && (
              <View style={chipRow}>
                {tags.map((t) => (
                  <Pressable
                    key={t}
                    accessibilityRole="button"
                    accessibilityLabel={`Remove the tag ${t}`}
                    onPress={() => setTags(tags.filter((x) => x !== t))}
                    style={[chip, chipOn]}
                  >
                    <Text style={chipOnText}>{t}</Text>
                    <Icon name="close" size={12} color={colors.accent} />
                  </Pressable>
                ))}
              </View>
            )}
            <TextInput
              style={styles.input}
              value={draft}
              onChangeText={(v) => (v.endsWith(',') ? addTag(v) : setDraft(v))}
              onSubmitEditing={() => addTag(draft)}
              blurOnSubmit={false}
              placeholder="Type a tag and press Enter"
              placeholderTextColor={colors.faint}
              accessibilityLabel="New tag"
            />
            {suggestions.length > 0 && (
              <View style={chipRow}>
                {suggestions.slice(0, 12).map((t) => (
                  <Pressable key={t} accessibilityRole="button" onPress={() => addTag(t)} style={chip}>
                    <Icon name="plus" size={12} color={colors.muted} />
                    <Text style={chipText}>{t}</Text>
                  </Pressable>
                ))}
              </View>
            )}
          </Field>

          <Button
            label={busy ? 'Saving…' : saveLabel}
            onPress={() => void submit(false)}
            disabled={busy || invalid}
          />
        </ScrollView>
      )}
    </Sheet>
  );
}

function Field({
  label,
  count,
  over,
  children,
}: {
  label: string;
  count: string;
  over: boolean;
  children: React.ReactNode;
}): React.JSX.Element {
  return (
    <View style={{ gap: 6 }}>
      <View style={styles.rowInline}>
        <Text style={[styles.heading, { flex: 1, fontSize: 14 }]}>{label}</Text>
        <Text style={[styles.small, over && { color: colors.danger }]}>{count}</Text>
      </View>
      {children}
    </View>
  );
}

/** Chips, shared with the Store screen. */
export const chipRow = { flexDirection: 'row' as const, flexWrap: 'wrap' as const, gap: 6 };
export const chip = {
  flexDirection: 'row' as const,
  alignItems: 'center' as const,
  gap: 4,
  minHeight: 32,
  paddingHorizontal: 10,
  borderRadius: 16,
  borderWidth: 1,
  borderColor: colors.border,
};
export const chipOn = { backgroundColor: colors.accentSoft, borderColor: 'transparent' };
export const chipText = { color: colors.muted, fontSize: 13 };
export const chipOnText = { color: colors.accent, fontSize: 13, fontWeight: '600' as const };
