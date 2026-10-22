import { useEffect, useMemo, useRef, useState } from "react";
import {
  api,
  type DetectorView,
  type FilterKind,
  type FilterMatch,
  type FilterTarget,
  type FilterView,
  type SettingsView,
} from "../api";
import { Icon } from "../components/Icon";
import { Modal } from "../components/Modal";

/**
 * The filter editor (docs/internal/plans/auto-upload-filters.md §4.7).
 *
 * Two tabs over one draft: Custom, the user's own filters, which can be added,
 * edited, deleted and switched; and Built-in, the detectors, which can only be
 * switched. Each tab has "Turn all on" and "Turn all off", which flip that
 * tab's switches in the draft and nothing more. One Save sends both.
 *
 * The test box under the tabs asks the service, not the browser: Go's regular
 * expressions are not JavaScript's, and the box must answer exactly as the
 * clipboard watcher will. What is typed there is never saved.
 */

type Tab = "custom" | "builtin";

const KINDS: { kind: FilterKind; label: string }[] = [
  { kind: "contains", label: "Contains" },
  { kind: "prefix", label: "Starts with" },
  { kind: "suffix", label: "Ends with" },
  { kind: "regex", label: "Regex" },
  { kind: "exact", label: "Exact match" },
];

/** newId names a filter before it is saved, so a test can point at its row. */
function newId(): string {
  if (typeof crypto.randomUUID === "function") return crypto.randomUUID();
  const b = crypto.getRandomValues(new Uint8Array(16));
  return Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
}

function blankFilter(): FilterView {
  return {
    id: newId(),
    kind: "contains",
    target: "text",
    value: "",
    case_insensitive: true,
    enabled: true,
    label: "",
  };
}

/** casePossible is whether a kind has a case switch of its own. */
function casePossible(kind: FilterKind): boolean {
  return kind === "contains" || kind === "prefix" || kind === "suffix";
}

/**
 * testable is whether a row can be sent to the test endpoint as it stands: a
 * row still waiting for its value would only be refused.
 */
function testable(f: FilterView, savedExact: Set<string>): boolean {
  if (f.kind === "exact") return f.value !== "" || savedExact.has(f.id);
  return f.value !== "";
}

/** rowOfError finds the "filter n:" a validation message starts with. */
function rowOfError(message: string): number | null {
  const m = /filter (\d+):/.exec(message);
  return m ? Number(m[1]) - 1 : null;
}

export function FilterEditor({
  open,
  settings,
  onClose,
  onSaved,
}: {
  open: boolean;
  settings: SettingsView;
  onClose: () => void;
  onSaved: (s: SettingsView) => void;
}) {
  const [tab, setTab] = useState<Tab>("custom");
  const [filters, setFilters] = useState<FilterView[]>([]);
  const [detectors, setDetectors] = useState<DetectorView[]>([]);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [errorRow, setErrorRow] = useState<number | null>(null);
  const [discarding, setDiscarding] = useState(false);
  const [sample, setSample] = useState("");
  const [sampleName, setSampleName] = useState("");
  const [matches, setMatches] = useState<FilterMatch[] | null>(null);
  const [testError, setTestError] = useState("");

  // Exact filters that have a hash saved: their secret is never shown, and a
  // row without a new value keeps it.
  const savedExact = useMemo(
    () => new Set(settings.filters.filter((f) => f.kind === "exact").map((f) => f.id)),
    [settings.filters],
  );

  // Each opening starts from what is saved.
  useEffect(() => {
    if (!open) return;
    setFilters(settings.filters.map((f) => ({ ...f })));
    setDetectors(settings.detectors.map((d) => ({ ...d })));
    setTab("custom");
    setError("");
    setErrorRow(null);
    setDiscarding(false);
    setSample("");
    setSampleName("");
    setMatches(null);
    setTestError("");
  }, [open, settings]);

  const dirty = useMemo(
    () =>
      JSON.stringify(filters) !== JSON.stringify(settings.filters) ||
      JSON.stringify(detectors.map((d) => d.enabled)) !==
        JSON.stringify(settings.detectors.map((d) => d.enabled)),
    [filters, detectors, settings],
  );

  // The test box, debounced. Only rows that are complete are sent; the
  // message of a refused one is mapped back to its row number.
  const testSeq = useRef(0);
  useEffect(() => {
    if (!open) return;
    if (sample === "" && sampleName === "") {
      setMatches(null);
      setTestError("");
      return;
    }
    const seq = ++testSeq.current;
    const sent = filters.filter((f) => testable(f, savedExact));
    const timer = window.setTimeout(() => {
      void (async () => {
        try {
          const res = await api.testFilters({
            text: sample,
            filename: sampleName,
            detectors: detectors.filter((d) => d.enabled).map((d) => d.name),
            filters: sent,
          });
          if (seq !== testSeq.current) return;
          setMatches(res.matches);
          setTestError("");
        } catch (err) {
          if (seq !== testSeq.current) return;
          let msg = err instanceof Error ? err.message : String(err);
          const i = rowOfError(msg);
          if (i !== null && sent[i]) {
            msg = msg.replace(/filter \d+:/, `filter ${filters.indexOf(sent[i]) + 1}:`);
          }
          setMatches(null);
          setTestError(msg);
        }
      })();
    }, 300);
    return () => window.clearTimeout(timer);
  }, [open, sample, sampleName, filters, detectors, savedExact]);

  const matched = new Set((matches ?? []).map((m) => m.rule));
  const customMatched = (matches ?? []).some((m) => m.source === "filter");
  const builtinMatched = (matches ?? []).some((m) => m.source === "detector");

  const update = (i: number, change: Partial<FilterView>) =>
    setFilters((fs) => fs.map((f, j) => (j === i ? { ...f, ...change } : f)));

  const close = () => {
    if (saving) return;
    if (dirty && !discarding) {
      setDiscarding(true);
      return;
    }
    onClose();
  };

  const save = async () => {
    setSaving(true);
    setError("");
    setErrorRow(null);
    try {
      const s = await api.updateSettings({
        filters,
        detectors: detectors.filter((d) => d.enabled).map((d) => d.name),
      });
      onSaved(s);
      onClose();
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      setError(msg);
      const row = rowOfError(msg);
      setErrorRow(row);
      if (row !== null) setTab("custom");
    } finally {
      setSaving(false);
    }
  };

  const allCustom = (on: boolean) => setFilters((fs) => fs.map((f) => ({ ...f, enabled: on })));
  const allBuiltin = (on: boolean) => setDetectors((ds) => ds.map((d) => ({ ...d, enabled: on })));

  const rows = tab === "custom" ? filters : detectors;
  const allOn = rows.length > 0 && rows.every((r) => r.enabled);
  const allOff = rows.every((r) => !r.enabled);

  return (
    <Modal
      open={open}
      wide
      title="Sensitive content filters"
      onClose={close}
      // With unsaved changes, Escape and the backdrop do nothing: Cancel is
      // the way out, and it asks before throwing the draft away.
      dismissable={!dirty}
      footer={
        discarding ? (
          <>
            <span className="muted small grow-left">Discard your changes?</span>
            <button className="action" onClick={() => setDiscarding(false)}>
              Keep editing
            </button>
            <button className="action danger" onClick={onClose}>
              Discard
            </button>
          </>
        ) : (
          <>
            <button className="action" disabled={saving} onClick={close}>
              Cancel
            </button>
            <button className="action primary" disabled={saving || !dirty} onClick={() => void save()}>
              {saving ? <span className="spinner" /> : <Icon name="check" size={16} />}
              Save
            </button>
          </>
        )
      }
    >
      <div className="tabs" role="tablist">
        <button
          type="button"
          role="tab"
          aria-selected={tab === "custom"}
          onClick={() => setTab("custom")}
        >
          Custom
          <span className="faint"> · {filters.length}</span>
          {customMatched && tab !== "custom" ? <span className="tab-dot" aria-label="has a match" /> : null}
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === "builtin"}
          onClick={() => setTab("builtin")}
        >
          Built-in
          <span className="faint">
            {" "}
            · {detectors.filter((d) => d.enabled).length}/{detectors.length}
          </span>
          {builtinMatched && tab !== "builtin" ? <span className="tab-dot" aria-label="has a match" /> : null}
        </button>
      </div>

      <div className="row tab-tools">
        <button
          type="button"
          className="action small quiet"
          disabled={allOn}
          onClick={() => (tab === "custom" ? allCustom(true) : allBuiltin(true))}
        >
          Turn all on
        </button>
        <button
          type="button"
          className="action small quiet"
          disabled={allOff}
          onClick={() => (tab === "custom" ? allCustom(false) : allBuiltin(false))}
        >
          Turn all off
        </button>
      </div>

      {error ? (
        <div className="notice error">
          <Icon name="alert" />
          <span>{error}</span>
        </div>
      ) : null}

      {tab === "custom" ? (
        <div className="stack filter-scroll" style={{ gap: "0.6rem" }} role="tabpanel">
          {filters.length === 0 ? (
            <p className="muted">
              No filters yet. A text you copy that matches one stays on this device unless you
              push it by hand.
            </p>
          ) : null}
          {filters.map((f, i) => (
            <div
              key={f.id}
              className={`filter-row${matched.has(f.id) ? " match" : ""}${errorRow === i ? " invalid" : ""}`}
            >
              <div className="filter-line">
                <input
                  type="checkbox"
                  aria-label="Enabled"
                  checked={f.enabled}
                  onChange={(e) => update(i, { enabled: e.target.checked })}
                />
                <select
                  aria-label="Kind"
                  value={f.kind}
                  onChange={(e) => {
                    const kind = e.target.value as FilterKind;
                    update(i, {
                      kind,
                      // An exact secret is not carried over from another kind's
                      // value, which was shown in clear.
                      value: kind === "exact" || f.kind === "exact" ? "" : f.value,
                      target: kind === "exact" ? "text" : f.target,
                      case_insensitive: casePossible(kind) ? f.case_insensitive : false,
                    });
                  }}
                >
                  {KINDS.map((k) => (
                    <option key={k.kind} value={k.kind} disabled={k.kind === "exact" && f.target === "filename"}>
                      {k.label}
                    </option>
                  ))}
                </select>
                <select
                  aria-label="Applies to"
                  value={f.target}
                  onChange={(e) => update(i, { target: e.target.value as FilterTarget })}
                >
                  <option value="text">Text</option>
                  <option value="filename" disabled={f.kind === "exact"}>
                    File name
                  </option>
                </select>
                <button
                  type="button"
                  className="icon-button"
                  aria-label="Delete filter"
                  title="Delete filter"
                  onClick={() => setFilters((fs) => fs.filter((_, j) => j !== i))}
                >
                  <Icon name="trash" size={16} />
                </button>
              </div>
              <div className="filter-line">
                <input
                  className={f.kind === "regex" ? "mono grow" : "grow"}
                  type={f.kind === "exact" ? "password" : "text"}
                  autoComplete="off"
                  spellCheck={false}
                  aria-label="Value"
                  placeholder={
                    f.kind === "exact"
                      ? savedExact.has(f.id)
                        ? "Saved — type to replace"
                        : "The exact text; only a hash is kept"
                      : f.kind === "regex"
                        ? "Regular expression (Go RE2)"
                        : "Text to look for"
                  }
                  value={f.value}
                  onChange={(e) => update(i, { value: e.target.value })}
                />
                <input
                  type="text"
                  aria-label="Label"
                  placeholder="Label (optional)"
                  value={f.label}
                  onChange={(e) => update(i, { label: e.target.value })}
                />
              </div>
              {casePossible(f.kind) ? (
                <label className="row tight small muted">
                  <input
                    type="checkbox"
                    checked={f.case_insensitive}
                    onChange={(e) => update(i, { case_insensitive: e.target.checked })}
                  />
                  Ignore case
                </label>
              ) : null}
            </div>
          ))}
          <div>
            <button type="button" className="action small" onClick={() => setFilters((fs) => [...fs, blankFilter()])}>
              <Icon name="plus" size={15} />
              Add filter
            </button>
          </div>
        </div>
      ) : (
        <div className="list filter-scroll" role="tabpanel">
          {detectors.map((d) => (
            <label key={d.name} className={`toggle${matched.has(d.name) ? " match" : ""}`}>
              <input
                type="checkbox"
                checked={d.enabled}
                onChange={(e) =>
                  setDetectors((ds) => ds.map((x) => (x.name === d.name ? { ...x, enabled: e.target.checked } : x)))
                }
              />
              <span className="text">
                <strong>{d.title}</strong>
                <span className="why">{d.description}</span>
              </span>
            </label>
          ))}
        </div>
      )}

      <div className="filter-test">
        <h3>Test</h3>
        <textarea
          rows={3}
          spellCheck={false}
          placeholder="Paste a sample to see which filters match. It is not saved."
          value={sample}
          onChange={(e) => setSample(e.target.value)}
        />
        <input
          type="text"
          spellCheck={false}
          placeholder="Or a file name, for file name filters"
          value={sampleName}
          onChange={(e) => setSampleName(e.target.value)}
        />
        {testError ? (
          <p className="small" style={{ color: "var(--danger)" }}>
            {testError}
          </p>
        ) : matches === null ? null : matches.length === 0 ? (
          <p className="muted small">No filter matches this sample.</p>
        ) : (
          <p className="small">
            <Icon name="lock" size={13} /> Sensitive: {matches.map((m) => m.reason).join(", ")}
          </p>
        )}
      </div>
    </Modal>
  );
}
