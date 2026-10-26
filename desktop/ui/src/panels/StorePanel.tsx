import { useCallback, useEffect, useRef, useState, type DragEvent } from "react";
import {
  api,
  type EntryPreview,
  type ServiceEvent,
  type StoreDetails,
  type StoreItemView,
  type StoreSort,
  type StoreView,
} from "../api";
import { Icon } from "../components/Icon";
import { Modal } from "../components/Modal";
import { StoreDialog } from "../components/StoreDialog";
import { bytes, relative, useNow, when } from "../format";
import { PreviewBody, entryIcon } from "../preview";

/**
 * Store: what the group keeps on purpose, beside its 24-hour history (SPEC §6b).
 *
 * Items do not expire. The service holds them opened in memory and searches
 * them there; this tab asks it again as the user types, picks tags or sorts,
 * and whenever the service says the store changed — here or on another
 * device. An item's body is fetched only when the user copies it, saves it as
 * a file, or opens its preview — offered, as in the History, only for formats
 * the service can draw within the auto-download limit.
 */

const emptyDetails: StoreDetails = { name: "", description: "", tags: [] };

type Dialog =
  | { kind: "clipboard" }
  | { kind: "file"; file: File }
  | { kind: "edit"; item: StoreItemView }
  | null;

export function StorePanel({ inGroup, lastEvent }: { inGroup: boolean; lastEvent: ServiceEvent | null }) {
  const [view, setView] = useState<StoreView | null>(null);
  const [text, setText] = useState("");
  const [tags, setTags] = useState<string[]>([]);
  const [sort, setSort] = useState<StoreSort>("newest");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busyId, setBusyId] = useState<string | null>(null);
  const [dialog, setDialog] = useState<Dialog>(null);
  const [open, setOpen] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<{ id: string; name: string } | null>(null);
  const seq = useRef(0);

  // Previews are kept per item id, as in the History, so reopening one does
  // not fetch it again. An edit keeps the content, so its preview stays good.
  const [previews, setPreviews] = useState<Record<string, EntryPreview | { error: string }>>({});
  const loadingPreview = useRef<Set<string>>(new Set());

  useNow();

  // A file dropped beside the drop zone would otherwise be opened by the
  // browser in place of this page.
  useEffect(() => {
    const ignore = (e: globalThis.DragEvent) => {
      if (e.dataTransfer?.types.includes("Files")) e.preventDefault();
    };
    window.addEventListener("dragover", ignore);
    window.addEventListener("drop", ignore);
    return () => {
      window.removeEventListener("dragover", ignore);
      window.removeEventListener("drop", ignore);
    };
  }, []);

  const load = useCallback(async () => {
    // Answers can arrive out of order while the user types; only the newest
    // question's answer is shown.
    const mine = ++seq.current;
    try {
      const v = await api.store({ text, tags, sort });
      if (mine !== seq.current) return;
      setView(v);
      setError("");
      // A tag filter for a tag nobody carries any more matches nothing.
      setTags((cur) => {
        const next = cur.filter((t) => v.tags.some((x) => x.tag === t));
        return next.length === cur.length ? cur : next;
      });
    } catch (err) {
      if (mine === seq.current) setError(err instanceof Error ? err.message : String(err));
    }
  }, [text, tags, sort]);

  // Typing waits a moment; picking a tag or a sort order does not.
  useEffect(() => {
    if (!inGroup) return;
    const id = window.setTimeout(() => void load(), 150);
    return () => window.clearTimeout(id);
  }, [inGroup, load]);

  useEffect(() => {
    if (!inGroup || lastEvent === null) return;
    if (["store", "connected", "epoch"].includes(lastEvent.kind)) void load();
  }, [inGroup, lastEvent, load]);

  const run = async (id: string, what: () => Promise<string | void>) => {
    setBusyId(id);
    setError("");
    setNotice("");
    try {
      const msg = await what();
      if (msg) setNotice(msg);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusyId(null);
    }
  };

  const toggle = (item: StoreItemView) => {
    const id = item.id;
    if (open === id) {
      setOpen(null);
      return;
    }
    setOpen(id);
    if (previews[id] || loadingPreview.current.has(id)) return;
    loadingPreview.current.add(id);
    void (async () => {
      try {
        const view = await api.previewStoreItem(id);
        setPreviews((prev) => ({ ...prev, [id]: view }));
      } catch (err) {
        setPreviews((prev) => ({
          ...prev,
          [id]: { error: err instanceof Error ? err.message : String(err) },
        }));
      } finally {
        loadingPreview.current.delete(id);
      }
    })();
  };

  // A file from the drop zone or the picker is checked before the details
  // are asked for, so a file that cannot fit is refused at once.
  const pickFile = (file: File) => {
    setNotice("");
    if (file.size === 0) {
      setError("That file is empty.");
      return;
    }
    const room = view?.limits?.available_bytes;
    if (room !== undefined && file.size > room) {
      setError(`That file is ${bytes(file.size)}; the store has room for ${bytes(room)}.`);
      return;
    }
    setError("");
    setDialog({ kind: "file", file });
  };

  const saveAsFile = (item: StoreItemView) =>
    run(item.id, async () => {
      const blob = await api.storeItemContent(item.id);
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = fileName(item);
      document.body.appendChild(a);
      a.click();
      a.remove();
      window.setTimeout(() => URL.revokeObjectURL(url), 30_000);
      return `saved “${item.name}” as ${a.download}`;
    });

  if (!inGroup) {
    return (
      <>
        <h1>Store</h1>
        <div className="empty">This device is not in a group yet.</div>
      </>
    );
  }

  const knownTags = view?.tags.map((t) => t.tag) ?? [];
  const limits = view?.limits;

  return (
    <>
      <h1>Store</h1>
      <p className="lede">
        What the group keeps on purpose. Items do not expire: they stay until a device deletes them.
        Like the history, they are encrypted here — the relay cannot read a name, a tag or a byte.
      </p>

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

      {view === null ? (
        <div className="row">
          <span className="spinner" />
          <span className="muted">Opening the store…</span>
        </div>
      ) : !view.enabled ? (
        <div className="empty">
          The server admin has not turned the store on for this group. Ask them to, from the
          group&apos;s page in the admin screens.
        </div>
      ) : (
        <>
          {limits ? <SpaceBar limits={limits} /> : null}

          <AddZone onClipboard={() => setDialog({ kind: "clipboard" })} onFile={pickFile} />

          <div className="store-filters">
            <input
              type="text"
              className="grow"
              placeholder="Search names and descriptions"
              aria-label="Search the store"
              value={text}
              onChange={(e) => setText(e.target.value)}
            />
            <select aria-label="Sort" value={sort} onChange={(e) => setSort(e.target.value as StoreSort)}>
              <option value="newest">Newest first</option>
              <option value="oldest">Oldest first</option>
              <option value="name">Name, A to Z</option>
              <option value="name_desc">Name, Z to A</option>
            </select>
          </div>
          {view.tags.length ? (
            <div className="chips" role="group" aria-label="Filter by tag">
              {view.tags.map((t) => {
                const on = tags.includes(t.tag);
                return (
                  <button
                    key={t.tag}
                    type="button"
                    className={`chip${on ? " on" : ""}`}
                    aria-pressed={on}
                    onClick={() => setTags(on ? tags.filter((x) => x !== t.tag) : [...tags, t.tag])}
                  >
                    <Icon name="tag" size={12} />
                    {t.tag} <span className="count">{t.count}</span>
                  </button>
                );
              })}
            </div>
          ) : null}

          {view.total === 0 ? (
            <div className="empty">
              Nothing saved yet. Save the clipboard, drop a file above, or use “Save to store” on an
              entry in the History.
            </div>
          ) : view.items.length === 0 ? (
            <div className="empty">No item matches.</div>
          ) : (
            <div className="list">
              {view.items.map((item) => {
                const expanded = item.previewable && open === item.id;
                const preview = previews[item.id];
                return (
                  <div key={item.id}>
                    <div className="item store-item">
                      <Icon name={entryIcon(item)} size={18} />
                      <div className="body">
                        <div className="title">{item.name}</div>
                        {item.description ? (
                          <div className="description small">{item.description}</div>
                        ) : null}
                        <div className="muted small row tight">
                          <span title={when(item.saved_at)}>{relative(item.saved_at)}</span>
                          <span>· {item.filename ?? item.content_type}</span>
                          <span>· {bytes(item.bytes)}</span>
                        </div>
                        {item.tags.length ? (
                          <div className="chips small">
                            {item.tags.map((t) => (
                              <span key={t} className="chip">
                                {t}
                              </span>
                            ))}
                          </div>
                        ) : null}
                      </div>
                      <div className="actions">
                        {item.previewable ? (
                          <button
                            className="action small quiet"
                            aria-expanded={expanded}
                            onClick={() => toggle(item)}
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
                        ) : null}
                        <button
                          className="action small"
                          disabled={busyId !== null}
                          onClick={() =>
                            void run(item.id, async () => (await api.copyStoreItem(item.id)).message)
                          }
                        >
                          {busyId === item.id ? <span className="spinner" /> : <Icon name="copy" size={15} />}
                          Copy
                        </button>
                        <button
                          className="icon-button"
                          title="Save as file"
                          aria-label={`Save ${item.name} as a file`}
                          disabled={busyId !== null}
                          onClick={() => void saveAsFile(item)}
                        >
                          <Icon name="download" size={16} />
                        </button>
                        <button
                          className="icon-button"
                          title="Edit"
                          aria-label={`Edit ${item.name}`}
                          onClick={() => setDialog({ kind: "edit", item })}
                        >
                          <Icon name="edit" size={16} />
                        </button>
                        <button
                          className="icon-button danger"
                          title="Delete"
                          aria-label={`Delete ${item.name}`}
                          onClick={() => setDeleting({ id: item.id, name: item.name })}
                        >
                          <Icon name="trash" size={16} />
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
                          <PreviewBody preview={preview} loadMedia={api.storeItemMedia} />
                        )}
                      </div>
                    ) : null}
                  </div>
                );
              })}
            </div>
          )}

          {view.unreadable.length ? (
            <section className="group" style={{ marginTop: "1.5rem" }}>
              <h2>Items this device cannot open</h2>
              <div className="list">
                {view.unreadable.map((u) => (
                  <div key={u.id} className="item">
                    <Icon name="alert" size={18} />
                    <div className="body">
                      <div className="title">Saved {when(u.saved_at)}</div>
                      <div className="muted small">{u.reason}</div>
                    </div>
                    <div className="actions">
                      <button
                        className="action small danger"
                        onClick={() => setDeleting({ id: u.id, name: "this unreadable item" })}
                      >
                        <Icon name="trash" size={15} />
                        Delete
                      </button>
                    </div>
                  </div>
                ))}
              </div>
            </section>
          ) : null}
        </>
      )}

      <StoreDialog
        open={dialog !== null}
        title={dialog?.kind === "edit" ? "Edit item" : "Save to store"}
        what={
          dialog?.kind === "clipboard"
            ? "what is on the clipboard"
            : dialog?.kind === "file"
              ? `${dialog.file.name} (${bytes(dialog.file.size)})`
              : undefined
        }
        available={dialog?.kind === "edit" ? undefined : limits?.available_bytes}
        initial={
          dialog?.kind === "edit"
            ? { name: dialog.item.name, description: dialog.item.description, tags: dialog.item.tags }
            : dialog?.kind === "file"
              ? { ...emptyDetails, name: dialog.file.name }
              : emptyDetails
        }
        knownTags={knownTags}
        saveLabel={dialog?.kind === "edit" ? "Save changes" : "Save"}
        onClose={() => setDialog(null)}
        onSubmit={async (details, allowDuplicate) => {
          if (dialog?.kind === "edit") {
            const it = await api.editStoreItem(dialog.item.id, details);
            return `saved the changes to “${it.name}”`;
          }
          const it =
            dialog?.kind === "file"
              ? await api.uploadToStore(dialog.file, details, allowDuplicate)
              : await api.saveToStore({ source: "clipboard" }, details, allowDuplicate);
          return `saved “${it.name}” to the store`;
        }}
        onDone={(message) => {
          setDialog(null);
          setNotice(message);
          setError("");
          void load();
        }}
      />

      <Modal
        open={deleting !== null}
        title="Delete from the store?"
        onClose={() => setDeleting(null)}
        footer={
          <>
            <button type="button" className="action" onClick={() => setDeleting(null)}>
              Cancel
            </button>
            <button
              type="button"
              className="action solid-danger"
              disabled={busyId !== null}
              onClick={() => {
                const target = deleting;
                if (!target) return;
                setDeleting(null);
                void run(target.id, async () => {
                  await api.deleteStoreItem(target.id);
                  await load();
                  return `deleted ${target.name === "this unreadable item" ? "the item" : `“${target.name}”`}`;
                });
              }}
            >
              Delete
            </button>
          </>
        }
      >
        <p>
          {deleting ? <>“{deleting.name}” is deleted for every device of the group. </> : null}
          This cannot be undone.
        </p>
      </Modal>
    </>
  );
}

/** SpaceBar is the store's room: bytes and items used against the limits. */
function SpaceBar({ limits }: { limits: NonNullable<StoreView["limits"]> }) {
  const pct = limits.max_bytes > 0 ? Math.min(100, (limits.used_bytes / limits.max_bytes) * 100) : 0;
  return (
    <div className="space-bar">
      <div className="spread small">
        <span>
          {bytes(limits.used_bytes)}
          <span className="muted"> of {limits.max_bytes > 0 ? bytes(limits.max_bytes) : "no limit"}</span>
        </span>
        <span className="muted">
          {limits.used_items} of {limits.max_items} items · {bytes(limits.available_bytes)} free for one item
        </span>
      </div>
      {limits.max_bytes > 0 ? (
        <div className={`meter${pct >= 90 ? " high" : ""}`} aria-hidden>
          <span style={{ width: `${pct}%` }} />
        </div>
      ) : null}
    </div>
  );
}

/**
 * AddZone is how something gets into the store: a file dropped on it or
 * chosen with the system's picker, or the clipboard. The whole zone takes a
 * drop, so there is no dialog to open first.
 */
function AddZone({ onClipboard, onFile }: { onClipboard: () => void; onFile: (file: File) => void }) {
  const [over, setOver] = useState(false);
  const input = useRef<HTMLInputElement | null>(null);

  const onDrop = (event: DragEvent<HTMLDivElement>) => {
    event.preventDefault();
    setOver(false);
    const file = event.dataTransfer.files[0];
    if (file) onFile(file);
  };

  return (
    <div
      className={`drop-zone add-zone${over ? " over" : ""}`}
      onDragOver={(e) => {
        if (!e.dataTransfer.types.includes("Files")) return;
        e.preventDefault();
        e.dataTransfer.dropEffect = "copy";
        setOver(true);
      }}
      // Leaving for one of its own children is not leaving the zone.
      onDragLeave={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setOver(false);
      }}
      onDrop={onDrop}
    >
      <Icon name="upload" size={22} />
      <div className="add-zone-text">
        <strong>{over ? "Drop to save it to the store" : "Drop a file here to save it"}</strong>
        <span className="small">or save what is on the clipboard</span>
      </div>
      <div className="add-zone-actions">
        <button type="button" className="action primary" onClick={onClipboard}>
          <Icon name="copy" size={15} />
          Save clipboard
        </button>
        <button type="button" className="action" onClick={() => input.current?.click()}>
          <Icon name="upload" size={15} />
          Upload a file…
        </button>
      </div>
      <input
        ref={input}
        type="file"
        hidden
        onChange={(e) => {
          const file = e.target.files?.[0];
          e.target.value = "";
          if (file) onFile(file);
        }}
      />
    </div>
  );
}

/** fileName is what Save as file calls an item: its own filename, else its
 *  name with an extension from its type. */
function fileName(item: StoreItemView): string {
  if (item.filename) return item.filename;
  const base = item.name.replace(/[\\/:*?"<>|]+/g, "_").slice(0, 120) || "item";
  const type = item.content_type.split(";")[0].trim();
  const ext: Record<string, string> = {
    "text/plain": "txt",
    "text/html": "html",
    "image/png": "png",
    "image/jpeg": "jpg",
    "image/gif": "gif",
    "image/webp": "webp",
    "application/pdf": "pdf",
  };
  return ext[type] ? `${base}.${ext[type]}` : base;
}
