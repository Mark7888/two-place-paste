/**
 * Store: what the group keeps on purpose, beside its 24-hour history
 * (SPEC §6b).
 *
 * Items do not expire. Their names, descriptions and tags are opened here and
 * searched here — the relay cannot search what it cannot read — and an item's
 * body is downloaded only when the user copies, saves or opens it.
 *
 * Saving comes from three places: the clipboard and a file (here), and an
 * entry of the History. A file comes through the system's own picker, which
 * hands this app only the one file the user chose. It is staged rather than
 * read — copied and hashed by the platform — and saved in the background,
 * sealed and sent natively, the same way as a file shared to the app
 * (`backgroundJobs.ts`).
 */

import React, { useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react';
import { Alert, FlatList, Pressable, ScrollView, Text, TextInput, View } from 'react-native';

import type { Item, StoreItem, StoreSort } from '../../core';
import { background, clipboard, files, share, type SavedFile, type StagedFile } from '../../platform';
import { enqueue } from '../backgroundJobs';
import { describePayload, payloadHash, suggestedName, type Payload } from '../share';
import { keepAliveDuring } from '../keepAlive';
import { useSession } from '../SessionContext';
import { Icon, type IconName } from '../icons';
import { PreviewBody } from '../PreviewBody';
import { formatSize, headerPreviewable, MAX_FILE_PREVIEW_BYTES, previewOf, storeRoom, type Preview } from '../preview';
import { receiveLabel } from '../choices';
import {
  choiceFor,
  deliver,
  describeItem,
  failureMessage,
  handleFile,
  pendingFromStore,
  toClipboardItem,
  type FileChoice,
  type PendingFile,
} from '../session';
import { chip, chipOn, chipOnText, chipRow, chipText, StoreSheet } from '../StoreSheet';
import {
  DuplicateError,
  deleteItem,
  editItem,
  getStoreSnapshot,
  index,
  loadStore,
  saveItem,
  subscribeStore,
} from '../storeState';
import { colors, styles } from '../theme';
import { Button, FILE_CHOICES_TITLE, FileChoices, Notice, OutcomeNotice, Sheet, TransferList } from '../ui';

const SORTS: { id: StoreSort; label: string }[] = [
  { id: 'newest', label: 'Newest' },
  { id: 'oldest', label: 'Oldest' },
  { id: 'name', label: 'A–Z' },
  { id: 'name-desc', label: 'Z–A' },
  { id: 'largest', label: 'Largest' },
  { id: 'smallest', label: 'Smallest' },
];

/** Loaded is a preview that arrived, or the reason one could not. */
type Loaded = { ok: true; preview: Preview } | { ok: false; error: string };

/** previewable says whether an item offers a preview: by the same rule as a
 *  history entry, from what its opened metadata says. */
const previewable = (it: StoreItem) =>
  headerPreviewable({ contentType: it.contentType, filename: it.filename, size: it.bodySize });

type Dialog =
  | { kind: 'clipboard'; item: { contentType: string; filename: string; body: Uint8Array } }
  | { kind: 'staged'; payload: Payload }
  | { kind: 'edit'; item: StoreItem }
  | null;

/** stagedShare describes a staged payload as a share, for the name the sheet suggests. */
function stagedShare(p: Payload) {
  return p.kind === 'file'
    ? { id: 0, kind: 'file' as const, name: p.filename, contentType: p.contentType, size: p.file.size, previewUri: '' }
    : { id: 0, kind: 'unsupported' as const };
}

export function StoreScreen(): React.JSX.Element {
  const session = useSession();
  const client = session.client;
  const store = useSyncExternalStore(subscribeStore, getStoreSnapshot);
  const [text, setText] = useState('');
  const [tags, setTags] = useState<string[]>([]);
  const [sort, setSort] = useState<StoreSort>('newest');
  const [open, setOpen] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [message, setMessage] = useState('');
  const [ok, setOk] = useState(true);
  const [saved, setSaved] = useState<SavedFile | null>(null);
  const [dialog, setDialog] = useState<Dialog>(null);
  const [file, setFile] = useState<PendingFile | null>(null);

  // Previews live for as long as this screen does, as in the History. The
  // last item opened is kept whole too, so Copy, Save, Open or Share right after
  // its preview does not download it again.
  const [previews, setPreviews] = useState<Record<string, Loaded>>({});
  const fetching = useRef<Set<string>>(new Set());
  const lastFetched = useRef<{ id: string; item: Item } | null>(null);

  useEffect(() => {
    if (client !== null) {
      void loadStore(client);
    }
  }, [client]);

  // The query runs again whenever the index changes (store.version) or the
  // user types, picks a tag or a sort order.
  const items = useMemo(
    () => index.query({ text, tags, sort }),
    [text, tags, sort, store.version],
  );
  const tagCounts = useMemo(
    () => index.tags(),
    [store.version],
  );
  const total = index.size;

  const report = (good: boolean, msg: string, file?: SavedFile | null) => {
    setOk(good);
    setMessage(msg);
    setSaved(file ?? null);
  };

  const run = (id: string, work: () => Promise<void>) => {
    if (busy !== null) {
      return;
    }
    setBusy(id);
    void (async () => {
      try {
        await work();
      } catch (err) {
        report(false, failureMessage(err));
      } finally {
        setBusy(null);
      }
    })();
  };

  const saveClipboard = () =>
    run('clipboard', async () => {
      const item = await clipboard.read();
      if (item === null) {
        report(false, 'The clipboard is empty, or holds nothing this app can carry.');
        return;
      }
      setDialog({ kind: 'clipboard', item });
    });

  const addFile = () =>
    run('file', async () => {
      const room = store.limits?.availableBytes ?? 0;
      const picked = await files.pickToStage(room);
      if (picked === null) {
        return;
      }
      if (picked.tooLarge) {
        report(false, `${picked.name} is ${formatSize(picked.size)}; the store has room for ${formatSize(room)}.`);
        return;
      }
      if (picked.size === 0) {
        share.discard(picked.token);
        report(false, 'That file is empty.');
        return;
      }
      const file: StagedFile = {
        token: picked.token,
        name: picked.name,
        contentType: picked.contentType,
        size: picked.size,
        sha256: picked.sha256,
      };
      setDialog({ kind: 'staged', payload: { kind: 'file', file, contentType: picked.contentType, filename: picked.name } });
    });

  /** closeDialog closes the save sheet, deleting a staged file that will not be saved. */
  const closeDialog = () => {
    if (dialog?.kind === 'staged' && dialog.payload.kind === 'file') {
      share.discard(dialog.payload.file.token);
    }
    setDialog(null);
  };

  /** fetchItem downloads an item, or reuses the one its preview just read. */
  const fetchItem = async (it: StoreItem): Promise<Item> => {
    if (client === null) {
      throw new Error('not connected');
    }
    const kept = lastFetched.current;
    if (kept !== null && kept.id === it.id) {
      return kept.item;
    }
    // A large item's download carries on if the user switches apps meanwhile.
    const item = await keepAliveDuring(() => client.fetchStoreItem(it));
    // Only something a preview could have read is kept: a large file copied
    // once is not worth holding in memory.
    lastFetched.current = item.body.length <= MAX_FILE_PREVIEW_BYTES ? { id: it.id, item } : null;
    return item;
  };

  const toggle = (it: StoreItem) => {
    if (open === it.id) {
      setOpen(null);
      return;
    }
    setOpen(it.id);
    if (!previewable(it) || previews[it.id] !== undefined || fetching.current.has(it.id) || client === null) {
      return;
    }
    fetching.current.add(it.id);
    void (async () => {
      try {
        const item = await fetchItem(it);
        setPreviews((prev) => ({ ...prev, [it.id]: { ok: true, preview: previewOf(item) } }));
      } catch (err) {
        setPreviews((prev) => ({ ...prev, [it.id]: { ok: false, error: failureMessage(err) } }));
      } finally {
        fetching.current.delete(it.id);
      }
    })();
  };

  const copy = (it: StoreItem) => {
    const rule = choiceFor(it.contentType, it.filename, true);
    if (rule.ask && lastFetched.current?.id !== it.id) {
      // A file or an image: ask first, from its metadata, and download after
      // the answer — natively, when it is to be saved, opened or shared.
      setFile(pendingFromStore(it, rule.canCopy));
      return;
    }
    fetchAndDeliver(it);
  };

  const fetchAndDeliver = (it: StoreItem) =>
    run(it.id, async () => {
      if (client === null) {
        return;
      }
      const item = toClipboardItem(await fetchItem(it));
      const result = await deliver(item, true);
      if (result.direction === 'file' && result.file !== undefined) {
        setFile(result.file);
        return;
      }
      report(true, result.message);
    });

  const choose = (choice: FileChoice) => {
    const pending = file;
    if (pending === null || client === null) {
      return;
    }
    setFile(null);
    run('file-choice', async () => {
      const result = await handleFile(client, pending, choice);
      report(true, result.message, result.saved);
    });
  };

  const confirmDelete = (it: { id: string; name: string }) => {
    Alert.alert(
      'Delete from the store?',
      `“${it.name}” is deleted for every device of the group. This cannot be undone.`,
      [
        { text: 'Cancel', style: 'cancel' },
        {
          text: 'Delete',
          style: 'destructive',
          onPress: () =>
            run(it.id, async () => {
              if (client === null) {
                return;
              }
              await deleteItem(client, it.id);
              setOpen(null);
              report(true, `Deleted “${it.name}”.`);
            }),
        },
      ],
    );
  };

  const header = (
    <View style={{ gap: 12 }}>
      <Text style={styles.title}>Store</Text>
      <Text style={styles.lede}>
        What the group keeps on purpose. Items do not expire, and the relay cannot read a name, a tag or a byte.
      </Text>
      <TransferList transfers={session.transfers} />
      {message !== '' && (
        <OutcomeNotice message={message} ok={ok} saved={saved} onError={(m) => report(false, m)} />
      )}
      {store.status === 'error' && <Notice message={store.error} tone="error" />}
      {store.status === 'ready' && store.limits !== null && (
        <>
          <SpaceBar limits={store.limits} />
          <View style={{ flexDirection: 'row', gap: 8 }}>
            <View style={{ flex: 1 }}>
              <Button label="Save clipboard" icon="upload" onPress={saveClipboard} disabled={busy !== null} />
            </View>
            {files.available && (
              <View style={{ flex: 1 }}>
                <Button label="Add file" icon="plus" variant="secondary" onPress={addFile} disabled={busy !== null} />
              </View>
            )}
          </View>
          <View style={[styles.input, { flexDirection: 'row', alignItems: 'center', gap: 8, paddingVertical: 0 }]}>
            <Icon name="search" size={18} color={colors.faint} />
            <TextInput
              style={{ flex: 1, color: colors.text, fontSize: 15, minHeight: 44 }}
              value={text}
              onChangeText={setText}
              placeholder="Search names and descriptions"
              placeholderTextColor={colors.faint}
              accessibilityLabel="Search the store"
            />
          </View>
          <ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={{ gap: 6 }}>
            {SORTS.map((s) => (
              <Pressable
                key={s.id}
                accessibilityRole="button"
                accessibilityState={{ selected: sort === s.id }}
                onPress={() => setSort(s.id)}
                style={[chip, sort === s.id && chipOn]}
              >
                <Text style={sort === s.id ? chipOnText : chipText}>{s.label}</Text>
              </Pressable>
            ))}
          </ScrollView>
          {tagCounts.length > 0 && (
            <View style={chipRow}>
              {tagCounts.map((t) => {
                const on = tags.includes(t.tag);
                return (
                  <Pressable
                    key={t.tag}
                    accessibilityRole="button"
                    accessibilityState={{ selected: on }}
                    accessibilityLabel={`Tag ${t.tag}, ${t.count} items`}
                    onPress={() => setTags(on ? tags.filter((x) => x !== t.tag) : [...tags, t.tag])}
                    style={[chip, on && chipOn]}
                  >
                    <Icon name="tag" size={12} color={on ? colors.accent : colors.muted} />
                    <Text style={on ? chipOnText : chipText}>
                      {t.tag} <Text style={{ color: colors.faint }}>{t.count}</Text>
                    </Text>
                  </Pressable>
                );
              })}
            </View>
          )}
        </>
      )}
    </View>
  );

  const empty =
    store.status === 'off' ? (
      <View style={styles.card}>
        <Text style={styles.muted}>
          The server admin has not turned the store on for this group. Ask them to, from the group’s page in the admin
          screens.
        </Text>
      </View>
    ) : store.status === 'loading' || store.status === 'idle' ? (
      <View style={styles.card}>
        <Text style={styles.muted}>Opening the store…</Text>
      </View>
    ) : store.status === 'ready' ? (
      <View style={styles.card}>
        <Text style={styles.muted}>
          {total === 0
            ? 'Nothing saved yet. Save the clipboard, add a file, or save an entry from the History.'
            : 'No item matches.'}
        </Text>
      </View>
    ) : undefined;

  const knownTags = tagCounts.map((t) => t.tag);

  return (
    <View style={styles.screen}>
      <FlatList
        data={store.status === 'ready' ? items : []}
        keyExtractor={(it) => it.id}
        contentContainerStyle={styles.content}
        keyboardShouldPersistTaps="handled"
        ListHeaderComponent={header}
        ListEmptyComponent={empty}
        onRefresh={client === null ? undefined : () => void loadStore(client, true)}
        refreshing={store.status === 'loading'}
        renderItem={({ item: it }) => {
          const expanded = open === it.id;
          const loaded = previews[it.id];
          // Whether Copy asks first (a file, or an image), and whether Copy is among the answers.
          const rule = choiceFor(it.contentType, it.filename, true);
          return (
            <View style={styles.card}>
              <Pressable
                accessibilityRole="button"
                accessibilityState={{ expanded }}
                accessibilityLabel={it.name}
                onPress={() => toggle(it)}
                android_ripple={{ color: colors.accentSoft }}
                style={{ flexDirection: 'row', gap: 10 }}
              >
                <Icon name={iconOf(it)} size={20} color={colors.muted} />
                <View style={{ flex: 1, gap: 3 }}>
                  <Text style={[styles.text, { fontWeight: '600' }]}>{it.name}</Text>
                  {it.description !== '' && <Text style={styles.muted}>{it.description}</Text>}
                  <Text style={styles.small}>
                    {it.savedAt.toLocaleDateString()} · {it.filename !== '' ? it.filename : it.contentType.split(';')[0]} ·{' '}
                    {formatSize(it.bodySize)}
                  </Text>
                  {it.tags.length > 0 && (
                    <View style={chipRow}>
                      {it.tags.map((t) => (
                        <View key={t} style={[chip, { minHeight: 24 }]}>
                          <Text style={[chipText, { fontSize: 12 }]}>{t}</Text>
                        </View>
                      ))}
                    </View>
                  )}
                </View>
              </Pressable>
              {expanded && (
                <View style={{ gap: 8, paddingTop: 4 }}>
                  {!previewable(it) ? null : loaded === undefined ? (
                    <Text style={styles.small}>Decrypting…</Text>
                  ) : !loaded.ok ? (
                    <Notice message={loaded.error} tone="error" />
                  ) : (
                    <PreviewBody preview={loaded.preview} />
                  )}
                  <Button
                    label={busy === it.id ? 'Downloading…' : receiveLabel(rule)}
                    icon={rule.ask ? 'download' : 'check'}
                    onPress={() => copy(it)}
                    disabled={busy !== null}
                  />
                  <View style={{ flexDirection: 'row', gap: 8 }}>
                    <View style={{ flex: 1 }}>
                      <Button
                        label="Edit"
                        icon="edit"
                        variant="secondary"
                        onPress={() => setDialog({ kind: 'edit', item: it })}
                        disabled={busy !== null}
                      />
                    </View>
                    <View style={{ flex: 1 }}>
                      <Button
                        label="Delete"
                        icon="trash"
                        variant="secondary"
                        onPress={() => confirmDelete(it)}
                        disabled={busy !== null}
                      />
                    </View>
                  </View>
                </View>
              )}
            </View>
          );
        }}
        ListFooterComponent={
          store.status === 'ready' && store.unreadable.length > 0 ? (
            <View style={{ gap: 8 }}>
              <Text style={styles.sectionTitle}>Items this device cannot open</Text>
              {store.unreadable.map((u) => (
                <View key={u.id} style={[styles.card, styles.rowInline]}>
                  <View style={{ flex: 1 }}>
                    <Text style={styles.text}>Saved {u.savedAt.toLocaleString()}</Text>
                    <Text style={styles.small}>{u.reason}</Text>
                  </View>
                  <Pressable
                    accessibilityRole="button"
                    accessibilityLabel="Delete this unreadable item"
                    onPress={() => confirmDelete({ id: u.id, name: 'This unreadable item' })}
                    hitSlop={12}
                  >
                    <Icon name="trash" size={20} color={colors.danger} />
                  </Pressable>
                </View>
              ))}
            </View>
          ) : undefined
        }
      />

      <StoreSheet
        visible={dialog !== null}
        title={dialog?.kind === 'edit' ? 'Edit item' : 'Save to store'}
        what={
          dialog === null || dialog.kind === 'edit'
            ? undefined
            : dialog.kind === 'staged'
              ? describePayload(dialog.payload)
              : describeItem(dialog.item)
        }
        available={dialog?.kind === 'edit' ? undefined : storeRoom(store.limits)}
        initial={
          dialog?.kind === 'edit'
            ? { name: dialog.item.name, description: dialog.item.description, tags: dialog.item.tags }
            : { name: dialog?.kind === 'staged' ? suggestedName(stagedShare(dialog.payload)) : '', description: '', tags: [] }
        }
        knownTags={knownTags}
        saveLabel={dialog?.kind === 'edit' ? 'Save changes' : 'Save'}
        onClose={closeDialog}
        onSubmit={async (details, allowDuplicate) => {
          if (client === null || dialog === null) {
            return '';
          }
          if (dialog.kind === 'edit') {
            const it = await editItem(client, dialog.item, details);
            return `Saved the changes to “${it.name}”.`;
          }
          if (dialog.kind === 'staged') {
            // The duplicate check is made here, while "Save anyway" can still
            // be answered; the save runs in the background, with a notification.
            if (!allowDuplicate) {
              await loadStore(client);
              const dups = index.duplicates(await payloadHash(dialog.payload));
              if (dups.length > 0) {
                throw new DuplicateError(dups);
              }
            }
            await background.askToNotify();
            await enqueue(dialog.payload, 'store', details);
            return `Saving “${details.name}” to the store in the background.`;
          }
          const it = await saveItem(client, dialog.item, details, allowDuplicate);
          return `Saved “${it.name}” to the store.`;
        }}
        onDone={(msg) => {
          setDialog(null);
          report(true, msg);
        }}
      />

      <Sheet visible={file !== null} title={FILE_CHOICES_TITLE} onClose={() => setFile(null)}>
        <FileChoices
          name={file?.name ?? ''}
          detail={file?.detail ?? ''}
          canCopy={file?.canCopy ?? false}
          disabled={busy !== null}
          onChoose={choose}
        />
      </Sheet>
    </View>
  );
}

/** SpaceBar is the store's room: bytes and items used against the limits. */
function SpaceBar({ limits }: { limits: { maxBytes: number; usedBytes: number; availableBytes: number; maxItems: number; usedItems: number } }): React.JSX.Element {
  const pct = limits.maxBytes > 0 ? Math.min(100, (limits.usedBytes / limits.maxBytes) * 100) : 0;
  return (
    <View style={{ gap: 6 }}>
      <Text style={styles.small}>
        {formatSize(limits.usedBytes)} of {limits.maxBytes > 0 ? formatSize(limits.maxBytes) : 'no limit'} ·{' '}
        {limits.usedItems} of {limits.maxItems} items · {formatSize(limits.availableBytes)} free for one item
      </Text>
      {limits.maxBytes > 0 && (
        <View style={{ height: 6, borderRadius: 3, backgroundColor: colors.surfaceRaised, overflow: 'hidden' }}>
          <View
            style={{ width: `${pct}%`, height: '100%', backgroundColor: pct >= 90 ? colors.danger : colors.accent }}
          />
        </View>
      )}
    </View>
  );
}

function iconOf(it: StoreItem): IconName {
  if (it.filename !== '') {
    return 'file';
  }
  if (it.contentType.startsWith('image/')) {
    return 'image';
  }
  return it.contentType.startsWith('text/') ? 'text' : 'file';
}
