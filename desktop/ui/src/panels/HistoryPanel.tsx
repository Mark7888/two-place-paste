import { useState } from "react";
import { api, type EntryView } from "../api";
import { bytes, when } from "../format";

// History is fetched when the user asks for it and at no other time. Clipboard
// history that travels without being asked for is the thing this project is
// careful not to build (SPEC §6).
export function HistoryPanel({ inGroup }: { inGroup: boolean }) {
  const [entries, setEntries] = useState<EntryView[] | null>(null);
  const [nextBefore, setNextBefore] = useState<string | undefined>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  const fetchPage = async (before?: string) => {
    setBusy(true);
    setError("");
    try {
      const page = await api.history(before);
      setEntries(before ? [...(entries ?? []), ...page.entries] : page.entries);
      setNextBefore(page.next_before);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const copy = async (id: string) => {
    setError("");
    try {
      const res = await api.copyEntry(id);
      setNotice(res.message);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  if (!inGroup) return <div className="panel muted">This device is not in a group yet.</div>;

  return (
    <div className="panel">
      <div className="spread">
        <h2>History</h2>
        <button className="action" disabled={busy} onClick={() => void fetchPage()}>
          {busy ? "Fetching…" : entries ? "Refresh" : "Fetch history"}
        </button>
      </div>
      {error ? <div className="notice error">{error}</div> : null}
      {notice ? <div className="notice info">{notice}</div> : null}
      {entries === null ? (
        <p className="muted">Nothing is fetched until you ask for it.</p>
      ) : entries.length === 0 ? (
        <p className="muted">No entries. They expire within 24 hours.</p>
      ) : (
        <>
          <table>
            <thead>
              <tr>
                <th>Created</th>
                <th>Size</th>
                <th>Epoch</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {entries.map((e) => (
                <tr key={e.id}>
                  <td>{when(e.created_at)}</td>
                  <td>{bytes(e.size)}</td>
                  <td>{e.epoch}</td>
                  <td style={{ textAlign: "right" }}>
                    <button className="action" onClick={() => void copy(e.id)}>
                      Copy to clipboard
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {nextBefore ? (
            <div className="row" style={{ marginTop: "0.75rem" }}>
              <button className="action" disabled={busy} onClick={() => void fetchPage(nextBefore)}>
                Load older
              </button>
            </div>
          ) : null}
        </>
      )}
    </div>
  );
}
