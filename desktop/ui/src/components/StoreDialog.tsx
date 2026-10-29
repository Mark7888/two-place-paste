/**
 * The save and edit dialog of the store: a name, a description and tags, with
 * the limits shown as counters, because the service refuses rather than cuts
 * what does not fit (/spec/crypto.md §7a.5).
 *
 * A save of something the store already holds comes back refused with the
 * items it duplicates; the dialog names them and offers "Save anyway", which
 * sends the same save again with permission.
 *
 * A save of a protected clipboard — a password manager's copy, or one a
 * sensitive-content filter matches — is refused the same way first, with the
 * plan id that confirms it. Its "Save anyway" sends the save again with that
 * id, which the service keeps valid through a duplicate question after it.
 */

import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react";
import {
  ApiError,
  protectedEntry,
  storeLimits,
  type ProtectedEntry,
  type StoreDetails,
  type StoreItemView,
  type StoreLimitsView,
} from "../api";
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
  /** Sends the save or edit. allowDuplicate is the "Save anyway" answer to a
   *  duplicate; planId confirms a protected clipboard. */
  onSubmit: (details: StoreDetails, allowDuplicate: boolean, planId?: string) => Promise<string>;
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
  // The protected-entry question, and the plan the user's answer to it holds.
  const [guarded, setGuarded] = useState<ProtectedEntry | null>(null);
  const [planId, setPlanId] = useState<string | undefined>(undefined);
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
    setGuarded(null);
    setPlanId(undefined);
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

  const submit = async (allowDuplicate: boolean, plan: string | undefined = planId) => {
    // A tag still being typed is meant to be one.
    const pending = tagDraft.trim();
    const allTags = pending && !tags.includes(pending) ? [...tags, pending] : tags;
    setBusy(true);
    setError("");
    try {
      const message = await onSubmit({ name: name.trim(), description, tags: allTags }, allowDuplicate, plan);
      onDone(message);
    } catch (err) {
      const guard = protectedEntry(err);
      if (guard) {
        setGuarded(guard);
        setDuplicates(null);
      } else if (err instanceof ApiError && err.duplicates?.length) {
        setGuarded(null);
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
        guarded ? (
          <>
            <button type="button" className="action" disabled={busy} onClick={() => setGuarded(null)}>
              Back
            </button>
            <button
              type="button"
              className="action primary"
              disabled={busy}
              onClick={() => {
                setPlanId(guarded.planId);
                setGuarded(null);
                void submit(false, guarded.planId);
              }}
            >
              {busy ? <span className="spinner" /> : null}
              Save anyway
            </button>
          </>
        ) : duplicates ? (
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
      {guarded ? (
        <div className="notice warn">
          <Icon name="lock" />
          <span>
            This is a protected entry{guarded.reason ? <> — {guarded.reason.charAt(0).toLowerCase() + guarded.reason.slice(1)}</> : null}.
            The store keeps it until someone deletes it, and every device in the group can open it.
            Save it anyway?
          </span>
        </div>
      ) : duplicates ? (
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

/**
 * storeRoom is how much the store has left, for the dialog to say.
 *
 * available_bytes is not it: that is the largest item a save may still be,
 * which the relay cuts to the per-item limit. Below that limit it is the room
 * left; at it, the room is the store limit less what is used, and a store
 * with no limit has nothing to say. Whether one file fits is still
 * available_bytes's to answer.
 */
export function storeRoom(limits: StoreLimitsView | null | undefined): number | undefined {
  if (!limits) return undefined;
  if (limits.available_bytes < limits.max_item_bytes) return limits.available_bytes;
  return limits.max_bytes > 0 ? Math.max(limits.max_bytes - limits.used_bytes, 0) : undefined;
}
