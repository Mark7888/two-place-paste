import { useEffect, useState } from "react";
import {
  api,
  type DetectorImportRow,
  type FilterImportPlan,
  type FilterImportRow,
  type FilterView,
} from "../api";
import { Icon } from "../components/Icon";
import { Modal } from "../components/Modal";
import { kindLabel, targetLabel } from "./filterKinds";

/** What the user chose to import, for the editor to apply to its draft. */
export interface ImportChoice {
  /** New filters and updates, by their own id. */
  filters: FilterView[];
  /** Detectors to switch, by name, to the state given. */
  detectors: { name: string; enabled: boolean }[];
}

/**
 * The import dialog: what the clipboard's export would do to the editor's
 * draft, row by row, for the user to tick.
 *
 * Custom filters are ticked to be imported. New filters start ticked. An
 * update — the same filter, changed on another device — starts unticked,
 * since it overwrites something.
 *
 * A built-in filter is different: it already exists, so its box is whether it
 * will be on after the import, like the editor's own switches. One the export
 * turns on starts ticked; one the export has off also starts ticked — still
 * on — and is turned off only if the user unticks it. An import should not
 * quietly lower the protection. What this device already has, and what it
 * cannot take, is listed without a box.
 */
export function FilterImportDialog({
  open,
  draft,
  storeItemId,
  onClose,
  onImport,
}: {
  open: boolean;
  draft: { filters: FilterView[]; detectors: string[] };
  /** Import the export kept as this store item, not the clipboard's. */
  storeItemId?: string;
  onClose: () => void;
  onImport: (choice: ImportChoice) => void;
}) {
  const [plan, setPlan] = useState<FilterImportPlan | null>(null);
  const [error, setError] = useState("");
  const [ticked, setTicked] = useState<Set<string>>(new Set());

  // The plan is made once per opening, against the draft as it was then.
  useEffect(() => {
    if (!open) return;
    setPlan(null);
    setError("");
    void (async () => {
      try {
        const p = await api.previewFilterImport(storeItemId ? { ...draft, store_item_id: storeItemId } : draft);
        setPlan(p);
        setTicked(
          new Set([
            ...p.filters.filter((r) => r.selected).map((r) => filterKey(r)),
            // For a built-in filter, ticked is "on after the import": the
            // service's selected says whether to apply the export's state.
            ...p.detectors.filter((r) => canTickDetector(r) && onAfter(r)).map((r) => detectorKey(r)),
          ]),
        );
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
      }
    })();
    // The draft is read when the dialog opens, not followed after: only
    // opening is a dependency.
  }, [open]);

  const toggle = (key: string, on: boolean) =>
    setTicked((t) => {
      const next = new Set(t);
      if (on) next.add(key);
      else next.delete(key);
      return next;
    });
  const setAll = (keys: string[], on: boolean) =>
    setTicked((t) => {
      const next = new Set(t);
      for (const k of keys) {
        if (on) next.add(k);
        else next.delete(k);
      }
      return next;
    });

  const filterRows = plan?.filters ?? [];
  const detectorRows = plan?.detectors ?? [];
  const tickableFilters = filterRows.filter(canTickFilter).map(filterKey);
  const tickableDetectors = detectorRows.filter(canTickDetector).map(detectorKey);
  // A built-in filter is a change only when its box differs from how it is
  // set on this device now.
  const changesDetector = (r: DetectorImportRow) => canTickDetector(r) && ticked.has(detectorKey(r)) !== (r.status === "turn_off");
  const count =
    tickableFilters.filter((k) => ticked.has(k)).length + detectorRows.filter(changesDetector).length;

  const apply = () => {
    if (!plan) return;
    onImport({
      filters: filterRows.filter((r) => canTickFilter(r) && ticked.has(filterKey(r))).map((r) => r.filter),
      detectors: detectorRows
        .filter(changesDetector)
        .map((r) => ({ name: r.name, enabled: ticked.has(detectorKey(r)) })),
    });
  };

  return (
    <Modal
      open={open}
      wide
      title="Import filters"
      onClose={onClose}
      footer={
        <>
          <button type="button" className="action" onClick={onClose}>
            Cancel
          </button>
          <button type="button" className="action primary" disabled={count === 0} onClick={apply}>
            <Icon name="download" size={16} />
            {count === 1 ? "Import 1 change" : `Import ${count} changes`}
          </button>
        </>
      }
    >
      {error ? (
        <div className="notice error">
          <Icon name="alert" />
          <span>{error}</span>
        </div>
      ) : !plan ? (
        <div className="row">
          <span className="spinner" />
          <span className="muted">{storeItemId ? "Reading the store item…" : "Reading the clipboard…"}</span>
        </div>
      ) : (
        <div className="stack">
          <p className="muted small">
            Imported filters are added to the editor; nothing is saved until you press <strong>Save</strong>{" "}
            there.
          </p>

          {filterRows.length > 0 ? (
            <section className="import-section">
              <div className="spread">
                <h3>Custom filters</h3>
                <SelectAll keys={tickableFilters} ticked={ticked} setAll={setAll} />
              </div>
              <div className="list">
                {filterRows.map((r) => (
                  <label key={filterKey(r)} className={`toggle import-row${canTickFilter(r) ? "" : " inert"}`}>
                    <input
                      type="checkbox"
                      disabled={!canTickFilter(r)}
                      checked={canTickFilter(r) && ticked.has(filterKey(r))}
                      onChange={(e) => toggle(filterKey(r), e.target.checked)}
                    />
                    <span className="text">
                      <strong>{r.filter.label || `${kindLabel(r.filter.kind)} filter`}</strong>
                      <span className="why">
                        {kindLabel(r.filter.kind)} · {targetLabel(r.filter.target)}
                        {r.filter.kind === "contains" || r.filter.kind === "prefix" || r.filter.kind === "suffix"
                          ? r.filter.case_insensitive
                            ? " · ignore case"
                            : " · match case"
                          : ""}
                        {r.filter.enabled ? "" : " · off"}
                        {" · "}
                        <code>{r.filter.value}</code>
                      </span>
                      <span className={`import-status ${r.status}`}>{filterStatus(r)}</span>
                    </span>
                  </label>
                ))}
              </div>
            </section>
          ) : null}

          {detectorRows.length > 0 ? (
            <section className="import-section">
              <div className="spread">
                <h3>Built-in filters</h3>
                <SelectAll
                  keys={tickableDetectors}
                  ticked={ticked}
                  setAll={setAll}
                  labels={["Turn all on", "Turn all off"]}
                />
              </div>
              <p className="muted small">Ticked means on after the import.</p>
              <div className="list">
                {detectorRows.map((r) => (
                  <label key={detectorKey(r)} className={`toggle import-row${canTickDetector(r) ? "" : " inert"}`}>
                    <input
                      type="checkbox"
                      disabled={!canTickDetector(r)}
                      checked={canTickDetector(r) ? ticked.has(detectorKey(r)) : r.status === "unchanged" && r.enabled}
                      onChange={(e) => toggle(detectorKey(r), e.target.checked)}
                    />
                    <span className="text">
                      <strong>{r.title || r.name}</strong>
                      {canTickDetector(r) ? <DetectorChange row={r} on={ticked.has(detectorKey(r))} /> : null}
                      {r.status === "unchanged" || r.status === "invalid" ? (
                        <span className={`import-status ${r.status}`}>
                          {r.status === "unchanged"
                            ? `Already ${r.enabled ? "on" : "off"} here and in the export`
                            : r.reason}
                        </span>
                      ) : null}
                    </span>
                  </label>
                ))}
              </div>
            </section>
          ) : null}

          {filterRows.length === 0 && detectorRows.length === 0 ? (
            <p className="muted">This export holds no filters.</p>
          ) : null}
        </div>
      )}
    </Modal>
  );
}

function SelectAll({
  keys,
  ticked,
  setAll,
  labels = ["Select all", "Select none"],
}: {
  keys: string[];
  ticked: Set<string>;
  setAll: (keys: string[], on: boolean) => void;
  labels?: [string, string];
}) {
  if (keys.length === 0) return null;
  return (
    <span className="row tight">
      <button
        type="button"
        className="action small quiet"
        disabled={keys.every((k) => ticked.has(k))}
        onClick={() => setAll(keys, true)}
      >
        {labels[0]}
      </button>
      <button
        type="button"
        className="action small quiet"
        disabled={keys.every((k) => !ticked.has(k))}
        onClick={() => setAll(keys, false)}
      >
        {labels[1]}
      </button>
    </span>
  );
}

const filterKey = (r: FilterImportRow) => `f:${r.filter.id}`;
const detectorKey = (r: DetectorImportRow) => `d:${r.name}`;
const canTickFilter = (r: FilterImportRow) => r.status === "new" || r.status === "update";
const canTickDetector = (r: DetectorImportRow) => r.status === "turn_on" || r.status === "turn_off";

/** onAfter is whether a built-in filter starts ticked — on after the import —
 *  from the service's suggestion: a turn-on it suggests applying ends on, a
 *  turn-off it suggests not applying also ends on. */
const onAfter = (r: DetectorImportRow) => (r.status === "turn_on" ? r.selected : !r.selected);

/** DetectorChange says where a built-in filter stands here and in the export,
 *  and what the box as it is set will do. */
function DetectorChange({ row, on }: { row: DetectorImportRow; on: boolean }) {
  const here = row.status === "turn_on" ? "off" : "on";
  const there = row.status === "turn_on" ? "on" : "off";
  const changes = on !== (here === "on");
  return (
    <span className={`import-status ${changes ? (on ? "new" : "update") : "same"}`}>
      {`${here === "on" ? "On" : "Off"} here, ${there} in the export — `}
      {changes ? (on ? "will be turned on" : "will be turned off") : `stays ${here}`}
    </span>
  );
}

function filterStatus(r: FilterImportRow): string {
  switch (r.status) {
    case "new":
      return "New";
    case "update":
      return `Update — ${r.reason ?? "changes it"}`;
    case "duplicate":
      return "Already on this device";
    default:
      return `Can't import — ${r.reason ?? "not valid here"}`;
  }
}
