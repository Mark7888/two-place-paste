import { useCallback, useEffect, useRef, useState } from "react";
import { api, type EntryPreview, type EntryView, type ServiceEvent } from "../api";
import { bytes, relative, useNow, when } from "../format";
import { Icon } from "../components/Icon";

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
 * text and images within the auto-download limit offer a preview: anything
 * else would be a download the user did not ask for, of something there is
 * nothing useful to draw.
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

  useEffect(() => {
    if (inGroup) void load();
  }, [inGroup, load]);

  // A sync, a rekey or a reconnect all change what the listing should say, so
  // the list follows them instead of waiting for a Refresh press.
  useEffect(() => {
    if (!inGroup || lastEvent === null) return;
    if (["sync", "epoch", "connected", "clipboard"].includes(lastEvent.kind)) {
      void load();
    }
    // The relay removed an entry early to stay within its storage limit: it
    // is gone, so it leaves the list now rather than failing when opened.
    if (lastEvent.kind === "evicted" && lastEvent.entry_id) {
      const gone = lastEvent.entry_id;
      setEntries((prev) => prev?.filter((e) => e.id !== gone) ?? prev);
      setOpen((cur) => (cur === gone ? null : cur));
    }
  }, [inGroup, lastEvent, load]);

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
                        <Icon name={e.kind ?? "file"} size={14} />
                        <span>
                          {describe(e)} · {bytes(e.bytes ?? e.size)}
                          {readable ? "" : " · predates the last re-key"}
                        </span>
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
                        <EntryBody preview={preview} />
                      )}
                    </div>
                  ) : null}
                </div>
              );
            })}
          </div>

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

/** EntryBody renders what a preview turned out to be. A file gets no body: its
 *  name and size are already in the row above, and drawing its bytes helps
 *  nobody. */
function EntryBody({ preview }: { preview: EntryPreview }) {
  if (preview.kind === "image") {
    if (preview.image_too_large || !preview.image_data_url) {
      return (
        <p className="muted small">
          {preview.content_type} · {bytes(preview.bytes)} — too large to show here. Copy it to see
          it.
        </p>
      );
    }
    return (
      <>
        <img className="preview-image" src={preview.image_data_url} alt="The entry's image" />
        <p className="muted small">
          {preview.content_type} · {bytes(preview.bytes)}
        </p>
      </>
    );
  }

  if (preview.kind === "file") {
    return (
      <p className="muted small row tight">
        <Icon name="file" size={15} />
        {preview.filename || "file"} · {preview.content_type} · {bytes(preview.bytes)}
      </p>
    );
  }

  return (
    <>
      <pre className="preview mono">{preview.text}</pre>
      <p className="muted small">
        {preview.truncated ? "Shown in part · " : ""}
        {preview.content_type} · {bytes(preview.bytes)}
      </p>
    </>
  );
}
