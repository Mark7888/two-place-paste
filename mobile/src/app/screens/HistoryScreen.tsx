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
 * Tapping a text or image row, or a file of a format src/app/preview.ts can
 * draw, opens a preview, which is the one thing that decrypts an entry — the
 * same operation copying it would do, on that entry alone and only when
 * asked; the client keeps what it just read, so saving a file right after its
 * preview does not download it again. Any other row opens to its Copy button only; a
 * file, which the Android clipboard cannot hold, is saved to Downloads or
 * opened in another app instead, whichever the user picks. An entry from an older epoch has no preview and no copy: this
 * device no longer holds the key it was written under (SPEC §3.3).
 */

import React, { useCallback, useEffect, useRef, useState, useSyncExternalStore } from 'react';
import { FlatList, Pressable, RefreshControl, Text, View } from 'react-native';

import type { EntryMeta, Progress } from '../../core';
import type { SavedFile } from '../../platform';
import { useSession } from '../SessionContext';
import {
  deliver,
  failureMessage,
  handleFile,
  needsFileChoice,
  pendingFromEntry,
  toClipboardItem,
  type FileChoice,
  type PendingFile,
} from '../session';
import { describeHeader, formatSize, headerPreviewable, previewOf, storeRoom, type Preview } from '../preview';
import { Button, FileChoices, Notice, OutcomeNotice, Sheet, Shimmer, TransferList } from '../ui';
import { colors, styles } from '../theme';
import { PreviewBody } from '../PreviewBody';
import { StoreSheet } from '../StoreSheet';
import { getStoreSnapshot, index as storeIndex, loadStore, saveItem, savedHash, subscribeStore } from '../storeState';

/** Loaded is a preview that arrived, or the reason one could not. */
type Loaded = { ok: true; preview: Preview } | { ok: false; error: string };

export function HistoryScreen(): React.JSX.Element {
  const session = useSession();
  const [entries, setEntries] = useState<EntryMeta[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState('');
  // saved is the file the last outcome saved, which its notice offers to open.
  const [saved, setSaved] = useState<SavedFile | null>(null);
  const [ok, setOk] = useState(true);
  const [open, setOpen] = useState<string | null>(null);
  // Previews live for as long as this screen does and no longer; reopening a
  // row does not decrypt it a second time.
  const [previews, setPreviews] = useState<Record<string, Loaded>>({});
  // downloading is the entries this screen is fetching right now, which their
  // rows show with a light sweeping along the top edge.
  const [downloading, setDownloading] = useState<ReadonlySet<string>>(new Set());
  const fetchingEntry = useCallback((id: string, on: boolean) => {
    setDownloading((prev) => {
      const next = new Set(prev);
      if (on) {
        next.add(id);
      } else {
        next.delete(id);
      }
      return next;
    });
  }, []);
  // jobs are the file rows being saved or opened right now, each on its own.
  const [jobs, setJobs] = useState<ReadonlySet<string>>(new Set());
  const setJob = useCallback((id: string, on: boolean) => {
    if (id === '') {
      return;
    }
    setJobs((prev) => {
      const next = new Set(prev);
      if (on) {
        next.add(id);
      } else {
        next.delete(id);
      }
      return next;
    });
  }, []);
  // A file waiting for save-or-open: asked before its download, from its header.
  const [file, setFile] = useState<PendingFile | null>(null);
  // The entry being saved to the store, while its sheet is open.
  const [saving, setSaving] = useState<EntryMeta | null>(null);
  // The store, for the "saved" marks and whether Save to store is offered.
  const store = useSyncExternalStore(subscribeStore, getStoreSnapshot);

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
    if (client !== null) {
      void loadStore(client);
    }
  }, [load, client]);

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
    fetchingEntry(id, true);
    void (async () => {
      try {
        const item = await client.getEntry(id, meta);
        setPreviews((prev) => ({ ...prev, [id]: { ok: true, preview: previewOf(item) } }));
      } catch (err) {
        setPreviews((prev) => ({ ...prev, [id]: { ok: false, error: failureMessage(err) } }));
      } finally {
        fetching.current.delete(id);
        fetchingEntry(id, false);
      }
    })();
  };

  const copy = (meta: EntryMeta) => {
    if (client === null) {
      return;
    }
    if (needsFileChoice(meta.header)) {
      // A file: ask first, from what its header says — the question appears
      // at once and names the file, and the download runs after the answer.
      setFile(pendingFromEntry(meta));
      return;
    }
    if (busy) {
      return;
    }
    setBusy(true);
    setSaved(null);
    fetchingEntry(meta.id, true);
    void (async () => {
      try {
        const item = await client.getEntry(meta.id, meta);
        // The download is over; what follows is local.
        fetchingEntry(meta.id, false);
        const result = await deliver(toClipboardItem(item));
        if (result.direction === 'file' && result.file !== undefined) {
          // Only when the header could not be read: the download came first.
          setFile(result.file);
          return;
        }
        setOk(true);
        setMessage(result.message);
      } catch (err) {
        setOk(false);
        setMessage(failureMessage(err));
        setSaved(null);
      } finally {
        fetchingEntry(meta.id, false);
        setBusy(false);
      }
    })();
  };

  /**
   * choose runs the answer to a file question as a job of its own: the sheet
   * closes at once, the row shows the light while its entry downloads, and the
   * rest of the screen stays usable — another row can be started meanwhile.
   */
  const choose = (choice: FileChoice) => {
    const pending = file;
    if (pending === null || client === null) {
      return;
    }
    const id = pending.entry?.id ?? '';
    if (id !== '' && jobs.has(id)) {
      return;
    }
    setFile(null);
    setJob(id, true);
    void (async () => {
      try {
        const result = await handleFile(client, pending, choice);
        setOk(true);
        setMessage(result.message);
        setSaved(result.saved ?? null);
      } catch (err) {
        // The question comes back, naming the file, with the reason: the
        // other option is one tap away, and the entry is cached by now.
        setOk(false);
        setMessage(failureMessage(err));
        setSaved(null);
        setFile(pending);
      } finally {
        setJob(id, false);
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
        <TransferList transfers={session.transfers} />
        {message !== '' && (
          <OutcomeNotice
            message={message}
            ok={ok}
            saved={saved}
            onError={(m) => {
              setOk(false);
              setMessage(m);
              setSaved(null);
            }}
          />
        )}
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
            <View style={[styles.card, { overflow: 'hidden' }, !canOpen && { opacity: 0.6 }]}>
              <Shimmer
                active={downloadingNow(item.id, downloading, jobs, session.transfers)}
              />
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
                  {savedHash(item.header?.sha256Hex) ? '  ·  ' : ''}
                  {savedHash(item.header?.sha256Hex) && (
                    <Text style={{ color: colors.ok }}>saved in the store</Text>
                  )}
                </Text>
                <Text style={{ color: canOpen ? colors.accent : colors.faint, fontSize: 13 }}>
                  {!canOpen
                    ? 'Predates the last re-key — cannot be opened'
                    : expanded
                      ? 'Hide'
                      : previewable
                        ? 'Tap to preview'
                        : needsFileChoice(item.header)
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
                    <PreviewBody preview={loaded.preview} />
                  )}
                  <Button
                    label={
                      jobs.has(item.id)
                        ? 'Downloading…'
                        : needsFileChoice(item.header)
                          ? 'Save or open on this device'
                          : 'Copy to this device'
                    }
                    variant="secondary"
                    onPress={() => copy(item)}
                    disabled={jobs.has(item.id) || (busy && !needsFileChoice(item.header))}
                  />
                  {store.status === 'ready' && (
                    <Button
                      label="Save to store"
                      icon="store"
                      variant="secondary"
                      onPress={() => setSaving(item)}
                      disabled={busy}
                    />
                  )}
                </View>
              )}
            </View>
          );
        }}
      />

      <StoreSheet
        visible={saving !== null}
        title="Save to store"
        what={saving === null ? undefined : `${describeHeader(saving.header)} from the history`}
        available={storeRoom(store.limits)}
        initial={{ name: saving?.header?.filename ?? '', description: '', tags: [] }}
        knownTags={storeIndex.tags().map((t) => t.tag)}
        onClose={() => setSaving(null)}
        onSubmit={async (details, allowDuplicate) => {
          if (client === null || saving === null) {
            return '';
          }
          fetchingEntry(saving.id, true);
          try {
            const entry = await client.getEntry(saving.id, saving);
            const it = await saveItem(client, toClipboardItem(entry), details, allowDuplicate);
            return `Saved “${it.name}” to the store.`;
          } finally {
            fetchingEntry(saving.id, false);
          }
        }}
        onDone={(msg) => {
          setSaving(null);
          setOk(true);
          setMessage(msg);
          setSaved(null);
        }}
      />

      <Sheet visible={file !== null} title="Save or open?" onClose={() => setFile(null)}>
        <FileChoices
          name={file?.name ?? ''}
          detail={file?.detail ?? ''}
          disabled={busy}
          onChoose={choose}
        />
        {!ok && message !== '' && <Notice message={message} tone="error" />}
      </Sheet>
    </View>
  );
}

/**
 * downloadingNow says whether an entry's row shows the sweeping light: it is
 * being read right now, and not merely queued behind another download. A
 * small entry has no progress reports, so this screen's own fetch is what
 * says it is moving.
 */
function downloadingNow(
  id: string,
  fetching: ReadonlySet<string>,
  jobs: ReadonlySet<string>,
  transfers: readonly Progress[],
): boolean {
  const t = transfers.find((x) => x.direction === 'download' && x.entryId === id);
  if (t !== undefined) {
    return !t.waiting;
  }
  return fetching.has(id) || jobs.has(id);
}
