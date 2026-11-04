import { useEffect, useState } from "react";
import { ApiError, api, type FilterExportResult } from "../api";
import { Icon } from "../components/Icon";
import { Modal } from "../components/Modal";

/**
 * Exporting filters: every custom filter with the built-in filters' settings,
 * or one custom filter. Either to the clipboard — marked private, so
 * clipboard history leaves it alone and it is never uploaded on its own — or
 * straight into the store, which is how it reaches another device of the
 * group without passing through anyone's clipboard.
 *
 * The export is made from the saved filters; the editor only offers it when
 * nothing is left unsaved.
 *
 * Saving an export the store already holds is a question, not a failure: the
 * dialog names the item it duplicates and offers "Save anyway", which saves
 * it again under the name given here.
 */
export function FilterExportDialog({
  open,
  ids,
  summary,
  defaultName,
  onClose,
  onDone,
}: {
  open: boolean;
  /** The filters to export; undefined exports everything. */
  ids?: string[];
  /** What the export holds, in a sentence. */
  summary: string;
  /** The store item's name when none is typed. */
  defaultName: string;
  onClose: () => void;
  onDone: (res: FilterExportResult) => void;
}) {
  const [name, setName] = useState("");
  const [busy, setBusy] = useState<"clipboard" | "store" | null>(null);
  const [error, setError] = useState("");
  // The name of the store item an export to the store duplicates, while the
  // dialog asks whether to save it anyway.
  const [duplicateOf, setDuplicateOf] = useState<string | null>(null);

  useEffect(() => {
    if (!open) return;
    setName("");
    setError("");
    setDuplicateOf(null);
  }, [open]);

  const run = async (to: "clipboard" | "store", allowDuplicate = false) => {
    setBusy(to);
    setError("");
    setDuplicateOf(null);
    try {
      const res = await api.exportFilters({
        ids,
        to,
        name: to === "store" ? name.trim() || undefined : undefined,
        allow_duplicate: allowDuplicate || undefined,
      });
      onDone(res);
    } catch (err) {
      if (err instanceof ApiError && err.duplicates?.length) {
        setDuplicateOf(err.duplicates[0].name);
      } else {
        setError(err instanceof Error ? err.message : String(err));
      }
    } finally {
      setBusy(null);
    }
  };

  return (
    <Modal
      open={open}
      wide
      title={ids ? "Export filter" : "Export filters"}
      onClose={() => (busy ? undefined : onClose())}
      dismissable={busy === null}
      footer={
        <>
          <button type="button" className="action" disabled={busy !== null} onClick={onClose}>
            Cancel
          </button>
          <button type="button" className="action" disabled={busy !== null} onClick={() => void run("clipboard")}>
            {busy === "clipboard" ? <span className="spinner" /> : <Icon name="copy" size={16} />}
            Copy to clipboard
          </button>
          <button
            type="button"
            className="action primary"
            disabled={busy !== null}
            onClick={() => void run("store")}
          >
            {busy === "store" ? <span className="spinner" /> : <Icon name="store" size={16} />}
            Save to Store
          </button>
        </>
      }
    >
      <div className="stack">
        <p>{summary}</p>
        <p className="muted small">
          The export holds each filter&apos;s value as you typed it. In the store it is encrypted like
          everything else; copied, it is marked private, so clipboard history skips it and it is never
          uploaded on its own. On another device, open the filters and use <strong>Import</strong>.
        </p>
        <label className="field">
          <span className="label">Name in the store</span>
          <input type="text" value={name} placeholder={defaultName} onChange={(e) => setName(e.target.value)} />
        </label>
        {duplicateOf !== null ? (
          <div className="notice warn centered">
            <Icon name="alert" />
            <span className="grow">The store already holds this export, as “{duplicateOf}”.</span>
            <button
              type="button"
              className="action small"
              disabled={busy !== null}
              onClick={() => void run("store", true)}
            >
              {busy === "store" ? <span className="spinner" /> : null}
              Save anyway
            </button>
          </div>
        ) : null}
        {error ? (
          <div className="notice error">
            <Icon name="alert" />
            <span>{error}</span>
          </div>
        ) : null}
      </div>
    </Modal>
  );
}
