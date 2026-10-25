/**
 * The save and edit dialog of the store: a name, a description and tags, with
 * the limits shown as counters, because the service refuses rather than cuts
 * what does not fit (/spec/crypto.md §7a.5).
 *
 * A save of something the store already holds comes back refused with the
 * items it duplicates; the dialog names them and offers "Save anyway", which
 * sends the same save again with permission.
 */

import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react";
import { ApiError, storeLimits, type StoreDetails, type StoreItemView } from "../api";
import { bytes, when } from "../format";
import { Icon } from "./Icon";
import { Modal } from "./Modal";

const encoder = new TextEncoder();
const utf8 = (s: string) => encoder.encode(s).length;

export function StoreDialog({
  open,
  title,
  what,
  initial,
  knownTags,
  available,
  saveLabel = "Save",
  onSubmit,
  onDone,
  onClose,
}: {
  open: boolean;
  title: string;
  /** What is being saved, in a few words: "the clipboard", "report.pdf". */
  what?: string;
  initial: StoreDetails;
  /** Tags already in use, offered as the user types. */
  knownTags: string[];
  /** Room left in the store, to show; undefined when not known. */
  available?: number;
  saveLabel?: string;
  /** Sends the save or edit. allowDuplicate is the "Save anyway" answer. */
  onSubmit: (details: StoreDetails, allowDuplicate: boolean) => Promise<string>;
  /** Called with the service's message once it succeeded. */
  onDone: (message: string) => void;
  onClose: () => void;
}) {
  const [name, setName] = useState(initial.name);
  const [description, setDescription] = useState(initial.description);
  const [tags, setTags] = useState<string[]>(initial.tags);
  const [tagDraft, setTagDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [duplicates, setDuplicates] = useState<StoreItemView[] | null>(null);
  const listId = useId();

  // A dialog opened again starts from what it is given, not from last time.
  // Only opening resets it: a parent that re-renders while it is open must
  // not wipe what the user typed.
  const initialRef = useRef(initial);
  initialRef.current = initial;
  useEffect(() => {
    if (!open) return;
    const init = initialRef.current;
    setName(init.name);
    setDescription(init.description);
    setTags(init.tags);
    setTagDraft("");
    setError("");
    setDuplicates(null);
  }, [open]);

  const addTag = (raw: string) => {
    const t = raw.trim();
    if (!t) return;
    if (tags.includes(t)) {
      setTagDraft("");
      return;
    }
    if (tags.length >= storeLimits.tags) {
      setError(`An item carries at most ${storeLimits.tags} tags.`);
      return;
    }
    if (utf8(t) > storeLimits.tag) {
      setError(`A tag is at most ${storeLimits.tag} bytes.`);
      return;
    }
    setError("");
    setTags([...tags, t]);
    setTagDraft("");
  };

  const onTagKey = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "Enter" || event.key === ",") {
      event.preventDefault();
      addTag(tagDraft);
    } else if (event.key === "Backspace" && tagDraft === "" && tags.length > 0) {
      setTags(tags.slice(0, -1));
    }
  };

  const nameBytes = utf8(name.trim());
  const descBytes = utf8(description);
  const invalid =
    nameBytes === 0 || nameBytes > storeLimits.name || descBytes > storeLimits.description;

  const submit = async (allowDuplicate: boolean) => {
    // A tag still being typed is meant to be one.
    const pending = tagDraft.trim();
    const allTags = pending && !tags.includes(pending) ? [...tags, pending] : tags;
    setBusy(true);
    setError("");
    try {
      const message = await onSubmit({ name: name.trim(), description, tags: allTags }, allowDuplicate);
      onDone(message);
    } catch (err) {
      if (err instanceof ApiError && err.duplicates?.length) {
        setDuplicates(err.duplicates);
      } else {
        setError(err instanceof Error ? err.message : String(err));
      }
    } finally {
      setBusy(false);
    }
  };

  const suggestions = knownTags.filter((t) => !tags.includes(t));

  return (
    <Modal
      open={open}
      title={title}
      onClose={onClose}
      wide
      footer={
        duplicates ? (
          <>
            <button type="button" className="action" disabled={busy} onClick={() => setDuplicates(null)}>
              Back
            </button>
            <button type="button" className="action primary" disabled={busy} onClick={() => void submit(true)}>
              {busy ? <span className="spinner" /> : null}
              Save anyway
            </button>
          </>
        ) : (
          <>
            <button type="button" className="action" disabled={busy} onClick={onClose}>
              Cancel
            </button>
            <button
              type="button"
              className="action primary"
              disabled={busy || invalid}
              onClick={() => void submit(false)}
            >
              {busy ? <span className="spinner" /> : null}
              {saveLabel}
            </button>
          </>
        )
      }
    >
      {duplicates ? (
        <div className="stack">
          <div className="notice warn">
            <Icon name="alert" />
            <span>
              The store already holds this content. Save it again anyway — under another name, say —
              or go back.
            </span>
          </div>
          <div className="list">
            {duplicates.map((d) => (
              <div key={d.id} className="item">
                <div className="body">
                  <div className="title">{d.name}</div>
                  <div className="muted small">saved {when(d.saved_at)}</div>
                </div>
              </div>
            ))}
          </div>
        </div>
      ) : (
        <form
          className="stack store-form"
          onSubmit={(event) => {
            event.preventDefault();
            if (!invalid) void submit(false);
          }}
        >
          {what ? (
            <p className="muted small">
              Saving {what}
              {available !== undefined ? ` · ${bytes(available)} left in the store` : ""}
            </p>
          ) : null}
          {error ? (
            <div className="notice error">
              <Icon name="alert" />
              <span>{error}</span>
            </div>
          ) : null}
          <label className="field">
            <span className="label">Name</span>
            <input
              type="text"
              value={name}
              autoFocus
              onChange={(e) => setName(e.target.value)}
              aria-invalid={nameBytes > storeLimits.name}
            />
            <span className={`counter${nameBytes > storeLimits.name ? " over" : ""}`}>
              {nameBytes}/{storeLimits.name}
            </span>
          </label>
          <label className="field">
            <span className="label">Description</span>
            <textarea
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              aria-invalid={descBytes > storeLimits.description}
            />
            <span className={`counter${descBytes > storeLimits.description ? " over" : ""}`}>
              {descBytes}/{storeLimits.description}
            </span>
          </label>
          <div className="field">
            <span className="label">Tags</span>
            <div className="tag-input">
              {tags.map((t) => (
                <span key={t} className="chip on">
                  {t}
                  <button
                    type="button"
                    aria-label={`Remove the tag ${t}`}
                    onClick={() => setTags(tags.filter((x) => x !== t))}
                  >
                    <Icon name="close" size={12} />
                  </button>
                </span>
              ))}
              <input
                type="text"
                value={tagDraft}
                list={listId}
                placeholder={tags.length ? "" : "Type a tag and press Enter"}
                onChange={(e) => setTagDraft(e.target.value)}
                onKeyDown={onTagKey}
                onBlur={() => addTag(tagDraft)}
              />
              <datalist id={listId}>
                {suggestions.map((t) => (
                  <option key={t} value={t} />
                ))}
              </datalist>
            </div>
            <span className="counter">
              {tags.length}/{storeLimits.tags}
            </span>
          </div>
        </form>
      )}
    </Modal>
  );
}
