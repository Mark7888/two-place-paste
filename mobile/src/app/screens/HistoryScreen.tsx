/**
 * History: the group's entry metadata, listed when the screen is opened
 * (SPEC §6, §7.1).
 *
 * Opening this tab is the user asking — that is the whole of the rule, and the
 * fetch button that used to stand in front of it was asking twice. Nothing
 * here runs while the tab is closed, nothing runs on reconnect, and what is
 * listed is what the relay holds — a size, an epoch, two timestamps — plus
 * each entry's sealed header, opened here, which says what the entry is
 * without fetching it (spec/crypto.md §5.5).
 *
 * Tapping a text or image row opens a preview, which is the one thing that
 * decrypts an entry — the same operation copying it would do, on that entry
 * alone and only when asked. Any other row opens to its Copy button only; a
 * file, which the Android clipboard cannot hold, is saved to Downloads or
 * opened in another app instead, whichever the user picks. An entry from an older epoch has no preview and no copy: this
 * device no longer holds the key it was written under (SPEC §3.3).
 */

import React, { useCallback, useEffect, useRef, useState } from 'react';
import { FlatList, Image, Pressable, RefreshControl, Text, View } from 'react-native';

import type { EntryMeta } from '../../core';
import type { ClipboardItem } from '../../platform';
import { useSession } from '../SessionContext';
import { deliver, failureMessage, handleFile, toClipboardItem, type FileChoice } from '../session';
import { describeHeader, formatSize, headerPreviewable, previewOf, type Preview } from '../preview';
import { Button, FileChoices, Notice, Sheet, TransferBar } from '../ui';
import { colors, styles } from '../theme';

/** Loaded is a preview that arrived, or the reason one could not. */
type Loaded = { ok: true; preview: Preview } | { ok: false; error: string };

export function HistoryScreen(): React.JSX.Element {
  const session = useSession();
  const [entries, setEntries] = useState<EntryMeta[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState('');
  const [ok, setOk] = useState(true);
  const [open, setOpen] = useState<string | null>(null);
  // Previews live for as long as this screen does and no longer; reopening a
  // row does not decrypt it a second time.
  const [previews, setPreviews] = useState<Record<string, Loaded>>({});
  // A fetched file the clipboard cannot hold, waiting for save-or-open.
  const [file, setFile] = useState<ClipboardItem | null>(null);

  const client = session.client;
  const inFlight = useRef(false);
  const fetching = useRef<Set<string>>(new Set());

  const load = useCallback(async () => {
    if (client === null || inFlight.current) {
      return;
    }
    inFlight.current = true;
    setLoading(true);
    try {
      await client.connect();
      const page = await client.getHistory({ limit: 50 });
      setEntries(page.entries);
      setOk(true);
      setMessage('');
    } catch (err) {
      setOk(false);
      setMessage(failureMessage(err));
    } finally {
      inFlight.current = false;
      setLoading(false);
    }
  }, [client]);

  useEffect(() => {
    void load();
  }, [load]);

  // The relay removed an entry early to stay within its storage limit: it is
  // gone, so it leaves the list now rather than failing when tapped.
  const evicted = session.evicted;
  useEffect(() => {
    if (evicted === null) {
      return;
    }
    setEntries((prev) => prev?.filter((e) => e.id !== evicted.id) ?? prev);
    setOpen((cur) => (cur === evicted.id ? null : cur));
  }, [evicted]);

  const toggle = (meta: EntryMeta) => {
    const id = meta.id;
    if (open === id) {
      setOpen(null);
      return;
    }
    setOpen(id);
    if (!headerPreviewable(meta.header)) {
      return;
    }
    if (previews[id] !== undefined || fetching.current.has(id) || client === null) {
      return;
    }
    fetching.current.add(id);
    void (async () => {
      try {
        const item = await client.getEntry(id);
        setPreviews((prev) => ({ ...prev, [id]: { ok: true, preview: previewOf(item) } }));
      } catch (err) {
        setPreviews((prev) => ({ ...prev, [id]: { ok: false, error: failureMessage(err) } }));
      } finally {
        fetching.current.delete(id);
      }
    })();
  };

  const copy = (meta: EntryMeta) => {
    if (client === null || busy) {
      return;
    }
    setBusy(true);
    void (async () => {
      try {
        const item = await client.getEntry(meta.id);
        const result = await deliver(toClipboardItem(item));
        if (result.direction === 'file' && result.item !== undefined) {
          setFile(result.item);
          return;
        }
        setOk(true);
        setMessage(result.message);
      } catch (err) {
        setOk(false);
        setMessage(failureMessage(err));
      } finally {
        setBusy(false);
      }
    })();
  };

  const choose = (choice: FileChoice) => {
    const item = file;
    if (item === null || busy) {
      return;
    }
    setBusy(true);
    void (async () => {
      try {
        const result = await handleFile(item, choice);
        setFile(null);
        setOk(true);
        setMessage(result.message);
      } catch (err) {
        // The sheet stays up so the other option is still one tap away.
        setOk(false);
        setMessage(failureMessage(err));
      } finally {
        setBusy(false);
      }
    })();
  };

  // An entry from an older epoch predates a rekey: this device no longer holds
  // the key it was written under, so it can be neither previewed nor copied.
  const readable = (meta: EntryMeta) => meta.epoch === session.epoch;

  return (
    <View style={styles.screen}>
      <View style={{ paddingHorizontal: 16, paddingTop: 16, gap: 8 }}>
        <Text style={styles.title}>History</Text>
        <Text style={styles.lede}>
          Newest first. Entries expire within 24 hours. Tap one to see what is in it.
        </Text>
        <TransferBar transfer={session.transfer} />
        {message !== '' && <Notice message={message} tone={ok ? 'ok' : 'error'} />}
      </View>

      <FlatList
        data={entries ?? []}
        keyExtractor={(item) => item.id}
        contentContainerStyle={styles.content}
        refreshControl={
          <RefreshControl
            refreshing={loading}
            onRefresh={() => void load()}
            tintColor={colors.accent}
            colors={[colors.accent]}
          />
        }
        ListEmptyComponent={
          <View style={styles.card}>
            <Text style={styles.muted}>
              {loading
                ? 'Loading the group’s entries…'
                : entries === null
                  ? 'Pull down to load the group’s entries.'
                  : 'The group has no entries yet.'}
            </Text>
          </View>
        }
        renderItem={({ item }) => {
          const canOpen = readable(item);
          const expanded = open === item.id;
          const loaded = previews[item.id];
          const previewable = headerPreviewable(item.header);
          return (
            <View style={[styles.card, !canOpen && { opacity: 0.6 }]}>
              <Pressable
                accessibilityRole="button"
                accessibilityLabel={`Entry from ${item.createdAt.toLocaleString()}`}
                accessibilityState={{ disabled: !canOpen, expanded }}
                onPress={() => toggle(item)}
                disabled={!canOpen}
                android_ripple={{ color: colors.accentSoft }}
                style={{ gap: 4 }}
              >
                <Text style={styles.text}>{item.createdAt.toLocaleString()}</Text>
                <Text style={styles.small}>
                  {describeHeader(item.header)} · {formatSize(item.header?.size ?? item.size)}
                </Text>
                <Text style={{ color: canOpen ? colors.accent : colors.faint, fontSize: 13 }}>
                  {!canOpen
                    ? 'Predates the last re-key — cannot be opened'
                    : expanded
                      ? 'Hide'
                      : previewable
                        ? 'Tap to preview'
                        : isFile(item)
                          ? 'No preview — tap to save or open'
                          : 'No preview — tap to copy'}
                </Text>
              </Pressable>

              {expanded && (
                <View style={{ gap: 10, paddingTop: 2 }}>
                  {!previewable ? null : loaded === undefined ? (
                    <Text style={styles.small}>Decrypting…</Text>
                  ) : !loaded.ok ? (
                    <Notice message={loaded.error} tone="error" />
                  ) : (
                    <Body preview={loaded.preview} />
                  )}
                  <Button
                    label={isFile(item) ? 'Save or open on this device' : 'Copy to this device'}
                    variant="secondary"
                    onPress={() => copy(item)}
                    disabled={busy}
                  />
                </View>
              )}
            </View>
          );
        }}
      />

      <Sheet visible={file !== null} title="Save or open?" onClose={() => setFile(null)}>
        <FileChoices
          name={file === null ? '' : file.filename !== '' ? file.filename : file.contentType}
          disabled={busy}
          onChoose={choose}
        />
        {!ok && message !== '' && <Notice message={message} tone="error" />}
      </Sheet>
    </View>
  );
}

/** isFile says, from the sealed header alone, whether an entry is a file rather than clipboard content. */
function isFile(meta: EntryMeta): boolean {
  return meta.header !== null && meta.header.filename !== '';
}

/** Body renders what a preview turned out to be. */
function Body({ preview }: { preview: Preview }): React.JSX.Element {
  if (preview.kind === 'image') {
    if (preview.imageTooLarge || preview.imageUri === '') {
      return (
        <Text style={styles.small}>
          {preview.contentType} · {formatSize(preview.bytes)} — too large to show here.
        </Text>
      );
    }
    return (
      <>
        <ImagePreview uri={preview.imageUri} />
        <Text style={styles.small}>
          {preview.contentType} · {formatSize(preview.bytes)}
        </Text>
      </>
    );
  }

  if (preview.kind === 'file') {
    // A file has no body worth drawing; its name and size are the preview.
    return (
      <Text style={styles.small}>
        {preview.filename === '' ? 'file' : preview.filename} · {preview.contentType} ·{' '}
        {formatSize(preview.bytes)}
      </Text>
    );
  }

  return (
    <>
      <View style={styles.previewBox}>
        <Text style={styles.previewText}>
          {preview.text === '' ? '(nothing to show)' : preview.text}
        </Text>
      </View>
      <Text style={styles.small}>
        {preview.truncated ? 'Shown in part · ' : ''}
        {preview.contentType} · {formatSize(preview.bytes)}
      </Text>
    </>
  );
}

/**
 * ImagePreview draws the image at its own aspect ratio.
 *
 * The ratio comes from the decoded image itself, through `onLoad`, because
 * nothing in an entry's metadata carries it. Until it arrives the box has a
 * provisional height; `resizeMode="contain"` means the picture is never
 * distorted at any point, only letterboxed.
 *
 * A landscape image takes the full width and derives its height; a portrait one
 * is pinned to a maximum height and derives its width, so a tall screenshot
 * does not take over the whole screen.
 */
function ImagePreview({ uri }: { uri: string }): React.JSX.Element {
  const [ratio, setRatio] = useState<number | null>(null);

  const box =
    ratio === null
      ? { width: '100%' as const, height: 160 }
      : ratio >= 1
        ? { width: '100%' as const, aspectRatio: ratio }
        : { height: 280, aspectRatio: ratio, alignSelf: 'flex-start' as const };

  return (
    <Image
      accessibilityIgnoresInvertColors
      source={{ uri }}
      resizeMode="contain"
      onLoad={(e) => {
        const { width, height } = e.nativeEvent.source;
        if (width > 0 && height > 0) {
          setRatio(width / height);
        }
      }}
      style={[styles.previewImage, box]}
    />
  );
}
