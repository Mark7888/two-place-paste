import { useCallback, useEffect, useRef, useState } from "react";
import { api, type EntryPreview, type EntryView, type ServiceEvent } from "../api";
import { StoreDialog, storeRoom } from "../components/StoreDialog";
import { bytes, relative, useNow, when } from "../format";
import { Icon } from "../components/Icon";
import { PreviewBody, entryIcon } from "../preview";

/**
 * History: the group's entry metadata, and a look inside the ones this device
 * still holds the key for.
 *
 * It loads when the screen is opened and refreshes when the service says
 * something changed. Opening the tab *is* the user asking — the button that
 * used to stand between the two was asking twice — but nothing is pulled while
 * the tab is closed, and no entry's body is fetched until the user opens that
 * row. The listing is metadata the relay holds — a size, an epoch, two
 * timestamps — plus each entry's sealed header, opened here, which says what
 * the entry is without fetching it (SPEC §2.3, /spec/crypto.md §5.5). Only
 * formats the service can draw, within the auto-download limit, offer a
 * preview: anything else would be a download the user did not ask for, of
 * something there is nothing useful to draw. How each one is drawn lives in
 * src/preview.
 */
export function HistoryPanel({
  inGroup,
  epoch,
  lastEvent,
}: {
  inGroup: boolean;
  epoch: number;
  lastEvent: ServiceEvent | null;
}) {
  const [entries, setEntries] = useState<EntryView[] | null>(null);
  const [nextBefore, setNextBefore] = useState<string | undefined>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [open, setOpen] = useState<string | null>(null);

  // The store, as far as this tab needs it: whether Save to store is offered,
  // the tags the dialog suggests, and the room left. Null until known.
  const [store, setStore] = useState<{ tags: string[]; available?: number } | null>(null);
  const [saving, setSaving] = useState<EntryView | null>(null);

  useNow();

  // Previews are kept per entry id so reopening a row does not decrypt it
  // again. They live for as long as the tab is on screen and no longer.
  const [previews, setPreviews] = useState<Record<string, EntryPreview | { error: string }>>({});
  const loading = useRef<Set<string>>(new Set());

  const load = useCallback(async (before?: string) => {
    setBusy(true);
    setError("");
    try {
      const page = await api.history(before);
      setEntries((prev) => (before ? [...(prev ?? []), ...page.entries] : page.entries));
      setNextBefore(page.next_before);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }, []);

  const loadStore = useCallback(async () => {
    try {
      const v = await api.store();
      setStore(v.enabled ? { tags: v.tags.map((t) => t.tag), available: storeRoom(v.limits) } : null);
    } catch {
      // No store is not a reason to fail the history.
      setStore(null);
    }
  }, []);

  useEffect(() => {
    if (inGroup) {
      void load();
      void loadStore();
    }
  }, [inGroup, load, loadStore]);

  // A sync, a rekey or a reconnect all change what the listing should say, so
  // the list follows them instead of waiting for a Refresh press.
  useEffect(() => {
    if (!inGroup || lastEvent === null) return;
    if (["sync", "epoch", "connected", "clipboard"].includes(lastEvent.kind)) {
      void load();
    }
    // The store changed: the "saved" marks and Save to store follow it.
    if (lastEvent.kind === "store") {
      void load();
      void loadStore();
    }
    // The relay removed an entry early to stay within its storage limit: it
    // is gone, so it leaves the list now rather than failing when opened.
    if (lastEvent.kind === "evicted" && lastEvent.entry_id) {
      const gone = lastEvent.entry_id;
      setEntries((prev) => prev?.filter((e) => e.id !== gone) ?? prev);
      setOpen((cur) => (cur === gone ? null : cur));
    }
  }, [inGroup, lastEvent, load, loadStore]);

  const copy = async (id: string) => {
    setError("");
    setNotice("");
    try {
      const res = await api.copyEntry(id);
      setNotice(res.message);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  const toggle = (entry: EntryView) => {
    const id = entry.id;
    if (open === id) {
      setOpen(null);
      return;
    }
    setOpen(id);
    if (previews[id] || loading.current.has(id)) return;
    loading.current.add(id);
    void (async () => {
      try {
        const view = await api.previewEntry(id);
        setPreviews((prev) => ({ ...prev, [id]: view }));
      } catch (err) {
        setPreviews((prev) => ({
          ...prev,
          [id]: { error: err instanceof Error ? err.message : String(err) },
        }));
      } finally {
        loading.current.delete(id);
      }
    })();
  };

  if (!inGroup) {
    return (
      <>
        <h1>History</h1>
        <div className="empty">This device is not in a group yet.</div>
      </>
    );
  }

  return (
    <>
      <h1>History</h1>
      <p className="lede">
        Entries the group has written, newest first. They expire within 24 hours. The relay cannot
        read them — their type is decrypted here, and so is a preview.
      </p>

      <div className="group-head">
        <h2>Entries</h2>
        <button className="action small" disabled={busy} onClick={() => void load()}>
          {busy ? <span className="spinner" /> : <Icon name="refresh" size={15} />}
          {busy ? "Loading…" : "Refresh"}
        </button>
      </div>

      {error ? (
        <div className="notice error">
          <Icon name="alert" />
          <span>{error}</span>
        </div>
      ) : null}
      {notice ? (
        <div className="notice ok" role="status">
          <Icon name="check" />
          <span>{notice}</span>
        </div>
      ) : null}

      {entries === null ? (
        <div className="row">
          <span className="spinner" />
          <span className="muted">Loading the group&apos;s entries…</span>
        </div>
      ) : entries.length === 0 ? (
        <div className="empty">No entries yet. They expire within 24 hours.</div>
      ) : (
        <>
          <div className="list">
            {entries.map((e) => {
              // An entry from an older epoch predates a rekey: this device no
              // longer holds the key it was written under, so there is nothing
              // to preview and nothing to copy.
              const readable = e.epoch === epoch;
              const previewable = readable && e.previewable;
              const expanded = previewable && open === e.id;
              const preview = previews[e.id];
              return (
                <div key={e.id}>
                  <div className="item">
                    <div className="body">
                      <div className="title" title={when(e.created_at)}>
                        {relative(e.created_at)}
                      </div>
                      <div className="muted small row tight">
                        <Icon name={entryIcon(e)} size={14} />
                        <span>
                          {describe(e)} · {bytes(e.bytes ?? e.size)}
                          {readable ? "" : " · predates the last re-key"}
                        </span>
                        {e.saved ? (
                          <span className="saved-mark" title="The store already holds this">
                            <Icon name="store" size={13} />
                            saved
                          </span>
                        ) : null}
                      </div>
                    </div>
                    <div className="actions">
                      {previewable ? (
                        <button
                          className="action small quiet"
                          aria-expanded={expanded}
                          onClick={() => toggle(e)}
                        >
                          <Icon
                            name="chevron"
                            size={15}
                            style={{
                              transform: expanded ? "rotate(90deg)" : undefined,
                              transition: "transform 120ms",
                            }}
                          />
                          {expanded ? "Hide" : "Preview"}
                        </button>
                      ) : readable ? (
                        <span className="muted small" title={e.no_preview}>
                          No preview
                        </span>
                      ) : null}
                      {store && readable ? (
                        <button
                          className="icon-button"
                          title="Save to store"
                          aria-label="Save this entry to the store"
                          onClick={() => setSaving(e)}
                        >
                          <Icon name="store" size={16} />
                        </button>
                      ) : null}
                      <button
                        className="action small"
                        disabled={!readable}
                        title={readable ? undefined : "This entry cannot be opened by this device"}
                        onClick={() => void copy(e.id)}
                      >
                        <Icon name="copy" size={15} />
                        Copy
                      </button>
                    </div>
                  </div>

                  {expanded ? (
                    <div className="entry-detail">
                      {!preview ? (
                        <div className="row">
                          <span className="spinner" />
                          <span className="muted small">Decrypting…</span>
                        </div>
                      ) : "error" in preview ? (
                        <div className="notice error">
                          <Icon name="alert" />
                          <span>{preview.error}</span>
                        </div>
                      ) : (
                        <PreviewBody preview={preview} />
                      )}
                    </div>
                  ) : null}
                </div>
              );
            })}
          </div>

          <StoreDialog
            open={saving !== null}
            title="Save to store"
            what={saving ? `${describe(saving)} (${bytes(saving.bytes ?? saving.size)}) from the history` : undefined}
            initial={{ name: saving?.filename ?? "", description: "", tags: [] }}
            knownTags={store?.tags ?? []}
            available={store?.available}
            onClose={() => setSaving(null)}
            onSubmit={async (details, allowDuplicate) => {
              if (!saving) return "";
              const it = await api.saveToStore({ source: "entry", entry_id: saving.id }, details, allowDuplicate);
              return `saved “${it.name}” to the store`;
            }}
            onDone={(message) => {
              setSaving(null);
              setNotice(message);
              setError("");
            }}
          />

          {nextBefore ? (
            <div className="row" style={{ marginTop: "0.9rem" }}>
              <button className="action" disabled={busy} onClick={() => void load(nextBefore)}>
                Load older
              </button>
            </div>
          ) : null}
        </>
      )}
    </>
  );
}

/** describe names an entry from its header: the filename for a file, else
 *  the content type, else that the type is unknown. */
function describe(e: EntryView): string {
  if (e.filename) return e.filename;
  if (e.content_type) return e.content_type;
  return "type unknown";
}
